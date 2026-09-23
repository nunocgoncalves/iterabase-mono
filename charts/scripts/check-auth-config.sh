#!/usr/bin/env bash
# Renders the browser-authentication configuration and asserts the enablement
# contract: an enabled surface drops the legacy bootstrap service-account args
# (which runBootstrap refuses), carries the shared auth env, and fails closed
# when a prerequisite is missing.
set -euo pipefail

chart=charts/control-plane
release=auth-check

enabled_args=(
  --set auth.enabled=true
  --set auth.publicOrigin=https://app.example.com
  --set auth.email.host=smtp.example.com
  --set auth.email.from=auth@example.com
  --set auth.bootstrap.adminEmail=admin@example.com
)

fail() {
  echo "ERROR: $*" >&2
  exit 1
}

expect_fail() {
  if "$@" >/dev/null 2>&1; then
    fail "expected render failure but it succeeded: $*"
  fi
}

enabled=$(helm template "$release" "$chart" "${enabled_args[@]}")

bootstrap_block=$(awk '/- name: bootstrap$/{flag=1} flag{print} /- name: api$/{if(flag){exit}}' <<<"$enabled")
if grep -q -- '--service-account' <<<"$bootstrap_block"; then
  fail "enabled auth still passes legacy --service-account args to the bootstrap init container"
fi

auth_env_count=$(grep -c 'name: AUTH_ENABLED' <<<"$enabled")
[[ "$auth_env_count" -eq 2 ]] || fail "expected AUTH_ENABLED on the bootstrap init container and the api container (got ${auth_env_count})"

grep -q "^  name: ${release}-control-plane-auth-bootstrap$" <<<"$enabled" || fail "missing first-Admin Secret"

with_smtp=$(helm template "$release" "$chart" "${enabled_args[@]}" \
  --set auth.email.username=relay --set auth.email.existingSecret=relay-smtp)
grep -q 'name: relay-smtp' <<<"$with_smtp" || fail "SMTP credential Secret is not referenced"
grep -q 'key: smtp-password' <<<"$with_smtp" || fail "SMTP credential key is not referenced"

# Enabled surfaces fail closed when a prerequisite is missing.
expect_fail helm template "$release" "$chart" \
  --set auth.enabled=true \
  --set auth.email.host=smtp.example.com \
  --set auth.email.from=auth@example.com \
  --set auth.bootstrap.adminEmail=admin@example.com
expect_fail helm template "$release" "$chart" "${enabled_args[@]}" --set auth.email.mode=plain
expect_fail helm template "$release" "$chart" "${enabled_args[@]}" --set auth.publicOrigin=http://app.example.com
expect_fail helm template "$release" "$chart" "${enabled_args[@]}" --set auth.email.username=relay
expect_fail helm template "$release" "$chart" "${enabled_args[@]}" --set auth.bootstrap.adminEmail=""

# The disabled surface keeps the legacy bootstrap path and renders no auth env.
disabled=$(helm template "$release" "$chart")
grep -q -- '--service-account' <<<"$disabled" || fail "disabled auth must keep the legacy bootstrap service accounts"
if grep -q 'AUTH_ENABLED' <<<"$disabled"; then
  fail "disabled auth must not render the browser-auth environment"
fi

echo "OK: browser-auth chart enablement, prerequisites, and legacy fallback render correctly"
