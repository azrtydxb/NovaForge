#!/usr/bin/env bash
# bk-certs.sh — materialize the BuildKit client certificates and the registry
# credentials that buildctl needs, into $BK_CERTS.
#
# BK_CERTS defaults into a per-session scratchpad directory, so a new session
# starts with it empty and every build fails on a missing ca.crt. The
# certificates are not secrets we keep: they live in the cluster, in
# buildkit/buildkit-client-tls, and are fetched from there on demand. This runs
# from build-images.sh whenever they are absent, so no build needs a manual
# setup step first.
set -euo pipefail
cd "$(dirname "$0")/.."
source hack/env.sh >/dev/null 2>&1 || source hack/env.sh

mkdir -p "$BK_CERTS"
# The key names contain dots, which kubectl's jsonpath treats as path
# separators, so the secret is decoded in one pass instead of per key. The
# decoder reads the secret on stdin, so it cannot be passed as a heredoc: a
# heredoc would become stdin and the piped secret would never arrive.
kubectl --context "$KUBE_CONTEXT" -n buildkit get secret buildkit-client-tls -o json |
	python3 -c '
import base64, json, pathlib, sys
data = json.load(sys.stdin)["data"]
out = pathlib.Path(sys.argv[1])
for f in ("ca.crt", "tls.crt", "tls.key"):
    if f not in data:
        sys.exit("bk-certs.sh: buildkit/buildkit-client-tls has no " + f)
    blob = base64.b64decode(data[f])
    if not blob:
        sys.exit("bk-certs.sh: buildkit/buildkit-client-tls " + f + " is empty")
    (out / f).write_bytes(blob)
' "$BK_CERTS"
chmod 0600 "$BK_CERTS/tls.key"

# buildctl pushes to nexus with the credentials in DOCKER_CONFIG, which points
# inside $BK_CERTS. Without REGISTRY_PASSWORD the push fails with a 401 that
# reads like a registry outage, so say what is actually missing.
if [ -z "${REGISTRY_PASSWORD:-}" ]; then
	echo "bk-certs.sh: REGISTRY_PASSWORD is unset; create hack/env.local.sh (see hack/env.sh)" >&2
	exit 1
fi
mkdir -p "$DOCKER_CONFIG"
python3 - "$DOCKER_CONFIG/config.json" <<'PY'
import base64, json, os, sys
auth = base64.b64encode(f"{os.environ['REGISTRY_USER']}:{os.environ['REGISTRY_PASSWORD']}".encode()).decode()
# Pushes go to the :5000 connector and pulls come from :443; both addresses are
# the same registry and the same account, so both carry the credential.
hosts = {os.environ["REGISTRY_PUSH"]: auth, os.environ["REGISTRY_PULL"]: auth}
json.dump({"auths": {h: {"auth": a} for h, a in hosts.items()}}, open(sys.argv[1], "w"))
PY
chmod 0600 "$DOCKER_CONFIG/config.json"
echo "bk-certs.sh: BuildKit certificates and registry credentials ready in $BK_CERTS"
