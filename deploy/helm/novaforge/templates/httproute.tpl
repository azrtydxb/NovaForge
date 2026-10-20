{{- /*
One HTTPRoute for a cluster whose ingress is a shared Gateway API Gateway. The
Gateway terminates TLS, so both backends are reached over plain HTTP inside
the cluster: git-platform on its plaintext port and edge on its HTTP port.

Git smart-HTTP and LFS share the application's host. The git rule comes first
and matches only the paths internal/gitops' router answers
({org}/{repo}.git/info/refs, git-upload-pack, git-receive-pack and the LFS batch
and object endpoints), so a page of the web application can never be captured
by it. Envoy Gateway ranks a RegularExpression match above a PathPrefix one
whatever the order, but the order is kept explicit too, so the route reads the
way it routes and an implementation that honours rule order agrees.

git-lfs is told to come back over https because Envoy sets X-Forwarded-Proto,
which internal/gitops' lfsScheme reads before r.TLS.
*/ -}}
{{- if .Values.gateway.enabled }}
{{- if not .Values.gateway.parentRefs }}{{ fail "gateway.enabled is set but gateway.parentRefs names no Gateway to attach to" }}{{ end }}
{{- if not .Values.gateway.hostnames }}{{ fail "gateway.enabled is set but gateway.hostnames is empty: the route would claim every host on its listener" }}{{ end }}
{{- $git := index .Values.services "git-platform" }}
{{- $edge := index .Values.services "edge" }}
{{- if not (and $git $git.httpPort) }}{{ fail "gateway.enabled needs git-platform with an httpPort" }}{{ end }}
{{- if not (and $edge $edge.httpPort) }}{{ fail "gateway.enabled needs edge with an httpPort" }}{{ end }}
---
apiVersion: gateway.networking.k8s.io/v1
kind: HTTPRoute
metadata:
  name: {{ .Release.Name }}
  labels:
    {{- include "novaforge.labels" . | nindent 4 }}
    app.kubernetes.io/component: route
spec:
  parentRefs:
    {{- range .Values.gateway.parentRefs }}
    - group: gateway.networking.k8s.io
      kind: Gateway
      name: {{ .name | quote }}
      {{- with .namespace }}
      namespace: {{ . | quote }}
      {{- end }}
      {{- with .sectionName }}
      sectionName: {{ . | quote }}
      {{- end }}
    {{- end }}
  hostnames:
    {{- range .Values.gateway.hostnames }}
    - {{ . | quote }}
    {{- end }}
  rules:
    - name: git
      matches:
        - path:
            type: RegularExpression
            value: {{ .Values.gateway.gitPathRegex | quote }}
      {{- with .Values.gateway.gitRequestTimeout }}
      timeouts:
        request: {{ . | quote }}
      {{- end }}
      backendRefs:
        - group: ""
          kind: Service
          name: {{ printf "%s-git-platform" .Release.Name }}
          port: {{ $git.httpPort }}
    - name: app
      matches:
        - path:
            type: PathPrefix
            value: /
      {{- with .Values.gateway.edgeRequestTimeout }}
      timeouts:
        request: {{ . | quote }}
      {{- end }}
      backendRefs:
        - group: ""
          kind: Service
          name: {{ printf "%s-edge" .Release.Name }}
          port: {{ $edge.httpPort }}
{{- end }}
