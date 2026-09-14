#!/usr/bin/env bash
# search_test.sh — prove semantic code search end to end on the live cluster:
# a standard git push is indexed (push event -> indexer -> the embedding model
# through the gateway -> graph.code_chunks), and a query that shares no word
# with the code it should find ranks that code first through the edge.
#
# Every link of this chain was broken at once before this test existed: the
# indexer read repositories with no credential, a first push could not be
# diffed, the embedder sent no gateway credential, the schema could not store
# the model's vectors, and no route reached the search at all. Each failure was
# a log line, and all of them together looked like an index with nothing in it.
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
BASE="http://$EDGE_IP:8080"

XDG_CONFIG_HOME="$(mktemp -d)"
export XDG_CONFIG_HOME
USER="se$RANDOM$$"
ORG="seorg$RANDOM$$"
# Every run creates its own organization so runs cannot see each other's
# data; remove it on exit, pass or fail, or the cluster fills with them.
# NF_KEEP_TEST_DATA=1 keeps it for debugging a failure.
cleanup_org() { [ -n "${NF_KEEP_TEST_DATA:-}" ] || ./hack/purge-orgs.sh "^$ORG\$" --yes >/dev/null 2>&1 || true; }
trap cleanup_org EXIT
REPO="ledger$RANDOM"
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

echo "== 2. push code whose meaning is distinct, file by file =="
# The target is invoicing/invoice.go. None of these files may contain the words
# of the query in step 4 ("tax", "calculation", "bill"), in code or comments:
# a lexical match must have nothing to find, so only a search by meaning can
# rank the invoice file first.
WORK="$(mktemp -d)"
git clone -q "http://$USER:$TOKEN@$GIT_IP:8081/$ORG/$REPO.git" "$WORK/repo" 2>/dev/null || fail "clone failed"
mkdir -p "$WORK/repo/invoicing" "$WORK/repo/config" "$WORK/repo/geo" "$WORK/repo/text"

cat >"$WORK/repo/invoicing/invoice.go" <<'GO'
package invoicing

// InvoiceLine is one priced line on a customer invoice.
type InvoiceLine struct {
	Description string
	Quantity    int
	UnitPrice   float64
}

// InvoiceVATTotal sums the net amount of every invoice line and applies the
// value-added rate to it, returning the VAT owed and the gross amount due.
func InvoiceVATTotal(lines []InvoiceLine, vatRatePercent float64) (vat, gross float64) {
	var net float64
	for _, l := range lines {
		net += float64(l.Quantity) * l.UnitPrice
	}
	vat = net * vatRatePercent / 100
	return vat, net + vat
}
GO

cat >"$WORK/repo/config/toml.go" <<'GO'
package config

import (
	"bufio"
	"strings"
)

// ParseTOML reads a TOML document of [section] headers and key = "value"
// pairs into a map keyed by "section.key".
func ParseTOML(doc string) map[string]string {
	out := map[string]string{}
	section := ""
	sc := bufio.NewScanner(strings.NewReader(doc))
	for sc.Scan() {
		line := strings.TrimSpace(sc.Text())
		if line == "" || strings.HasPrefix(line, "#") {
			continue
		}
		if strings.HasPrefix(line, "[") && strings.HasSuffix(line, "]") {
			section = strings.Trim(line, "[]")
			continue
		}
		k, v, found := strings.Cut(line, "=")
		if !found {
			continue
		}
		out[section+"."+strings.TrimSpace(k)] = strings.Trim(strings.TrimSpace(v), `"`)
	}
	return out
}
GO

cat >"$WORK/repo/geo/distance.go" <<'GO'
package geo

import "math"

// HaversineKm returns the great-circle distance in kilometres between two
// points given as latitude and longitude in degrees.
func HaversineKm(lat1, lon1, lat2, lon2 float64) float64 {
	const earthRadiusKm = 6371.0
	rad := func(d float64) float64 { return d * math.Pi / 180 }
	dLat, dLon := rad(lat2-lat1), rad(lon2-lon1)
	a := math.Sin(dLat/2)*math.Sin(dLat/2) +
		math.Cos(rad(lat1))*math.Cos(rad(lat2))*math.Sin(dLon/2)*math.Sin(dLon/2)
	return earthRadiusKm * 2 * math.Atan2(math.Sqrt(a), math.Sqrt(1-a))
}
GO

cat >"$WORK/repo/text/slug.go" <<'GO'
package text

import (
	"strings"
	"unicode"
)

// Slugify lowercases a headline and joins its words with hyphens, dropping
// punctuation, for use in a URL path.
func Slugify(headline string) string {
	var b strings.Builder
	dash := false
	for _, r := range strings.ToLower(headline) {
		switch {
		case unicode.IsLetter(r) || unicode.IsDigit(r):
			b.WriteRune(r)
			dash = false
		case !dash && b.Len() > 0:
			b.WriteByte('-')
			dash = true
		}
	}
	return strings.TrimSuffix(b.String(), "-")
}
GO

QUERY="tax calculation on a bill"
for w in tax calculation bill; do
	if grep -rqi --include='*.go' "$w" "$WORK/repo"; then
		fail "the pushed code contains \"$w\" from the query, so a lexical match could pass this test"
	fi
done

git -C "$WORK/repo" config user.email search@example.com
git -C "$WORK/repo" config user.name "Search E2E"
git -C "$WORK/repo" add -A
git -C "$WORK/repo" commit -q -m "four unrelated packages"
git -C "$WORK/repo" push -q origin HEAD:main || fail "push failed"
ok "pushed $(git -C "$WORK/repo" rev-parse HEAD)"

# search QUERY — prints the edge's JSON answer for QUERY against this repo.
search() {
	local q
	q="$(python3 -c 'import sys,urllib.parse;print(urllib.parse.quote(sys.argv[1]))' "$1")"
	curl -fsS --max-time 60 -H "Authorization: Bearer $TOKEN" \
		"$BASE/api/v1/orgs/$ORG/repos/$REPO/search?q=$q"
}

echo "== 3. the push is indexed with embeddings =="
# The response is {"mode": "semantic"|"lexical", "results": [{"path",
# "start_line", "end_line", "score", "text"}]}. A semantic search over a small
# index returns every chunk, nearest first, so the index is complete when all
# four files appear. Indexing embeds one chunk per request, so allow time.
WANT="invoicing/invoice.go config/toml.go geo/distance.go text/slug.go"
BODY=""
INDEXED=""
for _ in $(seq 1 60); do
	BODY="$(search "$QUERY" 2>/dev/null || true)"
	if [ -n "$BODY" ]; then
		INDEXED="$(printf '%s' "$BODY" | python3 -c '
import json, sys
want = set(sys.argv[1].split())
d = json.load(sys.stdin)
have = {r["path"] for r in d.get("results", [])}
print("yes" if d.get("mode") == "semantic" and want <= have else "no")
' "$WANT")"
		[ "$INDEXED" = "yes" ] && break
	fi
	sleep 5
done
[ -n "$BODY" ] || fail "the search route never answered"
MODE="$(printf '%s' "$BODY" | python3 -c 'import json,sys;print(json.load(sys.stdin).get("mode",""))')"
[ "$MODE" = "semantic" ] ||
	fail "search answered in \"$MODE\" mode — no embedding model answered engineering-graph (check its log for the embedder): $BODY"
[ "$INDEXED" = "yes" ] ||
	fail "after 300s the index still lacks some of: $WANT — the push was not fully indexed: $BODY"
ok "all four files are indexed with embeddings"

echo "== 4. a query sharing no words with the code ranks it first =="
BODY="$(search "$QUERY")" || fail "search for \"$QUERY\" failed"
printf '%s' "$BODY" | python3 -c '
import json, sys
d = json.load(sys.stdin)
for r in d["results"]:
    print("  %.4f  %s:%d-%d" % (r["score"], r["path"], r["start_line"], r["end_line"]))
'
TOP="$(printf '%s' "$BODY" | python3 -c 'import json,sys;r=json.load(sys.stdin)["results"];print(r[0]["path"] if r else "")')"
[ "$TOP" = "invoicing/invoice.go" ] ||
	fail "\"$QUERY\" ranked \"$TOP\" first, not invoicing/invoice.go"
ok "\"$QUERY\" ranks invoicing/invoice.go first"

echo
echo "PASS: a push is indexed and searched by meaning end to end on the kw cluster."
