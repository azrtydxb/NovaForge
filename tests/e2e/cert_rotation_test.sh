#!/usr/bin/env bash
set -euo pipefail
cd "$(dirname "$0")/../.."
source hack/env.sh
export NF_NAMESPACE="${NF_NAMESPACE:-novaforge}" REL="${REL:-novaforge}"
python3 tests/e2e/cert_rotation_probe.py
