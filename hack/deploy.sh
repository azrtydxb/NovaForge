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

# The gates service runs repository code inside the analysis sandbox image and
# refuses to run executable gates without one, so the deployment must carry it.
# The chart demands a digest, not a tag: a gate's verdict is reproducible only
# if the tools that produced it are exactly the ones that were qualified. The
# digest is read from the registry rather than written down, so it always
# describes the image actually published at this tag.
#
# This is checked like the model credential above, and for the same reason: a
# deployment with no sandbox comes up healthy, serves the whole API, and fails
# every merge gate with "isolated analysis sandbox is not configured", which
# reads like a platform outage rather than a missing setting.
ANALYSIS_DIGEST="$(curl -sk -o /dev/null -D - \
	-u "$REGISTRY_USER:$REGISTRY_PASSWORD" \
	-H 'Accept: application/vnd.oci.image.manifest.v1+json' \
	"https://$REGISTRY_PUSH/v2/$REGISTRY_REPO/gate-analysis/manifests/$TAG" |
	tr -d '\r' | awk -F': ' 'tolower($1)=="docker-content-digest"{print $2}')"
case "$ANALYSIS_DIGEST" in
sha256:????????????????????????????????????????????????????????????????) ;;
*)
	echo "no gate-analysis image at tag $TAG (got digest \"$ANALYSIS_DIGEST\")" >&2
	echo "run: ./hack/build-images.sh gate-analysis" >&2
	exit 1
	;;
esac
ANALYSIS_IMAGE="$REGISTRY_PULL/$REGISTRY_REPO/gate-analysis@$ANALYSIS_DIGEST"
echo "gate analysis sandbox: $ANALYSIS_IMAGE"

helm --kube-context "$KUBE_CONTEXT" upgrade --install "$REL" deploy/helm/novaforge \
	--namespace "$NS" \
	--set image.tag="$TAG" \
	--set ai.apiKey="${AI_API_KEY:?set AI_API_KEY (hack/env.local.sh) — the model gateway rejects unauthenticated calls}" \
	--set services.gates.analysisImage="$ANALYSIS_IMAGE" \
	--wait --timeout 15m "$@"

kubectl --context "$KUBE_CONTEXT" -n "$NS" get pods

# The addresses the platform is reached at must actually answer (see lb-check.sh).
./hack/lb-check.sh
