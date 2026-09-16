package sshprovisioner

import (
	"context"
	"fmt"
	"strings"
)

const (
	hostActiveSwapsPath = "/proc/swaps"
	hostFstabPath       = "/etc/fstab"
)

// EnsureHostSwapDisabled implements provisioner.Provisioner. The operation is
// one fail-closed remote transaction: it inspects active and configured swap,
// atomically comments active fstab swap entries without changing unrelated
// lines or file ownership/mode, disables active swap, and then re-reads both
// authoritative sources before returning.
func (p *SSHProvisioner) EnsureHostSwapDisabled(ctx context.Context) error {
	script := hostSwapScript(hostActiveSwapsPath, hostFstabPath, "swapoff")
	if _, err := p.run(ctx, "sudo bash -ceu "+shellQuote(script)); err != nil {
		return fmt.Errorf("disable and verify host swap: %w", err)
	}
	return nil
}

// hostSwapScript accepts paths and a swapoff executable only so executable
// tests can use unprivileged fixtures. Production always supplies the constants
// above and the host's swapoff command; there is no CLI or configuration knob.
func hostSwapScript(activeSwapsPath, fstabPath, swapoffCommand string) string {
	script := `
active_swaps=__FORGE_ACTIVE_SWAPS__
fstab=__FORGE_FSTAB__
swapoff_command=__FORGE_SWAPOFF__
fstab_tmp=

fail() { printf 'host-swap refusal: %s\n' "$*" >&2; exit 42; }
need() { command -v "$1" >/dev/null 2>&1 || fail "required command $1 is unavailable"; }
cleanup_fstab_tmp() { test -z "${fstab_tmp:-}" || rm -f -- "$fstab_tmp"; }
trap cleanup_fstab_tmp EXIT

for tool in awk stat dirname mktemp chown chmod sync mv rm; do need "$tool"; done
test -r "$active_swaps" || fail "active swap state $active_swaps is not readable"
test -f "$fstab" && test ! -L "$fstab" && test -r "$fstab" || fail "configured swap state $fstab is not a readable regular file"

inspect_active_swaps() {
  local output rc
  set +e
  output=$(LC_ALL=C awk '
    NR == 1 {
      if ($1 != "Filename" || $2 != "Type" || $3 != "Size" || $4 != "Used" || $5 != "Priority") exit 42
      next
    }
    NF == 0 { next }
    NF < 5 { exit 43 }
    { print $1 }
    END { if (NR == 0) exit 44 }
  ' "$active_swaps" 2>&1)
  rc=$?
  set -e
  test "$rc" = 0 || fail "cannot inspect active swap state $active_swaps: $output"
  printf '%s' "$output"
}

inspect_configured_swap_count() {
  local output rc
  set +e
  output=$(LC_ALL=C awk '
    function is_active_swap(line, trimmed, fields, count) {
      trimmed=line
      sub(/^[[:space:]]*/, "", trimmed)
      if (trimmed == "" || substr(trimmed, 1, 1) == "#") return 0
      count=split(trimmed, fields, /[[:space:]]+/)
      return count >= 3 && fields[3] == "swap"
    }
    { if (is_active_swap($0)) configured++ }
    END { print configured+0 }
  ' "$fstab" 2>&1)
  rc=$?
  set -e
  test "$rc" = 0 || fail "cannot inspect configured swap state $fstab: $output"
  case "$output" in ''|*[!0-9]*) fail "configured swap inspection returned invalid count: $output" ;; esac
  printf '%s' "$output"
}

active=$(inspect_active_swaps)
configured=$(inspect_configured_swap_count)
if test -n "$active"; then need "$swapoff_command"; fi

uid=$(stat -c '%u' -- "$fstab") || fail "cannot inspect $fstab owner"
gid=$(stat -c '%g' -- "$fstab") || fail "cannot inspect $fstab group"
mode=$(stat -c '%a' -- "$fstab") || fail "cannot inspect $fstab mode"
case "$uid:$gid" in *[!0-9:]*|:*|*:|*:*:*) fail "invalid $fstab ownership $uid:$gid" ;; esac
case "$mode" in ''|*[!0-7]*) fail "invalid $fstab mode $mode" ;; esac
fstab_dir=$(dirname -- "$fstab") || fail "cannot resolve $fstab parent directory"

if test "$configured" -gt 0; then
  fstab_tmp=$(mktemp "$fstab_dir/.forge-fstab.XXXXXX") || fail "cannot create temporary fstab beside $fstab"
  if ! LC_ALL=C awk '
    function is_active_swap(line, trimmed, fields, count) {
      trimmed=line
      sub(/^[[:space:]]*/, "", trimmed)
      if (trimmed == "" || substr(trimmed, 1, 1) == "#") return 0
      count=split(trimmed, fields, /[[:space:]]+/)
      return count >= 3 && fields[3] == "swap"
    }
    { if (is_active_swap($0)) print "# forge-disabled-swap " $0; else print $0 }
  ' "$fstab" >"$fstab_tmp"; then
    fail "cannot safely rewrite configured swap state $fstab"
  fi
  chown "$uid:$gid" "$fstab_tmp" || fail "cannot preserve $fstab ownership $uid:$gid"
  chmod "$mode" "$fstab_tmp" || fail "cannot preserve $fstab mode $mode"
  sync "$fstab_tmp" || fail "cannot persist rewritten configured swap state $fstab"
  mv -- "$fstab_tmp" "$fstab" || fail "cannot atomically replace configured swap state $fstab"
  fstab_tmp=
fi

# Re-establish the post-rename proof on every run. If a prior run was
# interrupted after rename, the converged retry still verifies stable metadata
# and flushes both the replacement inode and its directory entry before k3s.
test "$(stat -c '%u:%g:%a' -- "$fstab")" = "$uid:$gid:$mode" || fail "$fstab ownership or mode changed during rewrite"
sync "$fstab" || fail "cannot persist configured swap state $fstab"
sync "$fstab_dir" || fail "cannot persist configured swap directory $fstab_dir"

if test -n "$active"; then
  "$swapoff_command" --all || fail "swapoff --all failed; active swap may remain"
fi

remaining_active=$(inspect_active_swaps)
test -z "$remaining_active" || fail "active swap remains after swapoff: $remaining_active"
remaining_configured=$(inspect_configured_swap_count)
test "$remaining_configured" = 0 || fail "$remaining_configured uncommented swap entries remain in $fstab"
`
	return strings.NewReplacer(
		"__FORGE_ACTIVE_SWAPS__", shellQuote(activeSwapsPath),
		"__FORGE_FSTAB__", shellQuote(fstabPath),
		"__FORGE_SWAPOFF__", shellQuote(swapoffCommand),
	).Replace(script)
}
