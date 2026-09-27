#!/usr/bin/env python3
"""Prove the disposable OpenBao credential stops working at its target deadline.

Run after prepare-deployment-fixture.py. Credentials remain in memory or an
owner-only temporary kubeconfig. This uses the real provider and Kubernetes API.
"""

import base64
import json
import os
import pathlib
import subprocess
import tempfile
import time

k = ["kubectl", "--context", os.environ.get("KUBE_CONTEXT", "kw")]
secret = json.loads(
    subprocess.check_output(
        k
        + [
            "-n",
            "novaforge",
            "get",
            "secret",
            "novaforge-openbao-deployment",
            "-o",
            "json",
        ]
    )
)
provider_token = base64.b64decode(secret["data"]["token"]).decode()


def bao(arguments, data):
    """Call the fixture policy with its token supplied on stdin."""
    script = 'read -r BAO_TOKEN\nexport BAO_TOKEN BAO_ADDR=https://openbao.novaforge-bao.svc:8200 BAO_CACERT=/tls/ca.crt\nexec bao "$@"\n'
    result = subprocess.run(
        k
        + [
            "-n",
            "novaforge-bao",
            "exec",
            "-i",
            "openbao-0",
            "--",
            "sh",
            "-c",
            script,
            "expiry-qualification",
            *arguments,
        ],
        input=(provider_token + "\n" + json.dumps(data)).encode(),
        stdout=subprocess.PIPE,
        stderr=subprocess.PIPE,
    )
    if result.returncode:
        raise RuntimeError("fixture provider call refused")
    return json.loads(result.stdout) if result.stdout.strip().startswith(b"{") else {}


issued = bao(
    ["write", "-format=json", "novaforge-deploy/creds/approved-fixture", "-"],
    {"kubernetes_namespace": "novaforge-deploy-target", "ttl": "600s"},
)
lease = issued["lease_id"]
try:
    token = issued["data"]["service_account_token"]
    encoded = token.split(".")[1]
    claims = json.loads(base64.urlsafe_b64decode(encoded + "=" * (-len(encoded) % 4)))
    deadline = claims["exp"]
    assert 540 < deadline - time.time() <= 660, "unexpected target credential lifetime"
    # Copy cluster address/trust only, replacing all administrator authentication.
    current = json.loads(
        subprocess.check_output(
            k + ["config", "view", "--minify", "--raw", "--flatten", "-o", "json"]
        )
    )
    cluster = current["clusters"][0]["cluster"]
    config = {
        "apiVersion": "v1",
        "kind": "Config",
        "clusters": [{"name": "target", "cluster": cluster}],
        "users": [{"name": "leased", "user": {"token": token}}],
        "contexts": [
            {
                "name": "probe",
                "context": {
                    "cluster": "target",
                    "user": "leased",
                    "namespace": "novaforge-deploy-target",
                },
            }
        ],
        "current-context": "probe",
    }
    with tempfile.TemporaryDirectory() as directory:
        path = pathlib.Path(directory) / "config"
        path.write_text(json.dumps(config))
        path.chmod(0o600)
        args = [
            "kubectl",
            "--kubeconfig",
            str(path),
            "--request-timeout=15s",
            "get",
            "configmaps",
            "-o",
            "name",
        ]
        first = subprocess.run(args, stdout=subprocess.PIPE, stderr=subprocess.PIPE)
        assert first.returncode == 0, "new credential was not accepted by target"
        print(
            "Target accepted the short-lived fixture credential; awaiting its signed expiry",
            flush=True,
        )
        while time.time() <= deadline + 3:
            time.sleep(min(30, max(1, deadline + 4 - time.time())))
        expired = subprocess.run(args, stdout=subprocess.PIPE, stderr=subprocess.PIPE)
        assert expired.returncode != 0 and (
            b"Unauthorized" in expired.stderr
            or b"provide credentials" in expired.stderr
        ), "expired credential was not explicitly rejected by target authentication"
        print(
            "PASS target accepted the issued credential and rejected it after signed expiry",
            flush=True,
        )
finally:
    bao(
        ["write", "-format=json", "sys/leases/revoke", "-"],
        {"lease_id": lease, "sync": True},
    )
