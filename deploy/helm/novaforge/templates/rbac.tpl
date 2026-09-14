{{- /*
The agent runtime creates one namespace per Agent Run. Its Role is limited to
exactly the objects it must manage for isolation and nothing else, so a
compromised agent-runtime cannot reach the rest of the cluster.
*/ -}}
{{- range $name, $svc := .Values.services }}
{{- if $svc.rbac }}
apiVersion: v1
kind: ServiceAccount
metadata:
  name: {{ $.Release.Name }}-{{ $name }}
---
apiVersion: rbac.authorization.k8s.io/v1
kind: ClusterRole
metadata:
  name: {{ $.Release.Name }}-{{ $name }}
rules:
  - apiGroups: [""]
    resources: [namespaces]
    verbs: [create, get, list, watch, delete]
  - apiGroups: [""]
    resources: [pods, pods/log, resourcequotas, secrets, configmaps]
    verbs: [create, get, list, watch, delete]
  # The run's workspace pod is where its files are staged and its commands
  # run, over exec (a GET for the WebSocket protocol, a POST for SPDY).
  - apiGroups: [""]
    resources: [pods/exec]
    verbs: [create, get]
  - apiGroups: [networking.k8s.io]
    resources: [networkpolicies]
    verbs: [create, get, list, delete]
---
apiVersion: rbac.authorization.k8s.io/v1
kind: ClusterRoleBinding
metadata:
  name: {{ $.Release.Name }}-{{ $name }}
roleRef:
  apiGroup: rbac.authorization.k8s.io
  kind: ClusterRole
  name: {{ $.Release.Name }}-{{ $name }}
subjects:
  - kind: ServiceAccount
    name: {{ $.Release.Name }}-{{ $name }}
    namespace: {{ $.Release.Namespace }}
{{- end }}
{{- end }}
