{{- /*
One Deployment and Service per entry in .Values.services. The services differ
only in ports, schema and a couple of flags, so they are generated rather than
copied: a fix to the probe or the env wiring lands in one place.
*/ -}}
{{- range $name, $svc := .Values.services }}
---
apiVersion: apps/v1
kind: Deployment
metadata:
  name: {{ $.Release.Name }}-{{ $name }}
  labels:
    {{- include "novaforge.labels" $ | nindent 4 }}
    app.kubernetes.io/component: {{ $name }}
spec:
  replicas: {{ $svc.replicas | default 1 }}
  selector:
    matchLabels:
      app.kubernetes.io/component: {{ $name }}
      app.kubernetes.io/instance: {{ $.Release.Name }}
  template:
    metadata:
      labels:
        {{- include "novaforge.labels" $ | nindent 8 }}
        app.kubernetes.io/component: {{ $name }}
    spec:
      {{- if $.Values.image.pullSecret }}
      imagePullSecrets:
        - name: {{ $.Values.image.pullSecret }}
      {{- end }}
      {{- if $svc.rbac }}
      serviceAccountName: {{ $.Release.Name }}-{{ $name }}
      {{- end }}
      containers:
        - name: {{ $name }}
          image: "{{ $.Values.image.registry }}/{{ $.Values.image.repository }}/{{ $name }}:{{ $.Values.image.tag }}"
          imagePullPolicy: {{ $.Values.image.pullPolicy }}
          envFrom:
            - secretRef:
                name: {{ $.Release.Name }}-secrets
          env:
            - name: SERVICE_NAME
              value: {{ $name }}
            - name: DB_SCHEMA
              value: {{ $svc.schema | quote }}
            - name: HEALTH_PORT
              value: "8090"
            {{- if $svc.grpcPort }}
            - name: GRPC_PORT
              value: {{ $svc.grpcPort | quote }}
            {{- end }}
            {{- if $svc.httpPort }}
            - name: HTTP_PORT
              value: {{ $svc.httpPort | quote }}
            {{- end }}
            {{- if $svc.sshPort }}
            - name: SSH_PORT
              value: {{ $svc.sshPort | quote }}
            {{- end }}
            - name: GIT_DATA_DIR
              value: /data/repos
            - name: IDENTITY_ADDR
              value: "{{ $.Release.Name }}-identity:{{ (index $.Values.services "identity").grpcPort }}"
            - name: GIT_ADDR
              value: "{{ $.Release.Name }}-git-platform:{{ (index $.Values.services "git-platform").grpcPort }}"
{{- range $peer, $pcfg := $.Values.services }}
{{- if and $pcfg.grpcPort (ne $peer "identity") (ne $peer "git-platform") }}
            - name: {{ $peer | upper | replace "-" "_" }}_ADDR
              value: "{{ $.Release.Name }}-{{ $peer }}:{{ $pcfg.grpcPort }}"
{{- end }}
{{- end }}
            - name: AI_ENDPOINT
              value: {{ $.Values.ai.endpoint | quote }}
            - name: AI_MODEL
              value: {{ $.Values.ai.model | quote }}
            - name: EMBED_ENDPOINT
              value: {{ $.Values.ai.embedEndpoint | quote }}
            - name: EMBED_MODEL
              value: {{ $.Values.ai.embedModel | quote }}
          ports:
            - name: health
              containerPort: 8090
            {{- if $svc.grpcPort }}
            - name: grpc
              containerPort: {{ $svc.grpcPort }}
            {{- end }}
            {{- if $svc.httpPort }}
            - name: http
              containerPort: {{ $svc.httpPort }}
            {{- end }}
            {{- if $svc.sshPort }}
            - name: ssh
              containerPort: {{ $svc.sshPort }}
            {{- end }}
          readinessProbe:
            httpGet: {path: /healthz, port: health}
            initialDelaySeconds: 5
            periodSeconds: 10
          livenessProbe:
            httpGet: {path: /healthz, port: health}
            initialDelaySeconds: 20
            periodSeconds: 20
          resources: {{- toYaml ($svc.resources | default $.Values.resources) | nindent 12 }}
          {{- if $svc.needsRepos }}
          volumeMounts:
            - name: repos
              mountPath: /data/repos
          {{- end }}
      {{- if $svc.needsRepos }}
      volumes:
        - name: repos
          persistentVolumeClaim:
            claimName: {{ $.Release.Name }}-repos
      {{- end }}
---
apiVersion: v1
kind: Service
metadata:
  name: {{ $.Release.Name }}-{{ $name }}
  labels:
    {{- include "novaforge.labels" $ | nindent 4 }}
    app.kubernetes.io/component: {{ $name }}
spec:
  type: {{ $svc.extraServiceType | default "ClusterIP" }}
  selector:
    app.kubernetes.io/component: {{ $name }}
    app.kubernetes.io/instance: {{ $.Release.Name }}
  ports:
    - name: health
      port: 8090
      targetPort: health
    {{- if $svc.grpcPort }}
    - name: grpc
      port: {{ $svc.grpcPort }}
      targetPort: grpc
    {{- end }}
    {{- if $svc.httpPort }}
    - name: http
      port: {{ $svc.httpPort }}
      targetPort: http
    {{- end }}
    {{- if $svc.sshPort }}
    - name: ssh
      port: {{ $svc.sshPort }}
      targetPort: ssh
    {{- end }}
{{- end }}
