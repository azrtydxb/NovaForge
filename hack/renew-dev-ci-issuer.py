#!/usr/bin/env python3
"""Inspect/renew only NovaForge's development CI certificate fixture issuer."""

import argparse
import base64
import datetime
import json
import subprocess

parser = argparse.ArgumentParser(description=__doc__)
parser.add_argument("--renew-expired", action="store_true")
args = parser.parse_args()
k = ["kubectl", "--context", "kw", "-n", "novaforge-bao"]
secret = json.loads(
    subprocess.check_output(k + ["get", "secret", "openbao-init", "-o", "json"])
)
token = base64.b64decode(secret["data"]["root_token"]).decode()


def bao(*argv):
    script = 'read -r BAO_TOKEN\nexport BAO_TOKEN BAO_ADDR=https://openbao.novaforge-bao.svc:8200 BAO_CACERT=/tls/ca.crt\nexec bao "$@"\n'
    result = subprocess.run(
        k + ["exec", "-i", "openbao-0", "--", "sh", "-c", script, "ci-issuer", *argv],
        input=(token + "\n").encode(),
        capture_output=True,
    )
    if result.returncode:
        raise RuntimeError("Development CI issuer operation failed; output withheld")
    return json.loads(result.stdout) if "-format=json" in argv else {}


role = bao("read", "-format=json", "pki/roles/novaforge-ci")["data"]
roles = bao("list", "-format=json", "pki/roles")
assert roles == ["novaforge-ci"], "Refuse to change a PKI mount with other roles"
assert (
    role["allowed_domains"] == ["novaforge.local"]
    and role["generate_lease"]
    and role["issuer_ref"] == "default"
)
cert = bao("read", "-format=json", "pki/cert/ca")["data"]["certificate"]
info = subprocess.check_output(
    ["openssl", "x509", "-noout", "-subject", "-enddate"], input=cert.encode()
).decode()
assert "CN=NovaForge CI Issuing CA" in info or "CN = NovaForge CI Issuing CA" in info, (
    "Unexpected issuer identity"
)
print(info.strip())
valid = (
    subprocess.run(
        ["openssl", "x509", "-noout", "-checkend", "0"],
        input=cert.encode(),
        capture_output=True,
    ).returncode
    == 0
)
if valid or not args.renew_expired:
    print(
        "Issuer is valid"
        if valid
        else "Issuer expired; --renew-expired is required to renew this development fixture"
    )
    raise SystemExit(0)
# A 30-day CA avoids the previous one-day fixture lifetime. Leaf authorization
# remains the existing 600-second default / 3600-second maximum role policy.
bao("secrets", "tune", "-max-lease-ttl=720h", "pki")
name = "dev-ci-" + datetime.datetime.now(datetime.timezone.utc).strftime("%Y%m%d%H%M%S")
issued = bao(
    "write",
    "-format=json",
    "pki/root/generate/internal",
    "common_name=NovaForge CI Issuing CA",
    "ttl=720h",
    "issuer_name=" + name,
)
issuer = issued["data"]["issuer_id"]
bao(
    "write",
    "-format=json",
    "pki/config/issuers",
    "default=" + issuer,
    "default_follows_latest_issuer=false",
)
assert bao("read", "-format=json", "pki/roles/novaforge-ci")["data"] == role, (
    "Leaf role changed unexpectedly"
)
print(
    "Renewed only the development CI issuer; preserved leaf role, old issuer history and unseal custody"
)
