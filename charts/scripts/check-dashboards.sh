#!/usr/bin/env bash
set -euo pipefail

./scripts/render-iterabase-dashboards.py --check
rendered=$(helm template dashboard-contract charts/iterabase-platform -f values-observability.yaml)
labels=$(grep -c 'grafana_dashboard: "1"' <<<"$rendered")
folders=$(grep -c 'grafana_folder:' <<<"$rendered")
iterabase=$(grep -c 'grafana_folder: Iterabase' <<<"$rendered")
infrastructure=$(grep -c 'grafana_folder: Infrastructure' <<<"$rendered")
observability=$(grep -c 'grafana_folder: Observability' <<<"$rendered")
if [[ "$labels" -ne "$folders" ]]; then
  echo "ERROR: every provisioned dashboard must have an organized Grafana folder (dashboards=$labels folders=$folders)" >&2
  exit 1
fi
if [[ "$iterabase" -ne 7 || "$infrastructure" -ne 1 || "$observability" -ne 1 ]]; then
  echo "ERROR: expected organized dashboard suite Iterabase=7 Infrastructure=1 Observability=1; got $iterabase/$infrastructure/$observability" >&2
  exit 1
fi
for uid in platform-overview control-plane execution-runtime tool-runtime inference-model-serving data-storage platform-infrastructure; do
  grep -q '"uid": "iterabase-'"$uid"'"' <<<"$rendered" || {
    echo "ERROR: missing stable dashboard uid iterabase-$uid" >&2
    exit 1
  }
done
for uid in infrastructure-components observability-stack; do
  grep -q '"uid": "iterabase-'"$uid"'"' <<<"$rendered" || {
    echo "ERROR: missing stable auxiliary dashboard uid iterabase-$uid" >&2
    exit 1
  }
done
echo "OK: $labels provisioned dashboards are organized across Kubernetes, Iterabase, Infrastructure, and Observability; stable UIDs are enforced"
