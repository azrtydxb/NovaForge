{{- /*
Air-gapped egress. A service listed in .Values.networkPolicy.airGapped may reach
the cluster — its peers, the datastores, DNS and the model gateway, which the
chart points at an in-cluster address — and the Kubernetes API, and nothing
outside it. Model traffic therefore cannot reach a hosted provider even if an
endpoint were misconfigured: the packet has nowhere to go.

It is a CiliumNetworkPolicy because the cluster runs Cilium, and a plain
NetworkPolicy's ipBlock does not match the API server's host addresses there, so
the one egress agent-runtime needs outside the pod network would be refused.

gates and work-reviews are not listed: their scanners fetch vulnerability data
and module metadata from the internet (osv-scanner, go list -m -u).
*/ -}}
{{- if .Values.networkPolicy.enabled }}
{{- range $name := .Values.networkPolicy.airGapped }}
---
apiVersion: cilium.io/v2
kind: CiliumNetworkPolicy
metadata:
  name: {{ $.Release.Name }}-{{ $name }}-egress
  labels:
    {{- include "novaforge.labels" $ | nindent 4 }}
    app.kubernetes.io/component: {{ $name }}
spec:
  endpointSelector:
    matchLabels:
      app.kubernetes.io/component: {{ $name }}
      app.kubernetes.io/instance: {{ $.Release.Name }}
  egress:
    - toEntities:
        - cluster
        - kube-apiserver
{{- end }}
{{- end }}
