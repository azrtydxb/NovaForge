#!/usr/bin/env bash
# e2e-in-cluster.sh [suite...] — run the e2e suites from a pod inside the
# cluster instead of from this workstation.
#
# The workstation reaches the cluster's network through a site-to-site VPN
# gateway. Under the e2e suites' bursts of short connections the gateway began
# dropping every connection to one address:port for minutes at a time — the
# edge's load balancer address, then its NodePort — while the same addresses
# answered from every node and pod. From the workstation that looks exactly like
# the platform going down mid-suite. A pod sees the platform as a client on the
# cluster's own network does, with nothing in between.
#
# The pod runs the gates image (it carries go, git, ssh, curl and python3),
# fetches kubectl and helm, receives this commit's tree, and talks to the API
# server as a ServiceAccount bound to cluster-admin in a throwaway namespace
# that is deleted afterwards. The suites use the same kube context name they
# use on the workstation.
set -euo pipefail
cd "$(dirname "$0")/.."
source hack/env.sh >/dev/null

NS="${NF_NAMESPACE:-novaforge}"
REL="${REL:-novaforge}"
RUNNER_NS="novaforge-e2e"
KUBECTL_VERSION="v1.34.4"
HELM_VERSION="v3.19.0"
suites=("$@")
if [ ${#suites[@]} -eq 0 ]; then
	suites=(airgap deploy work_ci gui search graph factory agent agent_ci merge cli crossorg)
fi
k() { kubectl --context "$KUBE_CONTEXT" "$@"; }

TAG="$(k -n "$NS" get deploy "$REL-gates" -o jsonpath='{.spec.template.spec.containers[0].image}' | sed 's/.*://')"
IMAGE="$REGISTRY_PULL/$REGISTRY_REPO/gates:$TAG"

cleanup() {
	k delete namespace "$RUNNER_NS" --ignore-not-found --wait=false >/dev/null 2>&1 || true
	k delete clusterrolebinding novaforge-e2e --ignore-not-found >/dev/null 2>&1 || true
}
trap cleanup EXIT

k create namespace "$RUNNER_NS" --dry-run=client -o yaml | k apply -f - >/dev/null
k -n "$NS" get secret nexus-pull -o json |
	python3 -c 'import json,sys; s=json.load(sys.stdin); m=s["metadata"]; s["metadata"]={"name":m["name"],"namespace":"'"$RUNNER_NS"'"}; print(json.dumps(s))' |
	k apply -f - >/dev/null
k apply -f - >/dev/null <<YAML
apiVersion: v1
kind: ServiceAccount
metadata:
  name: e2e
  namespace: $RUNNER_NS
---
apiVersion: rbac.authorization.k8s.io/v1
kind: ClusterRoleBinding
metadata:
  name: novaforge-e2e
roleRef: {apiGroup: rbac.authorization.k8s.io, kind: ClusterRole, name: cluster-admin}
subjects:
  - {kind: ServiceAccount, name: e2e, namespace: $RUNNER_NS}
---
apiVersion: v1
kind: Pod
metadata:
  name: e2e
  namespace: $RUNNER_NS
spec:
  serviceAccountName: e2e
  restartPolicy: Never
  imagePullSecrets: [{name: nexus-pull}]
  containers:
    - name: e2e
      image: $IMAGE
      command: ["sleep", "infinity"]
      resources:
        requests: {cpu: 500m, memory: 1Gi}
        limits: {cpu: "4", memory: 4Gi}
YAML
k -n "$RUNNER_NS" wait --for=condition=Ready pod/e2e --timeout=300s >/dev/null

# The tree under test is exactly this commit's, plus the untracked local
# credentials file the suites' environment sources.
tmp="$(mktemp -d)"
git archive --format=tar HEAD >"$tmp/tree.tar"
k -n "$RUNNER_NS" exec -i e2e -- sh -c 'mkdir -p /src && tar -x -C /src' <"$tmp/tree.tar"
if [ -f hack/env.local.sh ]; then
	k -n "$RUNNER_NS" exec -i e2e -- sh -c 'cat > /src/hack/env.local.sh' <hack/env.local.sh
fi
rm -rf "$tmp"

k -n "$RUNNER_NS" exec e2e -- sh -ec "
arch=arm64
curl -sSfL -o /usr/local/bin/kubectl https://dl.k8s.io/release/$KUBECTL_VERSION/bin/linux/\$arch/kubectl
chmod +x /usr/local/bin/kubectl
curl -sSfL https://get.helm.sh/helm-$HELM_VERSION-linux-\$arch.tar.gz | tar -xz -C /tmp
mv /tmp/linux-\$arch/helm /usr/local/bin/helm
sa=/var/run/secrets/kubernetes.io/serviceaccount
kubectl config set-cluster in-cluster --server=https://kubernetes.default.svc --certificate-authority=\$sa/ca.crt >/dev/null
kubectl config set-credentials e2e --token=\"\$(cat \$sa/token)\" >/dev/null
kubectl config set-context $KUBE_CONTEXT --cluster=in-cluster --user=e2e >/dev/null
kubectl config use-context $KUBE_CONTEXT >/dev/null
git config --global user.email e2e@novaforge.local
git config --global user.name e2e
"

failed=0
for s in "${suites[@]}"; do
	if k -n "$RUNNER_NS" exec e2e -- sh -c "cd /src && GOFLAGS=-buildvcs=false bash tests/e2e/${s}_test.sh" >"/tmp/e2e.$s.log" 2>&1; then
		echo "$s: PASS — $(tail -1 "/tmp/e2e.$s.log")"
	else
		echo "$s: FAIL — $(grep -m1 '^FAIL' "/tmp/e2e.$s.log" || tail -1 "/tmp/e2e.$s.log")"
		failed=1
	fi
done
exit "$failed"
