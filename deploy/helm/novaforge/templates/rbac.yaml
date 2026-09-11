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
