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
# The expected set comes from the chart, rendered with the values this release
# was installed with: every entry of values.yaml's services, the datastores
# when enabled, and the runner when it has an organization. Checking only that
# the deployments present are ready passed a chart that omitted a service.
VALUES="$(mktemp)"
helm --kube-context "$KUBE_CONTEXT" -n "$NS" get values "$REL" -o yaml >"$VALUES" || fail "cannot read the release's values"
EXPECTED="$(helm template "$REL" deploy/helm/novaforge -f "$VALUES" | python3 -c '
import sys
kind=None
names=[]
for doc in sys.stdin.read().split("\n---"):
    kind=None; name=None; inmeta=False
    for line in doc.splitlines():
        if line.startswith("kind:"): kind=line.split(":",1)[1].strip()
        if line.startswith("metadata:"):
            inmeta=True
            rest=line.split(":",1)[1].strip()
            if rest.startswith("{") and "name:" in rest:
                name=rest.split("name:",1)[1].strip(" {}").split(",")[0].strip()
            continue
        if inmeta and line.startswith("  name:") and name is None:
            name=line.split(":",1)[1].strip()
        if line and not line.startswith(" ") and not line.startswith("metadata:"): inmeta=False
    if kind=="Deployment" and name: names.append(name)
print(" ".join(sorted(set(names))))
')" || fail "helm template failed"
[ -n "$EXPECTED" ] || fail "the chart renders no Deployments"
# Values the chart itself defaults (the service list) must be in the expected
# set even if a release overrode nothing, so check each values.yaml service too.
for svc in $(python3 -c '
import re
inside=False
for line in open("deploy/helm/novaforge/values.yaml"):
    if line.startswith("services:"): inside=True; continue
    if inside and line.strip() and not line.startswith(" "): break
    m=re.match(r"^  ([a-z0-9-]+):\s*$", line)
    if inside and m: print(m.group(1))
'); do
	case " $EXPECTED " in *" $REL-$svc "*) ;; *) fail "values.yaml lists $svc but the rendered chart has no Deployment $REL-$svc" ;; esac
done
PRESENT="$($KC get deploy -o jsonpath='{range .items[*]}{.metadata.name}{"\n"}{end}')"
for want in $EXPECTED; do
	echo "$PRESENT" | grep -qx "$want" || fail "expected Deployment $want is not on the cluster"
done
ok "all $(echo "$EXPECTED" | wc -w | tr -d ' ') expected deployments exist"
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
# NF_EDGE_IP/NF_EDGE_PORT reach the edge another way (e.g. its NodePort) from a
# network that filters the load balancer address; by default the VIP is used.
EDGE_IP="${NF_EDGE_IP:-$($KC get svc "$REL-edge" -o jsonpath='{.status.loadBalancer.ingress[0].ip}')}"
EDGE_PORT="${NF_EDGE_PORT:-8080}"
[ -n "$EDGE_IP" ] || fail "edge has no LoadBalancer IP"
curl -fsS "http://$EDGE_IP:$EDGE_PORT/healthz" >/dev/null || fail "edge /healthz did not answer 200"
ok "edge healthy at $EDGE_IP"

echo "== 3. register, log in, create an org and a repo through the API =="
export XDG_CONFIG_HOME="$(mktemp -d)"
USER="e2e$RANDOM$$"
# The cluster keeps state between runs, so names must be unique per run or the
# second run fails on a conflict rather than on a real defect.
ORG="e2eorg$RANDOM$$"
# Every run creates its own organization so runs cannot see each other's
# data; remove it on exit, pass or fail, or the cluster fills with them.
# NF_KEEP_TEST_DATA=1 keeps it for debugging a failure.
cleanup_org() { [ -n "${NF_KEEP_TEST_DATA:-}" ] || ./hack/delete-org.sh "$ORG" "http://${EDGE_IP:-}:${EDGE_PORT:-8080}" "${XDG_CONFIG_HOME:-}" >/dev/null 2>&1 || true; }
trap cleanup_org EXIT
REPO="widgets$RANDOM"
go build -o /tmp/nf ./cmd/nf
curl -fsS -X POST "http://$EDGE_IP:$EDGE_PORT/api/v1/auth/register" \
	-H 'Content-Type: application/json' \
	-d "{\"email\":\"$USER@example.com\",\"username\":\"$USER\",\"password\":\"correct horse battery staple\"}" \
	>/dev/null || fail "register failed"
/tmp/nf login --server "http://$EDGE_IP:$EDGE_PORT" --username "$USER" --password "correct horse battery staple" || fail "login failed"
/tmp/nf org create "$ORG" || fail "org create failed"
/tmp/nf org use "$ORG"
/tmp/nf repo create "$REPO" || fail "repo create failed"
/tmp/nf repo list | grep -q "$REPO" || fail "repo list did not show the new repository"
ok "org and repo created through the API"

echo "== 4. a standard git client clones, commits and pushes over HTTPS =="
GIT_IP="$($KC get svc "$REL-git-platform" -o jsonpath='{.status.loadBalancer.ingress[0].ip}')"
[ -n "$GIT_IP" ] || fail "git-platform has no LoadBalancer IP"
TOKEN="$(python3 -c "import json,os;print(json.load(open(os.environ['XDG_CONFIG_HOME']+'/novaforge/config.json'))['token'])")"
WORK="$(mktemp -d)"
git -c http.sslVerify=false clone "http://$USER:$TOKEN@$GIT_IP:8081/$ORG/$REPO.git" "$WORK/repo" || fail "git clone failed"
cd "$WORK/repo"
git config user.email e2e@example.com
git config user.name "E2E"
echo "hello from the end-to-end test" >README.md
git add README.md
git commit -q -m "e2e: first commit"
git push origin HEAD:main || fail "git push failed"
PUSHED="$(git rev-parse HEAD)"
cd - >/dev/null
ok "pushed $PUSHED with a standard git client"

echo "== 5. the same repository works over SSH =="
SSH_IP="$GIT_IP"
KEYDIR="$(mktemp -d)"
ssh-keygen -t ed25519 -N "" -f "$KEYDIR/id" -q
PUBKEY="$(cat "$KEYDIR/id.pub")"
curl -fsS -X POST "http://$EDGE_IP:$EDGE_PORT/api/v1/user/ssh-keys" \
	-H "Authorization: Bearer $TOKEN" -H 'Content-Type: application/json' \
	-d "{\"title\":\"e2e\",\"key\":\"$PUBKEY\"}" >/dev/null || fail "adding the ssh key failed"

GIT_SSH_CMD="ssh -i $KEYDIR/id -o StrictHostKeyChecking=no -o UserKnownHostsFile=/dev/null -p 2222"
SSHWORK="$(mktemp -d)"
GIT_SSH_COMMAND="$GIT_SSH_CMD" git clone "ssh://git@$SSH_IP/$ORG/$REPO.git" "$SSHWORK/repo" ||
	fail "git clone over SSH failed"
grep -q "end-to-end test" "$SSHWORK/repo/README.md" || fail "SSH clone did not carry the pushed content"
cd "$SSHWORK/repo"
git config user.email e2e@example.com
git config user.name "E2E"
echo "second commit over ssh" >>README.md
git add README.md
git commit -q -m "e2e: pushed over ssh"
GIT_SSH_COMMAND="$GIT_SSH_CMD" git push origin HEAD:main || fail "git push over SSH failed"
SSH_PUSHED="$(git rev-parse HEAD)"
cd - >/dev/null
ok "cloned and pushed $SSH_PUSHED over SSH"

echo "== 6. the pushed commit is readable through the REST API =="
/tmp/nf repo log "$REPO" main | grep -q "${PUSHED:0:8}" || fail "HTTPS-pushed commit not visible through the API"
/tmp/nf repo log "$REPO" main | grep -q "${SSH_PUSHED:0:8}" || fail "SSH-pushed commit not visible through the API"
/tmp/nf repo branches "$REPO" | grep -q main || fail "main branch not listed"
ok "commit and branch visible through the API"

echo "== 7. deleting a repository and an organization removes what other services held =="
# A throwaway organization with a repository and a Work Item: the repository is
# deleted first and its Work Item must go with it (work-reviews consumes the
# announcement), then the organization, which must stop resolving.
DOOMED="e2edoomed$RANDOM$$"
API="http://$EDGE_IP:$EDGE_PORT/api/v1"
AUTH="Authorization: Bearer $TOKEN"
curl -fsS -X POST "$API/orgs" -H "$AUTH" -H 'Content-Type: application/json' -d "{\"name\":\"$DOOMED\"}" >/dev/null || fail "creating the throwaway organization failed"
curl -fsS -X POST "$API/orgs/$DOOMED/repos" -H "$AUTH" -H 'Content-Type: application/json' -d '{"name":"gone"}' >/dev/null || fail "creating the throwaway repository failed"
KEY="$(curl -fsS -X POST "$API/orgs/$DOOMED/repos/gone/work" -H "$AUTH" -H 'Content-Type: application/json' \
	-d '{"type":"feature","goal":"removed with its repository"}' | python3 -c 'import json,sys;print(json.load(sys.stdin)["key"])')" ||
	fail "creating the throwaway Work Item failed"
curl -fsS -X DELETE "$API/orgs/$DOOMED/repos/gone" -H "$AUTH" >/dev/null || fail "deleting the repository failed"
# The name is reusable at once; the Work Item is read by its key, which does
# not depend on which repository now carries the name.
curl -fsS -X POST "$API/orgs/$DOOMED/repos" -H "$AUTH" -H 'Content-Type: application/json' -d '{"name":"gone"}' >/dev/null || fail "re-creating the repository name failed"
GONE=""
for _ in $(seq 1 30); do
	if ! curl -fsS -H "$AUTH" "$API/orgs/$DOOMED/repos/gone/work/$KEY" >/dev/null 2>&1; then
		GONE=1
		break
	fi
	sleep 2
done
[ -n "$GONE" ] || fail "the deleted repository's Work Item $KEY is still readable after 60s"
curl -fsS -X DELETE "$API/orgs/$DOOMED" -H "$AUTH" -H 'Content-Type: application/json' -d '{"confirm_name":"wrong"}' >/dev/null 2>&1 &&
	fail "an organization was deleted with the wrong confirmation name"
curl -fsS -X DELETE "$API/orgs/$DOOMED" -H "$AUTH" -H 'Content-Type: application/json' -d "{\"confirm_name\":\"$DOOMED\"}" >/dev/null ||
	fail "the owner could not delete the organization"
if curl -fsS -H "$AUTH" "$API/orgs/$DOOMED" >/dev/null 2>&1; then
	fail "the deleted organization still resolves"
fi
ok "repository and organization deletion reached the services holding their data"

echo
echo "PASS: NovaForge is deployed on the kw cluster and a real git round trip works."
