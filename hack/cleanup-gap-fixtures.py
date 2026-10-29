#!/usr/bin/env python3
"""Remove disposable qualification resources after restoring Helm configuration."""

import base64
import json
import os
import subprocess
import urllib.request

import bao as shared_bao

k = ["kubectl", "--context", os.environ.get("KUBE_CONTEXT", "kw")]


def command(args, **kwargs):
    """Capture command output without displaying credentials."""
    return subprocess.check_output(args, stderr=subprocess.PIPE, **kwargs)


def secret(name, namespace="novaforge"):
    """Read one operator-owned fixture Secret into memory."""
    return json.loads(
        command(k + ["-n", namespace, "get", "secret", name, "-o", "json"])
    )["data"]


values = json.loads(
    command(
        [
            "helm",
            "--kube-context",
            os.environ.get("KUBE_CONTEXT", "kw"),
            "get",
            "values",
            "novaforge",
            "-n",
            "novaforge",
            "-o",
            "json",
        ]
    )
)
configs = values.get("operatorConfigs", {})
assert configs.get("openbao", {}).get("secretName") == "novaforge-openbao", (
    "restore original broker Secret through Helm before cleanup"
)
assert not configs.get("deployments", {}).get("secretName"), (
    "disable disposable deployment target through Helm before cleanup"
)
users = {
    name: base64.b64decode(value).decode()
    for name, value in secret("deployment-fixture-users").items()
}
edge = json.loads(
    command(k + ["-n", "novaforge", "get", "svc", "novaforge-edge", "-o", "json"])
)["status"]["loadBalancer"]["ingress"][0]["ip"]
base = "http://" + edge + ":8080/api/v1"
token = ""


def api(method, path, body):
    """Use the actual fixture author to remove its organization."""
    request = urllib.request.Request(
        base + path,
        method=method,
        data=json.dumps(body).encode(),
        headers={
            "Content-Type": "application/json",
            "Authorization": "Bearer " + token,
        },
    )
    with urllib.request.urlopen(request, timeout=60) as response:
        raw = response.read()
        return json.loads(raw) if raw else None


token = api(
    "POST",
    "/auth/login",
    {"username": users["author"], "password": users["author_password"]},
)["session_token"]
api("DELETE", "/orgs/" + users["org"], {"confirm_name": users["org"]})
combined = base64.b64decode(secret("novaforge-openbao-deployment")["token"]).decode()
# The mount, role and policies belong to OpenTofu; only this fixture's token is ours.
with shared_bao.session() as bao:
    bao.call("POST", "auth/token/revoke", {"token": combined})
for namespace in (
    "novaforge-deploy-exec",
    "novaforge-deploy-target",
    "novaforge-gap-fixture",
):
    command(
        k + ["delete", "namespace", namespace, "--ignore-not-found", "--wait=false"]
    )
command(
    k
    + [
        "-n",
        "novaforge",
        "delete",
        "secret",
        "novaforge-openbao-deployment",
        "novaforge-deployment-config",
        "deployment-fixture-users",
        "--ignore-not-found",
    ]
)
print("Removed fixture organization, provider token, namespaces and operator Secrets")
