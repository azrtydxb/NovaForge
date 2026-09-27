#!/usr/bin/env bash
# Creates a controlled upstream outside the application namespace and writes
# reviewable Helm overrides. No application policy is changed by this script.
set -euo pipefail
cd "$(dirname "$0")/.."
source hack/env.sh
ns=novaforge-gap-fixture
out="${1:?usage: prepare-git-host-fixture.sh <values-output>}"
k() { kubectl --context "$KUBE_CONTEXT" "$@"; }
k create namespace "$ns" --dry-run=client -o yaml | k apply -f - >/dev/null
k -n novaforge get secret nexus-pull -o json | python3 -c 'import json,sys;d=json.load(sys.stdin);d["metadata"]={"name":"nexus-pull","namespace":"novaforge-gap-fixture"};print(json.dumps(d))' | k apply -f - >/dev/null
k -n "$ns" create configmap fixture --from-file=server.py=tests/fixtures/git-host/server.py --dry-run=client -o yaml | k apply -f - >/dev/null
image="$(k -n novaforge get deploy novaforge-gates -o jsonpath='{.spec.template.spec.containers[0].image}')"
script_sha="$(shasum -a 256 tests/fixtures/git-host/server.py | awk '{print $1}')"
k apply -f - >/dev/null <<YAML
apiVersion: apps/v1
kind: Deployment
metadata: {name: fixture, namespace: $ns}
spec:
  replicas: 1
  selector: {matchLabels: {app: git-host-fixture}}
  template:
    metadata:
      labels: {app: git-host-fixture}
      annotations: {fixture-script-sha: "$script_sha"}
    spec:
      automountServiceAccountToken: false
      imagePullSecrets: [{name: nexus-pull}]
      containers:
        - name: fixture
          image: $image
          command: [python3, /fixture/server.py]
          ports: [{containerPort: 8088}]
          readinessProbe: {httpGet: {path: /health, port: 8088}}
          resources: {requests: {cpu: 50m, memory: 64Mi}, limits: {cpu: "1", memory: 256Mi}}
          volumeMounts:
            - {name: script, mountPath: /fixture, readOnly: true}
            - {name: data, mountPath: /data}
      volumes:
        - {name: script, configMap: {name: fixture}}
        - {name: data, emptyDir: {}}
---
apiVersion: v1
kind: Service
metadata: {name: fixture, namespace: $ns}
spec:
  type: LoadBalancer
  selector: {app: git-host-fixture}
  ports: [{port: 8088, targetPort: 8088}]
YAML
k -n "$ns" rollout status deployment/fixture --timeout=180s
fixture_ip="$(k -n "$ns" get svc fixture -o jsonpath='{.status.loadBalancer.ingress[0].ip}')"
git_ip="$(k -n novaforge get svc novaforge-git-platform -o jsonpath='{.status.loadBalancer.ingress[0].ip}')"
[ -n "$fixture_ip" ] && [ -n "$git_ip" ]
python3 - "$fixture_ip" "$git_ip" "$out" <<'PY'
import json,sys
fixture,git,path=sys.argv[1:]
values={"outbound":{"destinations":[{"scheme":"http","host":fixture,"port":8088,"cidrs":[fixture+"/32"]},{"scheme":"https","host":"github.com","port":443,"cidrs":["0.0.0.0/0","::/0"]}]},"gitTLS":{"ipAddresses":[git]},"lfs":{"publicURL":"https://"+git+":8443"}}
with open(path,"w") as out: json.dump(values,out,indent=2)
print("Prepared exact fixture destination, github.com:443, and HTTPS Git endpoint in",path)
PY
