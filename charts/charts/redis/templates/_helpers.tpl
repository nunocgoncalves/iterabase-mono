{{- define "redis.name" -}}
{{- printf "%s-redis" .Release.Name -}}
{{- end -}}

{{- define "redis.secretName" -}}
{{- printf "%s-redis" .Release.Name -}}
{{- end -}}

{{- /* redis.authEnabled is the authoritative effective-AUTH predicate. The
     umbrella's internal-TLS mode pairs TLS with AUTH; standalone charts can
     still enable AUTH independently. Consumers compare this rendered boolean
     explicitly because a rendered "false" string is truthy in Go templates. */ -}}
{{- define "redis.authEnabled" -}}
{{- or .Values.auth.enabled (dig "internalTLS" "enabled" false (.Values.global | default (dict))) -}}
{{- end -}}

{{- define "redis.labels" -}}
app.kubernetes.io/name: redis
app.kubernetes.io/instance: {{ .Release.Name }}
app.kubernetes.io/managed-by: {{ .Release.Service }}
app.kubernetes.io/component: cache
{{- end -}}

{{- /* redis.startCommand builds the redis-server argv from the tls/auth flags.
     Rendered as a single sh -c string; $REDIS_PASSWORD is expanded at runtime
     from the env (so the password is not baked into the chart). */ -}}
{{- define "redis.startCommand" -}}
{{- $parts := list "exec" "redis-server" -}}
{{- if (or .Values.tls.enabled (dig "internalTLS" "enabled" false (.Values.global | default (dict)))) -}}
{{- $parts = concat $parts (list "--tls-port" "6379" "--port" "0" "--tls-cert-file" "/tls/tls.crt" "--tls-key-file" "/tls/tls.key" "--tls-auth-clients" "no") -}}
{{- end -}}
{{- if eq (include "redis.authEnabled" .) "true" -}}
{{- $parts = concat $parts (list "--requirepass" (printf "%q" "$REDIS_PASSWORD")) -}}
{{- end -}}
{{- join " " $parts -}}
{{- end -}}

{{- /* redis.readyCommand builds the redis-cli readiness argv. --insecure skips
     cert verification for the local probe (the server cert is internal-CA; the
     CA is not mounted in the server pod). */ -}}
{{- define "redis.readyCommand" -}}
{{- $parts := list "redis-cli" -}}
{{- if (or .Values.tls.enabled (dig "internalTLS" "enabled" false (.Values.global | default (dict)))) -}}
{{- $parts = concat $parts (list "--tls" "--insecure") -}}
{{- end -}}
{{- if eq (include "redis.authEnabled" .) "true" -}}
{{- $parts = concat $parts (list "-a" (printf "%q" "$REDIS_PASSWORD")) -}}
{{- end -}}
{{- $parts = concat $parts (list "ping") -}}
{{- join " " $parts -}}
{{- end -}}
