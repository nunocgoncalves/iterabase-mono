{{- define "metallb-config.name" -}}
{{- printf "%s-edge" .Release.Name -}}
{{- end -}}

{{/* Name an additional pool without changing the established edge identity. */}}
{{- define "metallb-config.additionalPoolName" -}}
{{- printf "%s-%s" .root.Release.Name (required "metallb-config.additionalPools[].name is required" .pool.name) | trunc 63 | trimSuffix "-" -}}
{{- end -}}

{{- define "metallb-config.labels" -}}
app.kubernetes.io/name: metallb-config
app.kubernetes.io/instance: {{ .Release.Name }}
app.kubernetes.io/managed-by: {{ .Release.Service }}
app.kubernetes.io/component: metallb-config
{{- end -}}
