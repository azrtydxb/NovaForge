"""Real REST/CLI/Git/SSH/LFS/CI/mirror/webhook/blob acceptance on kw."""
import base64
import hashlib
import json
import os
import pathlib
import subprocess
import tempfile
import time
import urllib.error
import urllib.request
import uuid

ns=os.environ.get("NF_NAMESPACE","novaforge")
rel=os.environ.get("REL","novaforge")
k=["kubectl","--context",os.environ["KUBE_CONTEXT"]]
def command(args,**kw):
    try:
        return subprocess.check_output(args,stderr=subprocess.PIPE,**kw).decode().strip()
    except subprocess.CalledProcessError as error:
        # This fixture keeps credentials out of argv and Git remote URLs.
        raise RuntimeError(error.stderr.decode(errors="replace")[-6000:]) from None
def obj(*args): return json.loads(command(k+list(args)+["-o","json"]))
def api(method,path,body=None):
    headers={"Content-Type":"application/json"}
    if token: headers["Authorization"]="Bearer "+token
    req=urllib.request.Request(base+"/api/v1"+path,data=json.dumps(body).encode() if body is not None else None,headers=headers,method=method)
    with urllib.request.urlopen(req,timeout=180) as r:
        data=r.read()
        return json.loads(data) if data else None

def wait(check,seconds=180):
    deadline=time.monotonic()+seconds
    while time.monotonic()<deadline:
        result=check()
        if result:return result
        time.sleep(2)
    raise AssertionError("acceptance condition timed out")

def nf(*args):return command(["/tmp/nf",*args])

def git(*args,cwd=None):return command(["git",*args],cwd=cwd)

def post_fixture(path):
    with urllib.request.urlopen(urllib.request.Request(fixture+path,data=b"",method="POST")) as r:return json.load(r)

def received():
    with urllib.request.urlopen(fixture+"/events") as r:return json.load(r)

edge=obj("-n",ns,"get","svc",rel+"-edge")["status"]["loadBalancer"]["ingress"][0]["ip"]
gitip=obj("-n",ns,"get","svc",rel+"-git-platform")["status"]["loadBalancer"]["ingress"][0]["ip"]
fixtureip=obj("-n","novaforge-gap-fixture","get","svc","fixture")["status"]["loadBalancer"]["ingress"][0]["ip"]
base="http://"+edge+":8080"
fixture="http://"+fixtureip+":8088"
token=""
name="gh"+uuid.uuid4().hex[:12]
org=name
with tempfile.TemporaryDirectory() as tmp:
    os.environ["XDG_CONFIG_HOME"]=tmp+"/config"
    api("POST","/auth/register",{"username":name,"email":name+"@example.test","password":"correct horse battery staple"})
    token=api("POST","/auth/login",{"username":name,"password":"correct horse battery staple"})["session_token"]
    cfg=pathlib.Path(os.environ["XDG_CONFIG_HOME"])/"novaforge"
    cfg.mkdir(parents=True)
    (cfg/"config.json").write_text(json.dumps({"server":base,"token":token,"org":org}))
    (cfg/"config.json").chmod(0o600)
    organization=api("POST","/orgs",{"name":org})
    orgid=organization["id"]
    root="/orgs/"+org+"/repos"
    try:
        imported=json.loads(nf("repo","import","mirror","--remote",fixture+"/upstream.git","--mirror"))
        assert imported["mirror"] and imported["repo"]["name"]=="mirror"
        advanced=post_fixture("/advance")["sha"]
        status=json.loads(nf("repo","mirror","mirror","refresh"))
        assert status["last_synced_at"] and not status["last_error"]
        branches=api("GET",root+"/mirror/branches")
        assert any(x["sha"]==advanced for x in branches["refs"]),branches
        nf("repo","mirror","mirror","stop")
        try:api("GET",root+"/mirror/mirror");raise AssertionError("mirror still exists")
        except urllib.error.HTTPError as error:assert error.code==404
        # Public import proves connected egress, separately from the controlled
        # mutable upstream which verifies refresh fidelity.
        nf("repo","import","public","--remote","https://github.com/octocat/Hello-World.git")
        try:api("POST",root+"/import",{"name":"blocked","remote":"http://169.254.169.254/latest/meta-data"});raise AssertionError("metadata import accepted")
        except urllib.error.HTTPError as error:assert "policy" in error.read().decode()
        print("PASS imports, mirror refresh/conversion, public host and policy refusal",flush=True)

        repo=api("POST",root,{"name":"assets"})
        repoid=repo["id"]
        hook=api("POST",root+"/assets/hooks",{"url":fixture+"/hooks","events":["push","engineering_run","ci_result"],"secret":"fixture-signing-secret"})
        key=tmp+"/id"
        command(["ssh-keygen","-t","ed25519","-N","","-f",key,"-q"])
        api("POST","/user/ssh-keys",{"title":"git-host acceptance","key":pathlib.Path(key+".pub").read_text()})
        # Discover once for this disposable test; subsequent Git calls enforce the recorded host key.
        known=tmp+"/known_hosts"
        pathlib.Path(known).write_text(command(["ssh-keyscan","-p","2222",gitip])+"\n")
        os.environ["GIT_SSH_COMMAND"]=f"ssh -i {key} -o IdentitiesOnly=yes -o StrictHostKeyChecking=yes -o UserKnownHostsFile={known} -p 2222"
        cert=obj("-n",ns,"get","secret",rel+"-git-platform-tls")
        ca=tmp+"/ca.pem";pathlib.Path(ca).write_bytes(base64.b64decode(cert["data"]["ca.crt"]))
        os.environ["GIT_SSL_CAINFO"]=ca
        os.environ["GIT_CONFIG_NOSYSTEM"]="1"
        os.environ["GIT_CONFIG_GLOBAL"]=tmp+"/gitconfig"
        os.environ["GIT_TERMINAL_PROMPT"]="0"
        git("lfs","install","--skip-repo")
        remote=f"ssh://git@{gitip}:2222/{org}/assets.git"
        work=tmp+"/work"
        git("clone",remote,work)
        git("config","user.name","Git host acceptance",cwd=work)
        git("config","user.email","git-host@example.test",cwd=work)
        git("lfs","install","--local",cwd=work)
        git("lfs","track","*.bin",cwd=work)
        content=os.urandom(2<<20)
        pathlib.Path(work+"/payload.bin").write_bytes(content)
        git("add",".",cwd=work);git("commit","-m","SSH LFS payload",cwd=work);git("push","origin","HEAD:main",cwd=work)
        clone=tmp+"/clone";git("clone",remote,clone)
        assert pathlib.Path(clone+"/payload.bin").read_bytes()==content
        fork=json.loads(nf("repo","fork","assets","--name","fork"))
        forkclone=tmp+"/fork";git("clone",f"ssh://git@{gitip}:2222/{org}/fork.git",forkclone)
        assert pathlib.Path(forkclone+"/payload.bin").read_bytes()==content
        # Same-named branches from a fork can open a Run via the CLI.
        nf("run","create","assets","--title","Cross-fork review","--source","main","--source-repo","fork")
        wait(lambda:any(e.get("repo_id")==repoid and e.get("event")=="engineering_run" for e in received()))
        wait(lambda:any(e.get("repo_id")==repoid and e.get("event")=="push" for e in received()))
        print("PASS standard SSH LFS, fork payloads, CLI cross-fork Run, signed events",flush=True)

        # A small real CI job produces an aggregate success event.
        image=obj("-n",ns,"get","deploy",rel+"-ci-runner")["spec"]["template"]["spec"]["containers"][0]["image"].replace("/ci-runner:","/runner:")
        runner="git-host-runner"
        env=[{"name":n,"value":v} for n,v in {"CI_ADDR":rel+"-ci-runner:9094","RUNNER_ORG_ID":orgid,"RUNNER_LABELS":"linux","RUNNER_JOB_NAMESPACE":ns,"RUNNER_NAME":runner,"CI_DEFAULT_JOB_IMAGE":image}.items()]
        manifest={"apiVersion":"apps/v1","kind":"Deployment","metadata":{"name":runner,"namespace":ns},"spec":{"replicas":1,"selector":{"matchLabels":{"app":runner}},"template":{"metadata":{"labels":{"app":runner}},"spec":{"serviceAccountName":rel+"-runner","imagePullSecrets":[{"name":"nexus-pull"}],"containers":[{"name":"runner","image":image,"envFrom":[{"secretRef":{"name":rel+"-secrets"}}],"env":env}]}}}}
        subprocess.run(k+["apply","-f","-"],input=json.dumps(manifest).encode(),check=True,stdout=subprocess.DEVNULL)
        command(k+["-n",ns,"rollout","status","deploy/"+runner,"--timeout=180s"])
        pathlib.Path(work+"/.novaforge").mkdir()
        pathlib.Path(work+"/.novaforge/workflow.yaml").write_text("jobs:\n  check:\n    run: echo verified-ci-result\n")
        git("add",".",cwd=work);git("commit","-m","CI notification",cwd=work);git("push","origin","HEAD:main",cwd=work)
        wait(lambda:any(e.get("repo_id")==repoid and e.get("event")=="ci_result" and e.get("run",{}).get("state")=="success" for e in received()),300)
        print("PASS signed aggregate CI success webhook",flush=True)

        # Archive rules are checked again during LFS object transfer.
        api("PATCH",root+"/assets",{"archived":True})
        ticket=json.loads(command(["ssh","-i",key,"-o","IdentitiesOnly=yes","-o","StrictHostKeyChecking=yes","-o","UserKnownHostsFile="+known,"-p","2222","git@"+gitip,"git-lfs-authenticate",org+"/assets.git","upload"]))
        import ssl
        request=urllib.request.Request(ticket["href"]+"/objects/batch",data=json.dumps({"operation":"upload","objects":[{"oid":hashlib.sha256(b"new").hexdigest(),"size":3}]}).encode(),headers={**ticket["header"],"Content-Type":"application/vnd.git-lfs+json"})
        try:urllib.request.urlopen(request,context=ssl.create_default_context(cafile=ca));raise AssertionError("archived LFS upload accepted")
        except urllib.error.HTTPError as error:assert error.code==403
        api("PATCH",root+"/assets",{"archived":False})
        # Direct physical inventory is scoped to this test's durable IDs.
        secret=obj("-n",ns,"get","secret",rel+"-secrets")
        blobenv=os.environ.copy()
        for field in ["S3_ENDPOINT","S3_ACCESS_KEY","S3_SECRET_KEY"]:blobenv[field]=base64.b64decode(secret["data"][field]).decode()
        blobenv["S3_ENDPOINT"]=rel+"-minio."+ns+".svc:9000"
        probe=["/tmp/nf-blob-probe","--org",orgid,"--repo",repoid]
        command(probe+["--want","1"],env=blobenv)
        api("DELETE",root+"/assets")
        git("lfs","fetch","--all",cwd=forkclone)
        api("DELETE",root+"/fork")
        def reclaimed():
            return subprocess.run(probe+["--want","0"],env=blobenv,stdout=subprocess.DEVNULL,stderr=subprocess.DEVNULL).returncode==0
        wait(reclaimed)
        api("POST",root,{"name":"assets"})
        assert api("GET",root+"/assets")["id"]!=repoid
        print("PASS archived upload refusal, last-reference physical cleanup and safe name reuse",flush=True)
    finally:
        subprocess.run(k+["-n",ns,"delete","deploy","git-host-runner","--ignore-not-found"],stdout=subprocess.DEVNULL,stderr=subprocess.DEVNULL)
        if not os.environ.get("NF_KEEP_TEST_DATA"):
            try:api("DELETE","/orgs/"+org,{"confirm_name":org})
            except Exception:pass
