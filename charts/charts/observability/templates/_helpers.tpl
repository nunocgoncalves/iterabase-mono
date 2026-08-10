{{/*
Common labels for the observability chart's own resources (not the upstream
kube-prometheus-stack / loki deps, which carry their own labels).
*/}}
{{- define "observability.labels" -}}
app.kubernetes.io/name: observability
app.kubernetes.io/instance: {{ .Release.Name }}
app.kubernetes.io/managed-by: {{ .Release.Service }}
app.kubernetes.io/part-of: iterabase-platform
helm.sh/chart: {{ printf "observability-%s" .Chart.Version | replace "+" "_" | trunc 63 | trimSuffix "-" }}
{{- end -}}

{{/*
The Loki Service URL for the Grafana datasource. The loki subchart (single-binary)
exposes the HTTP port on `<release>-loki`; all subcharts share the umbrella
release name.
*/}}
{{/*
Whether internal TLS (global.internalTLS.enabled) is on. The observability
chart reads this to render stack-component leaf Certificates and to switch the
Grafana datasource URLs to HTTPS (see stack-internal-tls.yaml + the datasource
templates). Mirrors the (or tls.enabled (dig internalTLS...)) pattern used by the
postgresql/redis/control-plane charts.
*/}}
{{/*
Whether internal TLS (global.internalTLS.enabled) is on. Use dig directly in
`if` (NOT via include — include stringifies "false", which is truthy).
*/}}
{{- define "observability.internalTLS" -}}
{{- dig "internalTLS" "enabled" false (.Values.global | default (dict)) -}}
{{- end -}}

{{/*
The Prometheus / Alertmanager / Loki Service URLs for the Grafana datasources.
Each subchart exposes its HTTP port on a release-scoped Service; all subcharts
share the umbrella release name. When internalTLS is on, switch to https (the
stack-component leaf certs' SANs cover these Service DNS names).
*/}}
{{- define "observability.urlScheme" -}}
{{- if (dig "internalTLS" "enabled" false (.Values.global | default (dict))) }}https{{- else -}}http{{- end -}}
{{- end -}}

{{- define "observability.prometheusURL" -}}
{{- printf "%s://%s-kube-prometheus-prometheus.%s.svc:9090" (include "observability.urlScheme" .) .Release.Name .Release.Namespace -}}
{{- end -}}

{{- define "observability.alertmanagerURL" -}}
{{- printf "%s://%s-kube-prometheus-alertmanager.%s.svc:9093" (include "observability.urlScheme" .) .Release.Name .Release.Namespace -}}
{{- end -}}

{{- define "observability.lokiURL" -}}
{{- printf "%s://%s-loki.%s.svc:3100" (include "observability.urlScheme" .) .Release.Name .Release.Namespace -}}
{{- end -}}
