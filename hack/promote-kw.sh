#!/usr/bin/env bash
# Record a published immutable application image in Git for Kuvryn Sync.
# Building and publishing remain hack/build-images.sh's responsibility.
set -euo pipefail
cd "$(dirname "$0")/.."
source hack/env.sh >/dev/null
TAG="${1:?usage: hack/promote-kw.sh <built-commit-tag>}"
[[ "$TAG" =~ ^[a-f0-9]{7,40}$ ]] || {
	echo "use an immutable Git commit tag" >&2
	exit 1
}
git rev-parse --verify "${TAG}^{commit}" >/dev/null
git merge-base --is-ancestor "$TAG" HEAD || {
	echo "image commit is not in this checkout's history" >&2
	exit 1
}
VALUES=deploy/helm/novaforge/values-kw.yaml
git diff --quiet HEAD -- "$VALUES" || {
	echo "kw values have uncommitted edits; preserve/review them before promotion" >&2
	exit 1
}
# Every deployed service must exist through the node-facing registry. Compare
# against the push endpoint as well: publishing one connector is insufficient.
ANALYSIS_DIGEST=""
for svc in $(python3 -c 'import yaml; v=yaml.safe_load(open("deploy/helm/novaforge/values.yaml")); print(" ".join(v["services"])+" runner gate-analysis")'); do
	digests=()
	for registry in "$REGISTRY_PUSH" "$REGISTRY_PULL"; do
		headers=$(curl --fail --silent --show-error --insecure --head \
			--connect-timeout 10 --max-time 30 \
			--user "$REGISTRY_USER:$REGISTRY_PASSWORD" \
			-H 'Accept: application/vnd.oci.image.manifest.v1+json, application/vnd.docker.distribution.manifest.v2+json' \
			"https://$registry/v2/$REGISTRY_REPO/$svc/manifests/$TAG")
		digest=$(printf '%s\n' "$headers" | tr -d '\r' | awk -F': ' 'tolower($1)=="docker-content-digest"{print $2}')
		[[ "$digest" =~ ^sha256:[a-f0-9]{64}$ ]] || {
			echo "invalid registry digest for $svc" >&2
			exit 1
		}
		digests+=("$digest")
	done
	[ "${digests[0]}" = "${digests[1]}" ] || {
		echo "push/pull image mismatch for $svc" >&2
		exit 1
	}
	[ "$svc" != gate-analysis ] || ANALYSIS_DIGEST="${digests[0]}"
	echo "$svc verified $TAG ${digests[0]}"
done
export TAG ANALYSIS_DIGEST
python3 - <<'PY'
import os, pathlib, yaml
p=pathlib.Path('deploy/helm/novaforge/values-kw.yaml')
v=yaml.safe_load(p.read_text())
v['image']['tag']=os.environ['TAG']
v['services']['gates']['analysisImage']=os.environ['REGISTRY_PULL']+'/'+os.environ['REGISTRY_REPO']+'/gate-analysis@'+os.environ['ANALYSIS_DIGEST']
p.write_text('# Desired kw release. Promote only images verified at the Nexus pull endpoint.\n'+yaml.safe_dump(v,sort_keys=False))
PY
if command -v prettier >/dev/null; then prettier --write "$VALUES" >/dev/null; fi
helm lint deploy/helm/novaforge -f "$VALUES"
git diff -- "$VALUES"
echo "Review, commit and push these values to main; Kuvryn Sync performs deployment."
