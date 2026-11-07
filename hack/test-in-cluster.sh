#!/usr/bin/env bash
# test-in-cluster.sh [-- command...] — run a command against this working tree
# in a pod on kw instead of on the workstation. Default: go test ./... -count=1.
#   ./hack/test-in-cluster.sh -- go test ./internal/gitops/ -run TestMergeSet -count=1
#   ./hack/test-in-cluster.sh -- go vet ./...
#   ./hack/test-in-cluster.sh --down      # delete the pod (the cache volume stays)
#
# Builds and tests do not belong on the workstation: its Go cache had grown to
# 105 GB and filled the disk, which crashed a write part-way through. The pod
# keeps GOCACHE and GOMODCACHE on a cluster volume, so a rerun is incremental,
# and it reaches the dev datastores by their in-cluster names rather than
# through the workstation's VPN (see e2e-in-cluster.sh for why that matters).
#
# The tree sent is the working tree, uncommitted and untracked files included
# (git ls-files honours .gitignore), because the red/green loop runs before a
# commit exists. CI remains the gate: it runs the full suite on every push.
set -euo pipefail
cd "$(dirname "$0")/.."
source hack/env.sh >/dev/null 2>&1 || true
NS="${NF_NAMESPACE:-novaforge}"
REL="${REL:-novaforge}"
RUNNER_NS="novaforge-test"
POD=runner
k() { kubectl --context "$KUBE_CONTEXT" "$@"; }

if [ "${1:-}" = "--down" ]; then
	k -n "$RUNNER_NS" delete pod "$POD" --ignore-not-found --wait=false
	exit 0
fi
[ "${1:-}" = "--" ] && shift
cmd=("$@")
[ ${#cmd[@]} -eq 0 ] && cmd=(go test ./... -count=1)

# The gates image carries go, git, gcc and the analysis tools the suites call.
TAG="$(k -n "$NS" get deploy "$REL-gates" -o jsonpath='{.spec.template.spec.containers[0].image}' | sed 's/.*://')"
IMAGE="$REGISTRY_PULL/$REGISTRY_REPO/gates:$TAG"
DEV="${NF_DEV_NAMESPACE:-novaforge-dev}"

if [ "$(k -n "$RUNNER_NS" get pod "$POD" -o jsonpath='{.status.phase}' 2>/dev/null || true)" != Running ] ||
	[ "$(k -n "$RUNNER_NS" get pod "$POD" -o jsonpath='{.spec.containers[0].image}' 2>/dev/null || true)" != "$IMAGE" ]; then
	k -n "$RUNNER_NS" delete pod "$POD" --ignore-not-found --wait=true >/dev/null 2>&1 || true
	k create namespace "$RUNNER_NS" --dry-run=client -o yaml | k apply -f - >/dev/null
	k -n "$NS" get secret nexus-pull -o json |
		python3 -c 'import json,sys; s=json.load(sys.stdin); m=s["metadata"]; s["metadata"]={"name":m["name"],"namespace":"'"$RUNNER_NS"'"}; print(json.dumps(s))' |
		k apply -f - >/dev/null
	k apply -f - >/dev/null <<YAML
apiVersion: v1
kind: PersistentVolumeClaim
metadata: {name: go-cache, namespace: $RUNNER_NS}
spec:
  accessModes: [ReadWriteOnce]
  storageClassName: longhorn
  resources: {requests: {storage: 40Gi}}
---
apiVersion: v1
kind: Pod
metadata: {name: $POD, namespace: $RUNNER_NS}
spec:
  restartPolicy: Never
  # The suites never call the Kubernetes API; no token is mounted.
  automountServiceAccountToken: false
  imagePullSecrets: [{name: nexus-pull}]
  containers:
    - name: runner
      image: $IMAGE
      command: ["sleep", "infinity"]
      env:
        - {name: GOCACHE, value: /cache/go-build}
        - {name: GOMODCACHE, value: /cache/mod}
        - {name: GOFLAGS, value: -buildvcs=false}
        - {name: TEST_DATABASE_URL, value: "postgres://novaforge:novaforge@postgres.$DEV.svc:5432/novaforge?sslmode=disable"}
        - {name: TEST_REDIS_URL, value: "redis://redis.$DEV.svc:6379"}
        - {name: TEST_S3_ENDPOINT, value: "minio.$DEV.svc:9000"}
        - {name: TEST_S3_ACCESS_KEY, value: "${TEST_S3_ACCESS_KEY:-}"}
        - {name: TEST_S3_SECRET_KEY, value: "${TEST_S3_SECRET_KEY:-}"}
        - {name: TEST_S3_BUCKET, value: "${TEST_S3_BUCKET:-novaforge-test}"}
        - {name: GIT_AUTHOR_NAME, value: test}
        - {name: GIT_AUTHOR_EMAIL, value: test@novaforge.local}
        - {name: GIT_COMMITTER_NAME, value: test}
        - {name: GIT_COMMITTER_EMAIL, value: test@novaforge.local}
      volumeMounts: [{name: cache, mountPath: /cache}]
      resources:
        requests: {cpu: "1", memory: 2Gi}
        limits: {cpu: "4", memory: 8Gi}
  volumes:
    - name: cache
      persistentVolumeClaim: {claimName: go-cache}
YAML
	k -n "$RUNNER_NS" wait --for=condition=Ready pod/"$POD" --timeout=300s >/dev/null
fi

# retry rides out a dropped link to the API server; the work itself runs
# detached in the pod and only these short calls cross the link.
retry() {
	for _ in $(seq 1 20); do
		if k -n "$RUNNER_NS" exec "$POD" -- sh -c "$1" 2>/dev/null; then
			return 0
		fi
		sleep 5
	done
	return 1
}

git ls-files -co --exclude-standard -z | COPYFILE_DISABLE=1 tar --no-xattrs --no-mac-metadata --null -T - -cf - |
	k -n "$RUNNER_NS" exec -i "$POD" -- sh -c 'rm -rf /src && mkdir -p /src && tar -x -C /src'

run_id="$(date +%s)$$"
quoted="$(printf '%q ' "${cmd[@]}")"
retry "cd /src && nohup sh -c '$quoted >/tmp/$run_id.log 2>&1; echo \$? >/tmp/$run_id.rc' >/dev/null 2>&1 &" || {
	echo "could not start the command in the pod" >&2
	exit 1
}

# Stream the log as it grows, then exit with the command's status.
off=0
while :; do
	chunk="$(k -n "$RUNNER_NS" exec "$POD" -- sh -c "tail -c +$((off + 1)) /tmp/$run_id.log 2>/dev/null" 2>/dev/null || true)"
	if [ -n "$chunk" ]; then
		printf '%s\n' "$chunk"
		off=$((off + $(printf '%s\n' "$chunk" | wc -c)))
	fi
	rc="$(k -n "$RUNNER_NS" exec "$POD" -- sh -c "cat /tmp/$run_id.rc 2>/dev/null" 2>/dev/null || true)"
	if [ -n "$rc" ]; then
		rest="$(k -n "$RUNNER_NS" exec "$POD" -- sh -c "tail -c +$((off + 1)) /tmp/$run_id.log 2>/dev/null" 2>/dev/null || true)"
		[ -n "$rest" ] && printf '%s\n' "$rest"
		exit "$rc"
	fi
	sleep 3
done
