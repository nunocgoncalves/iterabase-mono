#!/usr/bin/env bash
set -euo pipefail

root=$(mktemp -d)
trap 'rm -rf "$root"' EXIT
mkdir -p "$root/bin" "$root/chart"
cat > "$root/bin/sleep" <<'SH'
#!/usr/bin/env bash
exit 0
SH
cat > "$root/bin/helm" <<'SH'
#!/usr/bin/env bash
count=0
test ! -f "$HELM_TEST_COUNT" || count=$(cat "$HELM_TEST_COUNT")
count=$((count + 1))
printf '%s\n' "$count" > "$HELM_TEST_COUNT"
test "$count" -ge "$HELM_TEST_SUCCEED_AT"
SH
chmod +x "$root/bin/helm" "$root/bin/sleep"

export PATH="$root/bin:$PATH"
export HELM_TEST_COUNT="$root/count"
export HELM_TEST_SUCCEED_AT=2
HELM_DEPENDENCY_ATTEMPTS=3 bash scripts/build-chart-dependency.sh "$root/chart"
test "$(cat "$HELM_TEST_COUNT")" = 2

rm -f "$HELM_TEST_COUNT"
export HELM_TEST_SUCCEED_AT=99
if HELM_DEPENDENCY_ATTEMPTS=3 bash scripts/build-chart-dependency.sh "$root/chart" >/dev/null 2>&1; then
  echo "expected exhausted dependency retries to fail" >&2
  exit 1
fi
test "$(cat "$HELM_TEST_COUNT")" = 3
echo "OK: Helm dependency builds retry transient failure and remain bounded"
