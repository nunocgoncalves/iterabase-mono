{{- define "inference-gateway.name" -}}
{{- printf "%s-gateway" .Release.Name -}}
{{- end -}}

{{- /* The supervisor mTLS workload listener (HOR-398) serving leaf Secret name. */ -}}
{{- define "inference-gateway.workloadTLSSecretName" -}}
{{- .Values.workload.tlsSecretName | default (printf "%s-gateway-workload-tls" .Release.Name) -}}
{{- end -}}

{{- /* The platform workflow CA Secret that worker leaves are chained to (client CA). */ -}}
{{- define "inference-gateway.workloadCASecretName" -}}
{{- .Values.workload.clientCASecretName | default (printf "%s-control-plane-gateway-ca" .Release.Name) -}}
{{- end -}}

{{- define "inference-gateway.adminSecretName" -}}
{{- if .Values.adminApiKey.secret -}}{{- .Values.adminApiKey.secret -}}{{- else -}}{{- printf "%s-gateway-admin" .Release.Name -}}{{- end -}}
{{- end -}}

{{- define "inference-gateway.pgHost" -}}
{{- if .Values.postgresql.host -}}{{- .Values.postgresql.host -}}{{- else -}}{{- printf "%s-postgresql" .Release.Name -}}{{- end -}}
{{- end -}}

{{- define "inference-gateway.pgSecret" -}}
{{- if .Values.postgresql.passwordSecret -}}{{- .Values.postgresql.passwordSecret -}}{{- else -}}{{- printf "%s-postgresql" .Release.Name -}}{{- end -}}
{{- end -}}

{{- define "inference-gateway.redisSecret" -}}
{{- if .Values.redis.passwordSecret -}}{{- .Values.redis.passwordSecret -}}{{- else -}}{{- printf "%s-redis" .Release.Name -}}{{- end -}}
{{- end -}}

{{- /* The internal CA root Secret name. Local override -> global -> the
     <release>-internal-ca-root convention (what cert-issuers creates), so the
     overlay never hardcodes the release name. */ -}}
{{- define "inference-gateway.tlsCASecretName" -}}
{{- .Values.tls.caSecretName | default (dig "internalTLS" "caSecretName" "" (.Values.global | default (dict))) | default (printf "%s-internal-ca-root" .Release.Name) -}}
{{- end -}}

{{- define "inference-gateway.redisHost" -}}
{{- if .Values.redis.host -}}{{- .Values.redis.host -}}{{- else -}}{{- printf "%s-redis" .Release.Name -}}{{- end -}}
{{- end -}}

{{- define "inference-gateway.databaseURL" -}}
{{- $ssl := "disable" -}}
{{- if (or .Values.tls.enabled (dig "internalTLS" "enabled" false (.Values.global | default (dict)))) -}}{{- $ssl = printf "verify-full&sslrootcert=%s" .Values.tls.caMountPath -}}{{- end -}}
postgres://{{ .Values.postgresql.username }}:$(PGPASSWORD)@{{ include "inference-gateway.pgHost" . }}:{{ .Values.postgresql.port }}/{{ .Values.postgresql.database }}?sslmode={{ $ssl }}
{{- end -}}

{{- define "inference-gateway.redisURL" -}}
{{- if (or .Values.tls.enabled (dig "internalTLS" "enabled" false (.Values.global | default (dict)))) -}}
rediss://:$(REDIS_PASSWORD)@{{ include "inference-gateway.redisHost" . }}:{{ .Values.redis.port }}/0
{{- else -}}
redis://{{ include "inference-gateway.redisHost" . }}:{{ .Values.redis.port }}/0
{{- end -}}
{{- end -}}

{{- define "inference-gateway.labels" -}}
app.kubernetes.io/name: inference-gateway
app.kubernetes.io/instance: {{ .Release.Name }}
app.kubernetes.io/managed-by: {{ .Release.Service }}
app.kubernetes.io/component: gateway
{{- end -}}
