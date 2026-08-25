#!/usr/bin/env bash
set -Eeuo pipefail

readonly TOPOLOGY="${ITERABASE_MANAGED_TOPOLOGY:?ITERABASE_MANAGED_TOPOLOGY is required}"
readonly LONGHORN_NAMESPACE="${LONGHORN_NAMESPACE:?LONGHORN_NAMESPACE is required}"
readonly DEADLINE=$((SECONDS + 900))

required_nodes=1
comparison="eq"
if [[ "$TOPOLOGY" == "three-node" ]]; then
  required_nodes=3
  comparison="ge"
elif [[ "$TOPOLOGY" != "single-node" ]]; then
  echo "unsupported managed topology: $TOPOLOGY" >&2
  exit 2
fi

last=""
while ((SECONDS < DEADLINE)); do
  kubernetes="$(kubectl get nodes -o json)"
  longhorn="$(kubectl -n "$LONGHORN_NAMESPACE" get nodes.longhorn.io -o json 2>/dev/null || printf '{"items":[]}')"
  ready_nodes="$(jq '[.items[] | select(.spec.unschedulable != true) | select(any(.status.conditions[]?; .type == "Ready" and .status == "True"))] | length' <<<"$kubernetes")"
  storage_nodes="$(jq '[.items[] | select(.spec.allowScheduling != false) | select(any(.status.conditions[]?; .type == "Ready" and .status == "True")) | select(any(.status.conditions[]?; .type == "Schedulable" and .status == "True"))] | length' <<<"$longhorn")"
  last="topology=${TOPOLOGY} readyNodes=${ready_nodes} storageNodes=${storage_nodes} required=${required_nodes}"
  if [[ "$comparison" == eq && "$ready_nodes" -eq "$required_nodes" && "$storage_nodes" -eq "$required_nodes" ]]; then
    echo "managed-profile=pass $last"
    exec /bin/bash /contract/hor-424-rwx-conformance.sh
  fi
  if [[ "$comparison" == ge && "$ready_nodes" -ge "$required_nodes" && "$storage_nodes" -ge "$required_nodes" ]]; then
    echo "managed-profile=pass $last replicas=3 distinct-node-anti-affinity=required"
    exec /bin/bash /contract/hor-424-rwx-conformance.sh
  fi
  sleep 5
done

echo "managed profile did not become eligible before timeout: $last" >&2
kubectl get nodes -o wide >&2 || true
kubectl -n "$LONGHORN_NAMESPACE" get nodes.longhorn.io -o yaml >&2 || true
exit 1
