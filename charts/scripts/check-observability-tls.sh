#!/usr/bin/env bash
set -euo pipefail

rendered=${1:?usage: check-observability-tls.sh RENDERED_MANIFEST RELEASE NAMESPACE}
release=${2:?usage: check-observability-tls.sh RENDERED_MANIFEST RELEASE NAMESPACE}
namespace=${3:?usage: check-observability-tls.sh RENDERED_MANIFEST RELEASE NAMESPACE}
[[ -f "$rendered" ]] || { echo "missing rendered manifest: $rendered" >&2; exit 1; }

kube_prometheus_chart=kube-prometheus-stack
if [[ "$release" == *"$kube_prometheus_chart"* ]]; then
  kube_prometheus_fullname=$release
else
  kube_prometheus_fullname="$release-$kube_prometheus_chart"
fi
kube_prometheus_fullname=${kube_prometheus_fullname:0:26}
kube_prometheus_fullname=${kube_prometheus_fullname%-}
prometheus_service="$kube_prometheus_fullname-prometheus"
alertmanager_service="$kube_prometheus_fullname-alertmanager"
prometheus_reloader_service="$kube_prometheus_fullname-prometheus-reloader-tls"
alertmanager_reloader_service="$kube_prometheus_fullname-alertmanager-reloader-tls"

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
  "$release-prometheus-reloader-internal-tls" \
  "$release-alertmanager-internal-tls" \
  "$release-alertmanager-reloader-internal-tls" \
  "$release-grafana-internal-tls" \
  "$release-loki-internal-tls"; do
  require "name: $monitor"
done
require "serverName: \"$prometheus_service.$namespace.svc\""
require "serverName: \"$prometheus_reloader_service.$namespace.svc\""
require "serverName: \"$alertmanager_service.$namespace.svc\""
require "serverName: \"$alertmanager_reloader_service.$namespace.svc\""
require "serverName: \"$release-grafana.$namespace.svc\""
require "serverName: \"$release-loki.$namespace.svc\""
require "name: $release-prometheus-alertmanager-tls-config"
require 'key: additional-alertmanager-configs.yaml'
require "- \"$alertmanager_service.$namespace.svc:9093\""
require "server_name: \"$alertmanager_service.$namespace.svc\""
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

service_port_app_protocols() {
  yq eval "select(.kind == \"Service\" and .metadata.name == \"$1\") | .spec.ports[] | select(.name == \"$2\") | .appProtocol" "$rendered" \
    | paste -sd, -
}
for service in "$prometheus_reloader_service" "$alertmanager_reloader_service"; do
  [[ "$(service_port_app_protocols "$service" reloader-web)" == "https" ]] || {
    echo "$service config-reloader scrape port does not advertise HTTPS" >&2
    exit 1
  }
done
for service in "$prometheus_service" "$alertmanager_service"; do
  [[ "$(yq eval-all "[select(.kind == \"Service\" and .metadata.name == \"$service\")] | length" "$rendered")" == "1" ]] || {
    echo "$service is not rendered exactly once under upstream ownership" >&2
    exit 1
  }
done

monitor_schemes() {
  yq eval "select(.kind == \"ServiceMonitor\" and .metadata.name == \"$1\") | .spec.endpoints[].scheme" "$rendered" \
    | paste -sd, -
}
for monitor in \
  "$release-prometheus-internal-tls" \
  "$release-prometheus-reloader-internal-tls" \
  "$release-alertmanager-internal-tls" \
  "$release-alertmanager-reloader-internal-tls" \
  "$release-grafana-internal-tls"; do
  [[ "$(monitor_schemes "$monitor")" == "https" ]] || {
    echo "$monitor is not a single verified HTTPS endpoint" >&2
    exit 1
  }
done
for stock_monitor in "$prometheus_service" "$alertmanager_service" "$release-grafana"; do
  if [[ -n "$(yq eval "select(.kind == \"ServiceMonitor\" and .metadata.name == \"$stock_monitor\") | .metadata.name" "$rendered")" ]]; then
    echo "duplicate upstream TLS-incompatible monitor remains: $stock_monitor" >&2
    exit 1
  fi
done

echo "OK: observability clients and self-monitors render verified internal-CA HTTPS identities for $release/$namespace"
