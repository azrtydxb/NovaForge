#!/usr/bin/env bash
set -euo pipefail
cd "$(dirname "$0")/../.."
source hack/env.sh
echo "== 1. governed deployment through real approval, provider and executor =="
python3 tests/e2e/governed_deploy_probe.py
