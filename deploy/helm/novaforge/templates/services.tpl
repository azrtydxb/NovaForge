{{- /*
One Deployment and Service per entry in .Values.services. The services differ
only in ports, schema and a couple of flags, so they are generated rather than
copied: a fix to the probe or the env wiring lands in one place.
*/ -}}
{{- $bindings := dict
  "openbao" (dict "owner" "gates" "env" "NF_OPENBAO_CONFIG_FILE")
  "deployments" (dict "owner" "gates" "env" "NF_DEPLOYMENT_CONFIG_FILE")
  "semanticProducer" (dict "owner" "engineering-graph" "env" "NF_SEMANTIC_PRODUCER_CONFIG_FILE")
  "mcpHTTP" (dict "owner" "agent-runtime" "env" "NF_MCP_HTTP_CONFIG_FILE")
  "reviews" (dict "owner" "work-reviews" "env" "NF_REVIEW_CONFIG_FILE") }}
{{- $configured := .Values.operatorConfigs | default dict }}
{{- range $key, $cfg := $configured }}
{{- if not (hasKey $bindings $key) }}{{ fail (printf "unknown operatorConfigs key %s" $key) }}{{ end }}
{{- range $field, $_ := $cfg }}
{{- if not (has $field (list "secretName" "configKey")) }}{{ fail (printf "unknown operatorConfigs.%s field %s" $key $field) }}{{ end }}
{{- end }}
{{- $secret := $cfg.secretName | default "" }}
{{- if and $secret (or (gt (len $secret) 253) (not (regexMatch "^[a-z0-9]([-a-z0-9]*[a-z0-9])?(\\.[a-z0-9]([-a-z0-9]*[a-z0-9])?)*$" $secret))) }}{{ fail "invalid operator secretName" }}{{ end }}
{{- $file := $cfg.configKey | default "config.json" }}
{{- if or (eq $file ".") (eq $file "..") (gt (len $file) 253) (not (regexMatch "^[a-zA-Z0-9._-]+$" $file)) }}{{ fail "invalid operator configKey" }}{{ end }}
{{- end }}
{{- range $name, $svc := .Values.services }}
{{- if and (eq $name "gates") $svc.analysisImage }}
{{- if not (regexMatch "^[^[:space:]@]+@sha256:[a-f0-9]{64}$" $svc.analysisImage) }}{{ fail "gates analysisImage must be digest-pinned" }}{{ end }}
{{- end }}
{{- /*
Only git-platform serves the git transport, so only it mounts the certificate
cert-manager issues in certificate.tpl. The mount is what makes the TLS listener
exist at all: git-platform binds its TLS port only when both files are named.
*/ -}}
{{- $gitTLS := and (eq $name "git-platform") $.Values.gitTLS.enabled $svc.httpsPort }}
{{- $configs := dict }}
{{- $mounts := dict }}
{{- range $key, $binding := $bindings }}
{{- if eq $binding.owner $name }}
{{- $cfg := get $configured $key | default dict }}
{{- $entry := dict "env" $binding.env "secret" ($cfg.secretName | default "") "file" ($cfg.configKey | default "config.json") }}
{{- $_ := set $configs $key $entry }}
{{- if $entry.secret }}{{ $_ := set $mounts $key $entry }}{{ end }}
{{- end }}
{{- end }}
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
      {{- if or (len $mounts) $gitTLS }}
      # Distroless consumers run as 65532; group-readable Secrets must not
      # become root-only just because a feature was enabled. The TLS keypair is
      # one of them: unreadable, and git-platform refuses to start.
      securityContext:
        fsGroup: 65532
      {{- end }}
      {{- if $.Values.image.pullSecret }}
      imagePullSecrets:
        - name: {{ $.Values.image.pullSecret }}
      {{- end }}
      {{- if or $svc.rbac $svc.sandboxRbac (hasKey $mounts "semanticProducer") }}
      serviceAccountName: {{ $.Release.Name }}-{{ $name }}
      {{- end }}
      containers:
        - name: {{ $name }}
          image: "{{ $.Values.image.registry }}/{{ $.Values.image.repository }}/{{ $name }}:{{ $.Values.image.tag }}"
          imagePullPolicy: {{ $.Values.image.pullPolicy }}
          envFrom:
            - secretRef:
                name: {{ include "novaforge.secretName" $ }}
          env:
            - name: SERVICE_NAME
              value: {{ $name }}
            - name: DB_SCHEMA
              value: {{ $svc.schema | quote }}
            - name: HEALTH_PORT
              value: "8090"
            {{- if eq $name "gates" }}
            - name: NF_GATE_ANALYSIS_IMAGE
              value: {{ $svc.analysisImage | default "" | quote }}
            {{- end }}
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
            {{- if $gitTLS }}
            # Paths, not bytes: cert-manager renews in place and the kubelet
            # swaps the files, which git-platform re-reads per handshake.
            - name: NF_GIT_TLS_CERT_FILE
              value: /etc/novaforge/git-tls/tls.crt
            - name: NF_GIT_TLS_KEY_FILE
              value: /etc/novaforge/git-tls/tls.key
            {{- end }}
            {{- if eq $name "git-platform" }}
            # Only git-platform serves LFS, so only it is given the limit. The
            # variable is not conditional on anything: a missing limit is a
            # deployment whose object storage one push can fill.
            - name: NF_LFS_MAX_OBJECT_BYTES
              value: {{ $.Values.lfs.maxObjectBytes | quote }}
            - name: NF_OUTBOUND_DESTINATIONS
              value: {{ $.Values.outbound.destinations | toJson | quote }}
            - name: NF_GIT_PUBLIC_URL
              value: {{ $.Values.lfs.publicURL | default (printf "https://%s-git-platform.%s.svc:8443" $.Release.Name $.Release.Namespace) | quote }}
            {{- end }}
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
                  name: {{ include "novaforge.secretName" $ }}
                  key: AI_API_KEY
            - name: EMBED_ENDPOINT
              value: {{ $.Values.ai.embedEndpoint | quote }}
            - name: EMBED_MODEL
              value: {{ $.Values.ai.embedModel | quote }}
            - name: AI_PROVIDER_OPTIONS
              value: {{ $.Values.ai.providerOptions | toJson | quote }}
            - name: AI_MODEL_PRICES
              value: {{ $.Values.ai.modelPrices | default dict | toJson | quote }}
            - name: AUTO_MERGE_ENABLED
              value: {{ $.Values.factory.autoMerge.enabled | quote }}
            - name: AUTO_MERGE_MAX_FILES_CHANGED
              value: {{ $.Values.factory.autoMerge.maxFilesChanged | quote }}
            - name: MAINTENANCE_INTERVAL_HOURS
              value: {{ $.Values.factory.maintenance.intervalHours | quote }}
            {{- range $key, $cfg := $configs }}
            - name: {{ $cfg.env }}
              value: {{ if $cfg.secret }}{{ printf "/etc/novaforge/operator/%s/%s" $key $cfg.file | quote }}{{ else }}""{{ end }}
            {{- end }}
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
            {{- if $gitTLS }}
            - name: https
              containerPort: {{ $svc.httpsPort }}
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
          {{- if or $svc.needsRepos (len $mounts) $gitTLS }}
          volumeMounts:
            {{- if $svc.needsRepos }}
            - name: repos
              mountPath: /data/repos
            {{- end }}
            {{- if $gitTLS }}
            - name: git-tls
              mountPath: /etc/novaforge/git-tls
              readOnly: true
            {{- end }}
            {{- range $key, $cfg := $mounts }}
            - name: operator-{{ lower $key }}
              mountPath: /etc/novaforge/operator/{{ $key }}
              readOnly: true
            {{- end }}
          {{- end }}
      {{- if or $svc.needsRepos (len $mounts) $gitTLS }}
      volumes:
        {{- if $svc.needsRepos }}
        - name: repos
          persistentVolumeClaim:
            claimName: {{ $.Release.Name }}-repos
        {{- end }}
        {{- if $gitTLS }}
        # The Secret certificate.tpl's Certificate writes. A directory mount,
        # not subPath items: a subPath does not see the kubelet's update when
        # cert-manager renews, and the pod would serve the old keypair.
        - name: git-tls
          secret:
            secretName: {{ $.Release.Name }}-git-platform-tls
            defaultMode: 0440
        {{- end }}
        {{- range $key, $cfg := $mounts }}
        - name: operator-{{ lower $key }}
          secret:
            secretName: {{ $cfg.secret | quote }}
            defaultMode: 0440
        {{- end }}
      {{- end }}
---
apiVersion: v1
kind: Service
metadata:
  name: {{ $.Release.Name }}-{{ $name }}
  labels:
    {{- include "novaforge.labels" $ | nindent 4 }}
    app.kubernetes.io/component: {{ $name }}
  {{- if and (eq ($svc.extraServiceType | default "") "LoadBalancer") $svc.loadBalancerIP }}
  annotations:
    # kube-vip's request for a fixed address. Without it the cloud provider
    # picks one, and on kw it once handed git-platform an address another
    # service already held (192.168.10.130, BuildKit's).
    kube-vip.io/loadbalancerIPs: {{ $svc.loadBalancerIP | quote }}
  {{- end }}
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
    {{- if $gitTLS }}
    - name: https
      port: {{ $svc.httpsPort }}
      targetPort: https
    {{- end }}
    {{- if $svc.sshPort }}
    - name: ssh
      port: {{ $svc.sshPort }}
      targetPort: ssh
    {{- end }}
{{- end }}
