package sshprovisioner

import (
	"context"
	"fmt"

	"github.com/nunocgoncalves/iterabase-mono/forge/internal/provisioner"
)

// PurgeAgentPoolWorkspace implements provisioner.WorkspacePurger. The remote
// script fails closed unless the configured stable device, hardware identity,
// Forge receipt, filesystem, mount, and fstab entry all agree. It never selects
// or discovers a replacement device.
func (p *SSHProvisioner) PurgeAgentPoolWorkspace(ctx context.Context, spec provisioner.AgentPoolWorkspaceSpec) error {
	script := workspacePurgeScript(spec)
	if _, err := p.run(ctx, "sudo bash -ceu "+shellQuote(script)); err != nil {
		return fmt.Errorf("purge AgentPool workspace %s: %w", spec.Device, err)
	}
	return nil
}

func workspacePurgeScript(spec provisioner.AgentPoolWorkspaceSpec) string {
	return fmt.Sprintf(`
install_name=%s
selected=%s
contract=%s
mount_path=%s
label=%s
receipt=%s
marker_name=%s

fail() { printf 'workspace purge refusal: %%s\n' "$*" >&2; exit 42; }
need() { command -v "$1" >/dev/null 2>&1 || fail "required probe/tool $1 is unavailable"; }
for tool in readlink lsblk findmnt blkid wipefs awk grep stat base64 sync umount fuser find cat dirname mktemp mv chown chmod tr rmdir; do need "$tool"; done
case "$selected" in /dev/disk/by-id/*) ;; *) fail "selected device is not a stable /dev/disk/by-id identity" ;; esac
case "$selected" in *-part[0-9]*) fail "selected device is a partition identity" ;; esac
test -L "$selected" || fail "selected stable identity is missing or not a symlink"
device=$(readlink -f -- "$selected")
test -b "$device" || fail "selected identity does not resolve to a block device"
kname=$(lsblk -dnro KNAME -- "$device")
test -n "$kname" || fail "cannot determine selected kernel device identity"
case "$kname" in loop*|dm-*|md*|zd*|nbd*) fail "loop/mapper/RAID/network block devices are unsupported" ;; esac
test "$(lsblk -dnro TYPE -- "$device")" = disk || fail "selected identity is not a whole disk"
test "$(lsblk -dnro RM -- "$device")" = 0 || fail "selected disk is removable"
test "$(lsblk -nrpo PATH -- "$device" | awk 'NF {n++} END {print n+0}')" = 1 || fail "selected disk has partitions or child devices"
test -z "$(lsblk -dnro PTTYPE -- "$device")" || fail "selected disk has a partition table"
test ! -d "/sys/class/block/$kname/holders" || test -z "$(find "/sys/class/block/$kname/holders" -mindepth 1 -maxdepth 1 -print -quit)" || fail "selected disk has active holders"
for target in / /boot /boot/efi /var /var/lib/rancher/k3s /var/lib/kubelet; do
  source=$(findmnt -n -o SOURCE --target "$target" 2>/dev/null || true)
  source=${source%%%%[*}
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

mounts=$(findmnt -rn -S "$device" -o TARGET 2>/dev/null || true)
unexpected=$(printf '%%s\n' "$mounts" | awk -v expected="$mount_path" 'NF && $0 != expected {print}')
test -z "$unexpected" || fail "selected disk is mounted by an unexpected consumer: $unexpected"

# A second purge is an idempotent success only when every Forge-owned surface
# is already absent and the selected disk has no recognized signature.
if test ! -e "$receipt"; then
  test -z "$mounts" || fail "workspace mount exists without its Forge receipt"
  test "$(awk -v target="$mount_path" '!/^[[:space:]]*#/ && NF && $2 == target {n++} END {print n+0}' /etc/fstab)" = 0 || fail "workspace fstab entry exists without its Forge receipt"
  set +e
  signatures=$(wipefs -n --noheadings --output TYPE -- "$device" 2>&1)
  signature_rc=$?
  set -e
  test "$signature_rc" = 0 || fail "required wipefs signature probe failed: $signatures"
  test -z "$(printf '%%s' "$signatures" | awk 'NF')" || fail "selected disk has a signature but no Forge receipt"
  if test -d "$mount_path"; then rmdir "$mount_path" 2>/dev/null || fail "already-clean workspace mount path is not empty"; fi
  printf 'FORGE_WORKSPACE_PURGE_RESULT\t%%s\talready-clean\n' "$selected"
  exit 0
fi

test -f "$receipt" && test ! -L "$receipt" || fail "workspace receipt is not a regular file"
test "$(stat -c '%%u:%%g:%%a' "$receipt")" = 0:0:600 || fail "workspace receipt ownership/mode drift"
receipt_value() { awk -F= -v wanted="$1" '$1 == wanted {sub(/^[^=]*=/, ""); print; found=1} END {if (!found) exit 3}' "$receipt"; }
decode_receipt() { receipt_value "$1" | base64 -d; }
test "$(receipt_value contract)" = "$contract" || fail "workspace receipt contract mismatch"
status=$(receipt_value status)
case "$status" in planned|formatted|fstab|mounted|complete) ;; *) fail "workspace receipt status is invalid" ;; esac
test "$(decode_receipt install_b64)" = "$install_name" || fail "workspace receipt install mismatch"
test "$(decode_receipt device_b64)" = "$selected" || fail "workspace config device differs from the recorded transaction"
model=$(lsblk -dnro MODEL -- "$device" | tr '\t\r\n' '   ' | awk '{$1=$1; print}')
serial=$(lsblk -dnro SERIAL -- "$device" | tr '\t\r\n' '   ' | awk '{$1=$1; print}')
wwn=$(lsblk -dnro WWN -- "$device" | tr '\t\r\n' '   ' | awk '{$1=$1; print}')
transport=$(lsblk -dnro TRAN -- "$device" | tr '[:upper:]' '[:lower:]' | tr '\t\r\n' '   ' | awk '{$1=$1; print}')
transport=${transport:-unknown}
size=$(lsblk -bdnro SIZE -- "$device")
test "$(decode_receipt model_b64)" = "$model" || fail "workspace disk model identity mismatch"
test "$(decode_receipt serial_b64)" = "$serial" || fail "workspace disk serial identity mismatch"
test "$(decode_receipt wwn_b64)" = "$wwn" || fail "workspace disk WWN identity mismatch"
test "$(decode_receipt transport_b64)" = "$transport" || fail "workspace disk transport identity mismatch"
test "$(receipt_value size)" = "$size" || fail "workspace disk size identity mismatch"
test "$(decode_receipt mount_b64)" = "$mount_path" || fail "workspace mount identity mismatch"
filesystem=$(receipt_value filesystem)
uuid=$(receipt_value uuid)
test "$(receipt_value label)" = "$label" || fail "workspace filesystem label mismatch"
case "$uuid" in ????????-????-????-????-????????????) ;; *) fail "workspace receipt UUID is invalid" ;; esac

set +e
actual_type=$(blkid -p -s TYPE -o value -- "$device" 2>&1)
actual_rc=$?
set -e
if test "$actual_rc" = 0; then
  test "$actual_type" = "$filesystem" || fail "workspace filesystem type drift"
  test "$(blkid -p -s UUID -o value -- "$device")" = "$uuid" || fail "workspace filesystem UUID drift"
  test "$(blkid -p -s LABEL -o value -- "$device")" = "$label" || fail "workspace filesystem label drift"
elif test "$actual_rc" = 2; then
  test "$status" = planned || fail "workspace filesystem disappeared before purge"
else
  fail "workspace filesystem signature is ambiguous"
fi

if test -n "$mounts"; then
  mounted_source=$(findmnt -n -o SOURCE --mountpoint "$mount_path")
  mounted_source=${mounted_source%%%%[*}
  test "$(readlink -f -- "$mounted_source")" = "$device" || fail "workspace mount source drift"
  if test "$status" = complete; then
    marker="$mount_path/$marker_name"
    test -f "$marker" && test ! -L "$marker" || fail "workspace identity marker is missing or unsafe"
    test "$(stat -c '%%u:%%g:%%a' "$marker")" = 0:0:600 || fail "workspace marker ownership/mode drift"
  fi
  set +e
  consumers=$(fuser -m "$mount_path" 2>&1)
  consumers_rc=$?
  set -e
  test "$consumers_rc" = 1 || { test "$consumers_rc" = 0 && fail "workspace filesystem is in use: $consumers"; fail "workspace in-use probe failed: $consumers"; }
fi

if test "$filesystem" = ext4; then fstab_pass=2; else fstab_pass=0; fi
fstab_line="UUID=$uuid $mount_path $filesystem nodev,nosuid 0 $fstab_pass"
conflicts=$(awk -v uuid="UUID=$uuid" -v target="$mount_path" -v expected="$fstab_line" '
  /^[[:space:]]*#/ || NF == 0 {next}
  ($1 == uuid || $2 == target) && $0 != expected {print}
' /etc/fstab)
test -z "$conflicts" || fail "workspace fstab identity drift: $conflicts"
count=$(grep -Fxc "$fstab_line" /etc/fstab || true)
test "$count" -le 1 || fail "workspace fstab identity is duplicated"

if test -n "$mounts"; then umount "$mount_path"; fi
set +e
raw_consumers=$(fuser "$device" 2>&1)
raw_rc=$?
set -e
test "$raw_rc" = 1 || { test "$raw_rc" = 0 && fail "workspace block device is in use: $raw_consumers"; fail "workspace raw-device probe failed: $raw_consumers"; }

wipefs --all --force -- "$device"
sync
set +e
remaining=$(wipefs -n --noheadings --output TYPE -- "$device" 2>&1)
remaining_rc=$?
set -e
test "$remaining_rc" = 0 && test -z "$(printf '%%s' "$remaining" | awk 'NF')" || fail "workspace signatures remain after purge: $remaining"
if test "$count" = 1; then
  tmp=$(mktemp /etc/.fstab.forge.XXXXXX)
  awk -v expected="$fstab_line" '$0 != expected {print}' /etc/fstab > "$tmp"
  chown --reference=/etc/fstab "$tmp"; chmod --reference=/etc/fstab "$tmp"
  mv -f "$tmp" /etc/fstab; sync -f /etc/fstab
fi
rm -f -- "$receipt"; sync -f "$(dirname "$receipt")"
if test -d "$mount_path"; then rmdir "$mount_path" 2>/dev/null || fail "workspace mount path is not empty after unmount"; fi
printf 'FORGE_WORKSPACE_PURGE_RESULT\t%%s\tpurged\n' "$selected"
`, shellQuote(spec.InstallName), shellQuote(spec.Device), shellQuote(workspaceContractVersion), shellQuote(provisioner.AgentPoolWorkspaceMount), shellQuote(workspaceFilesystemLabel), shellQuote(workspaceReceiptPath), shellQuote(workspaceMarkerName))
}
