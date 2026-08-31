package sshprovisioner

import (
	"context"
	"encoding/json"
	"fmt"
	"strconv"
	"strings"
	"time"

	"github.com/nunocgoncalves/iterabase-mono/forge/internal/config"
	"github.com/nunocgoncalves/iterabase-mono/forge/internal/provisioner"
)

const (
	workspaceContractVersion = "HOR-538/v2"
	workspaceFilesystemLabel = "iterabase-ws"
	workspaceReceiptPath     = "/var/lib/iterabase/agentpool-workspace.receipt"
	workspaceMarkerName      = ".iterabase-workspace-identity"
	k3sDefaultLocalPath      = "/var/lib/rancher/k3s/storage"
)

// ListAgentPoolWorkspaceDevices lists stable whole-disk identities without
// reading arbitrary device bytes or mutating the host.
func (p *SSHProvisioner) ListAgentPoolWorkspaceDevices(ctx context.Context) ([]provisioner.WorkspaceDevice, error) {
	cmd := `sudo bash -ceu '
for tool in readlink lsblk sort tr; do command -v "$tool" >/dev/null; done
for selected in /dev/disk/by-id/*; do
  test -L "$selected" || continue
  case "$selected" in *-part[0-9]*) continue ;; esac
  device=$(readlink -f -- "$selected")
  test -b "$device" || continue
  test "$(lsblk -dnro TYPE -- "$device")" = disk || continue
  test "$(lsblk -dnro RM -- "$device")" = 0 || continue
  model=$(lsblk -dnro MODEL -- "$device" | tr "\t\r\n" "   ")
  serial=$(lsblk -dnro SERIAL -- "$device" | tr "\t\r\n" "   ")
  size=$(lsblk -bdnro SIZE -- "$device")
  transport=$(lsblk -dnro TRAN -- "$device" | tr "[:upper:]" "[:lower:]" | tr "\t\r\n" "   ")
  printf "FORGE_WORKSPACE_DEVICE\t%s\t%s\t%s\t%s\t%s\n" "$selected" "$model" "$serial" "$size" "$transport"
done | sort -u
'`
	out, err := p.run(ctx, cmd)
	if err != nil {
		return nil, fmt.Errorf("list stable AgentPool workspace devices: %w", err)
	}
	var devices []provisioner.WorkspaceDevice
	for _, line := range strings.Split(out, "\n") {
		parts := strings.Split(line, "\t")
		if len(parts) != 6 || parts[0] != "FORGE_WORKSPACE_DEVICE" {
			continue
		}
		size, parseErr := strconv.ParseUint(strings.TrimSpace(parts[4]), 10, 64)
		if parseErr != nil {
			return nil, fmt.Errorf("parse workspace device size from %q: %w", line, parseErr)
		}
		devices = append(devices, provisioner.WorkspaceDevice{
			Path: parts[1], Model: strings.TrimSpace(parts[2]), Serial: strings.TrimSpace(parts[3]),
			SizeBytes: size, Transport: strings.TrimSpace(parts[5]),
		})
	}
	return devices, nil
}

// EnsureAgentPoolWorkspaceTools installs and verifies only the formatter tools
// needed by the already-resolved filesystem. It never reads or writes the
// selected device. Ubuntu/apt is the supported Forge host contract.
func (p *SSHProvisioner) EnsureAgentPoolWorkspaceTools(ctx context.Context, filesystem string) error {
	switch filesystem {
	case config.WorkspaceFilesystemExt4:
		if _, err := p.run(ctx, "command -v mkfs.ext4 >/dev/null"); err != nil {
			return fmt.Errorf("required ext4 tooling is unavailable: %w", err)
		}
		return nil
	case config.WorkspaceFilesystemXFS:
		const verify = "command -v mkfs.xfs >/dev/null && command -v xfs_info >/dev/null"
		if _, err := p.run(ctx, verify); err == nil {
			return nil
		}
		cmd := "sudo apt-get update && sudo apt-get install -y xfsprogs"
		for attempt := 0; ; attempt++ {
			out, err := p.run(ctx, cmd)
			if err == nil {
				if _, verifyErr := p.run(ctx, verify); verifyErr != nil {
					return fmt.Errorf("verify required XFS tooling after installing xfsprogs: %w", verifyErr)
				}
				return nil
			}
			if (!isAptLockHeld(err.Error()) && !isAptLockHeld(out)) || attempt >= 20 {
				return fmt.Errorf("install required XFS tooling (xfsprogs): %w", err)
			}
			select {
			case <-ctx.Done():
				return ctx.Err()
			case <-time.After(aptLockRetryInterval):
			}
		}
	default:
		return fmt.Errorf("unsupported resolved AgentPool workspace filesystem %q", filesystem)
	}
}

func (p *SSHProvisioner) InspectAgentPoolWorkspace(ctx context.Context, spec provisioner.AgentPoolWorkspaceSpec) (*provisioner.AgentPoolWorkspaceState, error) {
	return p.runAgentPoolWorkspace(ctx, spec, false)
}

func (p *SSHProvisioner) ReconcileAgentPoolWorkspace(ctx context.Context, spec provisioner.AgentPoolWorkspaceSpec) (*provisioner.AgentPoolWorkspaceState, error) {
	return p.runAgentPoolWorkspace(ctx, spec, true)
}

func (p *SSHProvisioner) runAgentPoolWorkspace(ctx context.Context, spec provisioner.AgentPoolWorkspaceSpec, reconcile bool) (*provisioner.AgentPoolWorkspaceState, error) {
	mode := "inspect"
	if reconcile {
		mode = "reconcile"
	}
	script := workspaceReconcileScript(spec, mode)
	out, err := p.run(ctx, "sudo bash -ceu "+shellQuote(script))
	if err != nil {
		return nil, fmt.Errorf("%s AgentPool workspace %s: %w", mode, spec.Device, err)
	}
	state, err := parseWorkspaceResult(out)
	if err != nil {
		return nil, err
	}
	return state, nil
}

func parseWorkspaceResult(out string) (*provisioner.AgentPoolWorkspaceState, error) {
	for _, line := range strings.Split(out, "\n") {
		parts := strings.Split(line, "\t")
		if len(parts) != 11 || parts[0] != "FORGE_WORKSPACE_RESULT" {
			continue
		}
		size, err := strconv.ParseUint(parts[6], 10, 64)
		if err != nil {
			return nil, fmt.Errorf("parse workspace result size: %w", err)
		}
		return &provisioner.AgentPoolWorkspaceState{
			Device: parts[1], Resolved: parts[2], Model: parts[3], Serial: parts[4], WWN: parts[5],
			SizeBytes: size, Transport: parts[7], Filesystem: parts[8], FilesystemUUID: parts[9], State: parts[10],
		}, nil
	}
	return nil, fmt.Errorf("workspace reconciliation returned no bounded result")
}

func workspaceReconcileScript(spec provisioner.AgentPoolWorkspaceSpec, mode string) string {
	return fmt.Sprintf(`
install_name=%s
selected=%s
filesystem_selection=%s
mode=%s
contract=%s
mount_path=%s
label=%s
receipt=%s
marker_name=%s

fail() { printf 'workspace refusal: %%s\n' "$*" >&2; exit 42; }
need() { command -v "$1" >/dev/null 2>&1 || fail "required probe/tool $1 is unavailable"; }
for tool in readlink lsblk findmnt blkid wipefs awk grep stat base64 sync mount install find cat dirname mktemp mv chown chmod tr; do need "$tool"; done
case "$selected" in /dev/disk/by-id/*) ;; *) fail "selected device is not a stable /dev/disk/by-id identity" ;; esac
case "$selected" in *-part[0-9]*) fail "selected device is a partition identity" ;; esac
test -L "$selected" || fail "selected stable identity is missing or not a symlink"
device=$(readlink -f -- "$selected")
test -b "$device" || fail "selected identity does not resolve to a block device"
kname=$(lsblk -dnro KNAME -- "$device")
test -n "$kname" || fail "cannot determine selected kernel device identity"
case "$kname" in loop*|dm-*|md*|zd*|nbd*) fail "loop/mapper/RAID/network block devices are unsupported" ;; esac
transport=$(lsblk -dnro TRAN -- "$device" | tr '[:upper:]' '[:lower:]' | tr '\t\r\n' '   ')
transport=$(printf '%%s' "$transport" | awk '{$1=$1; print}')
transport_identity=${transport:-unknown}
case "$filesystem_selection" in
  auto) if test "$transport" = nvme; then filesystem=xfs; else filesystem=ext4; fi ;;
  ext4|xfs) filesystem=$filesystem_selection ;;
  *) fail "workspace filesystem selection must be auto, ext4, or xfs" ;;
esac
if test "$mode" = reconcile; then
  case "$filesystem" in
    ext4) need mkfs.ext4 ;;
    xfs) need mkfs.xfs; need xfs_info ;;
  esac
fi

probe_identity_topology() {
  test -L "$selected" || fail "selected stable identity disappeared"
  test "$(readlink -f -- "$selected")" = "$device" || fail "selected stable identity drifted during reconciliation"
  current_transport=$(lsblk -dnro TRAN -- "$device" | tr '[:upper:]' '[:lower:]' | tr '\t\r\n' '   ')
  current_transport=$(printf '%%s' "$current_transport" | awk '{$1=$1; print}')
  current_transport=${current_transport:-unknown}
  test "$current_transport" = "$transport_identity" || fail "selected disk transport identity drifted during reconciliation"
  test "$(lsblk -dnro TYPE -- "$device")" = disk || fail "selected identity is not a whole disk"
  test "$(lsblk -dnro RM -- "$device")" = 0 || fail "selected disk is removable"
  test "$(lsblk -nrpo PATH -- "$device" | awk 'NF {n++} END {print n+0}')" = 1 || fail "selected disk has partitions or child devices"
  test -z "$(lsblk -dnro PTTYPE -- "$device")" || fail "selected disk has a partition table"
  test ! -d "/sys/class/block/$kname/holders" || test -z "$(find "/sys/class/block/$kname/holders" -mindepth 1 -maxdepth 1 -print -quit)" || fail "selected disk has active holders"
  for target in / /boot /boot/efi /var/lib/rancher/k3s /var/lib/kubelet; do
    source=$(findmnt -n -o SOURCE --target "$target" 2>/dev/null || true)
    source=${source%%[*}
    test -n "$source" || continue
    source=$(readlink -f -- "$source" 2>/dev/null || true)
    test -b "$source" || continue
    if lsblk -snro PATH -- "$source" | grep -Fxq "$device"; then fail "selected disk backs system path $target"; fi
  done
  while read -r source _; do
    test "$source" != Filename || continue
    source=$(readlink -f -- "$source" 2>/dev/null || true)
    test -b "$source" || continue
    if lsblk -snro PATH -- "$source" | grep -Fxq "$device"; then fail "selected disk backs active swap"; fi
  done < /proc/swaps
}

probe_active_raw_consumers() {
  device_number=$(stat -Lc '%%t:%%T' "$device" 2>/dev/null) || fail "cannot determine selected disk device number for the active-open probe"
  scanned_fds=0
  for process in /proc/[0-9]*; do
    test -d "$process/fd" || continue
    for fd in "$process"/fd/[0-9]*; do
      test -L "$fd" || continue
      scanned_fds=$((scanned_fds + 1))
      test "$scanned_fds" -le 65536 || fail "active-open probe exceeded its bounded 65536-descriptor limit"
      fd_rc=1
      fd_number=
      for fd_attempt in 1 2 3; do
        set +e
        fd_number=$(stat -Lc '%%t:%%T' "$fd" 2>&1)
        fd_rc=$?
        set -e
        test "$fd_rc" = 0 && break
        test -L "$fd" || break
      done
      if test "$fd_rc" != 0; then
        # A process may close and quickly reuse one descriptor number while
        # /proc is inspected. Retry that exact path three times, then ignore
        # only a proven disappearance; every persistently unreadable descriptor
        # remains uncertain and therefore refuses first format.
        test ! -L "$fd" || fail "active-open probe could not inspect $fd after 3 attempts: $fd_number"
        continue
      fi
      if test "$fd_number" = "$device_number"; then
        fail "selected disk is held open as a raw block device by process ${process##*/}"
      fi
    done
  done
}

probe_blank_signatures() {
  probe_identity_topology
  probe_active_raw_consumers
  mounts=$(findmnt -rn -S "$device" -o TARGET 2>/dev/null || true)
  test -z "$mounts" || fail "selected disk is mounted at $mounts"
  set +e
  wipe_types=$(wipefs -n --noheadings --output TYPE -- "$device" 2>&1)
  wipe_rc=$?
  set -e
  test "$wipe_rc" = 0 || fail "required wipefs signature probe failed: $wipe_types"
  test -z "$(printf '%%s' "$wipe_types" | awk 'NF')" || fail "selected disk has a recognized partition/filesystem/RAID/LVM/crypt signature"
  set +e
  blk_type=$(blkid -p -s TYPE -o value -- "$device" 2>&1)
  blk_rc=$?
  set -e
  test "$blk_rc" = 2 || { test "$blk_rc" = 0 && fail "selected disk has recognized signature $blk_type"; fail "required blkid signature probe failed or was ambiguous: $blk_type"; }
}

sanitize() { printf '%%s' "$1" | tr '\t|\r\n' '    '; }
model=$(sanitize "$(lsblk -dnro MODEL -- "$device")")
serial=$(sanitize "$(lsblk -dnro SERIAL -- "$device")")
wwn=$(sanitize "$(lsblk -dnro WWN -- "$device")")
size=$(lsblk -bdnro SIZE -- "$device")
case "$size" in ''|*[!0-9]*) fail "selected disk size probe is invalid" ;; esac
test -n "$serial$wwn" || fail "selected disk exposes neither serial nor WWN hardware identity"

receipt_value() { awk -F= -v wanted="$1" '$1 == wanted {sub(/^[^=]*=/, ""); print; found=1} END {if (!found) exit 3}' "$receipt"; }
decode_receipt() { receipt_value "$1" | base64 -d; }
write_receipt() {
  status_value=$1
  receipt_dir=$(dirname "$receipt")
  install -d -o root -g root -m 0700 "$receipt_dir"
  tmp=$(mktemp "$receipt_dir/.agentpool-workspace.receipt.XXXXXX")
  umask 077
  {
    printf 'contract=%%s\n' "$contract"
    printf 'status=%%s\n' "$status_value"
    printf 'install_b64=%%s\n' "$(printf '%%s' "$install_name" | base64 -w0)"
    printf 'device_b64=%%s\n' "$(printf '%%s' "$selected" | base64 -w0)"
    printf 'model_b64=%%s\n' "$(printf '%%s' "$model" | base64 -w0)"
    printf 'serial_b64=%%s\n' "$(printf '%%s' "$serial" | base64 -w0)"
    printf 'wwn_b64=%%s\n' "$(printf '%%s' "$wwn" | base64 -w0)"
    printf 'transport_b64=%%s\n' "$(printf '%%s' "$transport_identity" | base64 -w0)"
    printf 'size=%%s\n' "$size"
    printf 'filesystem_selection=%%s\n' "$filesystem_selection"
    printf 'filesystem=%%s\n' "$filesystem"
    printf 'uuid=%%s\n' "$planned_uuid"
    printf 'label=%%s\n' "$label"
    printf 'mount_b64=%%s\n' "$(printf '%%s' "$mount_path" | base64 -w0)"
  } > "$tmp"
  chown root:root "$tmp"; chmod 0600 "$tmp"; sync -f "$tmp"
  mv -f "$tmp" "$receipt"; sync -f "$receipt_dir"
}

planned_uuid=
status=
if test -e "$receipt"; then
  test -f "$receipt" && test ! -L "$receipt" || fail "workspace receipt is not a regular file"
  test "$(stat -c '%%u:%%g:%%a' "$receipt")" = 0:0:600 || fail "workspace receipt ownership/mode drift"
  test "$(receipt_value contract)" = "$contract" || fail "workspace receipt contract mismatch"
  status=$(receipt_value status)
  case "$status" in planned|formatted|fstab|mounted|complete) ;; *) fail "workspace receipt status is invalid" ;; esac
  test "$(decode_receipt install_b64)" = "$install_name" || fail "workspace receipt install mismatch"
  test "$(decode_receipt device_b64)" = "$selected" || fail "workspace config device differs from the recorded transaction"
  test "$(decode_receipt model_b64)" = "$model" || fail "workspace disk model identity mismatch"
  test "$(decode_receipt serial_b64)" = "$serial" || fail "workspace disk serial identity mismatch"
  test "$(decode_receipt wwn_b64)" = "$wwn" || fail "workspace disk WWN identity mismatch"
  test "$(decode_receipt transport_b64)" = "$transport_identity" || fail "workspace disk transport identity mismatch"
  test "$(receipt_value size)" = "$size" || fail "workspace disk size identity mismatch"
  test "$(receipt_value filesystem_selection)" = "$filesystem_selection" || fail "workspace filesystem selection differs from the recorded transaction"
  test "$(receipt_value filesystem)" = "$filesystem" || fail "workspace resolved filesystem mismatch"
  planned_uuid=$(receipt_value uuid)
  case "$planned_uuid" in ????????-????-????-????-????????????) ;; *) fail "workspace receipt UUID is invalid" ;; esac
  test "$(receipt_value label)" = "$label" || fail "workspace filesystem label mismatch"
  test "$(decode_receipt mount_b64)" = "$mount_path" || fail "workspace mount identity mismatch"
else
  probe_blank_signatures
  if test "$mode" = inspect; then
    printf 'FORGE_WORKSPACE_RESULT\t%%s\t%%s\t%%s\t%%s\t%%s\t%%s\t%%s\t%%s\t\tblank-candidate\n' "$selected" "$device" "$model" "$serial" "$wwn" "$size" "$transport_identity" "$filesystem"
    exit 0
  fi
  planned_uuid=$(cat /proc/sys/kernel/random/uuid)
  write_receipt planned
  status=planned
fi

probe_identity_topology
probe_active_raw_consumers
set +e
fs_type=$(blkid -p -s TYPE -o value -- "$device" 2>&1)
fs_rc=$?
set -e
if test "$fs_rc" = 2; then
  test "$status" = planned || fail "recorded workspace filesystem disappeared after format"
  if test "$mode" = inspect; then
    probe_blank_signatures
    printf 'FORGE_WORKSPACE_RESULT\t%%s\t%%s\t%%s\t%%s\t%%s\t%%s\t%%s\t%%s\t%%s\treceipt-blank\n' "$selected" "$device" "$model" "$serial" "$wwn" "$size" "$transport_identity" "$filesystem" "$planned_uuid"
    exit 0
  fi
  # DES-HOR-538-02: repeat every required check immediately before the one
  # authorized first format. wipefs/blkid are bounded signature probes; there is
  # deliberately no block-wide content scan. Filesystem choice is configuration,
  # not a second destructive confirmation.
  probe_blank_signatures
  case "$filesystem" in
    ext4) mkfs.ext4 -F -U "$planned_uuid" -L "$label" -- "$device" ;;
    xfs) mkfs.xfs -f -m uuid="$planned_uuid" -L "$label" "$device" ;;
  esac
  write_receipt formatted
  status=formatted
elif test "$fs_rc" = 0 && test "$fs_type" = "$filesystem"; then
  actual_uuid=$(blkid -p -s UUID -o value -- "$device")
  actual_label=$(blkid -p -s LABEL -o value -- "$device")
  test "$actual_uuid" = "$planned_uuid" || fail "workspace filesystem UUID mismatch"
  test "$actual_label" = "$label" || fail "workspace filesystem label mismatch"
else
  fail "selected disk has an unrecognized, ambiguous, wrong-type, or non-Forge filesystem signature"
fi

actual_type=$(blkid -p -s TYPE -o value -- "$device")
actual_uuid=$(blkid -p -s UUID -o value -- "$device")
actual_label=$(blkid -p -s LABEL -o value -- "$device")
test "$actual_type" = "$filesystem" && test "$actual_uuid" = "$planned_uuid" && test "$actual_label" = "$label" || fail "workspace filesystem identity drift"
test "$(blkid -t UUID="$planned_uuid" -o device | awk 'NF {n++} END {print n+0}')" = 1 || fail "workspace filesystem UUID is missing or duplicated"

if test "$mode" = inspect; then
  inspect_state="resumable-$status"
  if test "$status" = complete; then
    repair_required=false
    if test "$filesystem" = ext4; then fstab_pass=2; else fstab_pass=0; fi
    fstab_line="UUID=$planned_uuid $mount_path $filesystem nodev,nosuid 0 $fstab_pass"
    conflicts=$(awk -v uuid="UUID=$planned_uuid" -v target="$mount_path" -v expected="$fstab_line" '
      /^[[:space:]]*#/ || NF == 0 {next}
      ($1 == uuid || $2 == target) && $0 != expected {print}
    ' /etc/fstab)
    test -z "$conflicts" || fail "conflicting completed workspace fstab entry: $conflicts"
    fstab_count=$(grep -Fxc "$fstab_line" /etc/fstab || true)
    test "$fstab_count" -le 1 || fail "completed workspace fstab identity is duplicated"
    test "$fstab_count" = 1 || repair_required=true
    mounted_source=$(findmnt -n -o SOURCE --mountpoint "$mount_path" 2>/dev/null || true)
    if test -z "$mounted_source"; then
      repair_required=true
      unexpected=$(findmnt -rn -S "$device" -o TARGET 2>/dev/null || true)
      test -z "$unexpected" || fail "workspace disk has an unexpected active consumer: $unexpected"
    else
      mounted_source=${mounted_source%%%%[*}
      test "$(readlink -f -- "$mounted_source")" = "$device" || fail "completed workspace mount source drift"
      test "$(findmnt -n -o FSTYPE --mountpoint "$mount_path")" = "$filesystem" || fail "completed workspace mount type drift"
      options=$(findmnt -n -o OPTIONS --mountpoint "$mount_path")
      case ",$options," in *,nodev,*) ;; *) fail "completed workspace mount lacks nodev" ;; esac
      case ",$options," in *,nosuid,*) ;; *) fail "completed workspace mount lacks nosuid" ;; esac
      test "$(stat -c '%%u:%%g:%%a' "$mount_path")" = 0:0:711 || fail "completed workspace mount ownership/mode drift"
      marker="$mount_path/$marker_name"
      marker_content=$(printf 'contract=%%s\ninstall=%%s\ndevice=%%s\ntransport=%%s\nfilesystem_selection=%%s\nfilesystem=%%s\nuuid=%%s\nlabel=%%s\n' "$contract" "$install_name" "$selected" "$transport_identity" "$filesystem_selection" "$filesystem" "$planned_uuid" "$label")
      test -f "$marker" && test ! -L "$marker" || fail "completed workspace identity marker is missing or unsafe"
      test "$(stat -c '%%u:%%g:%%a' "$marker")" = 0:0:600 || fail "completed workspace marker ownership/mode drift"
      test "$(cat "$marker")" = "$marker_content" || fail "completed workspace marker content drift"
      unexpected=$(findmnt -rn -S "$device" -o TARGET | awk -v expected="$mount_path" '$0 != expected {print}')
      test -z "$unexpected" || fail "workspace disk has an unexpected active consumer: $unexpected"
    fi
    if test "$repair_required" = true; then inspect_state=repair-required; else inspect_state=complete; fi
  fi
  printf 'FORGE_WORKSPACE_RESULT\t%%s\t%%s\t%%s\t%%s\t%%s\t%%s\t%%s\t%%s\t%%s\t%%s\n' "$selected" "$device" "$model" "$serial" "$wwn" "$size" "$transport_identity" "$filesystem" "$planned_uuid" "$inspect_state"
  exit 0
fi

mounted_targets=$(findmnt -rn -S "$device" -o TARGET 2>/dev/null || true)
unexpected_targets=$(printf '%%s\n' "$mounted_targets" | awk -v expected="$mount_path" 'NF && $0 != expected {print}')
test -z "$unexpected_targets" || fail "workspace disk is mounted by an unexpected consumer: $unexpected_targets"

if test "$filesystem" = ext4; then fstab_pass=2; else fstab_pass=0; fi
fstab_line="UUID=$planned_uuid $mount_path $filesystem nodev,nosuid 0 $fstab_pass"
conflicts=$(awk -v uuid="UUID=$planned_uuid" -v target="$mount_path" -v expected="$fstab_line" '
  /^[[:space:]]*#/ || NF == 0 {next}
  ($1 == uuid || $2 == target) && $0 != expected {print}
' /etc/fstab)
test -z "$conflicts" || fail "conflicting workspace fstab entry: $conflicts"
count=$(grep -Fxc "$fstab_line" /etc/fstab || true)
test "$count" -le 1 || fail "duplicate workspace fstab entries"
if test "$count" = 0; then printf '%%s\n' "$fstab_line" >> /etc/fstab; sync -f /etc/fstab; fi
if test "$status" != complete; then write_receipt fstab; status=fstab; fi

install -d -o root -g root -m 0711 "$mount_path"
mounted_source=$(findmnt -n -o SOURCE --mountpoint "$mount_path" 2>/dev/null || true)
if test -n "$mounted_source"; then
  mounted_source=${mounted_source%%[*}
  test "$(readlink -f -- "$mounted_source")" = "$device" || fail "workspace mount is backed by a different device"
else
  mount "$mount_path"
fi
test "$(findmnt -n -o FSTYPE --mountpoint "$mount_path")" = "$filesystem" || fail "workspace mount filesystem type mismatch"
options=$(findmnt -n -o OPTIONS --mountpoint "$mount_path")
case ",$options," in *,nodev,*) ;; *) fail "workspace mount lacks nodev" ;; esac
case ",$options," in *,nosuid,*) ;; *) fail "workspace mount lacks nosuid" ;; esac
if test "$status" = complete; then
  test "$(stat -c '%%u:%%g:%%a' "$mount_path")" = 0:0:711 || fail "completed workspace mount ownership/mode drift"
else
  chown root:root "$mount_path"; chmod 0711 "$mount_path"
  test "$(stat -c '%%u:%%g:%%a' "$mount_path")" = 0:0:711 || fail "workspace mount ownership/mode mismatch"
  write_receipt mounted; status=mounted
fi

marker="$mount_path/$marker_name"
marker_content=$(printf 'contract=%%s\ninstall=%%s\ndevice=%%s\ntransport=%%s\nfilesystem_selection=%%s\nfilesystem=%%s\nuuid=%%s\nlabel=%%s\n' "$contract" "$install_name" "$selected" "$transport_identity" "$filesystem_selection" "$filesystem" "$planned_uuid" "$label")
if test "$status" = complete; then
  test -f "$marker" && test ! -L "$marker" || fail "completed workspace identity marker is missing or unsafe"
  test "$(stat -c '%%u:%%g:%%a' "$marker")" = 0:0:600 || fail "workspace identity marker ownership/mode drift"
  test "$(cat "$marker")" = "$marker_content" || fail "workspace identity marker content drift"
else
  if test -e "$marker"; then
    test -f "$marker" && test ! -L "$marker" || fail "workspace identity marker is unsafe"
    test "$(cat "$marker")" = "$marker_content" || fail "workspace identity marker conflicts with the transaction"
  else
    marker_tmp=$(mktemp "$mount_path/.iterabase-workspace-identity.XXXXXX")
    printf '%%s\n' "$marker_content" > "$marker_tmp"
    chown root:root "$marker_tmp"; chmod 0600 "$marker_tmp"; sync -f "$marker_tmp"
    mv -f "$marker_tmp" "$marker"; sync -f "$mount_path"
  fi
  write_receipt complete
  status=complete
fi

unexpected=$(findmnt -rn -S "$device" -o TARGET | awk -v expected="$mount_path" '$0 != expected {print}')
test -z "$unexpected" || fail "workspace disk has an unexpected active consumer: $unexpected"
printf 'FORGE_WORKSPACE_RESULT\t%%s\t%%s\t%%s\t%%s\t%%s\t%%s\t%%s\t%%s\t%%s\tcomplete\n' "$selected" "$device" "$model" "$serial" "$wwn" "$size" "$transport_identity" "$filesystem" "$planned_uuid"
`, shellQuote(spec.InstallName), shellQuote(spec.Device), shellQuote(spec.Filesystem), shellQuote(mode), shellQuote(workspaceContractVersion), shellQuote(provisioner.AgentPoolWorkspaceMount), shellQuote(workspaceFilesystemLabel), shellQuote(workspaceReceiptPath), shellQuote(workspaceMarkerName))
}

func agentPoolLocalPathSetupScript() string {
	return fmt.Sprintf(`#!/bin/sh
set -eu
mkdir -m 0777 -p "$VOL_DIR"
case "$VOL_DIR" in
  %s/*)
    test "${VOL_DIR%%/*}" != "$VOL_DIR"
    parent=${VOL_DIR%%/*}
    test "$parent" = %s
    chmod 0711 "$parent"
    ;;
  *) chmod 0701 "$VOL_DIR/.." ;;
esac
`, provisioner.AgentPoolWorkspaceMount, shellQuote(provisioner.AgentPoolWorkspaceMount))
}

// EnsureAgentPoolLocalPathStorage configures the bundled K3s local-path
// provisioner with exact per-class path maps while retaining the default class
// on K3s's normal root-filesystem path.
func (p *SSHProvisioner) EnsureAgentPoolLocalPathStorage(ctx context.Context) error {
	configJSON := fmt.Sprintf(`{"nodePathMap":[],"storageClassConfigs":{"local-path":{"nodePathMap":[{"node":"DEFAULT_PATH_FOR_NON_LISTED_NODES","paths":[%q]}]},%q:{"nodePathMap":[{"node":"DEFAULT_PATH_FOR_NON_LISTED_NODES","paths":[%q]}]}}}`,
		k3sDefaultLocalPath, provisioner.AgentPoolWorkspaceStorageClass, provisioner.AgentPoolWorkspaceMount)
	setupScript := agentPoolLocalPathSetupScript()
	current, err := p.run(ctx, `sudo bash -ceu '
attempt=0
while ! k3s kubectl get configmap local-path-config -n kube-system >/dev/null 2>&1; do
  attempt=$((attempt + 1))
  if test "$attempt" -ge 150; then
    printf "timed out waiting for the bundled local-path ConfigMap\n" >&2
    exit 1
  fi
  sleep 2
done
k3s kubectl get configmap local-path-config -n kube-system -o jsonpath="{.data.config\.json}"
'`)
	if err != nil {
		return fmt.Errorf("wait for/read bundled local-path configuration: %w", err)
	}
	currentSetup, err := p.run(ctx, `sudo k3s kubectl get configmap local-path-config -n kube-system -o jsonpath='{.data.setup}'`)
	if err != nil {
		return fmt.Errorf("read bundled local-path setup script: %w", err)
	}
	changed := strings.TrimSpace(current) != configJSON || strings.TrimSpace(currentSetup) != strings.TrimSpace(setupScript)
	if changed {
		patch, err := json.Marshal(map[string]any{"data": map[string]string{"config.json": configJSON, "setup": setupScript}})
		if err != nil {
			return err
		}
		if _, err := p.run(ctx, "sudo k3s kubectl patch configmap local-path-config -n kube-system --type=merge -p "+shellQuote(string(patch))); err != nil {
			return fmt.Errorf("configure isolated local-path class paths: %w", err)
		}
	}
	manifest := fmt.Sprintf(`apiVersion: storage.k8s.io/v1
kind: StorageClass
metadata:
  name: %s
  annotations:
    storageclass.kubernetes.io/is-default-class: "false"
provisioner: %s
reclaimPolicy: Delete
volumeBindingMode: WaitForFirstConsumer
allowVolumeExpansion: false
`, provisioner.AgentPoolWorkspaceStorageClass, provisioner.AgentPoolWorkspaceProvisioner)
	if _, err := p.runStdin(ctx, "sudo k3s kubectl apply -f -", manifest); err != nil {
		return fmt.Errorf("apply AgentPool local-path StorageClass: %w", err)
	}
	if changed {
		if _, err := p.run(ctx, "sudo k3s kubectl rollout restart deployment/local-path-provisioner -n kube-system && sudo k3s kubectl rollout status deployment/local-path-provisioner -n kube-system --timeout=5m"); err != nil {
			return fmt.Errorf("reload bundled local-path provisioner configuration: %w", err)
		}
	}
	verify := fmt.Sprintf(`sudo bash -ceu %s`, shellQuote(fmt.Sprintf(`
default=$(k3s kubectl get storageclass local-path -o jsonpath='{.provisioner}|{.volumeBindingMode}|{.reclaimPolicy}|{.metadata.annotations.storageclass\.kubernetes\.io/is-default-class}')
test "$default" = %s
agent=$(k3s kubectl get storageclass %s -o jsonpath='{.provisioner}|{.volumeBindingMode}|{.reclaimPolicy}|{.allowVolumeExpansion}|{.metadata.annotations.storageclass\.kubernetes\.io/is-default-class}')
test "$agent" = %s
test "$(k3s kubectl get configmap local-path-config -n kube-system -o jsonpath='{.data.config\.json}')" = %s
setup=$(k3s kubectl get configmap local-path-config -n kube-system -o jsonpath='{.data.setup}')
test "$setup" = %s
`, shellQuote(provisioner.AgentPoolWorkspaceProvisioner+"|WaitForFirstConsumer|Delete|true"), shellQuote(provisioner.AgentPoolWorkspaceStorageClass), shellQuote(provisioner.AgentPoolWorkspaceProvisioner+"|WaitForFirstConsumer|Delete|false|false"), shellQuote(configJSON), shellQuote(strings.TrimSpace(setupScript)))))
	if _, err := p.run(ctx, verify); err != nil {
		return fmt.Errorf("validate isolated local-path storage contract: %w", err)
	}
	return nil
}
