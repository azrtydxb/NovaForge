#!/usr/bin/env bash
# merge_test.sh — prove an Engineering Run can be reviewed and merged on the
# live cluster, and that the rules around it hold: an author cannot approve
# their own run, a merge without an independent approval is refused, and a
# merge that is allowed actually lands on the target branch.
#
# Every other suite stopped short of merging, which is how a merge path that
# refused every run ("gate controller unreachable: no authorization scope")
# went unnoticed: it failed closed, which looks like policy working.
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
API="http://$EDGE_IP:8080/api/v1"

AUTHOR="mra$RANDOM$$"
REVIEWER="mrb$RANDOM$$"
ORG="mrgorg$RANDOM$$"
# Every run creates its own organization so runs cannot see each other's
# data; remove it on exit, pass or fail, or the cluster fills with them.
# NF_KEEP_TEST_DATA=1 keeps it for debugging a failure.
cleanup_org() { [ -n "${NF_KEEP_TEST_DATA:-}" ] || ./hack/purge-orgs.sh "^$ORG\$" --yes >/dev/null 2>&1 || true; }
trap cleanup_org EXIT
REPO="ledger$RANDOM"
PASSWORD="correct horse battery staple"

# login logs a user in and prints "<session token> <user id>".
login() {
	curl -fsS -X POST "$API/auth/login" -H 'Content-Type: application/json' \
		-d "{\"username\":\"$1\",\"password\":\"$PASSWORD\"}" |
		python3 -c 'import json,sys; d=json.load(sys.stdin); print(d["session_token"], d["user_id"])'
}
# call METHOD PATH TOKEN [BODY] prints the HTTP status, then the body.
call() {
	local out code
	out="$(mktemp)"
	code="$(curl -sS -o "$out" -w '%{http_code}' -X "$1" "$API$2" \
		-H "Authorization: Bearer $3" -H 'Content-Type: application/json' ${4:+-d "$4"})"
	echo "$code $(cat "$out")"
	rm -f "$out"
}

echo "== 1. two people, one organization, one repository =="
for u in "$AUTHOR" "$REVIEWER"; do
	curl -fsS -X POST "$API/auth/register" -H 'Content-Type: application/json' \
		-d "{\"email\":\"$u@example.com\",\"username\":\"$u\",\"password\":\"$PASSWORD\"}" >/dev/null ||
		fail "register $u failed"
done
read -r A_TOKEN _ <<<"$(login "$AUTHOR")"
read -r B_TOKEN B_ID <<<"$(login "$REVIEWER")"
[ -n "$A_TOKEN" ] && [ -n "$B_TOKEN" ] || fail "login failed"
res="$(call POST /orgs "$A_TOKEN" "{\"name\":\"$ORG\"}")"
case "$res" in 2*) ;; *) fail "org create: $res" ;; esac
res="$(call POST "/orgs/$ORG/members" "$A_TOKEN" "{\"user_id\":\"$B_ID\",\"role\":\"member\"}")"
case "$res" in 2*) ;; *) fail "add member: $res" ;; esac
res="$(call POST "/orgs/$ORG/repos" "$A_TOKEN" "{\"name\":\"$REPO\"}")"
case "$res" in 2*) ;; *) fail "repo create: $res" ;; esac
ok "$AUTHOR and $REVIEWER in $ORG/$REPO"

echo "== 2. the author pushes main and a feature branch =="
WORK="$(mktemp -d)"
git clone -q "http://$AUTHOR:$A_TOKEN@$GIT_IP:8081/$ORG/$REPO.git" "$WORK/repo" 2>/dev/null || fail "clone failed"
cd "$WORK/repo"
git config user.email "$AUTHOR@example.com"
git config user.name "$AUTHOR"
printf '# Ledger\n' >README.md
git add README.md
git commit -qm "start the ledger"
git push -q origin HEAD:main || fail "push main failed"
git checkout -qb feature/balance
printf '# Ledger\n\nTracks balances.\n' >README.md
git commit -qam "describe what the ledger does"
git push -q origin feature/balance || fail "push feature failed"
FEATURE_SHA="$(git rev-parse HEAD)"
cd - >/dev/null
ok "pushed feature/balance at $FEATURE_SHA"

echo "== 3. the author opens an Engineering Run =="
res="$(call POST "/orgs/$ORG/repos/$REPO/runs" "$A_TOKEN" '{"title":"Describe the ledger","source_ref":"feature/balance","target_ref":"main"}')"
case "$res" in 2*) ;; *) fail "open run: $res" ;; esac
NUMBER="$(echo "${res#* }" | python3 -c 'import json,sys; print(json.load(sys.stdin)["number"])')"
ok "opened run #$NUMBER"

echo "== 4. the author cannot approve their own run, nor merge it unapproved =="
res="$(call POST "/orgs/$ORG/repos/$REPO/runs/$NUMBER/reviews" "$A_TOKEN" '{"verdict":"approve"}')"
case "$res" in 2*) fail "the author approved their own run: $res" ;; esac
res="$(call POST "/orgs/$ORG/repos/$REPO/runs/$NUMBER/merge" "$A_TOKEN" '{"method":"merge"}')"
case "$res" in
2*) fail "a run with no independent approval merged: $res" ;;
*"no authorization scope"* | *"unreachable"*) fail "the merge was refused for a broken seam, not for policy: $res" ;;
*"independent"*) ok "merge refused: no independent approval" ;;
*) fail "merge refused for an unexpected reason: $res" ;;
esac

echo "== 5. another member approves, and the merge lands =="
res="$(call POST "/orgs/$ORG/repos/$REPO/runs/$NUMBER/reviews" "$B_TOKEN" '{"verdict":"approve"}')"
case "$res" in 2*) ;; *) fail "the reviewer's approval was refused: $res" ;; esac
res="$(call POST "/orgs/$ORG/repos/$REPO/runs/$NUMBER/merge" "$A_TOKEN" '{"method":"merge"}')"
case "$res" in 2*) ;; *) fail "an approved run with no failing gate did not merge: $res" ;; esac
MAIN_README="$(curl -fsS "$API/orgs/$ORG/repos/$REPO/blob/main/README.md" -H "Authorization: Bearer $A_TOKEN")"
echo "$MAIN_README" | grep -q "Tracks balances." || fail "main does not carry the merged change: $MAIN_README"
STATE="$(call GET "/orgs/$ORG/repos/$REPO/runs/$NUMBER" "$A_TOKEN" | cut -d' ' -f2- | python3 -c 'import json,sys; print(json.load(sys.stdin)["state"])')"
[ "$STATE" = "merged" ] || fail "run #$NUMBER is $STATE after merging, want merged"
ok "run #$NUMBER merged into main"

echo
echo "PASS: an Engineering Run is reviewed independently and merges on the kw cluster."
