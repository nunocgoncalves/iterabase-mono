#!/usr/bin/env bash
set -euo pipefail

rendered=${1:?usage: check-observability-tls.sh RENDERED_MANIFEST}
[[ -f "$rendered" ]] || { echo "missing rendered manifest: $rendered" >&2; exit 1; }

require() {
  grep -Fq -- "$1" "$rendered" || {
    echo "missing observability TLS render contract: $1" >&2
    exit 1
  }
}
reject() {
  if grep -Fq -- "$1" "$rendered"; then
    echo "insecure observability TLS render contract remains: $1" >&2
    exit 1
  fi
}

for monitor in \
  iterabase-prometheus-internal-tls \
  iterabase-alertmanager-internal-tls \
  iterabase-grafana-internal-tls \
  iterabase-loki-internal-tls; do
  require "name: $monitor"
done
require 'serverName: "iterabase-kube-prometheus-prometheus.iterabase-system.svc"'
require 'serverName: "iterabase-kube-prometheus-alertmanager.iterabase-system.svc"'
require 'serverName: "iterabase-grafana.iterabase-system.svc"'
require 'serverName: "iterabase-loki.iterabase-system.svc"'
require 'REQUESTS_CA_BUNDLE'
require 'https://localhost:3000/api/admin/provisioning/datasources/reload'
require 'SSL_CERT_FILE'
require 'ca_file: /etc/iterabase/internal-ca/ca.crt'
require "server_name: 'iterabase-loki.iterabase-system.svc'"
require 'proxy_ssl_verify on;'
require 'proxy_pass       https://iterabase-loki.iterabase-system.svc.cluster.local:3100'
require 'tlsSkipVerify: false'
require 'insecure_skip_verify: false'
reject 'tlsSkipVerify: true'
reject 'insecure_skip_verify: true'
reject 'proxy_pass       http://iterabase-loki.iterabase-system.svc.cluster.local:3100'

echo "OK: observability clients and self-monitors render verified internal-CA HTTPS identities"
