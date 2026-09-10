#!/usr/bin/env bash
set -euo pipefail

platform=charts/iterabase-platform
substrate=charts/lvm-storage-substrate
certificates=charts/cert-manager-substrate
chart_version() { awk '$1 == "version:" { print $2; exit }' "$1/Chart.yaml"; }
platform_version=$(chart_version "$platform")
substrate_version=$(chart_version "$substrate")
certificate_version=$(chart_version "$certificates")
[[ "$platform_version" == "$substrate_version" && "$platform_version" == "$certificate_version" ]] || {
  echo "error: platform and both substrate companions must share one version" >&2
  exit 1
}

render=$(helm template release-lvm-storage "$substrate" -n iterabase-system \
  --set-string agentpool.authorizedManagerIdentity=system:serviceaccount:iterabase-system:release-control-plane-manager)
for crd in \
  lvmnodes.local.openebs.io lvmvolumes.local.openebs.io lvmsnapshots.local.openebs.io \
  volumesnapshots.snapshot.storage.k8s.io volumesnapshotcontents.snapshot.storage.k8s.io \
  volumesnapshotclasses.snapshot.storage.k8s.io; do
  grep -Fq "name: $crd" <<<"$render" || { echo "missing $crd" >&2; exit 1; }
done
grep -Fq 'name: snapshot-controller' <<<"$render"
grep -Fq 'value: "false"' <<<"$render"
grep -Fq -- '--kubelet-dir=/var/lib/kubelet/' <<<"$render"
grep -Fq '/var/lib/kubelet/plugins_registry/' <<<"$render"
grep -Fq 'name: local.csi.openebs.io' <<<"$render"

for image in \
  'docker.io/openebs/lvm-driver:1.10.0@sha256:b5932b4df5cde1f72914f665a4e4b480afd38dbc6577d7552b9f509d52e8c094' \
  'registry.k8s.io/sig-storage/csi-node-driver-registrar:v2.13.0@sha256:d7138bcc3aa5f267403d45ad4292c95397e421ea17a0035888850f424c7de25d' \
  'registry.k8s.io/sig-storage/csi-resizer:v2.0.0@sha256:4a95d94e57ad82f6977cd8d4fdcfcfc0b83f02d990e4e7715b688c20970a906d' \
  'registry.k8s.io/sig-storage/csi-snapshotter:v8.2.0@sha256:dd788d79cf4c1b8edee6d9b80b8a1ebfc51a38a365c5be656986b129be9ac784' \
  'registry.k8s.io/sig-storage/snapshot-controller:v8.2.0@sha256:9dade8f2f3ab29e3919c41b343f8d77b12178ac51f25574d7ed2d45a3e3ef69d' \
  'registry.k8s.io/sig-storage/csi-provisioner:v6.1.0@sha256:e5900dc98b0d02f317ea9572f3824983c47a2fb5d5fe2b160aedf1b99c14c9e4'; do
  grep -Fq "image: \"$image\"" <<<"$render" || { echo "missing exact image $image" >&2; exit 1; }
done

[[ $(grep -c '^kind: StorageClass$' <<<"$render") -eq 2 ]]
for class in iterabase-lvm-xfs iterabase-agentpool-lvm-xfs; do
  grep -Fq "name: $class" <<<"$render"
done
[[ $(grep -c 'provisioner: local.csi.openebs.io' <<<"$render") -eq 2 ]]
[[ $(grep -c 'vgpattern: \^iterabase-data\$' <<<"$render") -eq 2 ]]
[[ $(grep -c 'fsType: xfs' <<<"$render") -eq 2 ]]
[[ $(grep -c 'thinProvision: "no"' <<<"$render") -eq 2 ]]
[[ $(grep -c 'volumeBindingMode: WaitForFirstConsumer' <<<"$render") -eq 2 ]]
[[ $(grep -c 'reclaimPolicy: Delete' <<<"$render") -eq 2 ]]
[[ $(grep -c 'allowVolumeExpansion: false' <<<"$render") -eq 2 ]]
[[ $(grep -c 'storageclass.kubernetes.io/is-default-class: "false"' <<<"$render") -eq 2 ]]
grep -A14 'name: iterabase-lvm-xfs' <<<"$render" | grep -Fq 'shared: "no"'
grep -A15 'name: iterabase-agentpool-lvm-xfs' <<<"$render" | grep -Fq 'shared: "yes"'

# DES-HOR-545-04: exactly one non-default full-origin thick snapshot class.
[[ $(grep -c '^kind: VolumeSnapshotClass$' <<<"$render") -eq 1 ]]
grep -A10 'name: iterabase-lvm-snapshot' <<<"$render" | grep -Fq 'snapshot.storage.kubernetes.io/is-default-class: "false"'
grep -A10 'name: iterabase-lvm-snapshot' <<<"$render" | grep -Fq 'driver: local.csi.openebs.io'
grep -A10 'name: iterabase-lvm-snapshot' <<<"$render" | grep -Fq 'deletionPolicy: Delete'
grep -A10 'name: iterabase-lvm-snapshot' <<<"$render" | grep -Fq 'snapSize: 100%'
# The managed class has no secret parameters, so its controller SA must not have
# Secret read/list or CRD create/delete through the narrowed snapshot role.
snapshot_role=$(awk '/name: openebs-lvm-snapshotter-role/{capture=1} capture{print} capture && /^---$/{exit}' <<<"$render")
! grep -Fq 'resources: ["secrets"]' <<<"$snapshot_role"
! grep -Fq 'resources: ["customresourcedefinitions"]' <<<"$snapshot_role"

# DES-HOR-545-01: the agentpool class must be gated by a fail-closed admission
# policy bound to the exact control-plane manager service-account identity.
grep -Fq 'kind: ValidatingAdmissionPolicy' <<<"$render"
grep -Fq 'kind: ValidatingAdmissionPolicyBinding' <<<"$render"
grep -Fq 'name: iterabase-agentpool-claim-authority' <<<"$render"
grep -Fq 'failurePolicy: Fail' <<<"$render"
grep -Fq 'validationActions: [Deny]' <<<"$render"
grep -Fq 'request.userInfo.username == '\''system:serviceaccount:iterabase-system:release-control-plane-manager'\''' <<<"$render"
# Operation-aware: CREATE gated by manager identity; UPDATE denies mutating the
# protected class / AgentPool ownership while letting scheduler/CSI/kubelet pass.
grep -Fq 'operations: ["CREATE", "UPDATE"]' <<<"$render"
grep -Fq 'request.operation != '\''CREATE'\'' || request.userInfo.username' <<<"$render"
grep -Fq 'request.operation != '\''UPDATE'\'' || (has(oldObject.spec)' <<<"$render"
grep -Fq 'object.spec.storageClassName == oldObject.spec.storageClassName' <<<"$render"
grep -Fq 'resources: ["persistentvolumeclaims"]' <<<"$render"

for values in "" "-f values-observability.yaml"; do
  platform_render=$(helm template release "$platform" $values)
  explicit_classes=$(awk '$1 == "storageClassName:" && NF > 1 {gsub(/"/, "", $2); print $2}' <<<"$platform_render" | sort -u)
  [[ "$explicit_classes" == iterabase-lvm-xfs ]] || {
    echo "error: every rendered platform data claim must explicitly use iterabase-lvm-xfs: $explicit_classes" >&2
    exit 1
  }
done

if helm template release "$platform" -f values-observability.yaml --set observability.kube-prometheus-stack.grafana.persistence.storageClassName=alternate >/dev/null 2>&1; then
  echo "error: observability accepted an alternate StorageClass" >&2
  exit 1
fi
if helm template release-lvm-storage "$substrate" --set lvm-localpv.enabled=false >/dev/null 2>&1; then
  echo "error: LVM substrate accepted disabled OpenEBS authority" >&2
  exit 1
fi

echo "OK: same-version pinned OpenEBS LVM LocalPV volume/snapshot substrate, narrowed RBAC, and exact managed classes"
