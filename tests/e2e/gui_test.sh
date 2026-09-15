#!/usr/bin/env bash
# gui_test.sh — prove the web application is served by the live cluster and
# that the endpoints it depends on answer. A GUI that loads but whose screens
# cannot fetch anything is a GUI that looks like it works.
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
BASE="http://$EDGE_IP:8080"

echo "== 1. the application is served =="
curl -fsS --max-time 20 "$BASE/" | grep -q "<title>NovaForge</title>" ||
	fail "the edge does not serve the web application"
ok "index.html served"

# The app routes client-side, so a deep link is a path the server has no file
# for and must answer with the application itself.
CODE="$(curl -sS -o /dev/null -w '%{http_code}' --max-time 20 "$BASE/runs/platform/7")"
[ "$CODE" = "200" ] || fail "a client-side route answered $CODE, not the application"
ok "client-side routes reach the application"

ASSET="$(curl -fsS --max-time 20 "$BASE/" | grep -o '/assets/[^"]*\.js' | head -1)"
[ -n "$ASSET" ] || fail "the served page references no built asset"
curl -fsS -o /dev/null --max-time 20 "$BASE$ASSET" || fail "the built asset $ASSET is not served"
ok "built assets served ($ASSET)"

echo "== 2. the API is not shadowed by the application =="
curl -fsS --max-time 20 "$BASE/healthz" | grep -q '"status"' || fail "/healthz is shadowed"
ok "/healthz still answers"

echo "== 3. an account can sign in and read what the app reads =="
export XDG_CONFIG_HOME="$(mktemp -d)"
USER="gui$RANDOM$$"
ORG="guiorg$RANDOM$$"
# Every run creates its own organization so runs cannot see each other's
# data; remove it on exit, pass or fail, or the cluster fills with them.
# NF_KEEP_TEST_DATA=1 keeps it for debugging a failure.
cleanup_org() { [ -n "${NF_KEEP_TEST_DATA:-}" ] || ./hack/purge-orgs.sh "^$ORG\$" --yes >/dev/null 2>&1 || true; }
trap cleanup_org EXIT
REPO="app$RANDOM"
curl -fsS -X POST "$BASE/api/v1/auth/register" \
	-H 'Content-Type: application/json' \
	-d "{\"email\":\"$USER@example.com\",\"username\":\"$USER\",\"password\":\"correct horse battery staple\"}" \
	>/dev/null || fail "register failed"
TOKEN="$(curl -fsS -X POST "$BASE/api/v1/auth/login" \
	-H 'Content-Type: application/json' \
	-d "{\"username\":\"$USER\",\"password\":\"correct horse battery staple\"}" |
	python3 -c 'import json,sys;print(json.load(sys.stdin)["session_token"])')"
[ -n "$TOKEN" ] || fail "login returned no token"
AUTH=(-H "Authorization: Bearer $TOKEN")

go build -o /tmp/nf ./cmd/nf
/tmp/nf login --server "$BASE" --username "$USER" --password "correct horse battery staple" >/dev/null
/tmp/nf org create "$ORG" >/dev/null
/tmp/nf org use "$ORG" >/dev/null
/tmp/nf repo create "$REPO" >/dev/null
ok "signed in as $USER with $ORG/$REPO"

echo "== 4. every screen's endpoint answers =="
# One request per screen, in the order the rail lists them. A 200 with the
# expected key proves the screen has something real to render; anything else
# means a screen that would show an error to a user.
check() {
	local what="$1" path="$2" key="$3"
	local body
	body="$(curl -fsS --max-time 25 "${AUTH[@]}" "$BASE$path" 2>/dev/null)" ||
		fail "$what: GET $path failed"
	printf '%s' "$body" | grep -q "$key" || fail "$what: GET $path has no \"$key\": $body"
	ok "$what"
}

check "home (dashboard)" "/api/v1/orgs/$ORG/dashboard" "agents_running"
check "exceptions (approvals)" "/api/v1/orgs/$ORG/approvals" "can_decide"
check "work" "/api/v1/orgs/$ORG/repos/$REPO/work" "items"
check "repositories" "/api/v1/orgs/$ORG/repos" "repos"
check "ci" "/api/v1/orgs/$ORG/repos/$REPO/ci/runs" "runs"
check "runs" "/api/v1/orgs/$ORG/repos/$REPO/runs" "runs"
check "agents" "/api/v1/orgs/$ORG/agents" "agents"
check "agents (history)" "/api/v1/orgs/$ORG/agents/stats" "stats"
check "maintenance" "/api/v1/orgs/$ORG/repos/$REPO/maintenance" "proposals"
check "knowledge" "/api/v1/orgs/$ORG/repos/$REPO/knowledge?q=" "entries"
check "graph (code search)" "/api/v1/orgs/$ORG/repos/$REPO/search?q=anything" "results"
check "secrets" "/api/v1/orgs/$ORG/secrets" "secrets"
check "secrets (leases)" "/api/v1/orgs/$ORG/leases" "leases"
check "mcp" "/api/v1/mcp/tools" "novaforge.get_work_item"
check "mcp (external servers)" "/api/v1/orgs/$ORG/mcp/servers" "can_decide"
check "settings (policy)" "/api/v1/approvals/policy" "rules"
check "settings (gates)" "/api/v1/orgs/$ORG/repos/$REPO/gates" "documentation"
check "org & members" "/api/v1/orgs/$ORG/members" "members"
check "account (tokens)" "/api/v1/user/tokens" "tokens"
check "account (ssh keys)" "/api/v1/user/ssh-keys" "keys"

echo
echo "PASS: the NovaForge GUI is served by the cluster and every screen's data answers."
