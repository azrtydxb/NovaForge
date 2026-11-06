# NovaForge build/deploy environment (kw cluster)
export KUBE_CONTEXT=kw
export BUILDKIT_HOST=tcp://192.168.10.130:1234
export BK_CERTS="${BK_CERTS:-/private/tmp/claude-501/-Users-pascal-Development-NovaForge/5a8c3920-c543-4e9f-ad19-7fc324dd8240/scratchpad/bkcerts}"
# Images are a push/pull split on Kuvryn Ant: they are pushed to the org Ant
# (ant.azrty.com), and kw pulls them from its read replica of the same oci-hosted
# feed (the replica refuses pushes). Image names are <registry>/<repo>/<service>.
# Publishing needs your own `docker login ant.azrty.com -u x --password-stdin`
# with an Ant personal access token or publish token (hack/ant-auth.py copies it
# into BuildKit's docker config); never put the token in the repository.
export REGISTRY_PUSH=ant.azrty.com/oci-hosted
export REGISTRY_PULL=ant.kw.watteel.lab/oci-hosted
export REGISTRY_REPO=novaforge
# The host and the /v2 repository prefix of each registry, for the scripts that
# call the registry API (https://$REGISTRY_PUSH_HOST/v2/$REGISTRY_PUSH_V2/<svc>/manifests/<tag>).
export REGISTRY_PUSH_HOST="${REGISTRY_PUSH%%/*}"
export REGISTRY_PULL_HOST="${REGISTRY_PULL%%/*}"
export REGISTRY_PUSH_V2="${REGISTRY_PUSH#*/}/$REGISTRY_REPO"
export REGISTRY_PULL_V2="${REGISTRY_PULL#*/}/$REGISTRY_REPO"
# Basic-auth user for the registry API checks; REGISTRY_PASSWORD (an Ant token)
# lives in the untracked hack/env.local.sh.
export REGISTRY_USER=x
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
