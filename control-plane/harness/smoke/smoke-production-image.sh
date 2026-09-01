#!/usr/bin/env bash
# Smoke-test the exact production harness image (HOR-539).
#
# Models the production cert-manager CSI projection contract (DES-HOR-538-03):
# the supervisor may start only when a pod-scoped AtomicWriter chain beneath
# protected ancestors resolves to a root:root exact-0440 regular `tls.key`.
# The production image is run as root (the rendered root supervisor) against a
# root-owned bind fixture so the observe-only invariant is exercised exactly.
#
#   * POSITIVE  - a valid contained AtomicWriter projection must reach the
#                 /healthz "ok" contract.
#   * NEGATIVE  - a malformed projection (a direct regular `tls.key`, the exact
#                 regression HOR-538 rejected) must fail closed at startup
#                 (non-zero exit, never serves readiness) and must not be
#                 repaired (owner/mode/type unchanged).
#
# This is the ONE maintained fixture/contract shared by PR CI and release
# candidate CI. Any regression of the fixture to a direct file, wrong
# owner/mode, invalid link chain, or other unsupported representation fails
# here before merge.

set -euo pipefail

IMAGE="${1:?usage: $0 <image-reference>}"
HEALTHZ_TIMEOUT="${HEALTHZ_TIMEOUT:-30}"   # positive: max seconds to reach healthz "ok"
NEGATIVE_TIMEOUT="${NEGATIVE_TIMEOUT:-40}" # negative: max seconds to observe fail-closed exit

log() { printf '[harness-smoke] %s\n' "$*" >&2; }

as_root() {
  if [ "$(id -u)" -eq 0 ]; then "$@"; else sudo "$@"; fi
}

CONTAINER=""
SMOKE_BASE="$(mktemp -d "${TMPDIR:-/tmp}/harness-smoke.XXXXXX")"
VALID_DIR="$SMOKE_BASE/valid"
MALFORMED_DIR="$SMOKE_BASE/malformed"

cleanup() {
  if [ -n "$CONTAINER" ]; then docker rm -f "$CONTAINER" >/dev/null 2>&1 || true; fi
  as_root rm -rf "$SMOKE_BASE" >/dev/null 2>&1 || true
}
trap cleanup EXIT

# Timestamp name must match the AtomicWriter pattern `\.\.\d{4}_\d{2}_..._[0-9a-z]+`.
TIMESTAMP="..2026_08_31_12_00_00.000000001"
FIXTURE_CONTENT='release candidate smoke fixture'

write_config() { # <fixture-dir/>  (writes config.yaml as root)
  local dir="$1"
  as_root tee "$dir/config.yaml" >/dev/null <<'YAML'
controlPlane:
  url: https://127.0.0.1:65535
  serverName: localhost
worker:
  workerId: release-smoke
  poolId: release-smoke
tls:
  cert: /smoke/tls.crt
  key: /smoke/tls.key
  ca: /smoke/ca.crt
sandboxRoot: /tmp/harness-smoke-sandboxes
piDirs: []
toolGateway:
  url: https://127.0.0.1:65535
  serverName: localhost
inferenceGateway:
  url: https://127.0.0.1:65535
  serverName: localhost
walDir: /tmp/harness-smoke-wal
reconnect:
  initialBackoffMs: 100
  maxBackoffMs: 100
  resetAfterMs: 100
YAML
}

write_peer_material() { # <fixture-dir/>  root:root 0440 tls.crt/ca.crt (peer material, not the validated key)
  local dir="$1"
  local peer='release candidate smoke fixture'
  printf '%s\n' "$peer" | as_root tee "$dir/tls.crt" >/dev/null
  printf '%s\n' "$peer" | as_root tee "$dir/ca.crt" >/dev/null
  as_root chown root:root "$dir/tls.crt" "$dir/ca.crt"
  as_root chmod 0440 "$dir/tls.crt" "$dir/ca.crt"
}

# Valid: protected root-owned mount, `..data` -> one timestamp dir,
# `tls.key -> ..data/tls.key` resolving to a root:root exact-0440 regular file.
build_valid_fixture() {
  local dir="$1"
  as_root install -d -m 0700 -o root -g root "$dir"
  as_root install -d -m 0700 -o root -g root "$dir/$TIMESTAMP"
  printf '%s\n' "$FIXTURE_CONTENT" | as_root tee "$dir/$TIMESTAMP/tls.key" >/dev/null
  as_root chown root:root "$dir/$TIMESTAMP/tls.key"
  as_root chmod 0440 "$dir/$TIMESTAMP/tls.key"
  as_root ln -s "$TIMESTAMP" "$dir/..data"
  as_root ln -s "..data/tls.key" "$dir/tls.key"
  write_peer_material "$dir"
  write_config "$dir"
}

# Verify the valid projection independently of the image/validator under test,
# so fixture and validator drift cannot mask one another (HOR-539 acceptance #1):
# protected root-owned non-child-writable mount, the exact two-link AtomicWriter
# shape with the current timestamp target, and a root:root exact-0440 regular
# resolved key carrying the expected content.
assert_valid_fixture() { # <fixture-dir/>
  local dir="$1" mode owner type rem
  type="$(as_root stat -c '%F' "$dir")"
  test "$type" = directory || { log "valid: mount is not a directory: $type" >&2; return 1; }
  owner="$(as_root stat -c '%u:%g' "$dir")"
  test "$owner" = "0:0" || { log "valid: mount not root-owned: $owner" >&2; return 1; }
  mode="$(as_root stat -c '%a' "$dir")"
  rem=$(( 8#${mode} & 8#22 ))
  test "$rem" -eq 0 || { log "valid: mount is child-writable (mode $mode)" >&2; return 1; }

  local key_link data_link ts
  key_link="$(as_root readlink "$dir/tls.key")"
  test "$key_link" = "..data/tls.key" || { log "valid: tls.key -> $key_link" >&2; return 1; }
  data_link="$(as_root readlink "$dir/..data")"
  if ! printf '%s' "$data_link" | grep -Eq '^\.\.20[0-9]{2}_[0-9]{2}_[0-9]{2}_[0-9]{2}_[0-9]{2}_[0-9]{2}\.[0-9a-z]+$'; then
    log "valid: ..data -> invalid timestamp target $data_link" >&2
    return 1
  fi
  ts="$data_link"
  test "$ts" = "$TIMESTAMP" || { log "valid: ..data not the current timestamp (expected $TIMESTAMP, got $ts)" >&2; return 1; }

  type="$(as_root stat -c '%F' "$dir/$ts")"
  test "$type" = directory || { log "valid: timestamp target not a directory: $type" >&2; return 1; }
  owner="$(as_root stat -c '%u:%g' "$dir/$ts")"
  test "$owner" = "0:0" || { log "valid: timestamp dir not root-owned: $owner" >&2; return 1; }
  mode="$(as_root stat -c '%a' "$dir/$ts")"
  rem=$(( 8#${mode} & 8#22 ))
  test "$rem" -eq 0 || { log "valid: timestamp dir is child-writable (mode $mode)" >&2; return 1; }

  type="$(as_root stat -c '%F' "$dir/$ts/tls.key")"
  test "$type" = "regular file" || { log "valid: resolved key not regular: $type" >&2; return 1; }
  owner="$(as_root stat -c '%u:%g' "$dir/$ts/tls.key")"
  test "$owner" = "0:0" || { log "valid: resolved key not root:root: $owner" >&2; return 1; }
  mode="$(as_root stat -c '%a' "$dir/$ts/tls.key")"
  test "$mode" = 440 || { log "valid: resolved key mode $mode (expected 0440)" >&2; return 1; }
  test "$(as_root cat "$dir/$ts/tls.key")" = "$FIXTURE_CONTENT" || { log "valid: resolved key content mismatch" >&2; return 1; }

  log "positive: asserted contained AtomicWriter shape before image start"
}

# Malformed: the flat direct `tls.key` regression HOR-539 must fail closed on
# (a direct regular file is not the required AtomicWriter link chain).
build_malformed_fixture() {
  local dir="$1"; local before
  as_root install -d -m 0700 -o root -g root "$dir"
  printf '%s\n' 'malformed smoke fixture' | as_root tee "$dir/tls.key" >/dev/null
  as_root chown root:root "$dir/tls.key"
  as_root chmod 0440 "$dir/tls.key"
  write_peer_material "$dir"
  write_config "$dir"
  before="$(as_root stat -c '%F %a %u:%g' "$dir/tls.key")"
  printf '%s' "$before" > "$SMOKE_BASE/malformed.before"
}

run_container() { # <fixture-dir/>
  local fixture_dir="$1"
  CONTAINER="$(docker run --detach --user 0:0 \
    --env HARNESS_CONFIG=/smoke/config.yaml \
    --mount "type=bind,source=$fixture_dir,target=/smoke,readonly" \
    --publish 127.0.0.1::8081 "$IMAGE")"
}

# Positive: the exact production image accepts the valid projection and reaches
# its smoke health contract.
positive_smoke() {
  log "positive: valid AtomicWriter projection -> healthz ok"
  build_valid_fixture "$VALID_DIR"
  assert_valid_fixture "$VALID_DIR"
  run_container "$VALID_DIR"
  local port
  port="$(docker port "$CONTAINER" 8081/tcp | awk -F: 'NR == 1 {print $NF}')"
  for _ in $(seq 1 "$HEALTHZ_TIMEOUT"); do
    if body="$(curl --fail --silent "http://127.0.0.1:$port/healthz" 2>/dev/null)"; then
      if [ "$body" = ok ]; then
        log "positive: passed (/healthz ok)"
        return 0
      fi
    fi
    test "$(docker inspect --format '{{.State.Running}}' "$CONTAINER")" = true || {
      docker logs "$CONTAINER"
      log "positive: container exited before reporting health" >&2
      return 1
    }
    sleep 1
  done
  docker logs "$CONTAINER"
  log "positive: valid projection did not reach /healthz ok in ${HEALTHZ_TIMEOUT}s" >&2
  return 1
}

# Negative: a malformed projection must be refused at startup (fail closed,
# never readiness), with the fixture owner/mode/type unchanged (no repair).
negative_smoke() {
  log "negative: malformed direct tls.key must fail closed with no repair"
  build_malformed_fixture "$MALFORMED_DIR"
  run_container "$MALFORMED_DIR"
  local port
  port="$(docker port "$CONTAINER" 8081/tcp 2>/dev/null | awk -F: 'NR == 1 {print $NF}' || true)"
  for _ in $(seq 1 "$NEGATIVE_TIMEOUT"); do
    # Assert throughout the window that no readiness/startup probe ever succeeds.
    if [ -n "$port" ]; then
      if body="$(curl --fail --silent "http://127.0.0.1:$port/healthz" 2>/dev/null)"; then
        if [ "$body" = ok ]; then
          log "negative: malformed projection unexpectedly served /healthz ok" >&2
          return 1
        fi
      fi
    fi
    local running
    running="$(docker inspect --format '{{.State.Running}}' "$CONTAINER")"
    if [ "$running" != true ]; then break; fi
    sleep 1
  done
  if [ "$(docker inspect --format '{{.State.Running}}' "$CONTAINER")" = true ]; then
    docker logs "$CONTAINER"
    log "negative: malformed projection did not fail closed (container still running)" >&2
    return 1
  fi
  local exit_code
  exit_code="$(docker inspect --format '{{.State.ExitCode}}' "$CONTAINER")"
  if [ "$exit_code" -eq 0 ]; then
    docker logs "$CONTAINER"
    log "negative: malformed projection exited 0 (validator bypassed)" >&2
    return 1
  fi
  local logs
  logs="$(docker logs "$CONTAINER" 2>&1 || true)"
  if ! printf '%s\n' "$logs" | grep -qiE 'AtomicWriter path is not the required symlink|TLSKeyError|supervisor TLS'; then
    log "negative: missing fail-closed TLS signal in container logs" >&2
    printf '%s\n' "$logs" >&2
    return 1
  fi
  local before after
  before="$(cat "$SMOKE_BASE/malformed.before")"
  after="$(as_root stat -c '%F %a %u:%g' "$MALFORMED_DIR/tls.key")"
  if [ "$before" != "$after" ]; then
    log "negative: fixture was repaired ($before -> $after); observe-only invariant violated" >&2
    return 1
  fi
  log "negative: passed (fail-closed exit $exit_code, fixture unchanged)"
}

log "smoking production harness image $IMAGE"

positive_smoke
if [ -n "$CONTAINER" ]; then docker rm -f "$CONTAINER" >/dev/null 2>&1 || true; CONTAINER=""; fi

negative_smoke
log "all harness image smoke cases passed"
