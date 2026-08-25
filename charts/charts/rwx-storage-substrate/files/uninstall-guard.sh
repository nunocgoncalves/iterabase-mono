#!/usr/bin/env bash
set -Eeuo pipefail

readonly STORAGE_CLASS="iterabase-rwx"
readonly LONGHORN_NAMESPACE="${LONGHORN_NAMESPACE:?LONGHORN_NAMESPACE is required}"

pvc_consumers="$(kubectl get pvc -A -o json | jq -r --arg class "$STORAGE_CLASS" '.items[] | select(.spec.storageClassName == $class) | "\(.metadata.namespace)/\(.metadata.name)"')"
if [[ -n "$pvc_consumers" ]]; then
  echo "refusing managed RWX uninstall: active PVC consumers still select ${STORAGE_CLASS}:" >&2
  printf '%s\n' "$pvc_consumers" >&2
  echo "close starts, settle/reap sessions, and remove consumers deliberately before retrying" >&2
  exit 1
fi

retained_pvs="$(kubectl get pv -o json | jq -r --arg class "$STORAGE_CLASS" '.items[] | select(.spec.storageClassName == $class) | .metadata.name')"
if [[ -n "$retained_pvs" ]]; then
  echo "refusing managed RWX uninstall: retained ${STORAGE_CLASS} PVs still require explicit delete/sanitize or transfer disposition:" >&2
  printf '%s\n' "$retained_pvs" >&2
  exit 1
fi

volumes="$(kubectl -n "$LONGHORN_NAMESPACE" get volumes.longhorn.io -o json | jq -r '.items[] | .metadata.name')"
if [[ -n "$volumes" ]]; then
  echo "refusing managed RWX uninstall: Longhorn volumes remain after PVC/PV disposition:" >&2
  printf '%s\n' "$volumes" >&2
  exit 1
fi

kubectl -n "$LONGHORN_NAMESPACE" patch settings.longhorn.io deleting-confirmation-flag \
  --type=merge -p '{"value":"true"}' >/dev/null
observed="$(kubectl -n "$LONGHORN_NAMESPACE" get settings.longhorn.io deleting-confirmation-flag -o jsonpath='{.value}')"
[[ "$observed" == "true" ]] || {
  echo "Longhorn deletion confirmation did not become true (observed ${observed:-<empty>})" >&2
  exit 1
}
echo "managed RWX uninstall preflight=pass consumers=0 retainedPVs=0 volumes=0 deletionConfirmation=true"
