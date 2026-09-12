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
            {{- /*
            Peer addresses use the canonical names the services read, not names
            derived from the Helm keys: the deployment name is work-reviews but
            the variable is WORK_ADDR, and deriving it produced WORK_REVIEWS_ADDR,
            which every dependent service rejected at startup.
            */ -}}
            {{- $peers := dict
              "IDENTITY_ADDR" "identity"
              "GIT_ADDR" "git-platform"
              "WORK_ADDR" "work-reviews"
              "REVIEWS_ADDR" "work-reviews"
              "CI_ADDR" "ci-runner"
              "GATES_ADDR" "gates"
              "AGENTS_ADDR" "agent-runtime"
              "GRAPH_ADDR" "engineering-graph"
              "MCP_ADDR" "mcp-server" }}
            {{- range $var, $target := $peers }}
            {{- $tcfg := index $.Values.services $target }}
            {{- if and $tcfg $tcfg.grpcPort }}
            - name: {{ $var }}
              value: "{{ $.Release.Name }}-{{ $target }}:{{ $tcfg.grpcPort }}"
            {{- end }}
            {{- end }}
            - name: AI_ENDPOINT
              value: {{ $.Values.ai.endpoint | quote }}
            - name: AI_MODEL
              value: {{ $.Values.ai.model | quote }}
            - name: AI_API_KEY
              valueFrom:
                secretKeyRef:
                  name: {{ $.Release.Name }}-secrets
                  key: AI_API_KEY
            - name: EMBED_ENDPOINT
              value: {{ $.Values.ai.embedEndpoint | quote }}
            - name: EMBED_MODEL
              value: {{ $.Values.ai.embedModel | quote }}
            - name: AI_PROVIDER_OPTIONS
              value: {{ $.Values.ai.providerOptions | toJson | quote }}
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
