{{- define "minio.name" -}}
{{- printf "%s-minio" .Release.Name -}}
{{- end -}}

{{- define "minio.secretName" -}}
{{- printf "%s-minio" .Release.Name -}}
{{- end -}}

{{- define "minio.artifactSecretName" -}}
{{- default (printf "%s-minio-artifacts" .Release.Name) .Values.artifactService.credentialSecret -}}
{{- end -}}

{{- define "minio.artifactLabels" -}}
app.kubernetes.io/name: minio
app.kubernetes.io/instance: {{ .Release.Name }}
app.kubernetes.io/managed-by: {{ .Release.Service }}
app.kubernetes.io/component: artifact-provisioner
{{- end -}}

{{- define "minio.labels" -}}
app.kubernetes.io/name: minio
app.kubernetes.io/instance: {{ .Release.Name }}
app.kubernetes.io/managed-by: {{ .Release.Service }}
app.kubernetes.io/component: object-store
{{- end -}}
