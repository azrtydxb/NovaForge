#!/usr/bin/env bash
# deploy.sh — install or upgrade NovaForge on the kw cluster and wait for it.
set -euo pipefail
cd "$(dirname "$0")/.."
source hack/env.sh

NS="${NF_NAMESPACE:-novaforge}"
REL="${REL:-novaforge}"
TAG="${TAG:-$(git rev-parse --short HEAD)}"

kubectl --context "$KUBE_CONTEXT" create namespace "$NS" --dry-run=client -o yaml | kubectl --context "$KUBE_CONTEXT" apply -f -

# The nodes pull from the 443 address, so the pull secret is keyed on it.
kubectl --context "$KUBE_CONTEXT" -n "$NS" create secret docker-registry nexus-pull \
	--docker-server="$REGISTRY_PULL" \
	--docker-username="$REGISTRY_USER" \
	--docker-password="${REGISTRY_PASSWORD:?set REGISTRY_PASSWORD}" \
	--dry-run=client -o yaml | kubectl --context "$KUBE_CONTEXT" apply -f -

helm --kube-context "$KUBE_CONTEXT" upgrade --install "$REL" deploy/helm/novaforge \
	--namespace "$NS" \
	--set image.tag="$TAG" \
	--wait --timeout 15m "$@"

kubectl --context "$KUBE_CONTEXT" -n "$NS" get pods
