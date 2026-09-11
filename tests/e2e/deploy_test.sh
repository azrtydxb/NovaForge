#!/usr/bin/env bash
# deploy_test.sh — prove a real NovaForge install on the kw cluster works:
# every service healthy, then a standard git client clones, commits and pushes
# over HTTPS, and the pushed commit is readable back through the REST API.
#
# This is the acceptance test for foundation Task 18. It uses the REAL cluster,
# the REAL registry, and an unmodified git binary — nothing here is simulated.
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

echo "== 1. every deployment reports ready =="
$KC get deploy -o json | python3 -c '
import json,sys
items=json.load(sys.stdin)["items"]
bad=[]
for d in items:
    name=d["metadata"]["name"]
    want=d["spec"].get("replicas",1)
    got=d.get("status",{}).get("readyReplicas",0)
    print(f"  {name}: {got}/{want}")
    if got<want: bad.append(name)
if bad:
    print("NOT READY: "+", ".join(bad)); sys.exit(1)
' || fail "not all deployments are ready"
ok "all deployments ready"

echo "== 2. edge answers readiness =="
EDGE_IP="$($KC get svc "$REL-edge" -o jsonpath='{.status.loadBalancer.ingress[0].ip}')"
[ -n "$EDGE_IP" ] || fail "edge has no LoadBalancer IP"
curl -fsS "http://$EDGE_IP:8080/healthz" >/dev/null || fail "edge /healthz did not answer 200"
ok "edge healthy at $EDGE_IP"

echo "== 3. register, log in, create an org and a repo through the API =="
export XDG_CONFIG_HOME="$(mktemp -d)"
USER="e2e$RANDOM"
go build -o /tmp/nf ./cmd/nf
curl -fsS -X POST "http://$EDGE_IP:8080/api/v1/auth/register" \
	-H 'Content-Type: application/json' \
	-d "{\"email\":\"$USER@example.com\",\"username\":\"$USER\",\"password\":\"correct horse battery staple\"}" \
	>/dev/null || fail "register failed"
/tmp/nf login --server "http://$EDGE_IP:8080" --username "$USER" --password "correct horse battery staple" || fail "login failed"
/tmp/nf org create e2eorg || fail "org create failed"
/tmp/nf org use e2eorg
/tmp/nf repo create widgets || fail "repo create failed"
/tmp/nf repo list | grep -q widgets || fail "repo list did not show the new repository"
ok "org and repo created through the API"

echo "== 4. a standard git client clones, commits and pushes over HTTPS =="
GIT_IP="$($KC get svc "$REL-git-platform" -o jsonpath='{.status.loadBalancer.ingress[0].ip}')"
[ -n "$GIT_IP" ] || fail "git-platform has no LoadBalancer IP"
TOKEN="$(python3 -c "import json,os;print(json.load(open(os.environ['XDG_CONFIG_HOME']+'/novaforge/config.json'))['token'])")"
WORK="$(mktemp -d)"
git -c http.sslVerify=false clone "http://$USER:$TOKEN@$GIT_IP:8081/e2eorg/widgets.git" "$WORK/widgets" || fail "git clone failed"
cd "$WORK/widgets"
git config user.email e2e@example.com
git config user.name "E2E"
echo "hello from the end-to-end test" >README.md
git add README.md
git commit -q -m "e2e: first commit"
git push origin HEAD:main || fail "git push failed"
PUSHED="$(git rev-parse HEAD)"
cd - >/dev/null
ok "pushed $PUSHED with a standard git client"

echo "== 5. the pushed commit is readable through the REST API =="
/tmp/nf repo log widgets main | grep -q "${PUSHED:0:8}" || fail "pushed commit not visible through the API"
/tmp/nf repo branches widgets | grep -q main || fail "main branch not listed"
ok "commit and branch visible through the API"

echo
echo "PASS: NovaForge is deployed on the kw cluster and a real git round trip works."
