#!/usr/bin/env bash
# secrets_test.sh — prove a CI job receives a credential minted by the dynamic
# provider, and that the platform never held that credential itself (spec S-12).
#
# This is deliberately a separate suite rather than a step in work_ci. A binding
# is operator-owned and keyed on (organization, environment, secret name), and
# the broker reads its bindings once at startup so that no request can introduce
# one. Every other suite creates a throwaway organization per run, which means no
# binding can exist for it — so the brokered path cannot be exercised under that
# model at all. This suite uses the organization the operator configured a
# binding for, and touches nothing else in it.
#
# What used to be asserted, in work_ci, was that the job's credential hashed to
# the value someone had registered. That is the behaviour the broker deliberately
# no longer has: it mints from a provider and refuses to hand back a stored
# secret dressed as an expiring one. A test that demanded the stored value back
# was pinning the weakness rather than the fix.
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

EDGE_IP="${NF_EDGE_IP:-$($KC get svc "$REL-edge" -o jsonpath='{.status.loadBalancer.ingress[0].ip}')}"
EDGE_PORT="${NF_EDGE_PORT:-8080}"
GIT_IP="$($KC get svc "$REL-git-platform" -o jsonpath='{.status.loadBalancer.ingress[0].ip}')"
[ -n "$EDGE_IP" ] && [ -n "$GIT_IP" ] || fail "edge or git-platform has no LoadBalancer IP"
API="http://$EDGE_IP:$EDGE_PORT/api/v1"

# The organization the operator's binding names. Its account is long-lived
# because the binding is: both are configuration, not data this run creates.
ORG="${NF_SECRETS_ORG:-nfsecrets}"
USER="${NF_SECRETS_USER:-nfsecrets}"
PASSWORD="correct horse battery staple"
SECRET_NAME="${NF_SECRETS_NAME:-NF_CI_CERT}"
REPO="brokered$RANDOM$$"

XDG_CONFIG_HOME="$(mktemp -d)"
export XDG_CONFIG_HOME
go build -o /tmp/nf ./cmd/nf

# Only the repository this run made is removed. Deleting the organization would
# delete the operator's binding subject along with it.
cleanup() {
	[ -n "${NF_KEEP_TEST_DATA:-}" ] || curl -fsS -X DELETE "$API/orgs/$ORG/repos/$REPO" \
		-H "Authorization: Bearer ${TOKEN:-}" >/dev/null 2>&1 || true
	kubectl --context "$KUBE_CONTEXT" -n "$NS" delete deploy secrets-e2e-runner --ignore-not-found >/dev/null 2>&1 || true
}
trap cleanup EXIT

echo "== 1. the configured organization and a repository for this run =="
/tmp/nf login --server "http://$EDGE_IP:$EDGE_PORT" --username "$USER" --password "$PASSWORD" >/dev/null ||
	fail "login as $USER failed; the brokered-credential organization is provisioned by the operator"
TOKEN="$(python3 -c "import json,os;print(json.load(open(os.environ['XDG_CONFIG_HOME']+'/novaforge/config.json'))['token'])")"
/tmp/nf org use "$ORG" >/dev/null || fail "organization $ORG is not available to $USER"
/tmp/nf repo create "$REPO" >/dev/null || fail "repo create failed"
ORG_ID="$(curl -fsS "$API/orgs/$ORG" -H "Authorization: Bearer $TOKEN" |
	python3 -c 'import json,sys;print(json.load(sys.stdin)["id"])')"
[ -n "$ORG_ID" ] || fail "could not resolve the organization id"
ok "$ORG/$REPO ($ORG_ID)"

echo "== 2. the credential is offered by a binding, and this run stores nothing =="
# A listed name is not a stored value. The listing merges the provider's
# bindings with the organization's stored secrets on purpose, so a member can
# see what they may ask for; secrets.secret_values holds nothing for a name that
# only a binding provides. This suite never registers a value, so whatever the
# job receives below cannot have come from one.
LISTED="$(curl -fsS "$API/orgs/$ORG/secrets" -H "Authorization: Bearer $TOKEN")" || fail "listing secrets failed"
printf '%s' "$LISTED" | grep -q "$SECRET_NAME" ||
	fail "$SECRET_NAME is not offered in $ORG; the operator's binding is missing (see deploy/dev/openbao.yaml): $LISTED"
printf '%s' "$LISTED" | python3 -c '
import json, sys
# Names only, never values: the listing is a catalogue, not a read.
for s in json.load(sys.stdin)["secrets"]:
    if set(s) - {"name", "environment"}:
        raise SystemExit("listing returned more than a name and environment: %r" % s)
' || fail "the secret listing returned more than names: $LISTED"
ok "$SECRET_NAME is offered by a binding; this run registers no value"

echo "== 3. a runner is registered into this organization =="
IMG_TAG="$($KC get deploy "$REL-ci-runner" -o jsonpath='{.spec.template.spec.containers[0].image}' | sed 's/.*://')"
kubectl --context "$KUBE_CONTEXT" -n "$NS" apply -f - >/dev/null <<YAML || fail "creating the runner failed"
apiVersion: apps/v1
kind: Deployment
metadata:
  name: secrets-e2e-runner
spec:
  replicas: 1
  selector:
    matchLabels: {app: secrets-e2e-runner}
  template:
    metadata:
      labels: {app: secrets-e2e-runner}
    spec:
      serviceAccountName: $REL-runner
      imagePullSecrets:
        - name: nexus-pull
      containers:
        - name: runner
          image: 192.168.10.131/novaforge/runner:$IMG_TAG
          envFrom:
            - secretRef: {name: $REL-secrets}
          env:
            - {name: CI_ADDR, value: "$REL-ci-runner:9094"}
            - {name: RUNNER_ORG_ID, value: "$ORG_ID"}
            - {name: RUNNER_LABELS, value: "linux"}
            - {name: RUNNER_JOB_NAMESPACE, value: "$NS"}
            - {name: RUNNER_NAME, value: "secrets-e2e-runner"}
            - {name: CI_DEFAULT_JOB_IMAGE, value: "192.168.10.131/novaforge/runner:$IMG_TAG"}
YAML
kubectl --context "$KUBE_CONTEXT" -n "$NS" rollout status deploy/secrets-e2e-runner --timeout=180s >/dev/null ||
	fail "the runner did not become ready"
ok "runner registered into $ORG_ID"

echo "== 4. a job asks for the credential =="
WORK="$(mktemp -d)"
git clone -q "http://$USER:$TOKEN@$GIT_IP:8081/$ORG/$REPO.git" "$WORK/repo" 2>/dev/null || fail "clone failed"
cd "$WORK/repo"
mkdir -p .novaforge
# The job proves what it received without printing it: a PKI credential is a
# PEM private key, so its first line is structural and its body is not.
cat >.novaforge/workflow.yaml <<YAML
jobs:
  brokered:
    secrets: [$SECRET_NAME]
    run: |
      if [ -z "\$$SECRET_NAME" ]; then echo "no credential delivered"; exit 1; fi
      printf %s "\$$SECRET_NAME" | head -1 | grep -q "BEGIN .*PRIVATE KEY" || { echo "credential is not a minted key"; exit 1; }
      echo "credential bytes: \$(printf %s "\$$SECRET_NAME" | wc -c)"
      echo "brokered credential received"
      # A job that prints its credential must not put it in the stored log.
      echo "careless print: \$$SECRET_NAME"
YAML
git config user.email secrets@example.com
git config user.name "Secrets E2E"
git add -A
git commit -q -m "ci: a job that needs a brokered credential"
git push -q origin HEAD:main || fail "push failed"
cd - >/dev/null
ok "pushed a workflow needing $SECRET_NAME"

echo "== 5. the job runs and reports what it got =="
CI="$API/orgs/$ORG/repos/$REPO/ci"
AUTH="Authorization: Bearer $TOKEN"
STATE=""
for _ in $(seq 1 60); do
	STATE="$(/tmp/nf ci runs "$REPO" 2>/dev/null | head -1 | awk '{print $2}' || true)"
	case "$STATE" in
	success | failure) break ;;
	esac
	sleep 5
done
LOGS="$(/tmp/nf ci logs "$REPO" 2>&1 || true)"
case "$LOGS" in
*"no dynamic credential provider binding configured"*)
	fail "the deployment has no provider binding for $ORG/$SECRET_NAME (see NF_OPENBAO_CONFIG_FILE and deploy/dev/openbao.yaml)"
	;;
esac
[ "$STATE" = "success" ] || fail "the brokered job did not succeed ($STATE): $LOGS"
printf '%s' "$LOGS" | grep -q "brokered credential received" || fail "the job did not confirm the credential: $LOGS"
ok "the job received a minted credential the platform never stored"

echo "== 6. the credential is masked in the stored log, and its lease is recorded =="
RUN_ID="$(curl -fsS "$CI/runs" -H "$AUTH" |
	python3 -c 'import json,sys;print(json.load(sys.stdin)["runs"][0]["id"])')" || fail "listing CI runs failed"
JOB_ID="$(curl -fsS "$CI/runs/$RUN_ID" -H "$AUTH" |
	python3 -c 'import json,sys;print(next(j["id"] for j in json.load(sys.stdin)["jobs"] if j["name"]=="brokered"))')" ||
	fail "the brokered job is not part of the run"
JOB_LOG="$(curl -fsS "$CI/jobs/$JOB_ID/logs" -H "$AUTH")" || fail "reading the job log failed"
printf '%s' "$JOB_LOG" | grep -q "PRIVATE KEY" &&
	fail "the minted credential appears in the stored job log"
printf '%s' "$JOB_LOG" | grep -q 'careless print: \*\*\*' ||
	fail "the careless print was not masked: $JOB_LOG"
LEASES="$(curl -fsS "$API/orgs/$ORG/leases" -H "$AUTH")" || fail "listing leases failed"
printf '%s' "$LEASES" | grep -q "$JOB_ID" || fail "no lease was recorded for the job: $LEASES"
ok "credential masked in the log, lease recorded"

echo
echo "PASS: a CI job receives a credential minted by the dynamic provider on the kw cluster."
