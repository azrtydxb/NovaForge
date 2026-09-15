#!/usr/bin/env bash
# crossorg_test.sh — prove, on the live cluster, that credentials for one
# organization reach nothing in another: repositories over the REST API,
# smart-HTTP and SSH, Work Items, Engineering Runs, Agent Runs, CI runs and
# the MCP register. Every other suite uses a single organization, so a leak
# between two could never show up in one.
#
# Two people each own an organization. Everything is attempted by A against
# B, first naming B (identity refuses the membership), then naming A with B's
# ids and names (every service must scope the lookup). Each attempt must be
# refused; B's data is then read back as B to show it is untouched.
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
GIT_IP="$($KC get svc "$REL-git-platform" -o jsonpath='{.status.loadBalancer.ingress[0].ip}')"
[ -n "$EDGE_IP" ] && [ -n "$GIT_IP" ] || fail "edge or git-platform has no LoadBalancer IP"
API="http://$EDGE_IP:$EDGE_PORT/api/v1"

USER_A="xoa$RANDOM$$"
USER_B="xob$RANDOM$$"
ORG_A="xorga$RANDOM$$"
ORG_B="xorgb$RANDOM$$"
REPO_A="own$RANDOM"
REPO_B="secret$RANDOM"
PASSWORD="correct horse battery staple"
WORK="$(mktemp -d)"
cleanup_orgs() {
	[ -n "${NF_KEEP_TEST_DATA:-}" ] || ./hack/purge-orgs.sh "^($ORG_A|$ORG_B)\$" --yes >/dev/null 2>&1 || true
}
trap cleanup_orgs EXIT

# call METHOD PATH TOKEN [BODY] prints the HTTP status, a space, then the body.
call() {
	local out code
	out="$(mktemp)"
	code="$(curl -sS -o "$out" -w '%{http_code}' -X "$1" "$API$2" \
		-H "Authorization: Bearer $3" -H 'Content-Type: application/json' ${4:+-d "$4"})"
	echo "$code $(cat "$out")"
	rm -f "$out"
}
field() { python3 -c "import json,sys; print(json.loads(sys.stdin.read().split(' ',1)[1])$1)"; }
# refused METHOD PATH TOKEN [BODY] fails the test unless A's attempt is refused.
refused() {
	local res
	res="$(call "$@")"
	case "$res" in
	2*) fail "LEAK: $1 $2 answered ${res%% *} for $USER_A: ${res#* }" ;;
	40[1349]* | 422*) ;;
	*) fail "$1 $2 was not refused as a refusal: $res" ;;
	esac
}

echo "== 1. two people, each owning an organization with a repository =="
for u in "$USER_A" "$USER_B"; do
	curl -fsS -X POST "$API/auth/register" -H 'Content-Type: application/json' \
		-d "{\"email\":\"$u@example.com\",\"username\":\"$u\",\"password\":\"$PASSWORD\"}" >/dev/null ||
		fail "register $u failed"
done
login() {
	curl -fsS -X POST "$API/auth/login" -H 'Content-Type: application/json' \
		-d "{\"username\":\"$1\",\"password\":\"$PASSWORD\"}" |
		python3 -c 'import json,sys; print(json.load(sys.stdin)["session_token"])'
}
A_TOKEN="$(login "$USER_A")"
B_TOKEN="$(login "$USER_B")"
[ -n "$A_TOKEN" ] && [ -n "$B_TOKEN" ] || fail "login failed"
for pair in "$A_TOKEN $ORG_A $REPO_A" "$B_TOKEN $ORG_B $REPO_B"; do
	read -r tok org repo <<<"$pair"
	res="$(call POST /orgs "$tok" "{\"name\":\"$org\"}")"
	case "$res" in 2*) ;; *) fail "org create $org: $res" ;; esac
	res="$(call POST "/orgs/$org/repos" "$tok" "{\"name\":\"$repo\"}")"
	case "$res" in 2*) ;; *) fail "repo create $org/$repo: $res" ;; esac
done
A_PAT="$(call POST /user/tokens "$A_TOKEN" '{"name":"crossorg","scopes":["repo"]}' | field '["token"]')"
[ -n "$A_PAT" ] || fail "A could not mint a personal access token"
ok "$USER_A owns $ORG_A/$REPO_A; $USER_B owns $ORG_B/$REPO_B"

echo "== 2. B pushes a secret, opens a Work Item, an Engineering Run and an Agent Run =="
git clone -q "http://$USER_B:$B_TOKEN@$GIT_IP:8081/$ORG_B/$REPO_B.git" "$WORK/b" 2>/dev/null || fail "B's clone failed"
cd "$WORK/b"
git config user.email "$USER_B@example.com"
git config user.name "$USER_B"
echo "org B only" >SECRET.md
git add SECRET.md
git commit -qm "B's secret"
git push -q origin HEAD:main || fail "B's push failed"
git checkout -qb feature/b
echo "more" >>SECRET.md
git commit -qam "B's feature"
git push -q origin feature/b || fail "B's feature push failed"
B_HEAD="$(git rev-parse HEAD)"
cd - >/dev/null
res="$(call POST "/orgs/$ORG_B/repos/$REPO_B/work" "$B_TOKEN" '{"type":"feature","goal":"B only","acceptance":["x"]}')"
case "$res" in 2*) ;; *) fail "B's work item: $res" ;; esac
B_KEY="$(echo "$res" | field '["key"]')"
B_ITEM_ID="$(echo "$res" | field '["id"]')"
res="$(call POST "/orgs/$ORG_B/repos/$REPO_B/runs" "$B_TOKEN" '{"title":"B","source_ref":"feature/b","target_ref":"main"}')"
case "$res" in 2*) ;; *) fail "B's run: $res" ;; esac
B_RUN="$(echo "$res" | field '["number"]')"
res="$(call POST "/orgs/$ORG_B/agents" "$B_TOKEN" '{"name":"b-agent","role":"implementer"}')"
case "$res" in 2*) ;; *) fail "B's agent: $res" ;; esac
B_AGENT="$(echo "$res" | field '["id"]')"
res="$(call POST "/orgs/$ORG_B/repos/$REPO_B/agent-runs" "$B_TOKEN" "{\"agent_id\":\"$B_AGENT\",\"work_item_key\":\"$B_KEY\"}")"
case "$res" in 2*) ;; *) fail "B's agent run: $res" ;; esac
B_AGENT_RUN="$(echo "$res" | field '["id"]')"
ok "B has $B_KEY, run #$B_RUN and Agent Run $B_AGENT_RUN"

echo "== 3. A cannot read or write B's repository through the REST API =="
for org in "$ORG_B" "$ORG_A"; do
	refused GET "/orgs/$org/repos/$REPO_B" "$A_TOKEN"
	refused GET "/orgs/$org/repos/$REPO_B/tree/main/" "$A_TOKEN"
	refused GET "/orgs/$org/repos/$REPO_B/blob/main/SECRET.md" "$A_TOKEN"
	refused GET "/orgs/$org/repos/$REPO_B/diff?from=main&to=feature%2Fb" "$A_TOKEN"
	refused GET "/orgs/$org/repos/$REPO_B/tags" "$A_TOKEN"
	refused GET "/orgs/$org/repos/$REPO_B/commits/main" "$A_TOKEN"
	refused POST "/orgs/$org/repos/$REPO_B/branches" "$A_TOKEN" '{"name":"a-was-here"}'
	refused DELETE "/orgs/$org/repos/$REPO_B" "$A_TOKEN"
done
refused GET "/orgs/$ORG_B/repos" "$A_TOKEN"
if call GET "/orgs/$ORG_A/repos" "$A_TOKEN" | grep -q "$REPO_B"; then
	fail "LEAK: A's repository list shows B's repository"
fi
ok "REST repository reads and writes refused"

echo "== 4. A cannot clone or push B's repository over smart-HTTP or SSH =="
for cred in "$A_TOKEN" "$A_PAT"; do
	if GIT_TERMINAL_PROMPT=0 git clone -q "http://$USER_A:$cred@$GIT_IP:8081/$ORG_B/$REPO_B.git" "$WORK/leak" 2>/dev/null; then
		fail "LEAK: A cloned B's repository over HTTP"
	fi
	if GIT_TERMINAL_PROMPT=0 git ls-remote "http://$USER_A:$cred@$GIT_IP:8081/$ORG_A/$REPO_B.git" >/dev/null 2>&1; then
		fail "LEAK: A listed B's refs through A's organization path"
	fi
done
git clone -q "http://$USER_A:$A_TOKEN@$GIT_IP:8081/$ORG_A/$REPO_A.git" "$WORK/a" 2>/dev/null || fail "A's own clone failed"
cd "$WORK/a"
git config user.email "$USER_A@example.com"
git config user.name "$USER_A"
echo pwned >PWNED.md
git add PWNED.md
git commit -qm "cross-org push"
if GIT_TERMINAL_PROMPT=0 git push -q "http://$USER_A:$A_PAT@$GIT_IP:8081/$ORG_B/$REPO_B.git" HEAD:refs/heads/main 2>/dev/null; then
	fail "LEAK: A pushed to B's repository over HTTP"
fi
KEYDIR="$(mktemp -d)"
ssh-keygen -t ed25519 -N "" -f "$KEYDIR/id" -q
res="$(call POST /user/ssh-keys "$A_TOKEN" "{\"title\":\"crossorg\",\"key\":\"$(cat "$KEYDIR/id.pub")\"}")"
case "$res" in 2*) ;; *) fail "A's ssh key: $res" ;; esac
SSH_CMD="ssh -i $KEYDIR/id -o IdentitiesOnly=yes -o StrictHostKeyChecking=no -o UserKnownHostsFile=/dev/null -o BatchMode=yes -p 2222"
GIT_SSH_COMMAND="$SSH_CMD" git ls-remote "ssh://git@$GIT_IP/$ORG_A/$REPO_A.git" >/dev/null || fail "A's key does not work where A belongs"
if GIT_SSH_COMMAND="$SSH_CMD" git clone -q "ssh://git@$GIT_IP/$ORG_B/$REPO_B.git" "$WORK/leak-ssh" 2>/dev/null; then
	fail "LEAK: A's SSH key cloned B's repository"
fi
if GIT_SSH_COMMAND="$SSH_CMD" git push -q "ssh://git@$GIT_IP/$ORG_B/$REPO_B.git" HEAD:refs/heads/main 2>/dev/null; then
	fail "LEAK: A's SSH key pushed to B's repository"
fi
cd - >/dev/null
ok "transport clones and pushes refused over HTTP and SSH"

echo "== 5. A cannot reach B's Work Items, runs, Agent Runs, CI or MCP register =="
for org in "$ORG_B" "$ORG_A"; do
	refused GET "/orgs/$org/repos/$REPO_B/work/$B_KEY" "$A_TOKEN"
	refused POST "/orgs/$org/repos/$REPO_B/work/$B_KEY/comments" "$A_TOKEN" '{"body":"from A"}'
	refused POST "/orgs/$org/repos/$REPO_B/work/$B_KEY/assign" "$A_TOKEN" '{"assignee_id":"'"$B_ITEM_ID"'","assignee_kind":"user"}'
	refused GET "/orgs/$org/repos/$REPO_B/runs/$B_RUN" "$A_TOKEN"
	refused POST "/orgs/$org/repos/$REPO_B/runs/$B_RUN/reviews" "$A_TOKEN" '{"verdict":"approve"}'
	refused POST "/orgs/$org/repos/$REPO_B/runs/$B_RUN/merge" "$A_TOKEN" '{"method":"merge"}'
	refused GET "/orgs/$org/agent-runs/$B_AGENT_RUN" "$A_TOKEN"
	refused DELETE "/orgs/$org/agent-runs/$B_AGENT_RUN" "$A_TOKEN"
	refused GET "/orgs/$org/repos/$REPO_B/ci/runs" "$A_TOKEN"
	refused POST "/orgs/$org/repos/$REPO_B/ci/runs" "$A_TOKEN" '{"ref":"main"}'
done
refused GET "/orgs/$ORG_B/mcp/servers" "$A_TOKEN"
refused POST "/orgs/$ORG_B/mcp/servers" "$A_TOKEN" '{"name":"planted","url":"https://example.com/mcp","transport":"streamable_http"}'
ok "Work Item, run, Agent Run, CI and MCP access refused"

echo "== 6. B's data is exactly as B left it =="
BRANCHES="$(call GET "/orgs/$ORG_B/repos/$REPO_B/branches" "$B_TOKEN")"
echo "$BRANCHES" | grep -q "$B_HEAD" || fail "B's main moved: $BRANCHES"
echo "$BRANCHES" | grep -q "a-was-here" && fail "LEAK: A created a branch in B's repository"
COMMENTS="$(call GET "/orgs/$ORG_B/repos/$REPO_B/work/$B_KEY/comments" "$B_TOKEN")"
echo "$COMMENTS" | grep -q "from A" && fail "LEAK: A commented on B's Work Item"
STATE="$(call GET "/orgs/$ORG_B/agent-runs/$B_AGENT_RUN" "$B_TOKEN" | field '["state"]')"
[ "$STATE" != "cancelled" ] || fail "LEAK: A cancelled B's Agent Run"
RUN_STATE="$(call GET "/orgs/$ORG_B/repos/$REPO_B/runs/$B_RUN" "$B_TOKEN" | field '["state"]')"
[ "$RUN_STATE" != "merged" ] || fail "LEAK: A merged B's run"
call GET "/orgs/$ORG_B/mcp/servers" "$B_TOKEN" | grep -q planted && fail "LEAK: A requested an MCP server in B"
ok "B's repository, Work Item, runs and register untouched"

echo
echo "PASS: one organization's credentials reach nothing in another on the kw cluster."
