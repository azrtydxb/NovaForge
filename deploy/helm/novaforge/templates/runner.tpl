{{- /*
The CI runner holds a persistent outbound gRPC stream and has no inbound
surface, so it gets no Service. It needs permission to create the per-job pods
that isolate repository-supplied commands from the runner host.
*/ -}}
{{- if .Values.runner.orgId }}
apiVersion: v1
kind: ServiceAccount
metadata:
  name: {{ .Release.Name }}-runner
---
apiVersion: rbac.authorization.k8s.io/v1
kind: Role
metadata:
  name: {{ .Release.Name }}-runner
  namespace: {{ .Values.runner.jobNamespace }}
rules:
  - apiGroups: [""]
    resources: [pods, pods/log]
    verbs: [create, get, list, watch, delete]
---
apiVersion: rbac.authorization.k8s.io/v1
kind: RoleBinding
metadata:
  name: {{ .Release.Name }}-runner
  namespace: {{ .Values.runner.jobNamespace }}
roleRef:
  apiGroup: rbac.authorization.k8s.io
  kind: Role
  name: {{ .Release.Name }}-runner
subjects:
  - kind: ServiceAccount
    name: {{ .Release.Name }}-runner
    namespace: {{ .Release.Namespace }}
---
apiVersion: apps/v1
kind: Deployment
metadata:
  name: {{ .Release.Name }}-runner
  labels:
    {{- include "novaforge.labels" . | nindent 4 }}
    app.kubernetes.io/component: runner
spec:
  replicas: {{ .Values.runner.replicas }}
  selector:
    matchLabels:
      app.kubernetes.io/component: runner
      app.kubernetes.io/instance: {{ .Release.Name }}
  template:
    metadata:
      labels:
        {{- include "novaforge.labels" . | nindent 8 }}
        app.kubernetes.io/component: runner
    spec:
      serviceAccountName: {{ .Release.Name }}-runner
      {{- if .Values.image.pullSecret }}
      imagePullSecrets:
        - name: {{ .Values.image.pullSecret }}
      {{- end }}
      containers:
        - name: runner
          image: "{{ .Values.image.registry }}/{{ .Values.image.repository }}/runner:{{ .Values.image.tag }}"
          imagePullPolicy: {{ .Values.image.pullPolicy }}
          envFrom:
            - secretRef:
                name: {{ .Release.Name }}-secrets
          env:
            - name: CI_ADDR
              value: "{{ .Release.Name }}-ci-runner:{{ (index .Values.services "ci-runner").grpcPort }}"
            - name: RUNNER_LABELS
              value: {{ join "," .Values.runner.labels | quote }}
            - name: RUNNER_JOB_NAMESPACE
              value: {{ .Values.runner.jobNamespace | quote }}
            - name: RUNNER_ORG_ID
              value: {{ .Values.runner.orgId | quote }}
            - name: RUNNER_NAME
              valueFrom:
                fieldRef:
                  fieldPath: metadata.name
          resources: {{- toYaml .Values.resources | nindent 12 }}
{{- end }}
