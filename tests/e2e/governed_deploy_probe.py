"""Qualify the operator fixture prepared by hack/prepare-deployment-fixture.py."""
import base64
import json
import os
import pathlib
import subprocess
import tempfile
import time
import urllib.error
import urllib.request
import uuid

k = ["kubectl", "--context", os.environ["KUBE_CONTEXT"]]
def command(args, **kw):
    return subprocess.check_output(args, stderr=subprocess.PIPE, **kw).decode().strip()
def obj(*args):
    return json.loads(command(k + list(args) + ["-o", "json"]))
secret = obj("-n", "novaforge", "get", "secret", "deployment-fixture-users")
users = {key: base64.b64decode(value).decode() for key, value in secret["data"].items()}
edge = obj("-n", "novaforge", "get", "svc", "novaforge-edge")["status"]["loadBalancer"]["ingress"][0]["ip"]
gitip = obj("-n", "novaforge", "get", "svc", "novaforge-git-platform")["status"]["loadBalancer"]["ingress"][0]["ip"]
base = "http://" + edge + ":8080/api/v1"
token = ""
def api(method, path, body=None):
    request = urllib.request.Request(base + path, method=method, headers={"Content-Type": "application/json", "Authorization": "Bearer " + token}, data=json.dumps(body).encode() if body is not None else None)
    with urllib.request.urlopen(request, timeout=360) as response:
        raw = response.read()
        return json.loads(raw) if raw else None
def refuse(method, path, body=None):
    try:
        api(method, path, body)
    except urllib.error.HTTPError as error:
        assert error.code in (400, 403, 409, 412), (error.code, error.read().decode())
        return
    raise AssertionError("unauthorized or changed deployment accepted")
author = api("POST", "/auth/login", {"username": users["author"], "password": users["author_password"]})["session_token"]
reviewer = api("POST", "/auth/login", {"username": users["reviewer"], "password": users["reviewer_password"]})["session_token"]
token = author
root = "/orgs/" + users["org"]
repo = root + "/repos/approved-app"
config_secret = obj("-n", "novaforge", "get", "secret", "novaforge-deployment-config")
config = json.loads(base64.b64decode(config_secret["data"]["config.json"]))
artifact = obj("-n", "novaforge", "get", "secret", "deployment-fixture-users")["data"].get("artifact")
assert artifact, "fixture must record its immutable artifact"
artifact = base64.b64decode(artifact).decode()
with tempfile.TemporaryDirectory() as tmp:
    # Git credentials stay in an ephemeral askpass environment, not remote URLs.
    askpass = pathlib.Path(tmp) / "askpass"
    askpass.write_text('#!/bin/sh\ncase "$1" in *Username*) printf "%s" "$NF_USER";; *) printf "%s" "$NF_TOKEN";; esac\n')
    askpass.chmod(0o700)
    env = {**os.environ, "GIT_ASKPASS": str(askpass), "GIT_TERMINAL_PROMPT": "0", "NF_USER": users["author"], "NF_TOKEN": author}
    work = tmp + "/repo"
    command(["git", "clone", "http://" + gitip + ":8081/" + users["org"] + "/approved-app.git", work], env=env)
    def git(*args): return command(["git", *args], cwd=work, env=env)
    git("config", "user.name", "Deployment acceptance")
    git("config", "user.email", "deployment@example.test")
    pathlib.Path(work + "/README.md").write_text("Approved deployment fixture\n" + uuid.uuid4().hex)
    git("add", "."); git("commit", "-m", "Deployment fixture"); git("push", "origin", "HEAD:main")
    branch = "deploy/" + uuid.uuid4().hex[:10]
    git("checkout", "-b", branch)
    pathlib.Path(work + "/README.md").write_text("Approved deployment candidate\n" + uuid.uuid4().hex)
    git("commit", "-am", "Candidate"); git("push", "origin", branch)
    run = api("POST", repo + "/runs", {"title": "Governed deployment qualification", "source_ref": branch, "target_ref": "main"})
    assert api("GET", repo + "/deployment-targets")["targets"]
    intent = {"id": str(uuid.uuid4()), "run_id": run["id"], "target": "fixture", "artifact": artifact}
    operation = api("POST", repo + "/deployments", intent)
    path = repo + "/deployments/" + operation["id"]
    assert api("POST", repo + "/deployments", intent)["id"] == operation["id"]
    refuse("POST", repo + "/deployments", {**intent, "artifact": "sha256:" + "f" * 64})
    refuse("POST", path + "/execute", {})
    decision = root + "/approvals/" + operation["id"] + "/decision"
    refuse("POST", decision, {"decision": "approved"})
    token = reviewer
    api("POST", decision, {"decision": "approved", "comment": "Qualify the fixed disposable chart"})
    token = author
    result = api("POST", path + "/execute", {})
    assert result["state"] == "succeeded", result
    repeated = api("POST", path + "/execute", {})
    assert len(repeated["attempts"]) == len(result["attempts"]) == 1, repeated
    workload = obj("-n", "novaforge-deploy-target", "get", "deploy", "approved-app")
    assert workload["status"].get("availableReplicas") == 1
    assert workload["spec"]["template"]["spec"]["containers"][0]["image"].endswith("@" + artifact)
    deadline = time.monotonic() + 120
    while time.monotonic() < deadline:
        result = api("GET", path)
        if result["credentials"] and all(c["resolved_at"] for c in result["credentials"]): break
        time.sleep(2)
    else: raise AssertionError("credential cleanup remains pending")
    denied = api("POST", repo + "/deployments", {**intent, "id": str(uuid.uuid4())})
    token = reviewer
    api("POST", root + "/approvals/" + denied["id"] + "/decision", {"decision": "denied"})
    token = author
    refuse("POST", repo + "/deployments/" + denied["id"] + "/execute", {})
    print("PASS real approved workload, independent approval, denial, immutable intent, replay and credential cleanup", flush=True)
