{{- /*
Bare repositories live on a ReadWriteMany volume so any git-platform replica
can serve any repository. A ReadWriteOnce volume would pin every repository to
one node and make the service unscalable.
*/ -}}
apiVersion: v1
kind: PersistentVolumeClaim
metadata:
  name: {{ .Release.Name }}-repos
  {{- if .Values.storage.pruneProtection }}
  annotations: {sync.kuvryn.io/prune: disabled}
  {{- end }}
  labels: {{- include "novaforge.labels" . | nindent 4 }}
spec:
  accessModes: [ReadWriteMany]
  storageClassName: {{ .Values.repos.storageClass | quote }}
  resources:
    requests:
      storage: {{ .Values.repos.size }}
