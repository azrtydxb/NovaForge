#!/usr/bin/env bash
# dockerfile-for.sh <service> — print "<dockerfile> [target]" for one image.
#
# The single source of truth for which Dockerfile builds which service: both the
# in-cluster BuildKit path (hack/build-images.sh) and the OKE publish workflow
# (.github/workflows/publish-oke.yml) ask this script. Two copies of the mapping
# would drift, and a service built from the wrong Dockerfile is missing its
# toolchain (git, the analysis scanners, cgo) only at runtime.
set -euo pipefail
svc="${1:?usage: dockerfile-for.sh <service>}"
case "$svc" in
# The gate analysis sandbox is an image without a service: the toolchain the
# gates service runs repository code inside, the "sandbox" stage of
# Dockerfile.analysis.
gate-analysis) echo "deploy/docker/Dockerfile.analysis sandbox" ;;
# The edge serves the web application as well as the API, so its image is the
# only one that needs a Node toolchain to build.
edge) echo "deploy/docker/Dockerfile.edge" ;;
# Services that run analysis tools (go, gitleaks, osv-scanner, semgrep) on
# repository contents: the merge gates and the maintenance scanners.
gates | work-reviews) echo "deploy/docker/Dockerfile.analysis" ;;
engineering-graph) echo "deploy/docker/Dockerfile.cgo" ;;
# Services whose image needs the git binary at runtime.
git-platform | runner | ci-runner) echo "deploy/docker/Dockerfile.git" ;;
*) echo "deploy/docker/Dockerfile.static" ;;
esac
