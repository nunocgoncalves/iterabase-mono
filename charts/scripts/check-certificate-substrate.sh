#!/usr/bin/env bash
set -euo pipefail

platform=charts/iterabase-platform
substrate=charts/cert-manager-substrate

chart_version() {
  awk '$1 == "version:" { print $2; exit }' "$1/Chart.yaml"
}

platform_version=$(chart_version "$platform")
substrate_version=$(chart_version "$substrate")
if [[ "$platform_version" != "$substrate_version" ]]; then
  echo "error: platform $platform_version and certificate substrate $substrate_version must be released together" >&2
  exit 1
fi

substrate_render=$(helm template release-cert-manager "$substrate" -n iterabase-system)
substrate_tls_render=$(helm template release-cert-manager "$substrate" -n iterabase-system \
  --set global.internalTLS.enabled=true \
  --set global.internalTLS.platformRelease=release)
platform_render=$(helm template release "$platform" -n iterabase-system)

grep -q '^kind: CustomResourceDefinition$' <<<"$substrate_render"
grep -q 'app.kubernetes.io/name: cert-manager' <<<"$substrate_render"
! grep -Eq '^kind: (Certificate|Issuer|ClusterIssuer)$' <<<"$substrate_render"

# The TLS-on companion still owns only cert-manager runtime resources. Its
# ordered hook bootstraps the future platform release's existing CA resources,
# waits for all Ready conditions, and labels them for normal platform adoption.
grep -Fq 'app.kubernetes.io/component: internal-ca-bootstrap' <<<"$substrate_tls_render"
grep -Fq 'owner_release="release"' <<<"$substrate_tls_render"
grep -Fq 'meta.helm.sh/release-name: release' <<<"$substrate_tls_render"
grep -Fq 'kubectl wait --for=condition=Ready clusterissuer/internal-ca' <<<"$substrate_tls_render"
grep -Fq 'internal-ca-bootstrap=pass' <<<"$substrate_tls_render"
grep -Fq 'docker.io/alpine/k8s:1.34.1@sha256:ec714df3813b5405292860f8a1c55c5727bf8c33c88992f1e981efad8065547f' <<<"$substrate_tls_render"

grep -q '^kind: ClusterIssuer$' <<<"$platform_render"
grep -q '^kind: Certificate$' <<<"$platform_render"
! grep -q '# Source: .*cert-manager/templates/' <<<"$platform_render"

if grep -R -q 'helm.sh/hook' \
  charts/cert-issuers/templates \
  charts/control-plane/templates/certificate.yaml \
  charts/control-plane/templates/tool-runner.yaml \
  charts/observability/templates/stack-internal-tls.yaml \
  charts/postgresql/templates/certificate.yaml \
  charts/redis/templates/certificate.yaml; then
  echo "error: cert-manager consumers must be normal resources in the ordered platform release" >&2
  exit 1
fi

echo "OK: same-version certificate substrate orders the platform-owned internal CA before dependent platform workloads; platform owns hook-free issuers and leaves"
