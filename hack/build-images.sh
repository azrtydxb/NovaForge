#!/usr/bin/env bash
# build-images.sh [service...] — build arm64 images on the in-cluster BuildKit
# and push them to nexus. With no arguments it builds every cmd/ service that
# is not a local tool.
#
# There is no local Docker daemon: BuildKit runs in the cluster and is reached
# over mTLS, and the nodes pull from a different address than the one we push
# to. hack/env.sh carries both.
set -euo pipefail
cd "$(dirname "$0")/.."
source hack/env.sh

# Images are tagged with the commit they were built from. A mutable tag such as
# "dev" combined with imagePullPolicy IfNotPresent makes nodes serve a cached
# older image, which silently deploys stale code — that cost a full debug cycle.
TAG="${TAG:-$(git rev-parse --short HEAD)}"
SKIP="gen-openapi"

# Services whose image needs the git binary at runtime, and those needing cgo.
needs_git() { case "$1" in git-platform | runner | ci-runner | gates) return 0 ;; *) return 1 ;; esac }
needs_cgo() { case "$1" in engineering-graph) return 0 ;; *) return 1 ;; esac }

services=("$@")
if [ ${#services[@]} -eq 0 ]; then
	for d in cmd/*/; do
		n="$(basename "$d")"
		[[ " $SKIP " == *" $n "* ]] && continue
		services+=("$n")
	done
fi

[ ${#services[@]} -eq 0 ] && {
	echo "no services to build"
	exit 0
}

for svc in "${services[@]}"; do
	if needs_cgo "$svc"; then
		df=deploy/docker/Dockerfile.cgo
	elif needs_git "$svc"; then
		df=deploy/docker/Dockerfile.git
	else
		df=deploy/docker/Dockerfile.static
	fi
	echo "==> $svc  ($df)"
	buildctl \
		--tlscacert "$BK_CERTS/ca.crt" --tlscert "$BK_CERTS/tls.crt" --tlskey "$BK_CERTS/tls.key" \
		build --frontend dockerfile.v0 \
		--local context=. \
		--local dockerfile="$(dirname "$df")" \
		--opt filename="$(basename "$df")" \
		--opt platform=linux/arm64 \
		--opt build-arg:SERVICE="$svc" \
		--output "type=image,name=$REGISTRY_PUSH/$REGISTRY_REPO/$svc:$TAG,push=true" \
		--progress plain 2>&1 | tail -3
done
echo "built: ${services[*]}"
