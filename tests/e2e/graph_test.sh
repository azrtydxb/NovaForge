#!/usr/bin/env bash
# graph_test.sh — prove on the live cluster that a push to the default branch
# writes the engineering graph's edges, that the Graph screen's routes answer
# with them, that a feature branch cannot rewrite what the default branch says,
# and that project knowledge a person records is listed and found.
#
# Before this, the indexer discarded every reference it parsed: dependents,
# covering tests and change history answered empty for every repository, and a
# push to any branch overwrote the indexed content of the files it touched.
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
BASE="http://$EDGE_IP:$EDGE_PORT"

XDG_CONFIG_HOME="$(mktemp -d)"
export XDG_CONFIG_HOME
USER="gr$RANDOM$$"
ORG="grorg$RANDOM$$"
cleanup_org() { [ -n "${NF_KEEP_TEST_DATA:-}" ] || ./hack/purge-orgs.sh "^$ORG\$" --yes >/dev/null 2>&1 || true; }
trap cleanup_org EXIT
REPO="shop$RANDOM"
go build -o /tmp/nf ./cmd/nf

echo "== 1. account, organization and repository =="
curl -fsS -X POST "$BASE/api/v1/auth/register" \
	-H 'Content-Type: application/json' \
	-d "{\"email\":\"$USER@example.com\",\"username\":\"$USER\",\"password\":\"correct horse battery staple\"}" \
	>/dev/null || fail "register failed"
/tmp/nf login --server "$BASE" --username "$USER" --password "correct horse battery staple" >/dev/null || fail "login failed"
/tmp/nf org create "$ORG" >/dev/null || fail "org create failed"
/tmp/nf org use "$ORG" >/dev/null
/tmp/nf repo create "$REPO" >/dev/null || fail "repo create failed"
TOKEN="$(python3 -c "import json,os;print(json.load(open(os.environ['XDG_CONFIG_HOME']+'/novaforge/config.json'))['token'])")"
[ -n "$TOKEN" ] || fail "nf login stored no token"
ok "created $ORG/$REPO"

echo "== 2. push a package, its test and a caller to main =="
WORK="$(mktemp -d)"
git clone -q "http://$USER:$TOKEN@$GIT_IP:8081/$ORG/$REPO.git" "$WORK/repo" 2>/dev/null || fail "clone failed"
mkdir -p "$WORK/repo/invoicing" "$WORK/repo/api"
printf 'module example.com/shop\n\ngo 1.26\n' >"$WORK/repo/go.mod"
cat >"$WORK/repo/invoicing/invoice.go" <<'GO'
package invoicing

// Total sums the lines.
func Total(lines []int) int {
	sum := 0
	for _, l := range lines {
		sum += l
	}
	return sum
}
GO
cat >"$WORK/repo/invoicing/invoice_test.go" <<'GO'
package invoicing

import "testing"

func TestTotal(t *testing.T) {
	if Total([]int{1, 2}) != 3 {
		t.Fatal("wrong")
	}
}
GO
cat >"$WORK/repo/api/handler.go" <<'GO'
package api

import (
	"fmt"

	"example.com/shop/invoicing"
)

// Handle renders an invoice total.
func Handle(lines []int) string {
	return fmt.Sprint(invoicing.Total(lines))
}
GO
mkdir -p "$WORK/repo/.novaforge/context"
printf 'package shop\nfunc unusedMaintenance() {}\n' >"$WORK/repo/maintenance.go"
printf '[Removed API](symbol:maintenance.go#Removed)\n' >"$WORK/repo/.novaforge/context/maintenance.md"
git -C "$WORK/repo" config user.email graph@example.com
git -C "$WORK/repo" config user.name "Graph E2E"
git -C "$WORK/repo" add -A
git -C "$WORK/repo" commit -q -m "NF-7: invoicing and its handler"
git -C "$WORK/repo" push -q origin HEAD:main || fail "push failed"
MAIN_SHA="$(git -C "$WORK/repo" rev-parse HEAD)"
ok "pushed $MAIN_SHA"

# get PATH — the edge's JSON answer for PATH under this repository.
get() {
	curl -fsS --max-time 30 -H "Authorization: Bearer $TOKEN" "$BASE/api/v1/orgs/$ORG/repos/$REPO/$1"
}

echo "== 3. the symbol's dependents, tests and last change come from the push =="
# graph/symbol answers {"symbol", "dependencies", "dependents", "tests",
# "last_changed_by", "last_change", "history"}; each node carries "path",
# "name", "kind", "import_path", "sha", "work_item_key".
BODY=""
READY=""
for _ in $(seq 1 60); do
	BODY="$(get "graph/symbol?name=Total" 2>/dev/null || true)"
	READY="$(printf '%s' "$BODY" | python3 -c '
import json, sys
try:
    d = json.load(sys.stdin)
except Exception:
    print("no"); sys.exit()
deps = {(n["path"], n["name"]) for n in d.get("dependents", [])}
tests = {(n["path"], n["name"]) for n in d.get("tests", [])}
ok = d.get("symbol") and ("api/handler.go", "Handle") in deps \
    and ("invoicing/invoice_test.go", "TestTotal") in tests \
    and d.get("last_changed_by") == "NF-7"
print("yes" if ok else "no")
' 2>/dev/null || echo no)"
	[ "$READY" = "yes" ] && break
	sleep 5
done
[ "$READY" = "yes" ] || fail "after 300s Total's relations are not the pushed ones: $BODY"
ok "Total is depended on by api/handler.go#Handle, tested by TestTotal, last changed by NF-7"

echo "== 4. a file's imports and importers =="
FILE="$(get "graph/file?path=api/handler.go")" || fail "graph/file failed"
printf '%s' "$FILE" | python3 -c '
import json, sys
d = json.load(sys.stdin)
imports = sorted(n["import_path"] for n in d["imports"])
assert d["indexed"] and imports == ["example.com/shop/invoicing", "fmt"], d
' || fail "api/handler.go's imports are wrong: $FILE"
FILE="$(get "graph/file?path=invoicing/invoice.go")" || fail "graph/file failed"
printf '%s' "$FILE" | python3 -c '
import json, sys
d = json.load(sys.stdin)
assert [n["path"] for n in d["imported_by"]] == ["api/handler.go"], d
assert any(n["sha"] == sys.argv[1] for n in d["history"]), d
' "$MAIN_SHA" || fail "invoicing/invoice.go's importers or history are wrong: $FILE"
ok "imports, importers and history answer"
source tests/e2e/graph_maintenance_probe.sh
verify_graph_maintenance

echo "== 5. a feature branch does not rewrite the default branch's index =="
git -C "$WORK/repo" checkout -q -b feature
git -C "$WORK/repo" rm -q invoicing/invoice.go
git -C "$WORK/repo" commit -q -m "drop invoicing on a branch"
git -C "$WORK/repo" push -q origin HEAD:feature || fail "feature push failed"
sleep 30
BODY="$(get "graph/symbol?name=Total")" || fail "graph/symbol failed"
printf '%s' "$BODY" | python3 -c 'import json,sys;assert json.load(sys.stdin)["symbol"]' ||
	fail "a push to a feature branch removed Total from the index of main: $BODY"
ok "Total is still indexed from main"

echo "== 6. recorded knowledge is listed and found =="
curl -fsS -X POST -H "Authorization: Bearer $TOKEN" -H 'Content-Type: application/json' \
	"$BASE/api/v1/orgs/$ORG/repos/$REPO/knowledge" \
	-d '{"kind":"decision","title":"Invoice totals are integer cents","body":"Totals never use floating point; rounding happens per line."}' \
	>/dev/null || fail "record knowledge failed"
LIST="$(get "knowledge?q=")" || fail "knowledge list failed"
printf '%s' "$LIST" | python3 -c '
import json, sys
d = json.load(sys.stdin)
assert d["mode"] == "recent" and d["entries"][0]["title"] == "Invoice totals are integer cents", d
assert d["entries"][0]["source_run_id"] == "", d
' || fail "the recorded decision is not listed: $LIST"
FOUND="$(get "knowledge?q=how%20are%20invoice%20totals%20rounded")" || fail "knowledge search failed"
printf '%s' "$FOUND" | python3 -c '
import json, sys
d = json.load(sys.stdin)
assert any(e["title"] == "Invoice totals are integer cents" for e in d["entries"]), d
' || fail "the recorded decision is not found by a related question: $FOUND"
ok "the decision is listed and found"

echo
echo "PASS: pushes write graph edges for the default branch, and knowledge is recorded and found, on the kw cluster."
