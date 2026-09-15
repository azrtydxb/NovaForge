#!/usr/bin/env bash
# lb-check.sh — verify every NovaForge LoadBalancer address answers, and repair
# the one failure this cluster is known to produce.
#
# On the shared kw cluster, kube-vip holds each service's VIP and Cilium maps
# VIP:port to the service. Twice a Cilium datapath lost the frontend for the
# edge's VIP while the Service, the VIP and the pods were all healthy — traced
# to another project's LoadBalancer Services being created and deleted from the
# same address pool. From outside that is indistinguishable from the platform
# being down: every request times out and nothing logs an error. A no-op
# annotation update makes Cilium re-sync the Service; if the address still does
# not answer after that, this fails loudly instead of letting the e2e suites
# report a dozen unrelated failures.
set -euo pipefail
cd "$(dirname "$0")/.."
source hack/env.sh >/dev/null

NS="${NF_NAMESPACE:-novaforge}"
REL="${REL:-novaforge}"
k() { kubectl --context "$KUBE_CONTEXT" -n "$NS" "$@"; }

# probe SERVICE PORT prints the HTTP status for http://VIP:PORT/ (000 when the
# connection does not complete). Any status means the address routes.
probe() {
	local ip
	ip="$(k get svc "$1" -o jsonpath='{.status.loadBalancer.ingress[0].ip}')"
	curl -sS -m 5 -o /dev/null -w '%{http_code}' "http://$ip:$2/" 2>/dev/null || true
}

failed=0
for target in "$REL-edge 8080" "$REL-git-platform 8081"; do
	set -- $target
	svc="$1" port="$2"
	code="$(probe "$svc" "$port")"
	if [ "$code" = "000" ]; then
		echo "lb-check: $svc:$port does not answer; asking Cilium to re-sync the Service" >&2
		k annotate svc "$svc" novaforge.io/lb-resync="$(date +%s)" --overwrite >/dev/null
		for _ in $(seq 1 12); do
			sleep 5
			code="$(probe "$svc" "$port")"
			[ "$code" != "000" ] && break
		done
	fi
	if [ "$code" = "000" ]; then
		echo "lb-check: FAIL $svc:$port still does not answer" >&2
		failed=1
	else
		echo "lb-check: $svc:$port answers ($code)"
	fi
done
exit "$failed"
