{{- /*
The agent runtime creates one namespace per Agent Run. Its Role is limited to
exactly the objects it must manage for isolation and nothing else, so a
compromised agent-runtime cannot reach the rest of the cluster.
*/ -}}
{{- range $name, $svc := .Values.services }}
{{- if and (eq $name "gates") $svc.rbac }}
{{- fail "Gates cannot use generic cluster RBAC; set services.gates.sandboxRbac instead" }}
{{- end }}
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

{{- /*
The gates service runs a repository's own tests and compilers, so it needs to
create the sandbox that keeps them away from its authority: one namespace per
invocation, a deny-all NetworkPolicy in it, and one pod it streams the command
into. This is deliberately narrower than the runtime Role above — no secrets, no
configmaps, no resourcequotas — because the sandbox pod is given none of those
and code under review must not be able to reach any. Without this grant the
gates service comes up healthy and every executable gate fails when it tries to
create its namespace, which reads like a cluster fault rather than a missing
permission.
*/ -}}
{{- range $name, $svc := .Values.services }}
{{- if and (eq $name "gates") $svc.sandboxRbac }}
---
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
    verbs: [create, get, delete]
  - apiGroups: [""]
    resources: [pods]
    verbs: [create, get, delete]
  # The command under evaluation is streamed into the sandbox pod: a GET for
  # the WebSocket protocol, a POST for the SPDY fallback.
  - apiGroups: [""]
    resources: [pods/exec]
    verbs: [create, get]
  - apiGroups: [networking.k8s.io]
    resources: [networkpolicies]
    verbs: [create]
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
