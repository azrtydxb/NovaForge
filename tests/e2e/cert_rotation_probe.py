"""Cert-manager -> mounted Secret -> production TLS reload, with real Git."""
import base64
import copy
import json
import os
import pathlib
import socket
import ssl
import subprocess
import tempfile
import time
import urllib.request
import uuid

ns=os.environ.get("NF_NAMESPACE","novaforge");rel=os.environ.get("REL","novaforge")
k=["kubectl","--context",os.environ["KUBE_CONTEXT"],"-n",ns]
name="nf-cert-rotation"
dns=name+"."+ns+".svc"
def cmd(args,**kw):return subprocess.check_output(args,stderr=subprocess.PIPE,**kw).decode().strip()
def get(kind,n):return json.loads(cmd(k+["get",kind,n,"-o","json"]))
def apply(doc):subprocess.run(k+["apply","-f","-"],input=json.dumps(doc).encode(),check=True,stdout=subprocess.DEVNULL)
def wait(check,timeout=240):
    end=time.monotonic()+timeout
    while time.monotonic()<end:
        value=check()
        if value:return value
        time.sleep(2)
    raise AssertionError("rotation condition timed out")
edge=get("svc",rel+"-edge")["status"]["loadBalancer"]["ingress"][0]["ip"]
base="http://"+edge+":8080/api/v1"
token=""
def api(method,path,body=None):
    req=urllib.request.Request(base+path,headers={"Content-Type":"application/json",**({"Authorization":"Bearer "+token} if token else {})},data=json.dumps(body).encode() if body is not None else None,method=method)
    with urllib.request.urlopen(req,timeout=30) as r:return json.load(r)
user="cert"+uuid.uuid4().hex[:12]
api("POST","/auth/register",{"username":user,"email":user+"@example.test","password":"correct horse battery staple"})
token=api("POST","/auth/login",{"username":user,"password":"correct horse battery staple"})["session_token"]
org=api("POST","/orgs",{"name":user})
api("POST","/orgs/"+user+"/repos",{"name":"rotation"})
try:
    cert=get("certificate",rel+"-git-platform-tls")
    spec=copy.deepcopy(cert["spec"])
    spec.update(secretName=name,commonName=dns,dnsNames=[dns],duration="24h",renewBefore="1h",privateKey={"rotationPolicy":"Always"})
    spec.pop("ipAddresses",None)
    apply({"apiVersion":"cert-manager.io/v1","kind":"Certificate","metadata":{"name":name,"namespace":ns},"spec":spec})
    cmd(k+["wait","--for=condition=Ready","certificate/"+name,"--timeout=180s"])
    deploy=get("deployment",rel+"-git-platform")
    podspec=copy.deepcopy(deploy["spec"]["template"]["spec"])
    live=json.loads(cmd(k+["get","pods","-l","app.kubernetes.io/component=git-platform,app.kubernetes.io/instance="+rel,"-o","json"]))["items"][0]
    podspec["nodeName"]=live["spec"]["nodeName"]
    for volume in podspec["volumes"]:
        if volume["name"]=="git-tls":volume["secret"]["secretName"]=name
    # The fixture serves transport only; production Redis consumers and blob
    # maintenance remain on the application's own pods.
    container=podspec["containers"][0]
    container["env"]=[v for v in container.get("env",[]) if v["name"] not in ("REDIS_URL","S3_ENDPOINT")]
    container["env"].extend([{"name":"REDIS_URL","value":""},{"name":"S3_ENDPOINT","value":""}])
    apply({"apiVersion":"v1","kind":"Pod","metadata":{"name":name,"namespace":ns,"labels":{"app":name}},"spec":podspec})
    apply({"apiVersion":"v1","kind":"Service","metadata":{"name":name,"namespace":ns},"spec":{"selector":{"app":name},"ports":[{"port":8443,"targetPort":8443}]}})
    cmd(k+["wait","--for=condition=Ready","pod/"+name,"--timeout=180s"])
    firstpod=get("pod",name)
    def identity(p):return p["metadata"]["uid"],[(c["name"],c["restartCount"]) for c in p["status"]["containerStatuses"]]
    with tempfile.TemporaryDirectory() as tmp:
        ca=tmp+"/ca.pem";pathlib.Path(ca).write_bytes(base64.b64decode(get("secret",name)["data"]["ca.crt"]))
        context=ssl.create_default_context(cafile=ca)
        def serial():
            with socket.create_connection((dns,8443),timeout=10) as raw:
                with context.wrap_socket(raw,server_hostname=dns) as conn:return conn.getpeercert()["serialNumber"]
        before=serial()
        env=os.environ.copy();env.update(GIT_SSL_CAINFO=ca,GIT_TERMINAL_PROMPT="0",GIT_CONFIG_NOSYSTEM="1",GIT_CONFIG_GLOBAL="/dev/null")
        # Credentials remain in an ephemeral askpass environment, never URL/argv.
        helper=pathlib.Path(tmp+"/askpass");helper.write_text('#!/bin/sh\ncase "$1" in Username*) printf %s "$NF_TEST_USER";; *) printf %s "$NF_TEST_TOKEN";; esac\n');helper.chmod(0o700)
        env.update(GIT_ASKPASS=str(helper),NF_TEST_USER=user,NF_TEST_TOKEN=token)
        remote="https://"+dns+":8443/"+user+"/rotation.git"
        work=tmp+"/before";cmd(["git","clone",remote,work],env=env)
        cmd(["git","config","user.name","Rotation acceptance"],cwd=work);cmd(["git","config","user.email","rotation@example.test"],cwd=work)
        pathlib.Path(work+"/README.md").write_text("before certificate renewal\n")
        cmd(["git","add","."],cwd=work);cmd(["git","commit","-m","before renewal"],cwd=work);cmd(["git","push","origin","HEAD:main"],cwd=work,env=env)
        # A duration change requests real reissuance through cert-manager, without
        # modifying its issuer or writing certificate/key bytes ourselves.
        spec["duration"]="48h"
        apply({"apiVersion":"cert-manager.io/v1","kind":"Certificate","metadata":{"name":name,"namespace":ns},"spec":spec})
        after=wait(lambda: (v if (v:=serial())!=before else None))
        second=tmp+"/after";cmd(["git","clone",remote,second],env=env)
        assert pathlib.Path(second+"/README.md").read_text()=="before certificate renewal\n"
        pathlib.Path(work+"/README.md").write_text("after certificate renewal\n")
        cmd(["git","commit","-am","after renewal"],cwd=work);cmd(["git","push","origin","HEAD:main"],cwd=work,env=env)
        assert identity(get("pod",name))==identity(firstpod),"pod restarted during rotation"
        try:
            urllib.request.urlopen("http://"+dns+":8443/",timeout=10)
            raise AssertionError("TLS listener accepted plaintext")
        except (urllib.error.HTTPError,urllib.error.URLError,ConnectionError):pass
        print("PASS cert-manager rotation",before,"->",after,"same pod UID/restarts; verified Git before/after",flush=True)
finally:
    for kind in ["pod","service","certificate","secret"]:
        subprocess.run(k+["delete",kind,name,"--ignore-not-found","--wait=false"],stdout=subprocess.DEVNULL,stderr=subprocess.DEVNULL)
    try:api("DELETE","/orgs/"+user,{"confirm_name":user})
    except Exception:pass
