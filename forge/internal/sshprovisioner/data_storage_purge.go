package sshprovisioner

import (
	"context"
	"fmt"
	"sort"
	"strings"

	"github.com/nunocgoncalves/iterabase-mono/forge/internal/provisioner"
)

// PurgeDataStorage implements provisioner.DataStoragePurger. It removes only
// an empty receipt-matching iterabase-data VG and its exact PVs. Ordinary
// destroy never calls this method.
func (p *SSHProvisioner) PurgeDataStorage(ctx context.Context, spec provisioner.DataStorageSpec) error {
	if _, err := p.run(ctx, "sudo bash -ceu "+shellQuote(dataStoragePurgeScript(spec))); err != nil {
		return fmt.Errorf("purge data storage %v: %w", spec.Devices, err)
	}
	return nil
}

func dataStoragePurgeScript(spec provisioner.DataStorageSpec) string {
	devices := append([]string(nil), spec.Devices...)
	sort.Strings(devices)
	quoted := make([]string, len(devices))
	for i, device := range devices {
		quoted[i] = shellQuote(device)
	}
	return fmt.Sprintf(`
install_name=%s
contract=%s
receipt=%s
vg_name=%s
selected=(%s)
fail() { printf 'data-storage purge refusal: %%s\n' "$*" >&2; exit 42; }
need() { command -v "$1" >/dev/null 2>&1 || fail "required probe/tool $1 is unavailable"; }
for tool in readlink lsblk findmnt blkid wipefs awk grep stat base64 sync sort dirname tr; do need "$tool"; done
count=${#selected[@]}; test "$count" -gt 0 || fail "selected device set is empty"
resolved=(); model=(); serial=(); wwn=(); size=(); transport=(); planned_pv_uuid=()
sanitize() { printf '%%s' "$1" | tr '\t|\r\n' '    '; }
for ((i=0; i<count; i++)); do
  path=${selected[$i]}; case "$path" in /dev/disk/by-id/*) ;; *) fail "$path is not a stable by-id identity" ;; esac
  case "$path" in *-part[0-9]*) fail "$path is a partition identity" ;; esac
  test -L "$path" || fail "$path is missing or not a symlink"
  dev=$(readlink -f -- "$path"); test -b "$dev" || fail "$path does not resolve to a block device"
  test "$(lsblk -dnro TYPE -- "$dev")" = disk || fail "$path is not a whole disk"
  test "$(lsblk -dnro RM -- "$dev")" = 0 || fail "$path is removable"
  resolved[$i]=$dev; model[$i]=$(sanitize "$(lsblk -dnro MODEL -- "$dev")"); serial[$i]=$(sanitize "$(lsblk -dnro SERIAL -- "$dev")"); wwn[$i]=$(sanitize "$(lsblk -dnro WWN -- "$dev")"); size[$i]=$(lsblk -bdnro SIZE -- "$dev")
  transport[$i]=$(lsblk -dnro TRAN -- "$dev" | tr '[:upper:]' '[:lower:]' | tr '\t\r\n' '   ' | awk '{$1=$1;print}'); transport[$i]=${transport[$i]:-unknown}
  for ((j=0; j<i; j++)); do test "${resolved[$j]}" != "$dev" || fail "two selected identities resolve to $dev"; done
done

if test ! -e "$receipt"; then
  if command -v vgs >/dev/null 2>&1 && vgs "$vg_name" >/dev/null 2>&1; then fail "$vg_name exists without its Forge receipt"; fi
  for ((i=0; i<count; i++)); do
    set +e; signatures=$(wipefs -n --noheadings --output TYPE -- "${resolved[$i]}" 2>&1); rc=$?; set -e
    test "$rc" = 0 || fail "signature probe failed for ${selected[$i]}: $signatures"
    test -z "$(printf '%%s' "$signatures" | awk 'NF')" || fail "${selected[$i]} has signatures without a Forge receipt"
  done
  printf 'FORGE_DATA_STORAGE_PURGE_RESULT\talready-clean\t%%s\n' "$count"; exit 0
fi

for tool in pvs vgs lvs vgremove pvremove fuser; do need "$tool"; done
test -f "$receipt" && test ! -L "$receipt" || fail "receipt is not a regular file"
test "$(stat -c '%%u:%%g:%%a' "$receipt")" = 0:0:600 || fail "receipt ownership/mode drift"
receipt_value() { awk -F= -v wanted="$1" '$1 == wanted {sub(/^[^=]*=/, ""); print; found=1} END {if (!found) exit 3}' "$receipt"; }
decode_receipt() { receipt_value "$1" | base64 -d; }
test "$(receipt_value contract)" = "$contract" || fail "receipt contract mismatch"
test "$(decode_receipt install_b64)" = "$install_name" || fail "receipt install mismatch"
test "$(receipt_value device_count)" = "$count" || fail "configured device-set size differs from receipt"
test "$(receipt_value vg_name)" = "$vg_name" || fail "receipt VG name mismatch"
vg_uuid=$(receipt_value vg_uuid); status=$(receipt_value status); pv_done=$(receipt_value pv_done)
case "$status" in planned|pvs-created|vg-created|complete) ;; *) fail "receipt status is invalid" ;; esac
for ((i=0; i<count; i++)); do
  test "$(decode_receipt device_${i}_b64)" = "${selected[$i]}" || fail "configured device order/set differs from receipt"
  test "$(decode_receipt resolved_${i}_b64)" = "${resolved[$i]}" || fail "resolved device identity drift"
  test "$(decode_receipt model_${i}_b64)" = "${model[$i]}" || fail "model identity drift"
  test "$(decode_receipt serial_${i}_b64)" = "${serial[$i]}" || fail "serial identity drift"
  test "$(decode_receipt wwn_${i}_b64)" = "${wwn[$i]}" || fail "WWN identity drift"
  test "$(decode_receipt transport_${i}_b64)" = "${transport[$i]}" || fail "transport identity drift"
  test "$(receipt_value size_$i)" = "${size[$i]}" || fail "size identity drift"
  planned_pv_uuid[$i]=$(receipt_value pv_uuid_$i)
  for target in / /boot /boot/efi /var /var/lib/rancher/k3s /var/lib/kubelet; do
    source=$(findmnt -n -o SOURCE --target "$target" 2>/dev/null || true); source=${source%%[*}; test -n "$source" || continue
    source=$(readlink -f -- "$source" 2>/dev/null || true); test -b "$source" || continue
    if lsblk -snro PATH -- "$source" | grep -Fxq "${resolved[$i]}"; then fail "${selected[$i]} backs system path $target"; fi
  done
  while read -r source _; do
    test "$source" != Filename || continue; source=$(readlink -f -- "$source" 2>/dev/null || true); test -b "$source" || continue
    if lsblk -snro PATH -- "$source" | grep -Fxq "${resolved[$i]}"; then fail "${selected[$i]} backs active swap"; fi
  done < /proc/swaps
  direct_mounts=$(findmnt -rn -S "${resolved[$i]}" -o TARGET 2>/dev/null || true); test -z "$direct_mounts" || fail "${selected[$i]} is directly mounted at $direct_mounts"
  kernel=$(lsblk -dnro KNAME -- "${resolved[$i]}")
  test ! -d "/sys/class/block/$kernel/holders" || test -z "$(find "/sys/class/block/$kernel/holders" -mindepth 1 -maxdepth 1 -print -quit)" || fail "${selected[$i]} has active holders"
  set +e; consumers=$(fuser "${resolved[$i]}" 2>&1); consumer_rc=$?; set -e
  test "$consumer_rc" = 1 || { test "$consumer_rc" = 0 && fail "${selected[$i]} has raw consumers: $consumers"; fail "raw-consumer probe failed for ${selected[$i]}: $consumers"; }
done

if vgs "$vg_name" >/dev/null 2>&1; then
  observed_uuid=$(vgs --noheadings -o vg_uuid "$vg_name" | awk '{$1=$1;print}')
  test "$observed_uuid" = "$vg_uuid" || fail "$vg_name UUID differs from receipt"
  members=$(pvs --noheadings --select "vg_name=$vg_name" -o pv_name | awk '{$1=$1;if(NF)print}' | sort)
  expected=$(printf '%%s\n' "${resolved[@]}" | sort); test "$members" = "$expected" || fail "$vg_name membership differs from receipt"
  lv_count=$(lvs --noheadings --select "vg_name=$vg_name" -o lv_name | awk 'NF {n++} END {print n+0}')
  test "$lv_count" = 0 || fail "$vg_name still contains $lv_count logical volumes; delete claims and release consumers first"
  vgremove --yes "$vg_name"
else
  test "$status" = planned || test "$status" = pvs-created || fail "$vg_name disappeared after receipt stage $status"
fi

for ((i=0; i<count; i++)); do
  set +e; pv_line=$(pvs --noheadings --separator '|' -o pv_uuid,vg_name -- "${resolved[$i]}" 2>&1); pv_rc=$?; set -e
  if test "$pv_rc" = 0; then
    pv=$(printf '%%s' "$pv_line" | `+lvmReportPairParser+`)
    pv_uuid=${pv%%|*}; pv_vg=${pv#*|}
    test "$pv_uuid" = "${planned_pv_uuid[$i]}" && test -z "$pv_vg" || fail "${selected[$i]} PV identity/membership drift"
    pvremove --yes -- "${resolved[$i]}"
  elif test "$i" -lt "$pv_done"; then
    fail "receipt PV ${selected[$i]} disappeared before purge"
  fi
  sync
  set +e; remaining=$(wipefs -n --noheadings --output TYPE -- "${resolved[$i]}" 2>&1); rc=$?; set -e
  test "$rc" = 0 && test -z "$(printf '%%s' "$remaining" | awk 'NF')" || fail "signatures remain on ${selected[$i]} after purge: $remaining"
done
rm -f -- "$receipt"; sync -f "$(dirname "$receipt")"
printf 'FORGE_DATA_STORAGE_PURGE_RESULT\tpurged\t%%s\n' "$count"
`, shellQuote(spec.InstallName), shellQuote(dataStorageContractVersion), shellQuote(dataStorageReceiptPath), shellQuote(provisioner.DataVolumeGroupName), strings.Join(quoted, " "))
}
