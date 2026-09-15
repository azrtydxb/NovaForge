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

# Every service in the chart must already have an image at this tag, or the
# rollout half-applies and sits in ImagePullBackOff. Failing here says which
# service was not built, instead of leaving that to be read off pod events.
missing=""
for svc in $(python3 -c "
import sys, yaml
print(' '.join(yaml.safe_load(open('deploy/helm/novaforge/values.yaml'))['services']))
"); do
	code=$(curl -sk -o /dev/null -w '%{http_code}' \
		-u "$REGISTRY_USER:$REGISTRY_PASSWORD" \
		-H 'Accept: application/vnd.oci.image.manifest.v1+json' \
		"https://$REGISTRY_PUSH/v2/$REGISTRY_REPO/$svc/manifests/$TAG")
	[ "$code" = "200" ] || missing="$missing $svc"
done
if [ -n "$missing" ]; then
	echo "no image at tag $TAG for:$missing" >&2
	echo "run: ./hack/build-images.sh$missing" >&2
	exit 1
fi

helm --kube-context "$KUBE_CONTEXT" upgrade --install "$REL" deploy/helm/novaforge \
	--namespace "$NS" \
	--set image.tag="$TAG" \
	--set ai.apiKey="${AI_API_KEY:?set AI_API_KEY (hack/env.local.sh) — the model gateway rejects unauthenticated calls}" \
	--wait --timeout 15m "$@"

kubectl --context "$KUBE_CONTEXT" -n "$NS" get pods

# The addresses the platform is reached at must actually answer (see lb-check.sh).
./hack/lb-check.sh
