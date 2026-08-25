#!/usr/bin/env bash
# Validate the DES-HOR-424-03 generic RWX StorageClass contract.
# This script installs no backend and injects no backend-specific failure.
set -Eeuo pipefail

SCRIPT_DIR="$(cd "$(dirname "${BASH_SOURCE[0]}")" && pwd)"
readonly SCRIPT_DIR
readonly TEMPLATE="${SCRIPT_DIR}/hor-424-rwx-conformance.yaml"
readonly STORAGE_CLASS="${HOR424_STORAGE_CLASS:-}"
readonly NAMESPACE="${HOR424_NAMESPACE:-hor-424-rwx-conformance}"
readonly ATTEST_NAMESPACE="${HOR424_ATTEST_NAMESPACE:-iterabase-system}"
readonly TIMEOUT="${HOR424_TIMEOUT:-10m}"
readonly CLEANUP="${HOR424_CLEANUP:-false}"
readonly CONTRACT_VERSION="HOR-469/v1"
read -r -a KUBECTL_COMMAND <<< "${KUBECTL:-kubectl}"
RENDERED=""

k() {
  "${KUBECTL_COMMAND[@]}" "$@"
}

validate_dns_label() {
  local name="$1"
  [[ "$name" =~ ^[a-z0-9]([-a-z0-9]*[a-z0-9])?$ ]] && ((${#name} <= 63))
}

validate_dns_subdomain() {
  local name="$1"
  local label
  ((${#name} <= 253)) || return 1
  [[ "$name" =~ ^[a-z0-9]([.a-z0-9-]*[a-z0-9])?$ ]] || return 1
  IFS='.' read -r -a labels <<< "$name"
  for label in "${labels[@]}"; do
    validate_dns_label "$label" || return 1
  done
}

diagnostics() {
  set +e
  echo >&2
  echo "HOR-424 conformance failed; preserving resources and collecting bounded diagnostics." >&2
  echo "context=$(k config current-context 2>/dev/null) namespace=${NAMESPACE} storageClass=${STORAGE_CLASS}" >&2
  k get storageclass "$STORAGE_CLASS" -o yaml >&2
  k -n "$NAMESPACE" get pvc,pod,job -o wide >&2
  k -n "$NAMESPACE" get events --sort-by=.lastTimestamp >&2
  k -n "$NAMESPACE" describe pvc sessions >&2
  k -n "$NAMESPACE" logs job/setup --all-containers=true >&2
  for job in worker-a worker-b verifier replacement post-expansion; do
    k -n "$NAMESPACE" logs "job/${job}" --all-containers=true >&2
  done
  local pv
  pv="$(k -n "$NAMESPACE" get pvc sessions -o jsonpath='{.spec.volumeName}' 2>/dev/null)"
  if [[ -n "$pv" ]]; then
    k get pv "$pv" -o yaml >&2
  fi
  echo "Inspect the preserved synthetic namespace, fix the backend/class, delete it deliberately, and rerun." >&2
}

on_exit() {
  local rc=$?
  [[ -n "$RENDERED" ]] && rm -f "$RENDERED"
  if ((rc != 0)); then
    diagnostics
  fi
  exit "$rc"
}
trap on_exit EXIT

wait_for_pv_capacity() {
  local wanted="$1"
  local deadline=$((SECONDS + 600))
  local observed=""
  while ((SECONDS < deadline)); do
    observed="$(k get pv "$PV" -o jsonpath='{.spec.capacity.storage}' 2>/dev/null || true)"
    [[ "$observed" == "$wanted" ]] && return 0
    sleep 2
  done
  echo "PV capacity did not become ${wanted}; observed ${observed:-<empty>}" >&2
  return 1
}

wait_for_pvc_capacity() {
  local wanted="$1"
  local deadline=$((SECONDS + 600))
  local observed=""
  while ((SECONDS < deadline)); do
    observed="$(k -n "$NAMESPACE" get pvc sessions -o jsonpath='{.status.capacity.storage}' 2>/dev/null || true)"
    [[ "$observed" == "$wanted" ]] && return 0
    sleep 2
  done
  echo "PVC capacity did not become ${wanted}; observed ${observed:-<empty>}" >&2
  return 1
}

unsuspend() {
  k -n "$NAMESPACE" patch job "$1" --type=merge -p '{"spec":{"suspend":false}}' >/dev/null
}

sha256_text() {
  if command -v sha256sum >/dev/null 2>&1; then
    printf '%s' "$1" | sha256sum | awk '{print $1}'
  else
    printf '%s' "$1" | shasum -a 256 | awk '{print $1}'
  fi
}

write_attestation() {
  local class_uid="$1"
  local provisioner="$2"
  local context="$3"
  local digest name validated_at
  digest="$(sha256_text "$STORAGE_CLASS")"
  name="iterabase-rwx-conformance-${digest:0:16}"
  validated_at="$(date -u +%Y-%m-%dT%H:%M:%SZ)"

  k get namespace "$ATTEST_NAMESPACE" >/dev/null
  k -n "$ATTEST_NAMESPACE" create configmap "$name" \
    --from-literal=contractVersion="$CONTRACT_VERSION" \
    --from-literal=storageClassName="$STORAGE_CLASS" \
    --from-literal=storageClassUID="$class_uid" \
    --from-literal=provisioner="$provisioner" \
    --from-literal=context="$context" \
    --from-literal=validatedAt="$validated_at" \
    --from-literal=result=pass \
    --dry-run=client -o yaml | k apply -f - >/dev/null
  k -n "$ATTEST_NAMESPACE" label configmap "$name" --overwrite \
    platform.iterabase.com/storage-conformance=true >/dev/null
  k -n "$ATTEST_NAMESPACE" annotate configmap "$name" --overwrite \
    platform.iterabase.com/storage-class-uid="$class_uid" \
    platform.iterabase.com/storage-contract-version="$CONTRACT_VERSION" >/dev/null
  echo "attestation=pass namespace=${ATTEST_NAMESPACE} configmap=${name} classUID=${class_uid} contract=${CONTRACT_VERSION}"
}

[[ -n "$STORAGE_CLASS" ]] || {
  echo "HOR424_STORAGE_CLASS is required" >&2
  exit 2
}
validate_dns_subdomain "$STORAGE_CLASS" || {
  echo "HOR424_STORAGE_CLASS must be a Kubernetes DNS subdomain" >&2
  exit 2
}
validate_dns_label "$NAMESPACE" || {
  echo "HOR424_NAMESPACE must be a Kubernetes DNS label" >&2
  exit 2
}
validate_dns_label "$ATTEST_NAMESPACE" || {
  echo "HOR424_ATTEST_NAMESPACE must be a Kubernetes DNS label" >&2
  exit 2
}
[[ -f "$TEMPLATE" ]] || {
  echo "missing template: $TEMPLATE" >&2
  exit 2
}
command -v "${KUBECTL_COMMAND[0]}" >/dev/null || {
  echo "kubectl command not found: ${KUBECTL_COMMAND[0]}" >&2
  exit 2
}

if k get namespace "$NAMESPACE" >/dev/null 2>&1; then
  echo "namespace ${NAMESPACE} already exists; refusing to overwrite prior evidence" >&2
  exit 2
fi

PROVISIONER="$(k get storageclass "$STORAGE_CLASS" -o jsonpath='{.provisioner}')"
STORAGE_CLASS_UID="$(k get storageclass "$STORAGE_CLASS" -o jsonpath='{.metadata.uid}')"
RECLAIM="$(k get storageclass "$STORAGE_CLASS" -o jsonpath='{.reclaimPolicy}')"
EXPANSION="$(k get storageclass "$STORAGE_CLASS" -o jsonpath='{.allowVolumeExpansion}')"
readonly PROVISIONER STORAGE_CLASS_UID RECLAIM EXPANSION

[[ -n "$PROVISIONER" ]] || {
  echo "StorageClass ${STORAGE_CLASS} has no provisioner" >&2
  exit 1
}
[[ -n "$STORAGE_CLASS_UID" ]] || {
  echo "StorageClass ${STORAGE_CLASS} has no UID" >&2
  exit 1
}
[[ "$RECLAIM" == "Retain" ]] || {
  echo "StorageClass ${STORAGE_CLASS} reclaimPolicy must be Retain (got ${RECLAIM:-<empty>})" >&2
  exit 1
}
[[ "$EXPANSION" == "true" ]] || {
  echo "StorageClass ${STORAGE_CLASS} allowVolumeExpansion must be true (got ${EXPANSION:-<empty>})" >&2
  exit 1
}
CONTEXT="${HOR424_CONTEXT:-$(k config current-context 2>/dev/null || printf in-cluster)}"
readonly CONTEXT
echo "HOR-424 RWX conformance context=${CONTEXT} namespace=${NAMESPACE} storageClass=${STORAGE_CLASS} provisioner=${PROVISIONER}"
echo "The run creates a 1Gi synthetic claim, expands it to 2Gi, and preserves resources on failure."

RENDERED="$(mktemp)"
sed \
  -e "s/\${HOR424_NAMESPACE}/${NAMESPACE}/g" \
  -e "s/\${HOR424_STORAGE_CLASS}/${STORAGE_CLASS}/g" \
  "$TEMPLATE" > "$RENDERED"

k apply -f "$RENDERED" >/dev/null
k -n "$NAMESPACE" wait --for=condition=complete job/setup --timeout="$TIMEOUT"

echo "== setup =="
k -n "$NAMESPACE" logs job/setup

PV="$(k -n "$NAMESPACE" get pvc sessions -o jsonpath='{.spec.volumeName}')"
readonly PV
[[ -n "$PV" ]] || {
  echo "PVC did not bind to a PV" >&2
  exit 1
}
[[ "$(k get pv "$PV" -o jsonpath='{.spec.storageClassName}')" == "$STORAGE_CLASS" ]]
[[ "$(k get pv "$PV" -o jsonpath='{.spec.persistentVolumeReclaimPolicy}')" == "Retain" ]]
[[ "$(k get pv "$PV" -o jsonpath='{.spec.volumeMode}')" == "Filesystem" ]]
[[ "$(k get pv "$PV" -o jsonpath='{.spec.accessModes[0]}')" == "ReadWriteMany" ]]

unsuspend worker-a
unsuspend worker-b
k -n "$NAMESPACE" wait --for=condition=complete job/worker-a --timeout="$TIMEOUT"
k -n "$NAMESPACE" wait --for=condition=complete job/worker-b --timeout="$TIMEOUT"

echo "== concurrent workers =="
k -n "$NAMESPACE" logs job/worker-a -c worker
k -n "$NAMESPACE" logs job/worker-b -c worker
k -n "$NAMESPACE" get pods -l 'job-name in (worker-a,worker-b)' \
  -o custom-columns=NAME:.metadata.name,NODE:.spec.nodeName,START:.status.startTime,FINISH:.status.containerStatuses[0].state.terminated.finishedAt,EXIT:.status.containerStatuses[0].state.terminated.exitCode

unsuspend verifier
k -n "$NAMESPACE" wait --for=condition=complete job/verifier --timeout="$TIMEOUT"
echo "== verifier =="
k -n "$NAMESPACE" logs job/verifier -c verifier

unsuspend replacement
k -n "$NAMESPACE" wait --for=condition=complete job/replacement --timeout="$TIMEOUT"
echo "== worker replacement =="
k -n "$NAMESPACE" logs job/replacement -c worker

k -n "$NAMESPACE" patch pvc sessions --type=merge \
  -p '{"spec":{"resources":{"requests":{"storage":"2Gi"}}}}' >/dev/null
# Wait for controller expansion before creating a fresh mount. If the pod mounts
# the old size first, Kubernetes reports FileSystemResizePending and needs a
# later remount to perform node/filesystem expansion.
wait_for_pv_capacity 2Gi
unsuspend post-expansion
k -n "$NAMESPACE" wait --for=condition=complete job/post-expansion --timeout="$TIMEOUT"
wait_for_pvc_capacity 2Gi

echo "== expansion =="
k -n "$NAMESPACE" logs job/post-expansion -c verifier
k -n "$NAMESPACE" get pvc sessions \
  -o custom-columns=NAME:.metadata.name,REQUEST:.spec.resources.requests.storage,CAPACITY:.status.capacity.storage,PHASE:.status.phase,CONDITIONS:.status.conditions[*].type

echo "HOR-424 RWX conformance PASS context=${CONTEXT} class=${STORAGE_CLASS} pv=${PV}"
write_attestation "$STORAGE_CLASS_UID" "$PROVISIONER" "$CONTEXT"
echo "Backend-specific server/node failure and AgentPool readiness evidence remain required by HOR-469."

if [[ "$CLEANUP" == "true" ]]; then
  echo "Cleaning the disposable successful run by deliberately changing only its PV to Delete."
  k patch pv "$PV" --type=merge -p '{"spec":{"persistentVolumeReclaimPolicy":"Delete"}}' >/dev/null
  k delete namespace "$NAMESPACE" --wait=true >/dev/null
  deadline=$((SECONDS + 300))
  while ((SECONDS < deadline)); do
    if ! k get pv "$PV" >/dev/null 2>&1; then
      echo "cleanup=pass pv=${PV}"
      break
    fi
    sleep 2
  done
  if k get pv "$PV" >/dev/null 2>&1; then
    echo "cleanup failed: PV ${PV} still exists" >&2
    exit 1
  fi
else
  echo "Synthetic evidence is preserved in namespace ${NAMESPACE}."
  echo "After inspection, clean it safely with:"
  echo "  kubectl patch pv ${PV} --type=merge -p '{\"spec\":{\"persistentVolumeReclaimPolicy\":\"Delete\"}}'"
  echo "  kubectl delete namespace ${NAMESPACE} --wait=true"
fi
