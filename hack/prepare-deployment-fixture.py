#!/usr/bin/env python3
"""Prepare a namespace-scoped OpenBao deployment fixture and Helm overrides.

Arguments: runner image digest, artifact digest, output directory.
No application restart or Helm upgrade is performed here. Credentials stay in
Kubernetes Secrets; only nonsecret fixture identities/overrides are written.
"""
import base64
import copy
import json
import os
import pathlib
import re
import subprocess
import sys
import urllib.request
import uuid

runner_image,artifact,outdir=sys.argv[1:]
assert re.fullmatch(r"[^\s]+@sha256:[a-f0-9]{64}",runner_image)
assert re.fullmatch(r"sha256:[a-f0-9]{64}",artifact)
outdir=pathlib.Path(outdir);outdir.mkdir(parents=True,exist_ok=True)
k=["kubectl","--context",os.environ.get("KUBE_CONTEXT","kw")]
ns="novaforge";execution="novaforge-deploy-exec";target="novaforge-deploy-target"
def command(args,**kw):return subprocess.check_output(args,stderr=subprocess.PIPE,**kw).decode().strip()
def get(kind,name,namespace=ns):return json.loads(command(k+["-n",namespace,"get",kind,name,"-o","json"]))
def apply(data):subprocess.run(k+["apply","-f","-"],input=json.dumps(data).encode(),check=True,stdout=subprocess.DEVNULL,stderr=subprocess.PIPE)
def meta(name,namespace):return {"name":name,"namespace":namespace}
root=base64.b64decode(get("secret","openbao-init","novaforge-bao")["data"]["root_token"]).decode()
def bao(args,data=None):
    script='read -r BAO_TOKEN\nexport BAO_TOKEN BAO_ADDR=https://openbao.novaforge-bao.svc:8200 BAO_CACERT=/tls/ca.crt\nexec bao "$@"\n'
    payload=root+"\n"+(json.dumps(data) if data is not None else "")
    # Pass the program as an argument, the root credential and JSON on stdin.
    result=subprocess.run(k+["-n","novaforge-bao","exec","-i","openbao-0","--","sh","-c",script,"bao-fixture",*args],input=payload.encode(),stdout=subprocess.PIPE,stderr=subprocess.PIPE)
    if result.returncode:raise RuntimeError("OpenBao fixture command failed: "+" ".join(args[:2]))
    return json.loads(result.stdout) if result.stdout.strip().startswith(b"{") else {}
for namespace in [execution,target]:apply({"apiVersion":"v1","kind":"Namespace","metadata":{"name":namespace}})
pull=get("secret","nexus-pull")
for namespace in [execution,target]:apply({"apiVersion":"v1","kind":"Secret","metadata":meta("nexus-pull",namespace),"type":pull["type"],"data":pull["data"]})
apply({"apiVersion":"v1","kind":"ServiceAccount","metadata":meta("deployment-runner",execution),"automountServiceAccountToken":False})
# Gates can manage executor objects only in this dedicated namespace. The runner
# service account itself receives no RoleBinding or ambient API credential.
apply({"apiVersion":"rbac.authorization.k8s.io/v1","kind":"Role","metadata":meta("deployment-executor",execution),"rules":[{"apiGroups":["batch"],"resources":["jobs"],"verbs":["create","get","list","watch","delete"]},{"apiGroups":[""],"resources":["pods","pods/log","secrets"],"verbs":["create","get","list","watch","delete"]}]})
apply({"apiVersion":"rbac.authorization.k8s.io/v1","kind":"RoleBinding","metadata":meta("deployment-executor",execution),"roleRef":{"apiGroup":"rbac.authorization.k8s.io","kind":"Role","name":"deployment-executor"},"subjects":[{"kind":"ServiceAccount","name":"novaforge-gates","namespace":ns}]})
# The fixed chart can only manipulate this disposable target's release/workload.
workload_rules=[{"apiGroups":[""],"resources":["secrets","configmaps","pods","services"],"verbs":["get","list","watch","create","update","patch","delete"]},{"apiGroups":["apps"],"resources":["deployments","replicasets"],"verbs":["get","list","watch","create","update","patch","delete"]}]
apply({"apiVersion":"rbac.authorization.k8s.io/v1","kind":"Role","metadata":meta("approved-fixture",target),"rules":workload_rules})
bao_rules=workload_rules+[{"apiGroups":[""],"resources":["serviceaccounts","serviceaccounts/token"],"verbs":["create","get","update","delete"]},{"apiGroups":["rbac.authorization.k8s.io"],"resources":["rolebindings"],"verbs":["create","get","update","delete"]},{"apiGroups":["rbac.authorization.k8s.io"],"resources":["roles"],"resourceNames":["approved-fixture"],"verbs":["get","bind"]}]
apply({"apiVersion":"rbac.authorization.k8s.io/v1","kind":"Role","metadata":meta("openbao-issuer",target),"rules":bao_rules})
apply({"apiVersion":"rbac.authorization.k8s.io/v1","kind":"RoleBinding","metadata":meta("openbao-issuer",target),"roleRef":{"apiGroup":"rbac.authorization.k8s.io","kind":"Role","name":"openbao-issuer"},"subjects":[{"kind":"ServiceAccount","name":"default","namespace":"novaforge-bao"}]})
apply({"apiVersion":"cilium.io/v2","kind":"CiliumNetworkPolicy","metadata":meta("executor-egress",execution),"spec":{"endpointSelector":{},"egress":[{"toEntities":["kube-apiserver"],"toPorts":[{"ports":[{"port":"6443","protocol":"TCP"},{"port":"443","protocol":"TCP"}]}]},{"toEndpoints":[{"matchLabels":{"k8s:io.kubernetes.pod.namespace":"kube-system","k8s:k8s-app":"kube-dns"}}],"toPorts":[{"ports":[{"port":"53","protocol":"ANY"}]}]}]}})
mounts=bao(["secrets","list","-format=json"])
if "novaforge-deploy/" not in mounts:bao(["secrets","enable","-path=novaforge-deploy","kubernetes"])
bao(["write","-format=json","novaforge-deploy/config","-"],{})
bao(["write","-format=json","novaforge-deploy/roles/approved-fixture","-"],{"allowed_kubernetes_namespaces":[target],"kubernetes_role_name":"approved-fixture","kubernetes_role_type":"Role","token_default_ttl":"600s","token_max_ttl":"600s"})
# The broker token has no role-management permission, only issuance plus scoped
# lease lookup/revoke; this fixture does not reuse the provider root token.
policy='path "novaforge-deploy/creds/approved-fixture" { capabilities = ["update"] }\npath "sys/leases/lookup" { capabilities = ["update"] }\npath "sys/leases/revoke" { capabilities = ["update"] }\n'
bao(["write","-format=json","sys/policies/acl/novaforge-deploy-fixture","-"],{"policy":policy})
broker_token=bao(["write","-format=json","auth/token/create","-"],{"policies":["novaforge-deploy-fixture"],"no_default_policy":True,"ttl":"24h","renewable":False})["auth"]["client_token"]
edge=get("svc","novaforge-edge")["status"]["loadBalancer"]["ingress"][0]["ip"]
base="http://"+edge+":8080/api/v1";token=""
def api(method,path,body=None):
    req=urllib.request.Request(base+path,method=method,headers={"Content-Type":"application/json",**({"Authorization":"Bearer "+token} if token else {})},data=json.dumps(body).encode() if body is not None else None)
    with urllib.request.urlopen(req,timeout=30) as response:return json.load(response)
name="deploy"+uuid.uuid4().hex[:10]
password=uuid.uuid4().hex+uuid.uuid4().hex
api("POST","/auth/register",{"username":name,"email":name+"@example.test","password":password})
token=api("POST","/auth/login",{"username":name,"password":password})["session_token"]
org=api("POST","/orgs",{"name":name})
repo=api("POST","/orgs/"+name+"/repos",{"name":"approved-app"})
reviewer=name+"r";reviewer_password=uuid.uuid4().hex+uuid.uuid4().hex
reviewer_user=api("POST","/auth/register",{"username":reviewer,"email":reviewer+"@example.test","password":reviewer_password})
reviewer_login=api("POST","/auth/login",{"username":reviewer,"password":reviewer_password})
api("POST","/orgs/"+name+"/members",{"user_id":reviewer_login["user_id"],"role":"admin"})
apply({"apiVersion":"v1","kind":"Secret","metadata":meta("deployment-fixture-users",ns),"stringData":{"author":name,"author_password":password,"reviewer":reviewer,"reviewer_password":reviewer_password,"org":name,"artifact":artifact}})
# Preserve existing provider bindings in a separate combined operator Secret.
existing=get("secret","novaforge-openbao")
provider=json.loads(base64.b64decode(existing["data"]["config.json"]))
# A provider token can only authorize one policy set, so retain the existing
# token for existing bindings and add the fixture policy to it through a new
# bounded token with the union of its existing policies.
old_token=base64.b64decode(existing["data"]["token"]).decode()
lookup=bao(["write","-format=json","auth/token/lookup","-"],{"token":old_token})
policies=sorted(set(lookup["data"]["policies"]+["novaforge-deploy-fixture"]))
combined_token=bao(["write","-format=json","auth/token/create","-"],{"policies":policies,"ttl":"24h","renewable":False})["auth"]["client_token"]
# Revoke the unused single-policy token immediately.
bao(["write","-format=json","auth/token/revoke","-"],{"token":broker_token})
mount="/etc/novaforge/operator/openbao"
provider["token_file"]=mount+"/token";provider["ca_file"]=mount+"/ca.crt"
provider["bindings"].append({"org_id":org["id"],"environment":"staging","name":"DEPLOY_KUBECONFIG","path":"novaforge-deploy/creds/approved-fixture","method":"POST","json":True,"require_hard_expiry":True,"kubernetes_deployment":{"repo_id":repo["id"],"target":"fixture","namespace":target,"server":"https://kubernetes.default.svc","ca_file":"/var/run/secrets/kubernetes.io/serviceaccount/ca.crt"}})
apply({"apiVersion":"v1","kind":"Secret","metadata":meta("novaforge-openbao-deployment",ns),"stringData":{"config.json":json.dumps(provider),"token":combined_token},"data":{"ca.crt":existing["data"]["ca.crt"]}})
config={"targets":[{"name":"fixture","credential_name":"DEPLOY_KUBECONFIG","org_id":org["id"],"repo_id":repo["id"],"environment":"staging","helm":{"TargetClusterID":"kw-development","ExecutionNamespace":execution,"TargetNamespace":target,"Release":"approved-app","Image":runner_image,"ChartPath":"/charts/approved.tgz","ArtifactValueKey":"image.digest","ServiceAccount":"deployment-runner","ImagePullSecrets":["nexus-pull"],"CredentialPolicyRevision":"novaforge-deploy-fixture-v1"}}]}
apply({"apiVersion":"v1","kind":"Secret","metadata":meta("novaforge-deployment-config",ns),"stringData":{"config.json":json.dumps(config)}})
(outdir/"deployment-values.json").write_text(json.dumps({"operatorConfigs":{"openbao":{"secretName":"novaforge-openbao-deployment"},"deployments":{"secretName":"novaforge-deployment-config"}}},indent=2))
(outdir/"deployment-fixture.json").write_text(json.dumps({"org":name,"org_id":org["id"],"repo":"approved-app","repo_id":repo["id"],"artifact":artifact,"runner_image":runner_image,"execution_namespace":execution,"target_namespace":target},indent=2))
print("Prepared namespace-scoped deployment fixture; Helm overrides and nonsecret identities written to",outdir)
