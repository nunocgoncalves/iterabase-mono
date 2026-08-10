{{- define "control-plane.name" -}}
{{- printf "%s-control-plane" .Release.Name -}}
{{- end -}}

{{- define "control-plane.managerName" -}}
{{- printf "%s-control-plane-manager" .Release.Name -}}
{{- end -}}

{{- define "control-plane.apiName" -}}
{{- printf "%s-control-plane-api" .Release.Name -}}
{{- end -}}

{{- define "control-plane.gatewayName" -}}
{{- printf "%s-control-plane-gateway" .Release.Name -}}
{{- end -}}

{{- define "control-plane.dispatchName" -}}
{{- printf "%s-control-plane-dispatch" .Release.Name -}}
{{- end -}}

{{- define "control-plane.dispatchTLSSecretName" -}}
{{- default (printf "%s-control-plane-dispatch-tls" .Release.Name) .Values.dispatch.tls.serverSecret -}}
{{- end -}}

{{- define "control-plane.serviceAccountName" -}}
{{- printf "%s-control-plane-manager" .Release.Name -}}
{{- end -}}

{{- define "control-plane.gatewayServiceAccountName" -}}
{{- printf "%s-control-plane-gateway" .Release.Name -}}
{{- end -}}

{{- define "control-plane.dispatchServiceAccountName" -}}
{{- printf "%s-control-plane-dispatch" .Release.Name -}}
{{- end -}}

{{- define "control-plane.toolRunnerName" -}}
{{- printf "%s-tool-runner" .Release.Name -}}
{{- end -}}

{{- define "control-plane.toolRunnerTLSSecretName" -}}
{{- printf "%s-tool-runner-tls" .Release.Name -}}
{{- end -}}

{{- define "control-plane.jwtSecretName" -}}
{{- if .Values.jwt.secret -}}{{- .Values.jwt.secret -}}{{- else -}}{{- printf "%s-control-plane-jwt" .Release.Name -}}{{- end -}}
{{- end -}}

{{- define "control-plane.pgHost" -}}
{{- if .Values.postgresql.host -}}{{- .Values.postgresql.host -}}{{- else -}}{{- printf "%s-postgresql" .Release.Name -}}{{- end -}}
{{- end -}}

{{- define "control-plane.pgSecret" -}}
{{- if .Values.postgresql.passwordSecret -}}{{- .Values.postgresql.passwordSecret -}}{{- else -}}{{- printf "%s-postgresql" .Release.Name -}}{{- end -}}
{{- end -}}

{{- define "control-plane.databaseURL" -}}
{{- $ssl := "disable" -}}
{{- if (or .Values.tls.enabled (dig "internalTLS" "enabled" false (.Values.global | default (dict)))) -}}{{- $ssl = printf "verify-full&sslrootcert=%s" .Values.tls.caMountPath -}}{{- end -}}
postgres://{{ .Values.postgresql.auth.username }}:$(PGPASSWORD)@{{ include "control-plane.pgHost" . }}:{{ .Values.postgresql.port }}/{{ .Values.postgresql.auth.database }}?sslmode={{ $ssl }}
{{- end -}}

{{- define "control-plane.artifactEndpoint" -}}
{{- default (printf "%s-minio:9000" .Release.Name) .Values.artifact.endpoint -}}
{{- end -}}

{{- define "control-plane.artifactSecretName" -}}
{{- default (printf "%s-minio-artifacts" .Release.Name) .Values.artifact.credentialSecret -}}
{{- end -}}

{{- define "control-plane.artifactEnv" -}}
- name: ARTIFACT_ENABLED
  value: {{ .Values.artifact.enabled | quote }}
{{- if .Values.artifact.enabled }}
- name: ARTIFACT_ENDPOINT
  value: {{ include "control-plane.artifactEndpoint" . | quote }}
- name: ARTIFACT_ACCESS_KEY
  valueFrom:
    secretKeyRef:
      name: {{ include "control-plane.artifactSecretName" . }}
      key: accessKey
- name: ARTIFACT_SECRET_KEY
  valueFrom:
    secretKeyRef:
      name: {{ include "control-plane.artifactSecretName" . }}
      key: secretKey
- name: ARTIFACT_BUCKET
  value: {{ .Values.artifact.bucket | quote }}
- name: ARTIFACT_SECURE
  value: {{ .Values.artifact.secure | quote }}
- name: ARTIFACT_MAX_SIZE_BYTES
  value: {{ printf "%d" (int64 .Values.artifact.maxSizeBytes) | quote }}
{{- with .Values.artifact.defaultRetention }}
- name: ARTIFACT_DEFAULT_RETENTION
  value: {{ . | quote }}
{{- end }}
- name: ARTIFACT_PENDING_TTL
  value: {{ .Values.artifact.pendingTTL | quote }}
- name: ARTIFACT_SWEEP_INTERVAL
  value: {{ .Values.artifact.sweepInterval | quote }}
{{- end }}
{{- end -}}

{{- define "control-plane.gatewayConfig" -}}
gateway:
  inline_limit: {{ .Values.gateway.inlineLimit }}
  {{- if .Values.toolRunner.enabled }}
  approved_runners:
    - namespace: {{ .Release.Namespace | quote }}
      runner_id: {{ .Values.toolRunner.runnerId | quote }}
      spiffe_id: {{ printf "spiffe://%s/tool-runners/%s/%s" .Values.gateway.trustDomain .Release.Namespace .Values.toolRunner.runnerId | quote }}
      allowed_tool_namespaces:
        {{- toYaml (required "toolRunner.allowedToolNamespaces is required" .Values.toolRunner.allowedToolNamespaces) | nindent 8 }}
  {{- end }}
{{- end -}}

{{- define "control-plane.gatewayTLSSecretName" -}}
{{- default (printf "%s-control-plane-gateway-tls" .Release.Name) .Values.gateway.tls.serverSecret -}}
{{- end -}}

{{- define "control-plane.gatewayCASecretName" -}}
{{- default (printf "%s-control-plane-gateway-ca" .Release.Name) .Values.gateway.tls.clientCASecret -}}
{{- end -}}

{{- define "control-plane.spiffeCASecretName" -}}
{{- include "control-plane.gatewayCASecretName" . -}}
{{- end -}}

{{- define "control-plane.apiTLSSecretName" -}}
{{- printf "%s-control-plane-api-tls" .Release.Name -}}
{{- end -}}

{{- /* The internal CA root Secret name. Local override -> global -> the
     <release>-internal-ca-root convention (what cert-issuers creates), so the
     overlay never hardcodes the release name. */ -}}
{{- define "control-plane.tlsCASecretName" -}}
{{- .Values.tls.caSecretName | default (dig "internalTLS" "caSecretName" "" (.Values.global | default (dict))) | default (printf "%s-internal-ca-root" .Release.Name) -}}
{{- end -}}

{{- define "control-plane.labels" -}}
app.kubernetes.io/name: control-plane
app.kubernetes.io/instance: {{ .Release.Name }}
app.kubernetes.io/managed-by: {{ .Release.Service }}
{{- end -}}
