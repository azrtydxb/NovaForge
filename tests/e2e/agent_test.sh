#!/usr/bin/env bash
# agent_test.sh — prove an Agent Run executes end to end on the live cluster:
# an agent is defined, a Work Item is assigned to it, the run is started
# through the API, it reaches a terminal state against the real model, and
# its actions are on the record.
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
[ -n "$EDGE_IP" ] || fail "edge has no LoadBalancer IP"

export XDG_CONFIG_HOME="$(mktemp -d)"
USER="ag$RANDOM$$"
ORG="agorg$RANDOM$$"
REPO="svc$RANDOM"
go build -o /tmp/nf ./cmd/nf

echo "== 1. account, organization and repository =="
curl -fsS -X POST "http://$EDGE_IP:8080/api/v1/auth/register" \
	-H 'Content-Type: application/json' \
	-d "{\"email\":\"$USER@example.com\",\"username\":\"$USER\",\"password\":\"correct horse battery staple\"}" \
	>/dev/null || fail "register failed"
/tmp/nf login --server "http://$EDGE_IP:8080" --username "$USER" --password "correct horse battery staple" >/dev/null || fail "login failed"
/tmp/nf org create "$ORG" >/dev/null || fail "org create failed"
/tmp/nf org use "$ORG" >/dev/null
/tmp/nf repo create "$REPO" >/dev/null || fail "repo create failed"
ok "created $ORG/$REPO"

echo "== 2. an agent is defined =="
/tmp/nf agent create builder --role implementer || fail "agent create failed"
/tmp/nf agent list | grep -q builder || fail "the agent is not listed"
ok "agent defined and listed"

echo "== 3. a Work Item is created for it =="
/tmp/nf work create "$REPO" --type documentation --goal "Add a README describing this service" || fail "work create failed"
KEY="$(/tmp/nf work list "$REPO" | grep "README" | awk '{print $1}' | head -1)"
[ -n "$KEY" ] || fail "the Work Item is not listed"
ok "created $KEY"

echo "== 4. an Agent Run starts through the API =="
RUN="$(/tmp/nf agent start "$REPO" "$KEY" | awk '{print $1}')"
[ -n "$RUN" ] || fail "no run id came back"
ok "started run $RUN"

echo "== 5. the run reaches a terminal state =="
# A run that stays queued forever is the failure this test exists to catch:
# it is what every unwired seam in this platform looked like from outside.
STATE=""
for _ in $(seq 1 60); do
	STATE="$(/tmp/nf agent get "$RUN" | awk '{print $2}')"
	case "$STATE" in
	succeeded | failed | over_budget | cancelled) break ;;
	esac
	sleep 5
done
[ -n "$STATE" ] || fail "the run never reported a state"
case "$STATE" in
queued | running) fail "the run was still $STATE after 300s — nothing is driving it" ;;
esac
ok "run reached a terminal state: $STATE"

echo "== 6. the run is on the record with its capability grant =="
BRANCH="$(/tmp/nf agent get "$RUN" | awk '{print $3}')"
case "$BRANCH" in
agents/$KEY/*) ;;
*) fail "the run's branch $BRANCH is not inside the grant prefix agents/$KEY/" ;;
esac
ok "the run records the branch it was allowed to write: $BRANCH"

echo "== 7. the agent actually did the work =="
# "succeeded" only means the model stopped asking for tools. The bar is
# evidence: the agent's branch must exist, which it can only do if a commit
# went through git.commit and the capability check let it.
[ "$STATE" = "succeeded" ] || fail "the run ended $STATE, not succeeded"
/tmp/nf repo branches "$REPO" 2>/dev/null | grep -q "$BRANCH" ||
	fail "the agent's branch $BRANCH does not exist: nothing was committed"
ok "the agent committed to $BRANCH"

echo
echo "PASS: an Agent Run executes end to end on the kw cluster (final state: $STATE)."
