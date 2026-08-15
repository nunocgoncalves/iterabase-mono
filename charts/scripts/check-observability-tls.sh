#!/usr/bin/env bash
set -euo pipefail

rendered=${1:?usage: check-observability-tls.sh RENDERED_MANIFEST RELEASE NAMESPACE}
release=${2:?usage: check-observability-tls.sh RENDERED_MANIFEST RELEASE NAMESPACE}
namespace=${3:?usage: check-observability-tls.sh RENDERED_MANIFEST RELEASE NAMESPACE}
[[ -f "$rendered" ]] || { echo "missing rendered manifest: $rendered" >&2; exit 1; }

require() {
  grep -Fq -- "$1" "$rendered" || {
    echo "missing observability TLS render contract: $1" >&2
    exit 1
  }
}
reject() {
  if grep -Fq -- "$1" "$rendered"; then
    echo "insecure or non-portable observability TLS render contract remains: $1" >&2
    exit 1
  fi
}

for monitor in \
  "$release-prometheus-internal-tls" \
  "$release-alertmanager-internal-tls" \
  "$release-grafana-internal-tls" \
  "$release-loki-internal-tls"; do
  require "name: $monitor"
done
require "serverName: \"$release-kube-prometheus-prometheus.$namespace.svc\""
require "serverName: \"$release-kube-prometheus-alertmanager.$namespace.svc\""
require "serverName: \"$release-grafana.$namespace.svc\""
require "serverName: \"$release-loki.$namespace.svc\""
require "name: $release-prometheus-alertmanager-tls-config"
require 'key: additional-alertmanager-configs.yaml'
require "- \"$release-kube-prometheus-alertmanager.$namespace.svc:9093\""
require "server_name: \"$release-kube-prometheus-alertmanager.$namespace.svc\""
require 'ca_file: /etc/prometheus/secrets/observability-alertmanager-tls/ca.crt'
require 'REQUESTS_CA_BUNDLE'
require 'https://localhost:3000/api/admin/provisioning/datasources/reload'
require 'SSL_CERT_FILE'
require 'ca_file: /etc/iterabase/internal-ca/ca.crt'
require "server_name: '$release-loki.$namespace.svc'"
require 'proxy_ssl_verify on;'
require "proxy_pass       https://$release-loki.$namespace.svc.cluster.local:3100"
require 'tlsSkipVerify: false'
require 'insecure_skip_verify: false'
reject 'tlsSkipVerify: true'
reject 'insecure_skip_verify: true'
reject "proxy_pass       http://$release-loki.$namespace.svc.cluster.local:3100"
if [[ "$release/$namespace" != "iterabase/iterabase-system" ]]; then
  reject 'iterabase-kube-prometheus-alertmanager.iterabase-system.svc'
fi

echo "OK: observability clients and self-monitors render verified internal-CA HTTPS identities for $release/$namespace"
