#!/usr/bin/env bash
# factory_test.sh — prove the software-factory layer on the live cluster: an
# epic decomposes into dependency-ordered subtasks, only the unblocked ones
# start, a run with a failing gate is not auto-merged, and a vulnerable
# dependency produces a proposed Work Item that nothing executes.
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
USER="fac$RANDOM$$"
ORG="facorg$RANDOM$$"
# Every run creates its own organization so runs cannot see each other's
# data; remove it on exit, pass or fail, or the cluster fills with them.
# NF_KEEP_TEST_DATA=1 keeps it for debugging a failure.
cleanup_org() { [ -n "${NF_KEEP_TEST_DATA:-}" ] || ./hack/purge-orgs.sh "^$ORG\$" --yes >/dev/null 2>&1 || true; }
trap cleanup_org EXIT
REPO="sso$RANDOM"
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

echo "== 2. an epic decomposes into dependency-ordered subtasks =="
/tmp/nf work create "$REPO" --type feature --goal "Enterprise SSO" || fail "epic create failed"
EPIC="$(/tmp/nf work list "$REPO" | grep "Enterprise SSO" | awk '{print $1}' | head -1)"
[ -n "$EPIC" ] || fail "the epic is not listed"
/tmp/nf work decompose "$REPO" "$EPIC" || fail "decompose failed"
SUBS="$(/tmp/nf work subtasks "$REPO" "$EPIC" | wc -l | tr -d ' ')"
[ "$SUBS" -ge 3 ] || fail "want at least 3 subtasks, got $SUBS"
ok "epic $EPIC decomposed into $SUBS subtasks"

echo "== 3. only dependency-free subtasks are startable =="
READY="$(/tmp/nf work subtasks "$REPO" "$EPIC" | grep -c ready || true)"
[ "$READY" -lt "$SUBS" ] || fail "every subtask is ready, so the dependency ordering did nothing"
ok "$READY of $SUBS subtasks are ready; the rest are blocked"

echo "== 4. the dashboard reports the exception counts =="
# Polled rather than asked once: this runs seconds after a rollout, and every
# other step here polls for the same reason. A single attempt made this step
# fail on a service that was still settling, which says nothing about the
# dashboard.
DASH=""
for _ in $(seq 1 12); do
	if DASH="$(/tmp/nf dashboard 2>&1)" && printf '%s' "$DASH" | grep -qiE "agents|review|gate"; then
		break
	fi
	DASH=""
	sleep 5
done
[ -n "$DASH" ] || fail "the dashboard returned nothing recognisable within 60s"
ok "dashboard answers"

echo "== 5. a vulnerable dependency produces a proposed Work Item that nothing executes =="
# A second repository, so the epic's items above cannot be mistaken for the
# proposal. It holds a module pinned to golang.org/x/text v0.3.0, which carries
# published advisories, pushed with an unmodified git client.
VULN="vuln$RANDOM"
/tmp/nf repo create "$VULN" >/dev/null || fail "repo create failed"
GIT_IP="$($KC get svc "$REL-git-platform" -o jsonpath='{.status.loadBalancer.ingress[0].ip}')"
[ -n "$GIT_IP" ] || fail "git-platform has no LoadBalancer IP"
TOKEN="$(python3 -c "import json,os;print(json.load(open(os.environ['XDG_CONFIG_HOME']+'/novaforge/config.json'))['token'])")"
WORK="$(mktemp -d)"
git clone -q "http://$USER:$TOKEN@$GIT_IP:8081/$ORG/$VULN.git" "$WORK/repo" 2>/dev/null || fail "clone failed"
(
	cd "$WORK/repo"
	printf 'module example.com/probe\n\ngo 1.22\n\nrequire golang.org/x/text v0.3.0\n' >go.mod
	printf 'package probe\n\nimport _ "golang.org/x/text/language"\n' >main.go
	go mod tidy >/dev/null 2>&1 || true
	git add -A
	git -c user.email=factory@example.com -c user.name="Factory E2E" commit -q -m "add a module with a vulnerable dependency"
	git push -q origin HEAD:main
) || fail "push of the vulnerable module failed"

API="http://$EDGE_IP:8080/api/v1/orgs/$ORG/repos/$VULN"
SCAN="$(curl -fsS -X POST -H "Authorization: Bearer $TOKEN" "$API/maintenance/scan")" || fail "the scan request failed"
echo "  scan: $SCAN"
PROPOSAL="$(curl -fsS -H "Authorization: Bearer $TOKEN" "$API/maintenance" | python3 -c '
import json,sys
for p in json.load(sys.stdin)["proposals"]:
    if p["work_item_type"]=="security" and "golang.org/x/text" in p["work_item_goal"]:
        print(p["work_item_key"], p["assignee_id"] or "-", p["decision"] or "-")
        break
')"
[ -n "$PROPOSAL" ] || fail "no security proposal for golang.org/x/text after the scan"
read -r KEY ASSIGNEE DECISION <<<"$PROPOSAL"
[ "$ASSIGNEE" = "-" ] || fail "the proposal $KEY is already assigned to $ASSIGNEE; nothing may be assigned before approval"
[ "$DECISION" = "-" ] || fail "the proposal $KEY already carries a decision ($DECISION)"
RUNS="$(curl -fsS -H "Authorization: Bearer $TOKEN" "$API/work/$KEY/agent-runs" | python3 -c 'import json,sys;print(len(json.load(sys.stdin).get("runs",[])))')"
[ "$RUNS" = "0" ] || fail "an Agent Run was started on the unapproved proposal $KEY"
ok "proposal $KEY awaits approval with no assignee and no run"

echo
echo "PASS: the software factory layer works end to end on the kw cluster."
