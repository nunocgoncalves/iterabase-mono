{{- define "metallb-config.name" -}}
{{- printf "%s-edge" .Release.Name -}}
{{- end -}}

{{- define "metallb-config.labels" -}}
app.kubernetes.io/name: metallb-config
app.kubernetes.io/instance: {{ .Release.Name }}
app.kubernetes.io/managed-by: {{ .Release.Service }}
app.kubernetes.io/component: metallb-config
{{- end -}}
