#!/usr/bin/env bash
# cli_test.sh — prove the nf CLI drives the whole lifecycle on the live
# cluster with no GUI: create a repository, clone it and push with plain git,
# create a Work Item with acceptance criteria and a required gate, start an
# Agent Run on it, open an Engineering Run, inspect its gates, record an
# independent review, and merge — and that main carries the change.
#
# `nf run gates` and `nf run merge` existed long before anything called them;
# TestCLIFullLifecycle runs the same sequence in process, this runs it where
# it has to work.
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
SERVER="http://$EDGE_IP:$EDGE_PORT"
GIT_URL="http://$GIT_IP:8081"

AUTHOR="clia$RANDOM$$"
REVIEWER="clib$RANDOM$$"
ORG="cliorg$RANDOM$$"
REPO="ledger$RANDOM"
PASSWORD="correct horse battery staple"
AUTHOR_HOME="$(mktemp -d)"
REVIEWER_HOME="$(mktemp -d)"
WORK="$(mktemp -d)"
cleanup_org() { [ -n "${NF_KEEP_TEST_DATA:-}" ] || ./hack/purge-orgs.sh "^$ORG\$" --yes >/dev/null 2>&1 || true; }
trap cleanup_org EXIT

NF="$WORK/nf"
go build -o "$NF" ./cmd/nf
as_author() { XDG_CONFIG_HOME="$AUTHOR_HOME" "$NF" "$@"; }
as_reviewer() { XDG_CONFIG_HOME="$REVIEWER_HOME" "$NF" "$@"; }

echo "== 1. two people log in with nf and the author creates the organization and repository =="
for u in "$AUTHOR" "$REVIEWER"; do
	# Registration is the one step nf has no command for: an account is made
	# once, by a person, before any CLI session exists.
	curl -fsS -X POST "$SERVER/api/v1/auth/register" -H 'Content-Type: application/json' \
		-d "{\"email\":\"$u@example.com\",\"username\":\"$u\",\"password\":\"$PASSWORD\"}" >/dev/null ||
		fail "register $u failed"
done
as_author login --server "$SERVER" --username "$AUTHOR" --password "$PASSWORD" >/dev/null || fail "author login failed"
as_reviewer login --server "$SERVER" --username "$REVIEWER" --password "$PASSWORD" >/dev/null || fail "reviewer login failed"
as_author org create "$ORG" >/dev/null || fail "org create failed"
as_author org use "$ORG" >/dev/null
as_reviewer org use "$ORG" >/dev/null
as_author org add-member "$REVIEWER" --role member >/dev/null || fail "add-member failed"
as_author repo create "$REPO" >/dev/null || fail "repo create failed"
as_author repo list | grep -q "$REPO" || fail "repo list does not show $REPO"
ok "$AUTHOR and $REVIEWER in $ORG/$REPO"

echo "== 2. nf clones the repository and a plain git push lands main and a feature branch =="
as_author repo clone "$REPO" "$WORK/repo" --git "$GIT_URL" >/dev/null || fail "nf repo clone failed"
cd "$WORK/repo"
git config user.email "$AUTHOR@example.com"
git config user.name "$AUTHOR"
mkdir -p .novaforge/gates
printf 'module example.com/ledger\n\ngo 1.22\n' >go.mod
printf '// Package ledger keeps balances.\npackage ledger\n\n// Balance is an account balance in cents.\ntype Balance int64\n' >ledger.go
printf 'name: documentation\nrequired: true\n' >.novaforge/gates/documentation.yaml
git add -A
git commit -qm "start the ledger"
git push -q origin HEAD:main || fail "push main failed"
git checkout -qb feature/credit
printf 'package ledger\n\n// Credit adds cents to a balance.\nfunc Credit(b Balance, cents int64) Balance { return b + Balance(cents) }\n' >credit.go
git add credit.go
git commit -qm "credit a balance"
git push -q origin feature/credit || fail "push feature failed"
FEATURE_SHA="$(git rev-parse HEAD)"
git diff "main...$FEATURE_SHA" | grep -q "Credit" || fail "reviewed diff missing Credit change"
cd - >/dev/null
ok "pushed main and feature/credit"

echo "== 3. nf creates a Work Item and starts an Agent Run on it =="
CREATED="$(as_author work create "$REPO" --type feature --goal "credit a balance" \
	--acceptance "Credit adds to a balance" --gate documentation)" || fail "work create failed"
KEY="$(echo "$CREATED" | grep -oE '[A-Z]+-[0-9]+' | head -1)"
[ -n "$KEY" ] || fail "work create printed no key: $CREATED"
as_author work get "$REPO" "$KEY" | grep -q "Credit adds to a balance" || fail "$KEY lost its acceptance criteria"
as_author agent create "cli-implementer" >/dev/null || fail "agent create failed"
STARTED="$(as_author agent start "$REPO" "$KEY")" || fail "agent start failed"
RUN_ID="$(echo "$STARTED" | awk '{print $1}')"
echo "$STARTED" | grep -q "agents/$KEY/" || fail "the Agent Run is not on its granted branch: $STARTED"
as_author agent get "$RUN_ID" | grep -q "$RUN_ID" || fail "agent get $RUN_ID failed"
ok "Agent Run $RUN_ID started on $KEY"

echo "== 4. nf opens an Engineering Run and inspects its gates =="
OPENED="$(as_author run create "$REPO" --title "Credit a balance" --source feature/credit --work-item "$KEY")" ||
	fail "run create failed"
NUMBER="$(echo "$OPENED" | awk '{print $1}' | tr -d '#')"
[ -n "$NUMBER" ] || fail "run create printed no number: $OPENED"
GATES="$(as_author run gates "$REPO" "$NUMBER")" || fail "gates did not pass: $GATES"
echo "$GATES" | grep -q "documentation	pass" || fail "the documentation gate did not pass: $GATES"
as_author run proof "$REPO" "$NUMBER" | grep -q "documentation	pass" || fail "no gate proof recorded on #$NUMBER"
ok "run #$NUMBER gates: $(echo "$GATES" | tr '\t\n' ' ')"

echo "== 5. nf refuses a self-approval and an unreviewed merge, then records an independent review =="
if as_author run review "$REPO" "$NUMBER" --verdict approve --source-sha "$FEATURE_SHA" >/dev/null 2>&1; then
	fail "the author approved their own run"
fi
if OUT="$(as_author run merge "$REPO" "$NUMBER" 2>&1)"; then
	fail "an unreviewed run merged: $OUT"
fi
echo "$OUT" | grep -q "independent" || fail "the merge was refused for an unexpected reason: $OUT"
as_reviewer run review "$REPO" "$NUMBER" --verdict approve --source-sha "$FEATURE_SHA" --summary "documented and small" >/dev/null ||
	fail "the reviewer's approval was refused"
ok "independent approval recorded"

echo "== 6. nf merges and main carries the change =="
as_author run merge "$REPO" "$NUMBER" | grep -q "merged #$NUMBER" || fail "merge failed"
as_author repo log "$REPO" main | grep -q "credit a balance" || fail "main does not carry the merged change"
as_author run list "$REPO" | grep -qE "#${NUMBER}[[:space:]]+merged" || fail "run #$NUMBER is not merged"
ok "run #$NUMBER merged into main"

echo
echo "PASS: nf drives repository, push, Work Item, Agent Run, Engineering Run, gates, review and merge on the kw cluster."
