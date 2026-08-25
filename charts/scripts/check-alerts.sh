#!/usr/bin/env bash
set -euo pipefail

base=$(helm template alert-contract charts/observability --show-only templates/iterabase-alerts.yaml)
longhorn_monitor=$(helm template alert-contract charts/observability --show-only templates/longhorn-servicemonitor.yaml)
configured=$(helm template alert-contract charts/observability --show-only templates/iterabase-alerts.yaml \
  --set alerts.performance.apiP95Seconds=2 \
  --set alerts.performance.inferenceP95Seconds=30 \
  --set alerts.performance.inferenceTTFTP95Seconds=5 \
  --set alerts.performance.harnessTurnP95Seconds=600 \
  --set alerts.performance.modelQueueDepth=8 \
  --set alerts.performance.gpuUtilizationPercent=95)

if grep -Eq '(^|[^[:alnum:]_])http_requests_total' <<<"$base"; then
  echo 'ERROR: stale nonexistent http_requests_total alert contract remains' >&2
  exit 1
fi
alerts=$(grep -c '^        - alert: Iterabase' <<<"$base")
runbooks=$(grep -c 'runbook_url:' <<<"$base")
actions=$(grep -c 'first_action:' <<<"$base")
if [[ "$alerts" -ne "$runbooks" || "$alerts" -ne "$actions" ]]; then
  echo "ERROR: every default alert requires runbook and first action (alerts=$alerts runbooks=$runbooks actions=$actions)" >&2
  exit 1
fi
for alert in ControlPlaneAPILatencyHigh InferenceLatencyHigh InferenceTTFTHigh HarnessTurnLatencyHigh ModelQueueHigh GPUUtilizationHigh; do
  if grep -q "Iterabase$alert" <<<"$base"; then
    echo "ERROR: configurable performance alert Iterabase$alert rendered without an accepted threshold" >&2
    exit 1
  fi
  grep -q "Iterabase$alert" <<<"$configured" || {
    echo "ERROR: configured performance alert Iterabase$alert did not render" >&2
    exit 1
  }
done
for rule in 'iterabase:control_plane_api_error_ratio5m' 'iterabase:inference_error_ratio5m' 'iterabase:inference_ttft_p95_5m'; do
  grep -q "$rule" <<<"$base" || { echo "ERROR: missing recording rule $rule" >&2; exit 1; }
done
# The manager reconciliation alert must target the stable control-plane manager
# scrape identity so unrelated controller-runtime producers (MetalLB, GPU
# Operator, ingress) cannot trigger it.
grep -Fq 'controller_runtime_reconcile_total{result="error",component="manager"}' <<<"$base" || {
  echo 'ERROR: manager reconciliation alert must target the stable component="manager" scrape identity' >&2
  exit 1
}
grep -Fq 'name: alert-contract-longhorn-manager' <<<"$longhorn_monitor" || {
  echo 'ERROR: Longhorn backend alerts have no manager ServiceMonitor' >&2
  exit 1
}
grep -Fq 'matchNames: [longhorn-system]' <<<"$longhorn_monitor" || {
  echo 'ERROR: Longhorn manager ServiceMonitor must select the backend namespace explicitly' >&2
  exit 1
}
echo "OK: $alerts invariant alerts carry runbooks/actions; six performance alerts are threshold-gated; recording rules and Longhorn scrape render"
