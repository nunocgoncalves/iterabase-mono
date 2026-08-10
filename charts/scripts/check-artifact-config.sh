#!/usr/bin/env bash
set -euo pipefail

render() {
  helm template artifact-check charts/control-plane \
    --set gateway.enabled=true \
    --set artifact.enabled=true \
    "$@"
}

assert_max_size() {
  local expected="$1"
  shift
  local rendered values count
  rendered=$(render "$@")
  values=$(awk '
    $1 == "-" && $2 == "name:" && $3 == "ARTIFACT_MAX_SIZE_BYTES" {
      getline
      if ($1 == "value:") {
        gsub(/"/, "", $2)
        print $2
      }
    }
  ' <<<"$rendered")
  count=$(printf '%s\n' "$values" | grep -c . || true)
  if [[ "$count" -ne 2 ]] || grep -qvx "$expected" <<<"$values"; then
    echo "ERROR: expected API + gateway ARTIFACT_MAX_SIZE_BYTES=$expected; rendered: ${values:-<missing>}" >&2
    return 1
  fi
  echo "OK: API + gateway render ARTIFACT_MAX_SIZE_BYTES=$expected as decimal integers"
}

assert_max_size 1073741824
assert_max_size 1048576 --set artifact.maxSizeBytes=1048576

gateway=$(helm template artifact-check charts/control-plane \
  --set gateway.enabled=true \
  --set artifact.enabled=true \
  --show-only templates/gateway.yaml)
grep -q '^kind: Deployment$' <<<"$gateway"
grep -q '^kind: Service$' <<<"$gateway"
grep -q 'command: \["/gateway"\]' <<<"$gateway"
grep -q 'name: artifact-check-minio-artifacts' <<<"$gateway"
echo "OK: gateway Deployment + Service render with the dedicated artifact Secret"

substrate=$(helm template artifact-cert-manager charts/cert-manager-substrate)
grep -q '^kind: CSIDriver$' <<<"$substrate"
grep -q '^  name: csi.cert-manager.io$' <<<"$substrate"
grep -q 'image: "quay.io/jetstack/cert-manager-csi-driver:v0.15.0"' <<<"$substrate"
echo "OK: certificate substrate renders the pinned CSI driver for AgentPool leaves"
