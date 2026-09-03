#!/usr/bin/env bash
set -euo pipefail

attempts=${HELM_REPOSITORY_ATTEMPTS:-4}
case "$attempts" in ''|*[!0-9]*|0) echo "invalid HELM_REPOSITORY_ATTEMPTS=$attempts" >&2; exit 2 ;; esac

repositories=(
  "ingress-nginx|https://kubernetes.github.io/ingress-nginx"
  "metallb|https://metallb.github.io/metallb"
  "jetstack|https://charts.jetstack.io"
  "external-dns|https://kubernetes-sigs.github.io/external-dns/"
  "stakater|https://stakater.github.io/stakater-charts"
  "prometheus-community|https://prometheus-community.github.io/helm-charts"
  "grafana|https://grafana.github.io/helm-charts"
)

for repository in "${repositories[@]}"; do
  IFS='|' read -r name url <<<"$repository"
  for ((attempt = 1; attempt <= attempts; attempt++)); do
    if helm repo add "$name" "$url" --force-update; then
      break
    fi
    if (( attempt == attempts )); then
      echo "helm repository acquisition failed after $attempts attempts: $name" >&2
      exit 1
    fi
    delay=$((attempt * 2))
    echo "helm repository transport failure; retrying $name in ${delay}s (attempt $((attempt + 1))/$attempts)" >&2
    sleep "$delay"
  done
done
