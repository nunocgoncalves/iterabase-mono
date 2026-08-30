#!/usr/bin/env bash
set -euo pipefail

rendered=$(helm template manager-contract charts/control-plane)

for crd in agentpools.platform.iterabase.com workflows.platform.iterabase.com; do
  if ! grep -q "^  name: ${crd}$" <<<"$rendered"; then
    echo "ERROR: missing manager-required CRD ${crd}" >&2
    exit 1
  fi
done
echo "OK: AgentPool and Workflow CRDs render"

rbac=$(helm template manager-contract charts/control-plane --show-only templates/rbac.yaml)
for resource in configmaps persistentvolumeclaims persistentvolumes pods services secrets storageclasses deployments networkpolicies agentpools identitymappings modelbackends models permissionpolicies workflows; do
  if ! grep -q "^  - ${resource}$" <<<"$rbac"; then
    echo "ERROR: manager ClusterRole is missing ${resource}" >&2
    exit 1
  fi
done
for subresource in agentpools/finalizers workflows/finalizers agentpools/status workflows/status; do
  if ! grep -q "^  - ${subresource}$" <<<"$rbac"; then
    echo "ERROR: manager ClusterRole is missing ${subresource}" >&2
    exit 1
  fi
done
echo "OK: manager ClusterRole renders all controller-required resources"
