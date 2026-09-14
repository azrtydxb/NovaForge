#!/usr/bin/env bash
# fetch-analysis-rules.sh — refresh the vendored semgrep gosec ruleset the
# security gate runs. The ruleset is committed (deploy/analysis/) rather than
# downloaded at image build: semgrep.dev serves it unversioned, so a download
# would make two builds of one commit evaluate different rules.
set -euo pipefail
cd "$(dirname "$0")/.."
curl -sSfL --retry 2 --max-time 120 "https://semgrep.dev/c/p/gosec" -o deploy/analysis/semgrep-gosec.yml
echo "updated deploy/analysis/semgrep-gosec.yml — review the diff before committing"
