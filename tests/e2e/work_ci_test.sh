#!/usr/bin/env bash
# work_ci_test.sh — prove the Work Item and CI halves on the live cluster: a
# Work Item is created and assigned, a push schedules a CI run from the
# repository's workflow file, a runner executes it, and its log and artifact
# come back.
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

XDG_CONFIG_HOME="$(mktemp -d)"
export XDG_CONFIG_HOME
USER="ci$RANDOM$$"
ORG="ciorg$RANDOM$$"
# Every run creates its own organization so runs cannot see each other's
# data; remove it on exit, pass or fail, or the cluster fills with them.
# NF_KEEP_TEST_DATA=1 keeps it for debugging a failure.
cleanup_org() { [ -n "${NF_KEEP_TEST_DATA:-}" ] || ./hack/delete-org.sh "$ORG" "http://${EDGE_IP:-}:${EDGE_PORT:-8080}" "${XDG_CONFIG_HOME:-}" >/dev/null 2>&1 || true; }
trap cleanup_org EXIT
REPO="pipeline$RANDOM"
go build -o /tmp/nf ./cmd/nf

echo "== 1. account, organization and repository =="
curl -fsS -X POST "http://$EDGE_IP:$EDGE_PORT/api/v1/auth/register" \
	-H 'Content-Type: application/json' \
	-d "{\"email\":\"$USER@example.com\",\"username\":\"$USER\",\"password\":\"correct horse battery staple\"}" \
	>/dev/null || fail "register failed"
/tmp/nf login --server "http://$EDGE_IP:$EDGE_PORT" --username "$USER" --password "correct horse battery staple" >/dev/null || fail "login failed"
/tmp/nf org create "$ORG" >/dev/null || fail "org create failed"
/tmp/nf org use "$ORG" >/dev/null
/tmp/nf repo create "$REPO" >/dev/null || fail "repo create failed"
ok "created $ORG/$REPO"

echo "== 2. a Work Item is created through the API =="
/tmp/nf work create "$REPO" --type feature --goal "add a pipeline" || fail "work create failed"
/tmp/nf work list "$REPO" | grep -q "add a pipeline" || fail "the Work Item is not listed"
ok "Work Item created and listed"

echo "== 3. a runner is registered into this organization =="
# Runners register into one organization, because organizations are a hard
# security boundary. The test therefore provisions its own rather than relying
# on a platform-wide runner, which would be a way across that boundary.
ORG_ID="$(curl -fsS "http://$EDGE_IP:$EDGE_PORT/api/v1/orgs/$ORG" \
	-H "Authorization: Bearer $(python3 -c "import json,os;print(json.load(open(os.environ['XDG_CONFIG_HOME']+'/novaforge/config.json'))['token'])")" |
	python3 -c 'import json,sys;print(json.load(sys.stdin)["id"])')"
[ -n "$ORG_ID" ] || fail "could not resolve the organization id"
# Create only the runner rather than upgrading the release: a helm upgrade
# restarts every deployment, which costs more than the test's budget and proves
# nothing about CI.
IMG_TAG="$(kubectl --context "$KUBE_CONTEXT" -n "$NS" get deploy "$REL-ci-runner" -o jsonpath='{.spec.template.spec.containers[0].image}' | sed 's/.*://')"
kubectl --context "$KUBE_CONTEXT" -n "$NS" apply -f - >/dev/null <<YAML || fail "creating the runner failed"
apiVersion: apps/v1
kind: Deployment
metadata:
  name: e2e-runner
spec:
  replicas: 1
  selector:
    matchLabels: {app: e2e-runner}
  template:
    metadata:
      labels: {app: e2e-runner}
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
            - {name: RUNNER_NAME, value: "e2e-runner"}
            - {name: CI_DEFAULT_JOB_IMAGE, value: "192.168.10.131/novaforge/runner:$IMG_TAG"}
YAML
trap 'kubectl --context "$KUBE_CONTEXT" -n "$NS" delete deploy e2e-runner --ignore-not-found >/dev/null 2>&1; cleanup_org' EXIT
kubectl --context "$KUBE_CONTEXT" -n "$NS" rollout status deploy/e2e-runner --timeout=180s >/dev/null || fail "the runner did not become ready"
ok "runner registered into $ORG_ID"

echo "== 4. a secret is registered through the API, and no read returns it =="
TOKEN="$(python3 -c "import json,os;print(json.load(open(os.environ['XDG_CONFIG_HOME']+'/novaforge/config.json'))['token'])")"
# Registering a secret must never echo or return its value. What a job receives
# is not this value: the broker mints a credential from the configured provider
# and refuses to hand a stored secret back as an expiring one, which
# tests/e2e/secrets_test.sh proves against the organization the operator
# configured a binding for. A binding cannot exist for the throwaway
# organization this suite creates, so the brokered path is not exercised here.
SECRET_VALUE="nfe2e-$(python3 -c 'import secrets;print(secrets.token_hex(16))')"
PUT="$(curl -fsS -X POST "http://$EDGE_IP:$EDGE_PORT/api/v1/orgs/$ORG/secrets" \
	-H "Authorization: Bearer $TOKEN" -H 'Content-Type: application/json' \
	-d "{\"name\":\"NF_E2E_TOKEN\",\"environment\":\"staging\",\"value\":\"$SECRET_VALUE\"}")" ||
	fail "registering a secret failed"
printf '%s' "$PUT" | grep -q "$SECRET_VALUE" && fail "registering a secret echoed its value: $PUT"
LISTED="$(curl -fsS "http://$EDGE_IP:$EDGE_PORT/api/v1/orgs/$ORG/secrets" -H "Authorization: Bearer $TOKEN")" ||
	fail "listing secrets failed"
printf '%s' "$LISTED" | grep -q NF_E2E_TOKEN || fail "the registered secret is not listed: $LISTED"
printf '%s' "$LISTED" | grep -q "$SECRET_VALUE" && fail "listing secrets returned a value"
ok "secret NF_E2E_TOKEN registered; no read returns its value"

echo "== 5. pushing a workflow schedules a CI run =="
WORK="$(mktemp -d)"
git clone "http://$USER:$TOKEN@$GIT_IP:8081/$ORG/$REPO.git" "$WORK/repo" 2>/dev/null || fail "clone failed"
cd "$WORK/repo"
mkdir -p .novaforge
cat >.novaforge/workflow.yaml <<YAML
jobs:
  build:
    run: |
      echo "hello from novaforge ci"
      sleep 30
      echo "build finished"
      mkdir -p out && echo "artifact body" > out/report.txt
    artifacts:
      - out/report.txt
  test_history:
    image: 192.168.10.131/novaforge/work-reviews:$IMG_TAG
    run: |
      cd history-fixture
      if go test -json -count=2; then exit 1; fi
  z_benchmark:
    image: 192.168.10.131/novaforge/work-reviews:$IMG_TAG
    run: sh benchmark-fixture/run.sh "$IMG_TAG"
    artifacts: [benchmark-fixture/benchmarks.txt]
YAML
git config user.email ci@example.com
git config user.name "CI E2E"
# A deterministic flaky test: identical code passes once, then fails on its
# second invocation. The job expects that failure; maintenance must use the
# individual JSON outcomes, not infer test success from the job's success.
mkdir -p history-fixture
printf 'module example.com/history\n\ngo 1.24\n' >history-fixture/go.mod
printf 'package history\nimport "testing"\nvar calls int\nfunc TestFlaky(t *testing.T) { calls++; if calls == 2 { t.Fatal("second invocation fails") } }\n' >history-fixture/flaky_test.go
# Benchmark fixture is shared with the second-run regression assertion below.
source "$OLDPWD/tests/e2e/benchmark_probe.sh"
create_benchmark_fixture
git add .novaforge/workflow.yaml history-fixture benchmark-fixture
git commit -q -m "ci: add a workflow"
git push -q origin HEAD:main || fail "push failed"
PUSHED="$(git rev-parse HEAD)"
cd - >/dev/null
ok "pushed $PUSHED with a workflow"

API="http://$EDGE_IP:$EDGE_PORT/api/v1/orgs/$ORG/repos/$REPO/ci"
AUTH="Authorization: Bearer $TOKEN"

echo "== 6. the job log is readable while the job runs =="
# The job prints a line and then sleeps, so a read that finds that line while
# the job still reports running is a read of a live log — not of a log sealed
# after the fact, which is all this step could see before logs streamed.
LIVE=""
for _ in $(seq 1 60); do
	RUN_ID="$(curl -fsS -H "$AUTH" "$API/runs" | python3 -c 'import json,sys;r=json.load(sys.stdin)["runs"];print(r[0]["id"] if r else "")')"
	if [ -n "$RUN_ID" ]; then
		JOB="$(curl -fsS -H "$AUTH" "$API/runs/$RUN_ID" | python3 -c 'import json,sys;j=json.load(sys.stdin)["jobs"];print(j[0]["id"]+" "+j[0]["status"] if j else "")')"
		JOB_ID="${JOB%% *}"
		JOB_STATE="${JOB##* }"
		if [ "$JOB_STATE" = "running" ]; then
			LINES="$(curl -fsS -H "$AUTH" "$API/jobs/$JOB_ID/logs")"
			if echo "$LINES" | grep -q "hello from novaforge ci"; then
				echo "$LINES" | grep -q "build finished" && fail "the log already has the job's last line; it was not read while running"
				STILL="$(curl -fsS -H "$AUTH" "$API/runs/$RUN_ID" | python3 -c 'import json,sys;print(json.load(sys.stdin)["jobs"][0]["status"])')"
				[ "$STILL" = "running" ] && LIVE=1 && break
			fi
		fi
	fi
	sleep 2
done
[ -n "$LIVE" ] || fail "the job's output was never readable while the job was running"
ok "read the running job's log before it finished"

echo "== 7. the run reaches a terminal state =="
STATE=""
for _ in $(seq 1 60); do
	STATE="$(/tmp/nf ci runs "$REPO" 2>/dev/null | head -1 | awk '{print $2}' || true)"
	case "$STATE" in
	success | failure) break ;;
	esac
	sleep 5
done
[ -n "$STATE" ] || fail "no CI run appeared within 300s"
ok "run reached state: $STATE"
[ "$STATE" = "success" ] || fail "the run did not succeed"

echo "== 8. the job log and artifact are retrievable =="
/tmp/nf ci logs "$REPO" | grep -q "hello from novaforge ci" || fail "the job log does not contain the command's output"
/tmp/nf ci logs "$REPO" | grep -q "build finished" || fail "the sealed job log is missing the job's last line"
/tmp/nf ci artifacts "$REPO" | grep -q "report.txt" || fail "the artifact is not listed"
ok "log and artifact retrievable"

echo "== 9. the artifact's content downloads through the API =="
ART_ID="$(curl -fsS -H "$AUTH" "$API/jobs/$JOB_ID/artifacts" | python3 -c 'import json,sys;a=[x for x in json.load(sys.stdin)["artifacts"] if x["name"]=="report.txt"];print(a[0]["id"] if a else "")')"
[ -n "$ART_ID" ] || fail "the job's artifacts do not list report.txt"
HEADERS="$(mktemp)"
BODY="$(curl -fsS -D "$HEADERS" -H "$AUTH" "$API/artifacts/$ART_ID")" || fail "the artifact download failed"
[ "$BODY" = "artifact body" ] || fail "downloaded artifact content is '$BODY', want 'artifact body'"
grep -qi '^content-disposition: attachment' "$HEADERS" || fail "the artifact is not served as an attachment"
ok "downloaded report.txt intact"

echo "== 10. maintenance reads real test outcomes from CI history =="
MAINT="http://$EDGE_IP:$EDGE_PORT/api/v1/orgs/$ORG/repos/$REPO/maintenance"
curl -fsS -X POST -H "$AUTH" "$MAINT/scan" >/dev/null || fail "maintenance scan failed"
HISTORY="$(curl -fsS -H "$AUTH" "$MAINT" | python3 -c '
import json,sys
for p in json.load(sys.stdin)["proposals"]:
    if "test_history: example.com/history/TestFlaky is flaky" in p["work_item_goal"]:
        assert not p["assignee_id"] and not p["decision"], "proposal must await approval"
        print(p["work_item_key"])
        break
')"
[ -n "$HISTORY" ] || fail "CI test outcomes produced no unapproved maintenance proposal"
ok "flaky-test proposal $HISTORY came from real CI test output"

echo "== 12. maintenance compares real benchmark artifacts across CI runs =="
verify_benchmark_regression

echo "PASS: Work Items and CI work end to end on the kw cluster."
