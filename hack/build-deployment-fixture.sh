#!/usr/bin/env bash
# Build an operator-owned chart/runner image. Requires a clean committed tree.
set -euo pipefail
cd "$(dirname "$0")/.."
source hack/env.sh
[ -z "$(git status --porcelain --untracked-files=no)" ] || { echo 'commit the tree before building the immutable deployment fixture' >&2; exit 1; }
TAG="$(git rev-parse --short HEAD)"
TMP="$(mktemp -d)"
trap 'rm -rf "$TMP"; rm -f deploy/fixtures/approved.tgz' EXIT
helm package deploy/fixtures/approved-app -d "$TMP" >/dev/null
cp "$TMP"/*.tgz deploy/fixtures/approved.tgz
CHART_SHA="$(shasum -a 256 deploy/fixtures/approved.tgz | awk '{print $1}')"
# Resolve immutable manifests from the upstream registry; never bind a mutable
# base tag into the resulting operator policy.
python3 - "$TMP/base-images.env" <<'PY'
import json,sys,urllib.request
out={}
for key,repo,tag in [('GO_IMAGE','library/golang','1.27'),('HELM_IMAGE','alpine/helm','3.19.0'),('RUNTIME_IMAGE','library/alpine','3.22')]:
    with urllib.request.urlopen('https://auth.docker.io/token?service=registry.docker.io&scope=repository:'+repo+':pull') as response:token=json.load(response)['token']
    request=urllib.request.Request('https://registry-1.docker.io/v2/'+repo+'/manifests/'+tag,headers={'Authorization':'Bearer '+token,'Accept':'application/vnd.oci.image.index.v1+json, application/vnd.docker.distribution.manifest.list.v2+json'})
    with urllib.request.urlopen(request) as response:digest=response.headers['Docker-Content-Digest']
    out[key]='docker.io/'+repo+'@'+digest
with open(sys.argv[1],'w') as f:
    for key,value in out.items():f.write(key+'='+value+'\n')
PY
source "$TMP/base-images.env"
buildctl --tlscacert "$BK_CERTS/ca.crt" --tlscert "$BK_CERTS/tls.crt" --tlskey "$BK_CERTS/tls.key" \
 build --frontend dockerfile.v0 --local context=. --local dockerfile=deploy/docker \
 --opt filename=Dockerfile.deployment-runner --opt platform=linux/arm64 \
 --opt build-arg:GO_IMAGE="$GO_IMAGE" --opt build-arg:HELM_IMAGE="$HELM_IMAGE" --opt build-arg:RUNTIME_IMAGE="$RUNTIME_IMAGE" \
 --opt build-arg:CHART_ARCHIVE=deploy/fixtures/approved.tgz --opt build-arg:CHART_SHA256="$CHART_SHA" \
 --output "type=image,name=$REGISTRY_PUSH/$REGISTRY_REPO/deployment-runner:$TAG,push=true" \
 --metadata-file "$TMP/result.json" --progress plain
python3 - "$TMP/result.json" "$CHART_SHA" "$REGISTRY_PULL/$REGISTRY_REPO/deployment-runner" <<'PY'
import json,sys
result=json.load(open(sys.argv[1]));print(json.dumps({'runner_image':sys.argv[3]+'@'+result['containerimage.digest'],'chart_sha256':sys.argv[2]}))
PY
