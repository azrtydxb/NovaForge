#!/usr/bin/env python3
"""Remove disposable qualification resources after restoring Helm configuration."""

import base64
import json
import os
import subprocess
import urllib.request

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
root = base64.b64decode(secret("openbao-init", "novaforge-bao")["root_token"]).decode()
combined = base64.b64decode(secret("novaforge-openbao-deployment")["token"]).decode()


def bao(args, data=None):
    """Restrict provider cleanup to this fixture's engine, policy and token."""
    script = 'read -r BAO_TOKEN\nexport BAO_TOKEN BAO_ADDR=https://openbao.novaforge-bao.svc:8200 BAO_CACERT=/tls/ca.crt\nexec bao "$@"\n'
    payload = root + "\n" + (json.dumps(data) if data is not None else "")
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
            "fixture-cleanup",
            *args,
        ],
        input=payload.encode(),
        capture_output=True,
        check=False,
    )
    if result.returncode:
        raise RuntimeError("fixture provider cleanup refused")


bao(["secrets", "disable", "novaforge-deploy"])
bao(["policy", "delete", "novaforge-deploy-fixture"])
bao(["write", "auth/token/revoke", "-"], {"token": combined})
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
print(
    "Removed fixture organization, provider mount/policy/token, namespaces and operator Secrets"
)
