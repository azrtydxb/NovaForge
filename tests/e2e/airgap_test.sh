#!/usr/bin/env bash
# airgap_test.sh — prove on the live cluster that a service calling the model
# gateway can reach the gateway and nothing outside the cluster (spec S-9).
#
# agent-runtime's image has no shell, so the probe runs in an ephemeral debug
# container attached to the running pod. An ephemeral container shares the
# pod's network namespace and therefore its Cilium identity and policy: what
# it can reach is exactly what agent-runtime can reach.
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

POD="$($KC get pods -l "app.kubernetes.io/component=agent-runtime,app.kubernetes.io/instance=$REL" -o jsonpath='{.items[0].metadata.name}')"
[ -n "$POD" ] || fail "no agent-runtime pod"
TAG="$($KC get deploy "$REL-gates" -o jsonpath='{.spec.template.spec.containers[0].image}' | sed 's/.*://')"
# The gates image carries curl; it is already on every node that ran gates.
PROBE_IMAGE="$REGISTRY_PULL/$REGISTRY_REPO/gates:$TAG"
GATEWAY="$($KC get deploy "$REL-agent-runtime" -o jsonpath='{.spec.template.spec.containers[0].env[?(@.name=="AI_ENDPOINT")].value}')"
[ -n "$GATEWAY" ] || fail "agent-runtime has no AI_ENDPOINT"

# probe URL prints the HTTP status the pod's network gets for URL, 000 when the
# connection never completes.
probe() {
	$KC debug "$POD" --image="$PROBE_IMAGE" --target=agent-runtime --profile=general --quiet -i --attach=true -- \
		sh -c "curl -sk -m 8 -o /dev/null -w '%{http_code}' '$1' || true" 2>/dev/null | tr -d '\r' | tail -c 3
}

echo "== 1. agent-runtime reaches the in-cluster model gateway =="
code="$(probe "${GATEWAY%/}/models")"
case "$code" in
000 | "") fail "agent-runtime could not reach its model gateway $GATEWAY" ;;
esac
ok "the gateway answered ($code)"

echo "== 2. agent-runtime reaches nothing outside the cluster =="
for url in https://api.openai.com/v1/models https://api.anthropic.com/v1/messages https://1.1.1.1/; do
	code="$(probe "$url")"
	[ "$code" = "000" ] || fail "agent-runtime reached $url ($code): its egress is not confined to the cluster"
	ok "no route to $url"
done

echo
echo "PASS: agent-runtime is confined to the cluster and its model gateway."
