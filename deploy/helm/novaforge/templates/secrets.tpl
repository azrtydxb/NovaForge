apiVersion: v1
kind: Secret
metadata:
  name: {{ .Release.Name }}-secrets
  labels: {{- include "novaforge.labels" . | nindent 4 }}
type: Opaque
stringData:
  JWT_SECRET: {{ .Values.secrets.jwtSecret | quote }}
  HMAC_SECRET: {{ .Values.secrets.hmacSecret | quote }}
  SECRETS_KEK: {{ .Values.secrets.secretsKek | quote }}
  DATABASE_URL: "postgres://novaforge:{{ .Values.secrets.postgresPassword }}@{{ .Release.Name }}-postgres:5432/novaforge?sslmode=disable"
  REDIS_URL: "redis://{{ .Release.Name }}-redis:6379"
  S3_ENDPOINT: "{{ .Release.Name }}-minio:9000"
  S3_ACCESS_KEY: {{ .Values.secrets.minioAccessKey | quote }}
  S3_SECRET_KEY: {{ .Values.secrets.minioSecretKey | quote }}
