package sshprovisioner

import (
	"context"
	"fmt"
	"strconv"
	"strings"

	"github.com/nunocgoncalves/iterabase-mono/forge/internal/provisioner"
)

const (
	hostInotifyProcPath      = "/proc/sys/fs/inotify/max_user_instances"
	hostInotifyDropInPath    = "/etc/sysctl.d/90-iterabase-k3s-inotify.conf"
	hostInotifySysctlCommand = "sysctl"
	hostInotifyOwner         = "0:0"
	hostInotifyMode          = "644"
)

// hostInotifyCanonicalBody is the single setting Forge owns in its drop-in.
// sysctl drop-ins apply in lexical order and later files win, so the stable
// 90- name deliberately matches the manual OPO1 remediation HOR-569 adopts.
var hostInotifyCanonicalBody = fmt.Sprintf(
	"fs.inotify.max_user_instances = %d", provisioner.InotifyMaxUserInstancesRequired,
)

// hostInotifyInspectFunction is the bounded, read-only observation shared by
// InspectHostInotify and the reconcile transaction. Paths and the expected
// owner are placeholders so the executable fixture tests can exercise the
// exact production logic with unprivileged local files.
const hostInotifyInspectFunction = `
proc_path=__FORGE_INOTIFY_PROC__
dropin_path=__FORGE_INOTIFY_DROPIN__
expected_owner=__FORGE_INOTIFY_OWNER__
canonical_body=__FORGE_INOTIFY_CANONICAL__

fail() { printf 'host-inotify refusal: %s\n' "$*" >&2; exit 42; }

inotify_effective=
inotify_persisted=
inotify_present=
inotify_regular=
inotify_owner=
inotify_mode=
inotify_canonical=

inspect_inotify() {
  local body parsed
  test -f "$proc_path" && test -r "$proc_path" || fail "effective inotify state $proc_path is not a readable regular file"
  body=$(cat -- "$proc_path") || fail "cannot read effective inotify state $proc_path"
  case "$body" in ''|*[!0-9]*) fail "effective inotify state $proc_path is not a decimal integer: $body" ;; esac
  inotify_effective=$body

  inotify_persisted=none
  inotify_present=false
  inotify_regular=false
  inotify_owner=none
  inotify_mode=none
  inotify_canonical=false

  if test -L "$dropin_path"; then
    inotify_present=true
  elif test -e "$dropin_path"; then
    inotify_present=true
    if test -f "$dropin_path"; then
      inotify_regular=true
      inotify_owner=$(stat -c '%u:%g' -- "$dropin_path") || fail "cannot inspect $dropin_path ownership"
      inotify_mode=$(stat -c '%a' -- "$dropin_path") || fail "cannot inspect $dropin_path mode"
      case "$inotify_owner" in ''|*[!0-9:]*|*:*:*) fail "invalid $dropin_path ownership: $inotify_owner" ;; esac
      case "$inotify_owner" in :*|*:) fail "invalid $dropin_path ownership: $inotify_owner" ;; esac
      case "$inotify_mode" in ''|*[!0-7]*) fail "invalid $dropin_path mode: $inotify_mode" ;; esac
      body=$(cat -- "$dropin_path") || fail "cannot read $dropin_path"
      parsed=$(printf '%s\n' "$body" | LC_ALL=C awk '
        {
          line=$0
          sub(/^[[:space:]]*/, "", line)
          if (line ~ /^#/) next
          pos=index(line, "=")
          if (pos == 0) next
          key=substr(line, 1, pos-1)
          sub(/[[:space:]]+$/, "", key)
          if (key != "fs.inotify.max_user_instances") next
          value=substr(line, pos+1)
          sub(/[[:space:]]*#.*$/, "", value)
          sub(/^[[:space:]]+/, "", value)
          sub(/[[:space:]]+$/, "", value)
          if (value !~ /^[0-9]+$/) value=""
        }
        END { if (value != "") print value }
      ')
      case "$parsed" in ''|*[!0-9]*) ;; *) inotify_persisted=$parsed ;; esac
      if test "$body" = "$canonical_body" && test "$inotify_owner" = "$expected_owner" && test "$inotify_mode" = __FORGE_INOTIFY_MODE__; then
        inotify_canonical=true
      fi
    fi
  fi
}

print_inotify_state() {
  printf '%s|%s|%s|%s|%s|%s|%s\n' "$inotify_effective" "$inotify_persisted" "$inotify_present" "$inotify_regular" "$inotify_owner" "$inotify_mode" "$inotify_canonical"
}
`

// hostInotifyReconcileTransaction owns the canonical drop-in and the live
// value. It never lowers an already-sufficient live value and never removes
// the existing drop-in before its replacement is durable, so a concurrent
// K3s cannot observe a capacity gap during convergence.
const hostInotifyReconcileTransaction = `
sysctl_command=__FORGE_INOTIFY_SYSCTL__
required=__FORGE_INOTIFY_REQUIRED__

tmp=
cleanup_tmp() { test -z "${tmp:-}" || rm -f -- "$tmp"; }
trap cleanup_tmp EXIT

need() { command -v "$1" >/dev/null 2>&1 || fail "required command $1 is unavailable"; }
for tool in awk cat stat dirname mktemp chown chmod sync mv rm; do need "$tool"; done

inspect_inotify

if test "$inotify_present" = true && test "$inotify_regular" != true; then
  fail "Forge-owned inotify drop-in $dropin_path is not a regular file"
fi

dropin_dir=$(dirname -- "$dropin_path") || fail "cannot resolve the parent directory of $dropin_path"
test -d "$dropin_dir" || fail "inotify drop-in directory $dropin_dir is not a directory"

if test "$inotify_canonical" != true; then
  tmp=$(mktemp "$dropin_dir/.forge-inotify.XXXXXX") || fail "cannot create a temporary inotify drop-in beside $dropin_path"
  printf '%s\n' "$canonical_body" >"$tmp" || fail "cannot stage the canonical inotify drop-in"
  chown "$expected_owner" "$tmp" || fail "cannot set inotify drop-in ownership $expected_owner"
  chmod 0644 "$tmp" || fail "cannot set inotify drop-in mode 0644"
  sync "$tmp" || fail "cannot persist the staged inotify drop-in $tmp"
  mv -- "$tmp" "$dropin_path" || fail "cannot atomically replace the inotify drop-in $dropin_path"
  tmp=
fi

# Re-establish the post-rename proof on every run, including a converged no-op,
# so an interruption after atomic replacement is retry-safe.
test "$(stat -c '%u:%g' -- "$dropin_path")" = "$expected_owner" || fail "inotify drop-in ownership is not $expected_owner"
test "$(stat -c '%a' -- "$dropin_path")" = __FORGE_INOTIFY_MODE__ || fail "inotify drop-in mode is not __FORGE_INOTIFY_MODE__"
sync "$dropin_path" || fail "cannot persist the inotify drop-in $dropin_path"
sync "$dropin_dir" || fail "cannot persist the inotify drop-in directory $dropin_dir"

if test "$inotify_effective" -lt "$required"; then
  need "$sysctl_command"
  "$sysctl_command" -q -w "fs.inotify.max_user_instances=$required" >/dev/null || fail "cannot apply the live inotify instance limit $required"
fi

inspect_inotify
test "$inotify_effective" -ge "$required" || fail "read-back effective inotify value $inotify_effective is below the required $required"
test "$inotify_present" = true || fail "Forge-owned inotify drop-in $dropin_path is missing after reconcile"
test "$inotify_regular" = true || fail "Forge-owned inotify drop-in $dropin_path is not a regular file after reconcile"
test "$inotify_canonical" = true || fail "Forge-owned inotify drop-in $dropin_path is not canonical after reconcile"
print_inotify_state
`

// InspectHostInotify implements provisioner.Provisioner. The single remote
// script is strictly read-only and reports the live value plus drop-in
// presence, type, ownership, mode, and canonical content.
func (p *SSHProvisioner) InspectHostInotify(ctx context.Context) (*provisioner.HostInotifyState, error) {
	out, err := p.run(ctx, "sudo bash -ceu "+shellQuote(hostInotifyInspectScript(hostInotifyProcPath, hostInotifyDropInPath, hostInotifyOwner)))
	if err != nil {
		return nil, fmt.Errorf("inspect host inotify capacity: %w", err)
	}
	state, err := parseHostInotifyState(out)
	if err != nil {
		return nil, fmt.Errorf("inspect host inotify capacity: %w", err)
	}
	return state, nil
}

// ReconcileHostInotify implements provisioner.Provisioner. The operation is
// one fail-closed remote transaction: it converges the canonical drop-in
// atomically without lowering live capacity, raises only a live value below
// the requirement, and refuses to return unless the read-back proves both the
// persistent and effective setting.
func (p *SSHProvisioner) ReconcileHostInotify(ctx context.Context) (*provisioner.HostInotifyState, error) {
	out, err := p.run(ctx, "sudo bash -ceu "+shellQuote(hostInotifyReconcileScript(hostInotifyProcPath, hostInotifyDropInPath, hostInotifySysctlCommand, hostInotifyOwner)))
	if err != nil {
		return nil, fmt.Errorf("reconcile host inotify capacity: %w", err)
	}
	state, err := parseHostInotifyState(out)
	if err != nil {
		return nil, fmt.Errorf("reconcile host inotify capacity: %w", err)
	}
	if !state.Ready() {
		return state, fmt.Errorf("reconcile host inotify capacity: read-back is not ready: %s", state)
	}
	return state, nil
}

// hostInotifyReplacer binds the bounded observation and reconcile steps to one
// host layout. Production always supplies the constants above; executable tests
// substitute unprivileged fixture paths and an equivalent owner.
func hostInotifyReplacer(procPath, dropInPath, owner, sysctlCommand string) *strings.Replacer {
	return strings.NewReplacer(
		"__FORGE_INOTIFY_PROC__", shellQuote(procPath),
		"__FORGE_INOTIFY_DROPIN__", shellQuote(dropInPath),
		"__FORGE_INOTIFY_OWNER__", shellQuote(owner),
		"__FORGE_INOTIFY_CANONICAL__", shellQuote(hostInotifyCanonicalBody),
		"__FORGE_INOTIFY_MODE__", shellQuote(hostInotifyMode),
		"__FORGE_INOTIFY_SYSCTL__", shellQuote(sysctlCommand),
		"__FORGE_INOTIFY_REQUIRED__", strconv.Itoa(provisioner.InotifyMaxUserInstancesRequired),
	)
}

func hostInotifyInspectScript(procPath, dropInPath, owner string) string {
	return hostInotifyReplacer(procPath, dropInPath, owner, hostInotifySysctlCommand).Replace(hostInotifyInspectFunction) +
		"\ninspect_inotify\nprint_inotify_state\n"
}

func hostInotifyReconcileScript(procPath, dropInPath, sysctlCommand, owner string) string {
	return hostInotifyReplacer(procPath, dropInPath, owner, sysctlCommand).Replace(hostInotifyInspectFunction + hostInotifyReconcileTransaction)
}

// parseHostInotifyState decodes the single bounded observation line.
func parseHostInotifyState(out string) (*provisioner.HostInotifyState, error) {
	line := strings.TrimSpace(out)
	fields := strings.Split(line, "|")
	if len(fields) != 7 {
		return nil, fmt.Errorf("unexpected inotify observation %q", line)
	}
	effective, err := strconv.Atoi(fields[0])
	if err != nil || effective < 0 {
		return nil, fmt.Errorf("inotify observation has invalid effective value %q", fields[0])
	}
	persisted := 0
	if fields[1] != "none" {
		persisted, err = strconv.Atoi(fields[1])
		if err != nil || persisted < 0 {
			return nil, fmt.Errorf("inotify observation has invalid persisted value %q", fields[1])
		}
	}
	present, err := hostInotifyBoolField("presence", fields[2])
	if err != nil {
		return nil, err
	}
	regular, err := hostInotifyBoolField("type", fields[3])
	if err != nil {
		return nil, err
	}
	canonical, err := hostInotifyBoolField("canonical", fields[6])
	if err != nil {
		return nil, err
	}
	state := &provisioner.HostInotifyState{
		Effective:       effective,
		Persisted:       persisted,
		DropInPresent:   present,
		DropInRegular:   regular,
		DropInCanonical: canonical,
	}
	if fields[4] != "none" {
		state.DropInOwner = fields[4]
	}
	if fields[5] != "none" {
		state.DropInMode = fields[5]
	}
	if state.DropInCanonical && (!present || !regular) {
		return nil, fmt.Errorf("inotify observation claims canonical content without a present regular drop-in")
	}
	return state, nil
}

func hostInotifyBoolField(name, value string) (bool, error) {
	switch value {
	case "true":
		return true, nil
	case "false":
		return false, nil
	default:
		return false, fmt.Errorf("inotify observation has invalid %s value %q", name, value)
	}
}
