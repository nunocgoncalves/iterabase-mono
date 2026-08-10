#!/usr/bin/env bash
set -euo pipefail

rendered=$(helm template exporter-auth-check charts/redis \
  --set metrics.enabled=true \
  --set global.internalTLS.enabled=true \
  --show-only templates/exporter.yaml)

deployment=$(awk '
  /^kind: Deployment$/ { in_deployment = 1 }
  in_deployment && /^---$/ { exit }
  in_deployment { print }
' <<<"$rendered")

assert_contains() {
  local expected="$1"
  local description="$2"

  if ! grep -Fq -- "$expected" <<<"$deployment"; then
    echo "ERROR: internal-TLS Redis exporter is missing $description ($expected)" >&2
    return 1
  fi
}

# The rendered argument must contain the literal Kubernetes env substitution.
# shellcheck disable=SC2016
assert_contains '- --redis.password=$(REDIS_PASSWORD)' 'the password argument'
assert_contains '- name: REDIS_PASSWORD' 'the password environment variable'
assert_contains 'secretKeyRef:' 'the password Secret reference'
assert_contains 'key: redis-password' 'the Redis password Secret key'

echo 'OK: global internal TLS configures Redis exporter AUTH from the Redis Secret'
