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
# The manager reconciliation panel must target the stable control-plane manager
# scrape identity so unrelated controller-runtime producers (MetalLB, GPU
# Operator, ingress) cannot be misattributed.
grep -Fq 'controller_runtime_reconcile_total{namespace=~\"$namespace\",result=\"error\",component=\"manager\"}' <<<"$rendered" || {
  echo 'ERROR: manager reconciliation dashboard panel must target the stable component="manager" scrape identity' >&2
  exit 1
}
for title in \
  'Workspace free bytes' \
  'Workspace free ratio' \
  'Workspace capacity warnings' \
  'Workspace credit gates'; do
  grep -Fq "\"title\": \"$title\"" <<<"$rendered" || {
    echo "ERROR: 50 — Data and Storage is missing dedicated workspace panel: $title" >&2
    exit 1
  }
done
for query in \
  'control_plane_harness_workspace_free_bytes' \
  'control_plane_harness_workspace_free_ratio' \
  'control_plane_harness_workspace_capacity_warning' \
  'control_plane_harness_workspace_credit_gated'; do
  grep -Fq "$query" <<<"$rendered" || {
    echo "ERROR: workspace dashboard contract is missing query fragment: $query" >&2
    exit 1
  }
done
echo "OK: $labels provisioned dashboards are organized across Kubernetes, Iterabase, Infrastructure, and Observability; stable UIDs and dedicated workspace capacity panels are enforced"
