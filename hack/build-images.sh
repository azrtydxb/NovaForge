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

# The client certificates live in a per-session directory that starts empty, so
# fetch them from the cluster when they are not there rather than failing on a
# missing ca.crt.
[ -s "$BK_CERTS/ca.crt" ] || ./hack/bk-certs.sh
# deployment-runner is not built here: its image is assembled by the operator
# from Dockerfile.deployment-runner, which takes their approved chart archive and
# its checksum as build arguments. The resulting digest binds the wrapper, the
# chart bytes and the Helm binary together, which is the point — a chart this
# repository built would not be the one the operator approved.
SKIP="gen-openapi deployment-runner"

# Services whose image needs the git binary at runtime, and those needing cgo.
needs_git() { case "$1" in git-platform | runner | ci-runner | gates) return 0 ;; *) return 1 ;; esac }
# Services that run analysis tools (go, gitleaks, osv-scanner, semgrep) on
# repository contents: the merge gates and the maintenance scanners.
needs_analysis() { case "$1" in gates | work-reviews) return 0 ;; *) return 1 ;; esac }
needs_cgo() { case "$1" in engineering-graph) return 0 ;; *) return 1 ;; esac }

# The gate analysis sandbox is an image without a service: the toolchain the
# gates service runs repository code inside, built from the "sandbox" stage of
# Dockerfile.analysis. It is not under cmd/, so it is named explicitly here and
# built by default — gates refuse to run executable gates without it, and a
# deployment missing it looks configured while every executable gate is
# unavailable.
SANDBOX_IMAGE_NAME=gate-analysis

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
		--output "type=image,name=$REGISTRY_PUSH/$REGISTRY_REPO/$svc:$TAG,push=true" \
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
		--output "type=image,name=$REGISTRY_PUSH/$REGISTRY_REPO/$SANDBOX_IMAGE_NAME:$TAG,push=true" \
		--progress plain >"/tmp/buildctl.$SANDBOX_IMAGE_NAME.log" 2>&1 || return 1
	local digest
	digest="$(python3 -c 'import json,sys; print(json.load(open(sys.argv[1]))["containerimage.digest"])' "$meta")"
	SANDBOX_DIGEST="$REGISTRY_PULL/$REGISTRY_REPO/$SANDBOX_IMAGE_NAME@$digest"
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
		mkdir -p "${NF_ADVISORY_CACHE:-$HOME/.cache/novaforge/osv}/.."
		printf '%s\n' "$SANDBOX_DIGEST" >"$HOME/.cache/novaforge/gate-analysis-image"
		continue
	fi
	if [ "$svc" = edge ]; then
		# The edge serves the web application as well as the API, so its
		# image is the only one that needs a Node toolchain to build.
		df=deploy/docker/Dockerfile.edge
	elif needs_analysis "$svc"; then
		df=deploy/docker/Dockerfile.analysis
	elif needs_cgo "$svc"; then
		df=deploy/docker/Dockerfile.cgo
	elif needs_git "$svc"; then
		df=deploy/docker/Dockerfile.git
	else
		df=deploy/docker/Dockerfile.static
	fi
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
