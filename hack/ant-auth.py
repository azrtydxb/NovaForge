#!/usr/bin/env python3
"""Copy the operator's Kuvryn Ant login into the BuildKit client's docker config.

Usage: hack/ant-auth.py <ant-host> <buildkit-docker-config-dir>

BuildKit pushes with whatever credential the client's DOCKER_CONFIG holds. No
Kubernetes or CI secret carries an ant.azrty.com publish credential, so it comes
from the operator's own `docker login <ant-host>` (NF_ANT_DOCKER_CONFIG names
another docker config directory). Only that one registry entry is copied, the
file stays mode 0600, and nothing is printed.
"""

import base64
import json
import os
import pathlib
import subprocess
import sys


def login(host):
    """Return the base64 user:secret for host from the operator's docker login."""
    source = (
        pathlib.Path(
            os.environ.get("NF_ANT_DOCKER_CONFIG", pathlib.Path.home() / ".docker")
        )
        / "config.json"
    )
    config = json.loads(source.read_text()) if source.exists() else {}
    entry = config.get("auths", {}).get(host, {})
    if entry.get("auth"):
        return entry["auth"]
    # Docker Desktop and most installs keep the secret in a credential helper.
    helper = config.get("credHelpers", {}).get(host) or config.get("credsStore")
    if helper:
        result = subprocess.run(
            ["docker-credential-" + helper, "get"],
            input=host.encode(),
            stdout=subprocess.PIPE,
            stderr=subprocess.DEVNULL,
            check=False,
        )
        if result.returncode == 0:
            found = json.loads(result.stdout)
            return base64.b64encode(
                f"{found['Username']}:{found['Secret']}".encode()
            ).decode()
    sys.exit(
        f"ant-auth.py: no {host} credential in {source}; run `docker login {host}` with an "
        "account that can publish to oci-hosted, or set NF_ANT_DOCKER_CONFIG"
    )


host, target = sys.argv[1], pathlib.Path(sys.argv[2]) / "config.json"
current = json.loads(target.read_text()) if target.exists() else {}
current.setdefault("auths", {})[host] = {"auth": login(host)}
target.parent.mkdir(parents=True, exist_ok=True, mode=0o700)
descriptor = os.open(target, os.O_WRONLY | os.O_CREAT | os.O_TRUNC, 0o600)
with os.fdopen(descriptor, "w") as handle:
    json.dump(current, handle)
