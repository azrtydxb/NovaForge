#!/usr/bin/env bash
set -euo pipefail
cd "$(dirname "$0")/../.."
source hack/env.sh
export NF_NAMESPACE="${NF_NAMESPACE:-novaforge}" REL="${REL:-novaforge}"
go build -o /tmp/nf ./cmd/nf
go build -o /tmp/nf-blob-probe tests/e2e/blob_probe.go
python3 tests/e2e/git_host_probe.py
