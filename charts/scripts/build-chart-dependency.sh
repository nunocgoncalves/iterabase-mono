#!/usr/bin/env bash
set -euo pipefail

chart=${1:?usage: build-chart-dependency.sh CHART_DIR}
attempts=${HELM_DEPENDENCY_ATTEMPTS:-4}
case "$attempts" in ''|*[!0-9]*|0) echo "invalid HELM_DEPENDENCY_ATTEMPTS=$attempts" >&2; exit 2 ;; esac

for ((attempt = 1; attempt <= attempts; attempt++)); do
  if helm dependency build "$chart"; then
    exit 0
  fi
  if (( attempt == attempts )); then
    echo "helm dependency build failed after $attempts attempts: $chart" >&2
    exit 1
  fi
  delay=$((attempt * 2))
  echo "helm dependency build transport failure; retrying $chart in ${delay}s (attempt $((attempt + 1))/$attempts)" >&2
  sleep "$delay"
done
