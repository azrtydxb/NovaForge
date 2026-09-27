{{- /*
PostgreSQL, Redis and MinIO are deployed in-chart so a NovaForge install is
self-contained and works air-gapped. Point the secret at external endpoints and
set datastores.enabled=false to use managed ones instead.
*/ -}}
{{- if .Values.datastores.enabled }}
apiVersion: v1
kind: PersistentVolumeClaim
metadata:
  name: {{ .Release.Name }}-pg-data
  {{- if .Values.storage.pruneProtection }}
  annotations: {sync.kuvryn.io/prune: disabled}
  {{- end }}
spec:
  accessModes: [ReadWriteOnce]
  storageClassName: {{ .Values.datastores.postgres.storageClass | quote }}
  resources: {requests: {storage: {{ .Values.datastores.postgres.size }}}}
---
apiVersion: apps/v1
kind: Deployment
metadata: {name: {{ .Release.Name }}-postgres}
spec:
  strategy: {type: RollingUpdate, rollingUpdate: {maxSurge: 0, maxUnavailable: 1}}
  replicas: 1
  selector: {matchLabels: {app: {{ .Release.Name }}-postgres}}
  template:
    metadata: {labels: {app: {{ .Release.Name }}-postgres}}
    spec:
      containers:
        - name: postgres
          image: {{ .Values.datastores.postgres.image }}
          env:
            - {name: POSTGRES_USER, value: novaforge}
            {{- if .Values.secrets.existingSecret }}
            - name: POSTGRES_PASSWORD
              value: null
              valueFrom:
                secretKeyRef:
                  name: {{ include "novaforge.secretName" . }}
                  key: POSTGRES_PASSWORD
            {{- else }}
            - {name: POSTGRES_PASSWORD, value: {{ .Values.secrets.postgresPassword | quote }}}
            {{- end }}
            - {name: POSTGRES_DB, value: novaforge}
            - {name: PGDATA, value: /var/lib/postgresql/data/pgdata}
          ports: [{containerPort: 5432}]
          volumeMounts: [{name: data, mountPath: /var/lib/postgresql/data}]
          readinessProbe:
            exec: {command: [pg_isready, -U, novaforge]}
            initialDelaySeconds: 10
      volumes:
        - name: data
          persistentVolumeClaim: {claimName: {{ .Release.Name }}-pg-data}
---
apiVersion: v1
kind: Service
metadata: {name: {{ .Release.Name }}-postgres}
spec:
  selector: {app: {{ .Release.Name }}-postgres}
  ports: [{port: 5432, targetPort: 5432}]
---
apiVersion: apps/v1
kind: Deployment
metadata: {name: {{ .Release.Name }}-redis}
spec:
  replicas: 1
  selector: {matchLabels: {app: {{ .Release.Name }}-redis}}
  template:
    metadata: {labels: {app: {{ .Release.Name }}-redis}}
    spec:
      containers:
        - name: redis
          image: {{ .Values.datastores.redis.image }}
          args: ["redis-server", "--appendonly", "yes"]
          ports: [{containerPort: 6379}]
          readinessProbe:
            exec: {command: [redis-cli, ping]}
            initialDelaySeconds: 5
---
apiVersion: v1
kind: Service
metadata: {name: {{ .Release.Name }}-redis}
spec:
  selector: {app: {{ .Release.Name }}-redis}
  ports: [{port: 6379, targetPort: 6379}]
---
apiVersion: v1
kind: PersistentVolumeClaim
metadata:
  name: {{ .Release.Name }}-minio-data
  {{- if .Values.storage.pruneProtection }}
  annotations: {sync.kuvryn.io/prune: disabled}
  {{- end }}
spec:
  accessModes: [ReadWriteOnce]
  storageClassName: {{ .Values.datastores.minio.storageClass | quote }}
  resources: {requests: {storage: {{ .Values.datastores.minio.size }}}}
---
apiVersion: apps/v1
kind: Deployment
metadata: {name: {{ .Release.Name }}-minio}
spec:
  strategy: {type: RollingUpdate, rollingUpdate: {maxSurge: 0, maxUnavailable: 1}}
  replicas: 1
  selector: {matchLabels: {app: {{ .Release.Name }}-minio}}
  template:
    metadata: {labels: {app: {{ .Release.Name }}-minio}}
    spec:
      containers:
        - name: minio
          image: {{ .Values.datastores.minio.image }}
          args: ["server", "/data", "--console-address", ":9001"]
          env:
            {{- if .Values.secrets.existingSecret }}
            - name: MINIO_ROOT_USER
              value: null
              valueFrom:
                secretKeyRef:
                  name: {{ include "novaforge.secretName" . }}
                  key: MINIO_ROOT_USER
            {{- else }}
            - {name: MINIO_ROOT_USER, value: {{ .Values.secrets.minioAccessKey | quote }}}
            {{- end }}
            {{- if .Values.secrets.existingSecret }}
            - name: MINIO_ROOT_PASSWORD
              value: null
              valueFrom:
                secretKeyRef:
                  name: {{ include "novaforge.secretName" . }}
                  key: MINIO_ROOT_PASSWORD
            {{- else }}
            - {name: MINIO_ROOT_PASSWORD, value: {{ .Values.secrets.minioSecretKey | quote }}}
            {{- end }}
          ports: [{containerPort: 9000}, {containerPort: 9001}]
          volumeMounts: [{name: data, mountPath: /data}]
          readinessProbe:
            httpGet: {path: /minio/health/live, port: 9000}
            initialDelaySeconds: 10
      volumes:
        - name: data
          persistentVolumeClaim: {claimName: {{ .Release.Name }}-minio-data}
---
apiVersion: v1
kind: Service
metadata: {name: {{ .Release.Name }}-minio}
spec:
  selector: {app: {{ .Release.Name }}-minio}
  ports: [{name: api, port: 9000, targetPort: 9000}, {name: console, port: 9001, targetPort: 9001}]
{{- end }}
