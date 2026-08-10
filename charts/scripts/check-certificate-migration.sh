#!/usr/bin/env bash
set -euo pipefail

namespace=${CERT_MIGRATION_NAMESPACE:-iterabase-system}
platform_release=${CERT_MIGRATION_PLATFORM_RELEASE:-iterabase}
substrate_release=${CERT_MIGRATION_SUBSTRATE_RELEASE:-iterabase-cert-manager}
released_version=0.2.2
released_chart=oci://ghcr.io/nunocgoncalves/iterabase-charts/iterabase-platform

# Keep the contract test focused on the certificate operator and CSI resources.
# The production migration uses the deployment's normal values files instead.
minimal_values=(
  --set redis.enabled=false
  --set minio.enabled=false
  --set inference-gateway.enabled=false
  --set control-plane.enabled=false
  --set ingress-nginx.enabled=false
  --set metallb.enabled=false
  --set metallb-config.enabled=false
  --set cert-issuers.enabled=false
  --set external-dns.enabled=false
  --set reloader.enabled=false
  --set observability.enabled=false
)

assert_owner() {
  local resource=$1 expected=$2
  local actual
  actual=$(kubectl get "$resource" -n "$namespace" -o jsonpath='{.metadata.annotations.meta\.helm\.sh/release-name}')
  if [[ "$actual" != "$expected" ]]; then
    echo "error: $resource is owned by ${actual:-<none>}, expected $expected" >&2
    exit 1
  fi
}

assert_substrate_ready() {
  kubectl rollout status deployment/"${platform_release}-cert-manager" -n "$namespace" --timeout=3m
  kubectl rollout status deployment/"${platform_release}-cert-manager-webhook" -n "$namespace" --timeout=3m
  kubectl rollout status daemonset/cert-manager-csi-driver -n "$namespace" --timeout=3m
  assert_owner deployment/"${platform_release}-cert-manager" "$1"
  assert_owner clusterrole/"${platform_release}-cert-manager-cainjector" "$1"
  assert_owner validatingwebhookconfiguration/"${platform_release}-cert-manager-webhook" "$1"
  assert_owner csidriver/csi.cert-manager.io "$1"
}

transfer_kept_crds() {
  local new_owner=$1
  local crd
  local -a crds=()
  while IFS= read -r crd; do crds+=("$crd"); done < <(kubectl get crd -l app.kubernetes.io/name=cert-manager -o name)
  if (( ${#crds[@]} < 6 )); then
    echo "error: expected the six kept cert-manager CRDs, found ${#crds[@]}" >&2
    exit 1
  fi
  kubectl annotate --overwrite "${crds[@]}" \
    meta.helm.sh/release-name="$new_owner" \
    meta.helm.sh/release-namespace="$namespace"
}

echo "Installing the released platform $released_version with bundled certificate substrate"
helm install "$platform_release" "$released_chart" \
  --version "$released_version" \
  --namespace "$namespace" \
  --create-namespace \
  --wait --timeout 8m \
  "${minimal_values[@]}"
assert_substrate_ready "$platform_release"

# Existing installations must remove the old release's ownership first. Helm's
# old release manifest performs that deletion during this platform upgrade;
# installing the companion release before this step would collide on 55 objects.
echo "Upgrading the platform first so Helm retires its bundled substrate"
helm upgrade "$platform_release" charts/iterabase-platform \
  --namespace "$namespace" \
  --wait --timeout 8m \
  "${minimal_values[@]}" \
  --set control-plane.toolRunner.enabled=false
if kubectl get deployment/"${platform_release}-cert-manager" -n "$namespace" >/dev/null 2>&1; then
  echo "error: the platform upgrade retained its old cert-manager Deployment" >&2
  exit 1
fi
kubectl wait --for=condition=Established crd/certificates.cert-manager.io --timeout=2m

# The old chart marks CRDs keep, so the short hand-off preserves all Certificate
# objects and issued Secrets. Those kept objects remain annotated to the old
# release and must be explicitly transferred before the companion adopts them.
transfer_kept_crds "$substrate_release"
echo "Installing the same-version companion substrate after the platform hand-off"
helm install "$substrate_release" charts/cert-manager-substrate \
  --namespace "$namespace" \
  --wait --timeout 8m
assert_substrate_ready "$substrate_release"

# Reconcile the intended platform values after the new owner is Ready. The test
# keeps control-plane disabled to stay focused, while production removes only
# the temporary toolRunner override used by the first upgrade.
helm upgrade "$platform_release" charts/iterabase-platform \
  --namespace "$namespace" \
  --wait --timeout 8m \
  "${minimal_values[@]}"
assert_substrate_ready "$substrate_release"

# Rollback is the inverse hand-off: remove the companion first, then restore the
# old platform revision. A direct rollback would encounter the same ownership
# collision in reverse.
echo "Validating the inverse rollback hand-off"
helm uninstall "$substrate_release" --namespace "$namespace" --wait --timeout 5m
transfer_kept_crds "$platform_release"
helm rollback "$platform_release" 1 --namespace "$namespace" --wait --timeout 8m
assert_substrate_ready "$platform_release"

echo "OK: released 0.2.2 -> staged 0.3.0 ownership migration and inverse rollback both converge"
