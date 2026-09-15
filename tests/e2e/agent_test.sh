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

# NF_EDGE_IP/NF_EDGE_PORT reach the edge another way (e.g. its NodePort) from a
# network that filters the load balancer address; by default the VIP is used.
EDGE_IP="${NF_EDGE_IP:-$($KC get svc "$REL-edge" -o jsonpath='{.status.loadBalancer.ingress[0].ip}')}"
EDGE_PORT="${NF_EDGE_PORT:-8080}"
[ -n "$EDGE_IP" ] || fail "edge has no LoadBalancer IP"

export XDG_CONFIG_HOME="$(mktemp -d)"
USER="ag$RANDOM$$"
ORG="agorg$RANDOM$$"
# Every run creates its own organization so runs cannot see each other's
# data; remove it on exit, pass or fail, or the cluster fills with them.
# NF_KEEP_TEST_DATA=1 keeps it for debugging a failure.
cleanup_org() { [ -n "${NF_KEEP_TEST_DATA:-}" ] || ./hack/delete-org.sh "$ORG" "http://${EDGE_IP:-}:${EDGE_PORT:-8080}" "${XDG_CONFIG_HOME:-}" >/dev/null 2>&1 || true; }
trap cleanup_org EXIT
REPO="svc$RANDOM"
go build -o /tmp/nf ./cmd/nf

echo "== 1. account, organization and repository =="
curl -fsS -X POST "http://$EDGE_IP:$EDGE_PORT/api/v1/auth/register" \
	-H 'Content-Type: application/json' \
	-d "{\"email\":\"$USER@example.com\",\"username\":\"$USER\",\"password\":\"correct horse battery staple\"}" \
	>/dev/null || fail "register failed"
/tmp/nf login --server "http://$EDGE_IP:$EDGE_PORT" --username "$USER" --password "correct horse battery staple" >/dev/null || fail "login failed"
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

echo "== 4a. a person's push to the agent's branch is refused while the run holds it =="
# The run holds its grant's prefix (agents/<key>/) from the moment it starts
# running until it ends. BranchLock existed and nothing acquired it, so a
# person could push into an agent's branch in the middle of its run. The
# workspace takes several seconds to provision, which is the window this step
# pushes in; a run that ended before it was ever seen running cannot show the
# lock either way, and says so rather than passing.
SEEN_RUNNING=""
for _ in $(seq 1 60); do
	case "$(/tmp/nf agent get "$RUN" | awk '{print $2}')" in
	running)
		SEEN_RUNNING=1
		break
		;;
	succeeded | failed | over_budget | cancelled) break ;;
	esac
	sleep 1
done
[ -n "$SEEN_RUNNING" ] || fail "run $RUN was never observed running, so the branch lock could not be exercised"
GIT_IP="$($KC get svc "$REL-git-platform" -o jsonpath='{.status.loadBalancer.ingress[0].ip}')"
[ -n "$GIT_IP" ] || fail "git-platform has no LoadBalancer IP"
TOKEN="$(python3 -c "import json,os;print(json.load(open(os.environ['XDG_CONFIG_HOME']+'/novaforge/config.json'))['token'])")"
LOCKWORK="$(mktemp -d)"
git clone -q "http://$USER:$TOKEN@$GIT_IP:8081/$ORG/$REPO.git" "$LOCKWORK/repo" 2>/dev/null ||
	fail "a person could not clone $REPO to attempt the push"
(
	cd "$LOCKWORK/repo"
	git config user.email person@example.com
	git config user.name "Person"
	echo "a person's change in the middle of an agent's run" >PERSON.md
	git add PERSON.md
	git commit -q -m "a person's commit"
)
if PUSH_OUT="$(git -C "$LOCKWORK/repo" push origin "HEAD:refs/heads/agents/$KEY/work" 2>&1)"; then
	AFTER="$(/tmp/nf agent get "$RUN" | awk '{print $2}')"
	fail "a person's push to agents/$KEY/work was accepted (run state now: $AFTER): $PUSH_OUT"
fi
echo "$PUSH_OUT" | grep -q "locked by Agent Run" ||
	fail "the push was refused, but not by the branch lock: $PUSH_OUT"
ok "a person's push to agents/$KEY/work was refused while run $RUN held it"

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
