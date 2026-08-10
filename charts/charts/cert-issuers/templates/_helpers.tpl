{{- define "cert-issuers.name" -}}
{{- printf "%s-cert-issuers" .Release.Name -}}
{{- end -}}

{{- define "cert-issuers.labels" -}}
app.kubernetes.io/name: cert-issuers
app.kubernetes.io/instance: {{ .Release.Name }}
app.kubernetes.io/managed-by: {{ .Release.Service }}
app.kubernetes.io/component: cert-issuers
{{- end -}}
