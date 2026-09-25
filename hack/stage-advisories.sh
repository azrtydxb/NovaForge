#!/usr/bin/env bash
# stage-advisories.sh — download the OSV advisory snapshot the offline
# vulnerability tests read, into a cache that survives sessions.
#
# The tests deliberately refuse to download anything themselves: an advisory
# scan that fetches its own database cannot be trusted to have scanned against
# the database under test, and in the sandbox there is no network at all. So the
# snapshot is staged here, explicitly, and the tests only ever read it.
#
# This is the test fixture, not what production reads. The gates sandbox carries
# its own snapshot baked into the analysis image (deploy/docker/Dockerfile.analysis).
set -euo pipefail
cd "$(dirname "$0")/.."

cache="${NF_ADVISORY_CACHE:-$HOME/.cache/novaforge/osv}"
mkdir -p "$cache"
for eco in Go; do
	out="$cache/$eco-all.zip"
	if [ -s "$out" ]; then
		echo "stage-advisories.sh: $eco already staged at $out"
		continue
	fi
	curl -sSfL -o "$out.part" "https://osv-vulnerabilities.storage.googleapis.com/$eco/all.zip"
	# A half-written file left behind by an interrupted download would be read
	# as a damaged snapshot and make every offline test fail on integrity
	# rather than on what it meant to assert, so it is only named once whole.
	unzip -tqq "$out.part" >/dev/null
	mv "$out.part" "$out"
	echo "stage-advisories.sh: staged $eco at $out"
done
