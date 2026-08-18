#!/usr/bin/env bash
set -euo pipefail

cp=$(helm template metrics charts/control-plane \
  --set metrics.enabled=true \
  --set gateway.enabled=true \
  --set dispatch.enabled=true \
  --set dispatch.defaultModel.id=e2e \
  --set dispatch.defaultModel.api=openai)
ig=$(helm template metrics charts/inference-gateway --set metrics.enabled=true)
cp_off=$(helm template metrics-off charts/control-plane)
ig_off=$(helm template metrics-off charts/inference-gateway)

for rendered in "$cp" "$ig"; do
  grep -q 'name: http-metrics' <<<"$rendered"
  grep -q 'name: METRICS_' <<<"$rendered"
  grep -q 'port: http-metrics' <<<"$rendered"
done
grep -q -- '--metrics-bind-address=:8080' <<<"$cp"
grep -q -- '--metrics-secure=false' <<<"$cp"
grep -q 'replacement: harness' <<<"$cp"
grep -q 'app.kubernetes.io/component: harness' <<<"$cp"

if grep -q 'name: http-metrics' <<<"$cp_off" || grep -q 'name: METRICS_ADDR' <<<"$cp_off"; then
  echo 'ERROR: control-plane metrics listener renders while disabled' >&2
  exit 1
fi
if grep -q 'name: http-metrics' <<<"$ig_off" || grep -q 'name: METRICS_ENABLED' <<<"$ig_off"; then
  echo 'ERROR: inference metrics listener renders while disabled' >&2
  exit 1
fi

echo 'OK: metrics-only listeners, named ports, monitors, manager binding, harness PodMonitor, and disabled paths render coherently'
