{{- /*
The keypair git-platform serves the git transport with on its TLS port.

cert-manager issues it and the kubelet mounts the resulting Secret read-only
into the pod (see services.tpl); git-platform re-reads the files per handshake,
so a renewal reaches the process without a rollout. Nothing here puts key bytes
in Helm values — the chart requests a certificate, it does not carry one.

The names are the ones a client can actually verify against: the Service's short
name for in-cluster callers, and both FQDN forms because a pod's resolv.conf
search path decides which one a client sends in SNI. A client reaching the
LoadBalancer address instead needs either that address in gitTLS.ipAddresses or
a resolve override to one of these names (tests/e2e/deploy_test.sh does the
latter), because an IP is never covered by a DNS SAN.
*/ -}}
{{- if .Values.gitTLS.enabled }}
{{- $git := index .Values.services "git-platform" }}
{{- if not $git }}{{ fail "gitTLS.enabled is set but values.yaml lists no git-platform service" }}{{ end }}
{{- if not $git.httpsPort }}{{ fail "gitTLS.enabled is set but git-platform has no httpsPort to serve it on" }}{{ end }}
{{- $name := printf "%s-git-platform" .Release.Name }}
---
apiVersion: cert-manager.io/v1
kind: Certificate
metadata:
  name: {{ $name }}-tls
  labels:
    {{- include "novaforge.labels" . | nindent 4 }}
    app.kubernetes.io/component: git-platform
spec:
  secretName: {{ $name }}-tls
  issuerRef:
    name: {{ .Values.gitTLS.issuer.name | quote }}
    kind: {{ .Values.gitTLS.issuer.kind | default "ClusterIssuer" }}
  commonName: {{ printf "%s.%s.svc" $name .Release.Namespace | quote }}
  dnsNames:
    - {{ $name | quote }}
    - {{ printf "%s.%s.svc" $name .Release.Namespace | quote }}
    - {{ printf "%s.%s.svc.cluster.local" $name .Release.Namespace | quote }}
    {{- range .Values.gitTLS.extraDNSNames }}
    - {{ . | quote }}
    {{- end }}
  {{- if .Values.gitTLS.ipAddresses }}
  ipAddresses:
    {{- range .Values.gitTLS.ipAddresses }}
    - {{ . | quote }}
    {{- end }}
  {{- end }}
{{- end }}
