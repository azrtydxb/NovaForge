#!/usr/bin/env bash
# build-images.sh [service...] — build arm64 images on the in-cluster BuildKit
# and push them to nexus and the Kuvryn Ant. With no arguments it builds every cmd/ service that
# is not a local tool.
#
# There is no local Docker daemon: BuildKit runs in the cluster and is reached
# over mTLS, and the nodes pull from a different address than the one we push
# to. hack/env.sh carries both.
set -euo pipefail
cd "$(dirname "$0")/.."
source hack/env.sh

# Another registry than kw's Nexus, e.g. OCIR for the azrty OKE cluster:
#   NF_REGISTRY_PUSH=dxb.ocir.io NF_REGISTRY_PULL=dxb.ocir.io \
#   NF_REGISTRY_REPO=axmcgpqk0w0z/azrty NF_IMAGE_PREFIX=novaforge- ./hack/build-images.sh
# env.sh exports the kw coordinates unconditionally, so the overrides are read
# after it. DOCKER_CONFIG must then hold a credential for that registry: BuildKit
# pushes with whatever it finds there. The image name is
# <push>/<repo>/<prefix><svc>, which is what the chart's image.namePrefix renders.
REGISTRY_PUSH="${NF_REGISTRY_PUSH:-$REGISTRY_PUSH}"
REGISTRY_PULL="${NF_REGISTRY_PULL:-$REGISTRY_PULL}"
REGISTRY_REPO="${NF_REGISTRY_REPO:-$REGISTRY_REPO}"
IMAGE_PREFIX="${NF_IMAGE_PREFIX:-}"

# The kw build also publishes every image to the Kuvryn Ant, which kw's pods
# pull from (values-kw.yaml image.registry): kw's oci-hosted feed replicates
# ant.azrty.com/oci-hosted, so promotion needs no copy. Nexus stays a target
# because the e2e suites and hack/deploy.sh pull from it. Another registry named
# by the overrides is published there alone.
ANT_REPO=""
if [ -n "${ANT_PUSH:-}" ] && [ -z "${NF_REGISTRY_PUSH:-}${NF_REGISTRY_PULL:-}${NF_REGISTRY_REPO:-}${NF_IMAGE_PREFIX:-}" ]; then
	ANT_REPO="$ANT_PUSH/$REGISTRY_REPO"
fi

# Images are tagged with the commit they were built from. A mutable tag such as
# "dev" combined with imagePullPolicy IfNotPresent makes nodes serve a cached
# older image, which silently deploys stale code — that cost a full debug cycle.
TAG="${TAG:-$(git rev-parse --short HEAD)}"

# The client certificates live in a per-session directory that starts empty, so
# fetch them from the cluster when they are not there rather than failing on a
# missing ca.crt.
[ -s "$BK_CERTS/ca.crt" ] || ./hack/bk-certs.sh
# The Ant credential is the operator's own docker login: no cluster or CI
# secret carries one. Refreshed on every build, so a re-login takes effect.
[ -z "$ANT_REPO" ] || ./hack/ant-auth.py "${ANT_REPO%%/*}" "$DOCKER_CONFIG"
# deployment-runner is not built here: its image is assembled by the operator
# from Dockerfile.deployment-runner, which takes their approved chart archive and
# its checksum as build arguments. The resulting digest binds the wrapper, the
# chart bytes and the Helm binary together, which is the point — a chart this
# repository built would not be the one the operator approved.
SKIP="gen-openapi deployment-runner"

# Which Dockerfile builds which service lives in hack/dockerfile-for.sh, shared
# with the OKE publish workflow so the two cannot drift apart.

# The gate analysis sandbox is an image without a service: the toolchain the
# gates service runs repository code inside, built from the "sandbox" stage of
# Dockerfile.analysis. It is not under cmd/, so it is named explicitly here and
# built by default — gates refuse to run executable gates without it, and a
# deployment missing it looks configured while every executable gate is
# unavailable.
SANDBOX_IMAGE_NAME=gate-analysis

# image_names prints the BuildKit output names for one image: the registry
# named above and, on the kw build, the Ant. Quoted, because the list is
# comma-separated inside the comma-separated --output value.
image_names() {
	local names="$REGISTRY_PUSH/$REGISTRY_REPO/$IMAGE_PREFIX$1:$TAG"
	[ -z "$ANT_REPO" ] || names="$names,$ANT_REPO/$1:$TAG"
	printf '"name=%s"' "$names"
}

services=("$@")
if [ ${#services[@]} -eq 0 ]; then
	for d in cmd/*/; do
		n="$(basename "$d")"
		[[ " $SKIP " == *" $n "* ]] && continue
		services+=("$n")
	done
	services+=("$SANDBOX_IMAGE_NAME")
fi

[ ${#services[@]} -eq 0 ] && {
	echo "no services to build"
	exit 0
}

# build_one builds and pushes one service. BuildKit is reached over the network
# and a dropped connection mid-build is transient, so callers retry: losing a
# ten-minute build to one reset is not worth it.
build_one() {
	local svc="$1" df="$2"
	buildctl \
		--tlscacert "$BK_CERTS/ca.crt" --tlscert "$BK_CERTS/tls.crt" --tlskey "$BK_CERTS/tls.key" \
		build --frontend dockerfile.v0 \
		--local context=. \
		--local dockerfile="$(dirname "$df")" \
		--opt filename="$(basename "$df")" \
		--opt platform=linux/arm64 \
		--opt build-arg:SERVICE="$svc" \
		--output "type=image,$(image_names "$svc"),push=true" \
		--progress plain >/tmp/buildctl.$svc.log 2>&1
}

# build_sandbox builds the toolchain-only stage and records the digest it was
# pushed under. The chart refuses an analysisImage that is not digest-pinned,
# because a gate's verdict is only reproducible if the tools that produced it
# are exactly the ones that were reviewed.
build_sandbox() {
	local meta="/tmp/buildctl.$SANDBOX_IMAGE_NAME.meta.json"
	buildctl \
		--tlscacert "$BK_CERTS/ca.crt" --tlscert "$BK_CERTS/tls.crt" --tlskey "$BK_CERTS/tls.key" \
		build --frontend dockerfile.v0 \
		--local context=. \
		--local dockerfile=deploy/docker \
		--opt filename=Dockerfile.analysis \
		--opt target=sandbox \
		--opt platform=linux/arm64 \
		--metadata-file "$meta" \
		--output "type=image,$(image_names "$SANDBOX_IMAGE_NAME"),push=true" \
		--progress plain >"/tmp/buildctl.$SANDBOX_IMAGE_NAME.log" 2>&1 || return 1
	local digest
	digest="$(python3 -c 'import json,sys; print(json.load(open(sys.argv[1]))["containerimage.digest"])' "$meta")"
	SANDBOX_DIGEST="$REGISTRY_PULL/$REGISTRY_REPO/$IMAGE_PREFIX$SANDBOX_IMAGE_NAME@$digest"
}

for svc in "${services[@]}"; do
	if [ "$svc" = "$SANDBOX_IMAGE_NAME" ]; then
		echo "==> $svc  (deploy/docker/Dockerfile.analysis, target sandbox)"
		attempt=1
		until build_sandbox; do
			tail -3 "/tmp/buildctl.$SANDBOX_IMAGE_NAME.log" >&2
			attempt=$((attempt + 1))
			if [ "$attempt" -gt 3 ]; then
				echo "build of $svc failed after 3 attempts" >&2
				exit 1
			fi
			echo "    retrying $svc (attempt $attempt)" >&2
			sleep 10
		done
		echo "    analysisImage: $SANDBOX_DIGEST"
		# Recorded where hack/env.sh looks for it, so the gate tests that need a
		# real sandbox find one without being told the digest by hand.
		# Only for the kw registry: the tests pull it from kw's nodes, and an
		# OCIR digest recorded here would point them at an image kw cannot pull.
		if [ -z "${NF_REGISTRY_PULL:-}${NF_REGISTRY_REPO:-}${NF_IMAGE_PREFIX:-}" ]; then
			mkdir -p "${NF_ADVISORY_CACHE:-$HOME/.cache/novaforge/osv}/.."
			printf '%s\n' "$SANDBOX_DIGEST" >"$HOME/.cache/novaforge/gate-analysis-image"
		fi
		continue
	fi
	# One mapping, shared with the OKE publish workflow.
	read -r df _ < <(./hack/dockerfile-for.sh "$svc")
	echo "==> $svc  ($df)"
	attempt=1
	until build_one "$svc" "$df"; do
		tail -3 "/tmp/buildctl.$svc.log" >&2
		attempt=$((attempt + 1))
		if [ "$attempt" -gt 3 ]; then
			echo "build of $svc failed after 3 attempts" >&2
			exit 1
		fi
		echo "    retrying $svc (attempt $attempt)" >&2
		sleep 10
	done
done
echo "built: ${services[*]}"
[ -z "$ANT_REPO" ] || echo "published to $ANT_REPO as well; kw pulls them from $ANT_PULL/$REGISTRY_REPO once replicated"
