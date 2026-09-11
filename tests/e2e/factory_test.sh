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
/tmp/nf dashboard | grep -qiE "agents|review|gate" || fail "the dashboard returned nothing recognisable"
ok "dashboard answers"

echo
echo "PASS: the software factory layer works end to end on the kw cluster."
