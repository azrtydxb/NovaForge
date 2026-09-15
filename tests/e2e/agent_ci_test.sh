#!/usr/bin/env bash
# agent_ci_test.sh — prove a CI job declared with an agent role runs as an
# Agent Run on the live cluster and reports its status like any other job
# (spec S-6). Before this, a runner was handed the job, ran an empty shell
# command, exited 0, and the "security review" passed with no agent asked —
# so this test checks for the agent run itself, not just a green job.
set -euo pipefail
cd "$(dirname "$0")/../.."
source hack/env.sh

NS="${NF_NAMESPACE:-novaforge}"
REL="${REL:-novaforge}"
KC="kubectl --context $KUBE_CONTEXT -n $NS"
fail() {
	echo "FAIL: $*" >&2
	exit 1
}
ok() { echo "ok: $*"; }

EDGE_IP="$($KC get svc "$REL-edge" -o jsonpath='{.status.loadBalancer.ingress[0].ip}')"
GIT_IP="$($KC get svc "$REL-git-platform" -o jsonpath='{.status.loadBalancer.ingress[0].ip}')"
[ -n "$EDGE_IP" ] && [ -n "$GIT_IP" ] || fail "edge or git-platform has no LoadBalancer IP"

XDG_CONFIG_HOME="$(mktemp -d)"
export XDG_CONFIG_HOME
USER="aci$RANDOM$$"
ORG="aciorg$RANDOM$$"
# Every run creates its own organization so runs cannot see each other's
# data; remove it on exit, pass or fail, or the cluster fills with them.
# NF_KEEP_TEST_DATA=1 keeps it for debugging a failure.
cleanup_org() { [ -n "${NF_KEEP_TEST_DATA:-}" ] || ./hack/delete-org.sh "$ORG" "http://${EDGE_IP:-}:8080" "${XDG_CONFIG_HOME:-}" >/dev/null 2>&1 || true; }
trap cleanup_org EXIT
REPO="review$RANDOM"
go build -o /tmp/nf ./cmd/nf

echo "== 1. account, organization, repository and a security agent =="
curl -fsS -X POST "http://$EDGE_IP:8080/api/v1/auth/register" \
	-H 'Content-Type: application/json' \
	-d "{\"email\":\"$USER@example.com\",\"username\":\"$USER\",\"password\":\"correct horse battery staple\"}" \
	>/dev/null || fail "register failed"
/tmp/nf login --server "http://$EDGE_IP:8080" --username "$USER" --password "correct horse battery staple" >/dev/null || fail "login failed"
/tmp/nf org create "$ORG" >/dev/null || fail "org create failed"
/tmp/nf org use "$ORG" >/dev/null
/tmp/nf repo create "$REPO" >/dev/null || fail "repo create failed"
/tmp/nf agent create reviewer --role security >/dev/null || fail "agent create failed"
ok "created $ORG/$REPO with a security agent"

echo "== 2. push a workflow whose only job is an agent review =="
# No runner is registered for this organization. An agent job that still went
# to a runner would sit pending forever; one that runs proves the platform
# executed it.
TOKEN="$(python3 -c "import json,os;print(json.load(open(os.environ['XDG_CONFIG_HOME']+'/novaforge/config.json'))['token'])")"
WORK="$(mktemp -d)"
git clone -q "http://$USER:$TOKEN@$GIT_IP:8081/$ORG/$REPO.git" "$WORK/repo" 2>/dev/null || fail "clone failed"
cd "$WORK/repo"
mkdir -p .novaforge
cat >.novaforge/workflow.yaml <<'YAML'
jobs:
  security-review:
    agent: security
YAML
cat >main.go <<'GO'
package main

import (
	"fmt"
	"os"
)

func main() {
	fmt.Println("hello", os.Getenv("NAME"))
}
GO
git config user.email aci@example.com
git config user.name "Agent CI E2E"
git add -A
git commit -q -m "add a service and a workflow with an agent review"
git push -q origin HEAD:main || fail "push failed"
cd - >/dev/null
ok "pushed a workflow with an agent job"

API="http://$EDGE_IP:8080/api/v1/orgs/$ORG/repos/$REPO/ci"
AUTH="Authorization: Bearer $TOKEN"

echo "== 3. the agent job starts an Agent Run =="
RUN_ID=""
AGENT_RUN=""
for _ in $(seq 1 36); do
	RUN_ID="$(curl -fsS -H "$AUTH" "$API/runs" | python3 -c 'import json,sys; r=json.load(sys.stdin)["runs"]; print(r[0]["id"] if r else "")')"
	if [ -n "$RUN_ID" ]; then
		AGENT_RUN="$(curl -fsS -H "$AUTH" "$API/runs/$RUN_ID" | python3 -c 'import json,sys; print(json.load(sys.stdin)["jobs"][0].get("agent_run_id",""))')"
		[ -n "$AGENT_RUN" ] && break
	fi
	sleep 5
done
[ -n "$RUN_ID" ] || fail "no CI run was scheduled for the push"
[ -n "$AGENT_RUN" ] || fail "the agent job never started an Agent Run: $(curl -fsS -H "$AUTH" "$API/runs/$RUN_ID")"
KEY="$(curl -fsS -H "$AUTH" "$API/runs/$RUN_ID" | python3 -c 'import json,sys; print(json.load(sys.stdin)["jobs"][0]["work_item_key"])')"
ok "job started agent run $AGENT_RUN, briefed by $KEY"

echo "== 4. the job settles with the run's outcome =="
JOB_STATUS=""
for _ in $(seq 1 90); do
	JOB_STATUS="$(curl -fsS -H "$AUTH" "$API/runs/$RUN_ID" | python3 -c 'import json,sys; print(json.load(sys.stdin)["jobs"][0]["status"])')"
	case "$JOB_STATUS" in
	success | failure | cancelled) break ;;
	esac
	sleep 5
done
case "$JOB_STATUS" in
success | failure | cancelled) ;;
*) fail "the agent job was still $JOB_STATUS after 450s" ;;
esac
AGENT_STATE="$(/tmp/nf agent get "$AGENT_RUN" | awk '{print $2}')"
case "$AGENT_STATE/$JOB_STATUS" in
succeeded/success | failed/failure | over_budget/failure | cancelled/cancelled) ;;
*) fail "job status $JOB_STATUS does not follow agent run state $AGENT_STATE" ;;
esac
ok "agent run ended $AGENT_STATE and the job reports $JOB_STATUS"

echo "== 5. the review is on the record =="
/tmp/nf work list "$REPO" | grep -q "$KEY" || fail "the Work Item $KEY that briefed the agent is not listed"
# A review that passed must have said something: the brief's first acceptance
# criterion is a comment stating the conclusion.
if [ "$JOB_STATUS" = "success" ]; then
	COMMENTS="$(curl -fsS -H "$AUTH" "http://$EDGE_IP:8080/api/v1/orgs/$ORG/repos/$REPO/work/$KEY/comments" |
		python3 -c 'import json,sys; print(sum(1 for c in json.load(sys.stdin)["comments"] if c.get("author_kind")=="agent"))')"
	[ "$COMMENTS" -gt 0 ] || fail "the review succeeded but the agent left no comment on $KEY"
	ok "the agent recorded its conclusion on $KEY"
else
	ok "work item $KEY is listed; the job's detail: $(curl -fsS -H "$AUTH" "$API/runs/$RUN_ID" | python3 -c 'import json,sys; print(json.load(sys.stdin)["jobs"][0]["detail"])')"
fi

echo
echo "PASS: a CI agent job runs as an Agent Run and reports its status (job: $JOB_STATUS)."
