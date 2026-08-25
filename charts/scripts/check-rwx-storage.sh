#!/usr/bin/env bash
set -Eeuo pipefail

root="$(cd "$(dirname "${BASH_SOURCE[0]}")/.." && pwd)"
chart="$root/charts/rwx-storage-substrate"
platform="$root/charts/iterabase-platform"
tmp="$(mktemp -d)"
trap 'rm -rf "$tmp"' EXIT

helm dependency build "$chart" >/dev/null

render_profile() {
  local topology="$1"
  helm template rwx "$chart" --namespace longhorn-system \
    --set "storage.rwx.managedLonghorn.topology=${topology}" >"$tmp/${topology}.yaml"
}
render_profile single-node
render_profile three-node

for profile in single-node three-node; do
  rendered="$tmp/${profile}.yaml"
  grep -Fq 'app.kubernetes.io/version: v1.12.1' "$rendered"
  grep -Fq 'kind: NetworkPolicy' "$rendered"
  grep -Fq 'create-default-disk-labeled-nodes: true' "$rendered"
  grep -Fq 'default-data-path: "/var/lib/longhorn"' "$rendered"
  grep -Fq 'replica-soft-anti-affinity: false' "$rendered"
  grep -Fq 'storage-over-provisioning-percentage: "100"' "$rendered"
  grep -Fq 'storage-minimal-available-percentage: "25"' "$rendered"
  grep -Fq 'allow-volume-creation-with-degraded-availability: false' "$rendered"
  grep -Fq 'v1-data-engine: true' "$rendered"
  grep -Fq 'v2-data-engine: false' "$rendered"
  grep -Fq 'rwx-volume-fast-failover: false' "$rendered"
  grep -Fq 'deleting-confirmation-flag: false' "$rendered"
  grep -Fq 'name: iterabase-rwx' "$rendered"
  grep -Fq 'reclaimPolicy: Retain' "$rendered"
  grep -Fq 'allowVolumeExpansion: true' "$rendered"
  grep -Fq 'dataEngine: v1' "$rendered"
  grep -Fq 'migratable: "false"' "$rendered"
  unpinned_images="$(grep -E '^ +image: .*longhornio/' "$rendered" | grep -v '@sha256:' || true)"
  if [[ -n "$unpinned_images" ]]; then
    echo "managed Longhorn rendered unpinned images for ${profile}:" >&2
    printf '%s\n' "$unpinned_images" >&2
    exit 1
  fi
  if grep -Eq '^kind: (Ingress|HTTPRoute)$' "$rendered"; then
    echo "managed Longhorn UI exposure rendered for ${profile}" >&2
    exit 1
  fi
done
grep -Fq 'numberOfReplicas: "1"' "$tmp/single-node.yaml"
grep -Fq 'numberOfReplicas: "3"' "$tmp/three-node.yaml"

helm template platform "$platform" -f "$root/values-managed-rwx-single-node.yaml" >"$tmp/platform-managed.yaml"
helm template platform "$platform" -f "$root/values-external-rwx.yaml" >"$tmp/platform-external.yaml"
grep -Fq 'mode: "managed-longhorn"' "$tmp/platform-managed.yaml"
grep -Fq 'storageClassName: "iterabase-rwx"' "$tmp/platform-managed.yaml"
grep -Fq 'topology: "single-node"' "$tmp/platform-managed.yaml"
grep -Fq 'mode: "external"' "$tmp/platform-external.yaml"
grep -Fq 'storageClassName: "customer-production-rwx"' "$tmp/platform-external.yaml"
if grep -Fq 'driver.longhorn.io' "$tmp/platform-external.yaml"; then
  echo "external platform mode rendered a Longhorn backend" >&2
  exit 1
fi

if helm template invalid "$platform" --set storage.rwx.mode=managed-longhorn --set storage.rwx.storageClassName=wrong --set storage.rwx.managedLonghorn.topology=single-node >/dev/null 2>&1; then
  echo "managed mode accepted a non-contract StorageClass" >&2
  exit 1
fi
if helm template invalid "$platform" --set storage.rwx.mode=external --set storage.rwx.storageClassName=external --set storage.rwx.managedLonghorn.topology=single-node >/dev/null 2>&1; then
  echo "external mode accepted managed topology settings" >&2
  exit 1
fi
if helm template invalid "$chart" --set storage.rwx.mode=external >/dev/null 2>&1; then
  echo "managed companion accepted external mode" >&2
  exit 1
fi
if helm template invalid "$chart" --set longhorn.defaultSettings.v2DataEngine=true >/dev/null 2>&1; then
  echo "managed companion accepted Longhorn V2 data engine" >&2
  exit 1
fi

cmp "$root/../docs/architecture/validation/hor-424-rwx-conformance.sh" "$chart/files/rwx-conformance.sh"
cmp "$root/../docs/architecture/validation/hor-424-rwx-conformance.yaml" "$chart/files/rwx-conformance.yaml"

grep -Fq 'managed-profile-gate.sh' "$tmp/single-node.yaml"
grep -Fq 'helm.sh/hook: post-install,post-upgrade' "$tmp/single-node.yaml"
grep -Fq 'helm.sh/hook: pre-delete' "$tmp/single-node.yaml"
grep -Fq 'refusing managed RWX uninstall' "$tmp/single-node.yaml"

echo 'OK: managed Longhorn and external RWX chart contracts render and fail closed'
