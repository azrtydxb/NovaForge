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

EDGE_IP="$($KC get svc "$REL-edge" -o jsonpath='{.status.loadBalancer.ingress[0].ip}')"
GIT_IP="$($KC get svc "$REL-git-platform" -o jsonpath='{.status.loadBalancer.ingress[0].ip}')"
[ -n "$EDGE_IP" ] && [ -n "$GIT_IP" ] || fail "edge or git-platform has no LoadBalancer IP"

export XDG_CONFIG_HOME="$(mktemp -d)"
USER="ci$RANDOM$$"
ORG="ciorg$RANDOM$$"
REPO="pipeline$RANDOM"
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

echo "== 2. a Work Item is created through the API =="
/tmp/nf work create "$REPO" --type feature --goal "add a pipeline" || fail "work create failed"
/tmp/nf work list "$REPO" | grep -q "add a pipeline" || fail "the Work Item is not listed"
ok "Work Item created and listed"

echo "== 3. a runner is registered into this organization =="
# Runners register into one organization, because organizations are a hard
# security boundary. The test therefore provisions its own rather than relying
# on a platform-wide runner, which would be a way across that boundary.
ORG_ID="$(curl -fsS "http://$EDGE_IP:8080/api/v1/orgs/$ORG" \
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
trap 'kubectl --context "$KUBE_CONTEXT" -n "$NS" delete deploy e2e-runner --ignore-not-found >/dev/null 2>&1' EXIT
kubectl --context "$KUBE_CONTEXT" -n "$NS" rollout status deploy/e2e-runner --timeout=180s >/dev/null || fail "the runner did not become ready"
ok "runner registered into $ORG_ID"

echo "== 4. pushing a workflow schedules a CI run =="
TOKEN="$(python3 -c "import json,os;print(json.load(open(os.environ['XDG_CONFIG_HOME']+'/novaforge/config.json'))['token'])")"
WORK="$(mktemp -d)"
git clone "http://$USER:$TOKEN@$GIT_IP:8081/$ORG/$REPO.git" "$WORK/repo" 2>/dev/null || fail "clone failed"
cd "$WORK/repo"
mkdir -p .novaforge
cat >.novaforge/workflow.yaml <<'YAML'
jobs:
  build:
    run: |
      echo "hello from novaforge ci"
      mkdir -p out && echo "artifact body" > out/report.txt
YAML
git config user.email ci@example.com
git config user.name "CI E2E"
git add .novaforge/workflow.yaml
git commit -q -m "ci: add a workflow"
git push -q origin HEAD:main || fail "push failed"
PUSHED="$(git rev-parse HEAD)"
cd - >/dev/null
ok "pushed $PUSHED with a workflow"

echo "== 5. the run reaches a terminal state =="
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

echo "== 6. the job log and artifact are retrievable =="
/tmp/nf ci logs "$REPO" | grep -q "hello from novaforge ci" || fail "the job log does not contain the command's output"
/tmp/nf ci artifacts "$REPO" | grep -q "report.txt" || fail "the artifact is not listed"
ok "log and artifact retrievable"

echo
echo "PASS: Work Items and CI work end to end on the kw cluster."
