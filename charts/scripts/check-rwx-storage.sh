#!/usr/bin/env bash
set -Eeuo pipefail

root="$(cd "$(dirname "${BASH_SOURCE[0]}")/.." && pwd)"
chart="$root/charts/rwx-storage-substrate"
platform="$root/charts/iterabase-platform"
tmp="$(mktemp -d)"
trap 'rm -rf "$tmp"' EXIT

"$root/scripts/build-rwx-storage-dependency.sh" >/dev/null

render_profile() {
  local topology="$1"
  helm template rwx "$chart" --namespace longhorn-system \
    --set "storage.rwx.managedLonghorn.topology=${topology}" >"$tmp/${topology}.yaml"
}
render_profile single-node
render_profile three-node
helm template rwx "$chart" --namespace longhorn-system \
  --set global.internalTLS.enabled=true >"$tmp/single-node-tls.yaml"
helm template rwx "$chart" --namespace longhorn-system \
  --show-only templates/recovery-backend-networkpolicy.yaml >"$tmp/recovery-backend-networkpolicy.yaml"

for profile in single-node three-node; do
  rendered="$tmp/${profile}.yaml"
  grep -Fq 'app.kubernetes.io/version: v1.12.1' "$rendered"
  grep -Fq 'kind: NetworkPolicy' "$rendered"
  grep -Fq 'manager-metrics' "$rendered"
  grep -Fq 'app.kubernetes.io/name: prometheus' "$rendered"
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

# DES-HOR-527-01: the additive rule admits only namespaced cluster pods to the
# recovery-backend's single TCP port. It must not depend on the share-manager's
# dynamic component label or admit external IP sources.
recovery_policy="$tmp/recovery-backend-networkpolicy.yaml"
grep -Fq 'app.kubernetes.io/component: recovery-backend' "$recovery_policy"
grep -Fq 'longhorn.io/recovery-backend: longhorn-recovery-backend' "$recovery_policy"
grep -Fq 'namespaceSelector: {}' "$recovery_policy"
grep -Fq '{protocol: TCP, port: 9503}' "$recovery_policy"
[[ "$(grep -Fc 'podSelector:' "$recovery_policy")" -eq 1 ]]
[[ "$(grep -Fc 'namespaceSelector: {}' "$recovery_policy")" -eq 1 ]]
[[ "$(grep -Fc 'port:' "$recovery_policy")" -eq 1 ]]
if grep -Eq 'longhorn.io/component: share-manager|ipBlock:|kind: (Ingress|HTTPRoute)' "$recovery_policy"; then
  echo 'recovery-backend policy widened beyond cluster-pod TCP/9503 ingress' >&2
  exit 1
fi

# DES-HOR-469-02: TLS-on rendering must issue the fixed upstream Secret before
# Longhorn starts and make authenticated, unauthenticated, and plaintext probes
# part of the post-install gate. Plain rendering must not invent the leaf.
tls_render="$tmp/single-node-tls.yaml"
grep -Fq 'kind: Certificate' "$tls_render"
grep -Fq 'name: longhorn-grpc-tls' "$tls_render"
grep -Fq 'commonName: longhorn-backend' "$tls_render"
grep -Fq 'name: "internal-ca"' "$tls_render"
grep -Fq 'helm.sh/hook: pre-install,pre-upgrade' "$tls_render"
grep -Fq 'longhorn-grpc-bootstrap=pass' "$tls_render"
grep -Fq 'ITERABASE_INTERNAL_TLS_ENABLED' "$tls_render"
grep -Fq 'longhorn-grpc-mtls=pass' "$tls_render"
grep -Fq 'unauthenticatedTLSRejected=' "$tls_render"
grep -Fq 'plaintextRejected=' "$tls_render"
grep -Fq "'.items[0].metadata.name // empty'" "$chart/files/managed-profile-gate.sh"
if grep -Fq "jsonpath='{.items[0].metadata.name}'" "$chart/files/managed-profile-gate.sh"; then
  echo "managed mTLS validation exits before its documented instance-manager pod fallback" >&2
  exit 1
fi
grep -Fq 'secret.reloader.stakater.com/reload: longhorn-grpc-tls' "$tls_render"

# Forge release names remain within Helm's 53-character boundary but can make
# hook Job names exceed the Kubernetes-generated 63-character job-name labels.
# Preserve each semantic suffix while truncating the shared base first.
long_release=forge-e2e-1787767040-rwx-storage
helm template "$long_release" "$chart" --namespace longhorn-system \
  --set global.internalTLS.enabled=true \
  --show-only templates/validation-job.yaml >"$tmp/long-validation.yaml"
helm template "$long_release" "$chart" --namespace longhorn-system \
  --show-only templates/uninstall-guard-job.yaml >"$tmp/long-uninstall.yaml"
helm template "$long_release" "$chart" --namespace longhorn-system \
  --show-only templates/recovery-backend-networkpolicy.yaml >"$tmp/long-recovery-policy.yaml"
validation_name="$(awk '$1 == "name:" { print $2; exit }' "$tmp/long-validation.yaml")"
uninstall_name="$(awk '$1 == "name:" { print $2; exit }' "$tmp/long-uninstall.yaml")"
recovery_policy_name="$(awk '$1 == "name:" { print $2; exit }' "$tmp/long-recovery-policy.yaml")"
[[ ${#validation_name} -le 63 && "$validation_name" == *-validation ]]
[[ ${#uninstall_name} -le 63 && "$uninstall_name" == *-uninstall-guard ]]
[[ ${#recovery_policy_name} -le 63 && "$recovery_policy_name" == *-recovery-ingress ]]

if grep -Fq 'kind: Certificate' "$tmp/single-node.yaml"; then
  echo "plaintext managed mode unexpectedly rendered the Longhorn gRPC leaf" >&2
  exit 1
fi

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
if helm template invalid "$chart" --set longhorn.defaultSettings.concurrentReplicaRebuildPerNodeLimit=99 >/dev/null 2>&1; then
  echo "managed companion accepted an arbitrary Longhorn tuning value" >&2
  exit 1
fi
if helm template invalid "$chart" --set longhorn.defaultSettings.nodeDownPodDeletionPolicy=delete-both-statefulset-and-deployment-pod >/dev/null 2>&1; then
  echo "managed companion accepted an arbitrary Longhorn lifecycle policy" >&2
  exit 1
fi
if helm template invalid "$chart" --namespace longhorn-system --set global.internalTLS.enabled=true --set global.internalTLS.issuerName=other-ca >/dev/null 2>&1; then
  echo "managed companion accepted a non-Iterabase gRPC issuer" >&2
  exit 1
fi
if helm template invalid "$chart" --namespace other-system --set global.internalTLS.enabled=true >/dev/null 2>&1; then
  echo "managed companion accepted internal TLS outside the upstream longhorn-system peer identity" >&2
  exit 1
fi

cmp "$root/../docs/architecture/validation/hor-424-rwx-conformance.sh" "$chart/files/rwx-conformance.sh"
cmp "$root/../docs/architecture/validation/hor-424-rwx-conformance.yaml" "$chart/files/rwx-conformance.yaml"
grep -Fq "delete configmap \"\$name\" --ignore-not-found --wait=true" "$chart/files/rwx-conformance.sh"

grep -Fq 'managed-profile-gate.sh' "$tmp/single-node.yaml"
grep -Fq 'helm.sh/hook: post-install,post-upgrade' "$tmp/single-node.yaml"
grep -Fq 'helm.sh/hook: pre-delete' "$tmp/single-node.yaml"
grep -Fq 'refusing managed RWX uninstall' "$tmp/single-node.yaml"

echo 'OK: managed Longhorn and external RWX chart contracts render and fail closed'
