{{- define "novaforge.labels" -}}
app.kubernetes.io/name: novaforge
app.kubernetes.io/instance: {{ .Release.Name }}
app.kubernetes.io/managed-by: {{ .Release.Service }}
{{- end -}}

{{- define "novaforge.image" -}}
{{ .Values.image.registry }}/{{ .Values.image.repository }}/{{ .svc }}:{{ .Values.image.tag }}
{{- end -}}
