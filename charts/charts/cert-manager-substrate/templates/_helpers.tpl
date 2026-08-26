{{- define "cert-manager-substrate.name" -}}
{{- default .Chart.Name .Values.nameOverride | trunc 63 | trimSuffix "-" -}}
{{- end -}}

{{- define "cert-manager-substrate.fullname" -}}
{{- printf "%s-%s" .Release.Name (include "cert-manager-substrate.name" .) | trunc 63 | trimSuffix "-" -}}
{{- end -}}

{{- define "cert-manager-substrate.labels" -}}
app.kubernetes.io/name: {{ include "cert-manager-substrate.name" . }}
app.kubernetes.io/instance: {{ .Release.Name }}
app.kubernetes.io/managed-by: {{ .Release.Service }}
helm.sh/chart: {{ printf "%s-%s" .Chart.Name .Chart.Version | replace "+" "_" }}
{{- end -}}

{{- define "cert-manager-substrate.platformRelease" -}}
{{- $configured := dig "internalTLS" "platformRelease" "" (.Values.global | default (dict)) -}}
{{- $configured | default (trimSuffix "-cert-manager" .Release.Name) -}}
{{- end -}}

{{- define "cert-manager-substrate.internalTLSIssuer" -}}
{{- dig "internalTLS" "issuerName" "internal-ca" (.Values.global | default (dict)) -}}
{{- end -}}

{{- define "cert-manager-substrate.validate" -}}
{{- if dig "internalTLS" "enabled" false (.Values.global | default (dict)) -}}
{{- $platformRelease := include "cert-manager-substrate.platformRelease" . -}}
{{- if eq $platformRelease "" -}}{{- fail "global.internalTLS.platformRelease could not be derived" -}}{{- end -}}
{{- if ne (include "cert-manager-substrate.internalTLSIssuer" .) "internal-ca" -}}{{- fail "the Iterabase internal CA ClusterIssuer name must remain internal-ca" -}}{{- end -}}
{{- if or (ne .Values.internalCABootstrap.image.repository "docker.io/alpine/k8s") (ne (toString .Values.internalCABootstrap.image.tag) "1.34.1") (ne .Values.internalCABootstrap.image.digest "sha256:ec714df3813b5405292860f8a1c55c5727bf8c33c88992f1e981efad8065547f") -}}
{{- fail "internal CA bootstrap image identity is chart-owned" -}}
{{- end -}}
{{- end -}}
{{- end -}}
