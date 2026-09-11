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
# Dev datastore endpoints (LoadBalancer, reachable from the workstation)
export TEST_DATABASE_URL="postgres://novaforge:novaforge@192.168.10.121:5432/novaforge?sslmode=disable"
export TEST_REDIS_URL="redis://192.168.10.124:6379"
export TEST_S3_ENDPOINT="192.168.10.130:9000"
export TEST_S3_ACCESS_KEY=minioadmin
export TEST_S3_SECRET_KEY=minioadmin
export TEST_S3_BUCKET=novaforge-test
bk() { buildctl --tlscacert "$BK_CERTS/ca.crt" --tlscert "$BK_CERTS/tls.crt" --tlskey "$BK_CERTS/tls.key" "$@"; }
