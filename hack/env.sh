# NovaForge build/deploy environment (kw cluster)
export KUBE_CONTEXT=kw
export BUILDKIT_HOST=tcp://192.168.10.130:1234
export BK_CERTS="${BK_CERTS:-/private/tmp/claude-501/-Users-pascal-Development-NovaForge/5a8c3920-c543-4e9f-ad19-7fc324dd8240/scratchpad/bkcerts}"
# Nexus is a push/pull split: writes are accepted only on the :5000 hosted
# connector (443 answers 403), while the k3s nodes trust only 192.168.10.131
# on 443, which is their configured pull-through mirror. So images are pushed
# to one address and pulled from another — they are the same registry.
export REGISTRY_PUSH=192.168.10.131:5000
export REGISTRY_PULL=192.168.10.131
export REGISTRY_REPO=novaforge
export REGISTRY_USER=ci
export DOCKER_CONFIG="${DOCKER_CONFIG:-$BK_CERTS/dockercfg}"
export NF_NAMESPACE=novaforge
export NF_DEV_NAMESPACE=novaforge-dev
# The security gate's semgrep ruleset, vendored; the image puts it at
# /opt/analysis/semgrep-gosec.yml.
export NOVAFORGE_SEMGREP_RULES="$(cd "$(dirname "${BASH_SOURCE[0]:-$0}")/.." && pwd)/deploy/analysis/semgrep-gosec.yml"
# Dev datastore endpoints, discovered from the cluster rather than written down
# here. These are plain LoadBalancer services with no pinned address: when the
# namespace was recreated, Cilium handed postgres its previous address but gave
# redis and minio different ones, and the hardcoded values then pointed at
# nothing. That does not fail loudly — every datastore-backed suite calls
# t.Skip when its URL is unset and reports a connection error when the URL is
# wrong, and a whole test run looked green while touching no datastore at all.
# The chart and the e2e scripts already read .status.loadBalancer.ingress[0].ip
# for every service they reach; the test environment now does the same.
nf_dev_lb() {
	kubectl --context "$KUBE_CONTEXT" -n "$NF_DEV_NAMESPACE" get svc "$1" \
		-o jsonpath='{.status.loadBalancer.ingress[0].ip}' 2>/dev/null
}
nf_pg_ip="$(nf_dev_lb postgres)"
nf_redis_ip="$(nf_dev_lb redis)"
nf_minio_ip="$(nf_dev_lb minio)"
# An address that could not be discovered leaves its variable unset, so the
# suites skip instead of failing against a stale address — but say so here,
# because an unnoticed skip is indistinguishable from a pass.
if [ -n "$nf_pg_ip" ]; then
	export TEST_DATABASE_URL="postgres://novaforge:novaforge@$nf_pg_ip:5432/novaforge?sslmode=disable"
else
	echo "hack/env.sh: no LoadBalancer address for $NF_DEV_NAMESPACE/postgres; database suites will SKIP, not pass" >&2
fi
if [ -n "$nf_redis_ip" ]; then
	export TEST_REDIS_URL="redis://$nf_redis_ip:6379"
else
	echo "hack/env.sh: no LoadBalancer address for $NF_DEV_NAMESPACE/redis; Redis suites will SKIP, not pass" >&2
fi
if [ -n "$nf_minio_ip" ]; then
	export TEST_S3_ENDPOINT="$nf_minio_ip:9000"
else
	echo "hack/env.sh: no LoadBalancer address for $NF_DEV_NAMESPACE/minio; blobstore suites will SKIP, not pass" >&2
fi
unset nf_pg_ip nf_redis_ip nf_minio_ip
# The offline vulnerability tests read an explicitly staged OSV snapshot and
# never download one themselves (see hack/stage-advisories.sh). The variable is
# exported only when the file is actually there: pointing it at a missing path
# makes those tests fail on a damaged snapshot instead of saying it is absent.
nf_osv_zip="${NF_ADVISORY_CACHE:-$HOME/.cache/novaforge/osv}/Go-all.zip"
if [ -s "$nf_osv_zip" ]; then
	export NF_TEST_OSV_GO_ZIP="$nf_osv_zip"
else
	echo "hack/env.sh: no staged OSV snapshot; run ./hack/stage-advisories.sh (offline vulnerability tests will FAIL without it)" >&2
fi
unset nf_osv_zip
# The gate tests that run repository code need a real analysis sandbox image,
# recorded by hack/build-images.sh when it builds one. Executable gates cannot
# run without it: the gates service deliberately refuses rather than falling
# back to running repository code in its own process.
nf_gate_image="$HOME/.cache/novaforge/gate-analysis-image"
if [ -s "$nf_gate_image" ]; then
	NF_GATE_SANDBOX_TEST_IMAGE="$(cat "$nf_gate_image")"
	export NF_GATE_SANDBOX_TEST_IMAGE
else
	echo "hack/env.sh: no gate analysis image recorded; run ./hack/build-images.sh gate-analysis (executable-gate tests will SKIP, not pass)" >&2
fi
unset nf_gate_image
export TEST_S3_ACCESS_KEY=minioadmin
export TEST_S3_SECRET_KEY=minioadmin
export TEST_S3_BUCKET=novaforge-test
bk() { buildctl --tlscacert "$BK_CERTS/ca.crt" --tlscert "$BK_CERTS/tls.crt" --tlskey "$BK_CERTS/tls.key" "$@"; }

# Machine-local credentials (registry password, model-gateway API key) live in
# an untracked sibling file so no secret is ever committed. Absent, the build
# still sources cleanly and the scripts that need a secret say which is missing.
if [ -f "$(dirname "${BASH_SOURCE[0]:-hack/env.sh}")/env.local.sh" ]; then
	. "$(dirname "${BASH_SOURCE[0]:-hack/env.sh}")/env.local.sh"
fi
