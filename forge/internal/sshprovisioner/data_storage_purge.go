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
ownership_tag=$(receipt_value ownership_tag)
printf '%%s' "$ownership_tag" | grep -Eq '^iterabase[.]hor545[.][0-9a-f]{32}$' || fail "receipt ownership tag is invalid"
vg_uuid=$(receipt_value vg_uuid); status=$(receipt_value status); pv_done=$(receipt_value pv_done)
case "$status" in planned|pvs-created|vg-created|complete) ;; *) fail "receipt status is invalid" ;; esac
case "$pv_done" in ''|*[!0-9]*) fail "receipt pv_done is invalid" ;; esac
test "$pv_done" -le "$count" || fail "receipt pv_done exceeds device count"
case "$status" in
  planned)
    test "$pv_done" = 0 || fail "planned receipt has completed PV stages"
    test -z "$vg_uuid" || fail "receipt records a VG UUID before the VG-created stage"
    ;;
  pvs-created)
    test "$pv_done" -gt 0 || fail "pvs-created receipt has no completed PV"
    test -z "$vg_uuid" || fail "receipt records a VG UUID before the VG-created stage"
    ;;
  vg-created|complete)
    test "$pv_done" = "$count" || fail "VG receipt does not bind the complete PV set"
    case "$vg_uuid" in ??????-????-????-????-????-????-??????) ;; *) fail "receipt VG UUID is invalid" ;; esac
    ;;
esac
for ((i=0; i<count; i++)); do
  test "$(decode_receipt device_${i}_b64)" = "${selected[$i]}" || fail "configured device order/set differs from receipt"
  test "$(decode_receipt resolved_${i}_b64)" = "${resolved[$i]}" || fail "resolved device identity drift"
  test "$(decode_receipt model_${i}_b64)" = "${model[$i]}" || fail "model identity drift"
  test "$(decode_receipt serial_${i}_b64)" = "${serial[$i]}" || fail "serial identity drift"
  test "$(decode_receipt wwn_${i}_b64)" = "${wwn[$i]}" || fail "WWN identity drift"
  test "$(decode_receipt transport_${i}_b64)" = "${transport[$i]}" || fail "transport identity drift"
  test "$(receipt_value size_$i)" = "${size[$i]}" || fail "size identity drift"
  planned_pv_uuid[$i]=$(receipt_value pv_uuid_$i)
  case "${planned_pv_uuid[$i]}" in ??????-????-????-????-????-????-??????) ;; *) fail "receipt PV UUID is invalid" ;; esac
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
  holders=""
  if test -d "/sys/class/block/$kernel/holders"; then
    holders=$(find "/sys/class/block/$kernel/holders" -mindepth 1 -maxdepth 1 -printf '%%f\n' | sort | awk 'BEGIN {separator=""} {printf "%%s%%s", separator, $0; separator=","}')
  fi
  test -z "$holders" || fail "${selected[$i]} has active holders: $holders"
  set +e; consumers=$(fuser "${resolved[$i]}" 2>&1); consumer_rc=$?; set -e
  test "$consumer_rc" = 1 || { test "$consumer_rc" = 0 && fail "${selected[$i]} has raw consumers: $consumers"; fail "raw-consumer probe failed for ${selected[$i]}: $consumers"; }
done

tag_owners=$(vgs --noheadings --separator '|' -o vg_name,vg_uuid,vg_tags | awk -F'|' -v wanted="$ownership_tag" '
  {for(i=1;i<=3;i++) gsub(/^[[:space:]]+|[[:space:]]+$/, "", $i); n=split($3, tags, ","); for(i=1;i<=n;i++) if(tags[i] == wanted) {print $1 "|" $2; break}}
' | sort)
vg_record_count=$(vgs --noheadings --select "vg_name=$vg_name" -o vg_uuid 2>/dev/null | awk 'NF {n++} END {print n+0}')
test "$vg_record_count" -le 1 || fail "multiple VGs named $vg_name are ambiguous"
if test "$vg_record_count" = 1; then
  observed=$(vgs --noheadings --separator '|' -o vg_uuid,vg_tags,pv_count "$vg_name") || fail "cannot inspect $vg_name"
  test "$(printf '%%s\n' "$observed" | awk 'NF {n++} END {print n+0}')" = 1 || fail "$vg_name identity is ambiguous"
  IFS='|' read -r observed_uuid observed_tags observed_pv_count <<<"$observed"
  observed_uuid=$(printf '%%s' "$observed_uuid" | awk '{$1=$1;print}')
  case "$observed_uuid" in ??????-????-????-????-????-????-??????) ;; *) fail "$vg_name reported an invalid VG UUID" ;; esac
  observed_tags=$(printf '%%s' "$observed_tags" | awk '{$1=$1;print}')
  observed_pv_count=$(printf '%%s' "$observed_pv_count" | awk '{$1=$1;print}')
  test "$observed_tags" = "$ownership_tag" || fail "$vg_name ownership tag differs from receipt"
  test "$tag_owners" = "$vg_name|$observed_uuid" || fail "$vg_name ownership tag is not globally unique"
  test "$observed_pv_count" = "$count" || fail "$vg_name PV membership count differs from receipt"
  if test -n "$vg_uuid"; then
    test "$observed_uuid" = "$vg_uuid" || fail "$vg_name UUID differs from receipt"
  else
    test "$status" = pvs-created || fail "$vg_name exists before a valid receipt stage"
  fi
  members=$(pvs --noheadings --select "vg_name=$vg_name" -o pv_name | awk '{$1=$1;if(NF)print}' | sort)
  expected=$(printf '%%s\n' "${resolved[@]}" | sort); test "$members" = "$expected" || fail "$vg_name membership differs from receipt"
  for ((i=0; i<count; i++)); do
    set +e; pv_line=$(pvs --noheadings --separator '|' -o pv_uuid,vg_name -- "${resolved[$i]}" 2>&1); pv_rc=$?; set -e
    test "$pv_rc" = 0 || fail "receipt PV ${selected[$i]} disappeared before VG purge"
    pv=$(printf '%%s' "$pv_line" | `+lvmReportPairParser+`); pv_uuid=${pv%%|*}; pv_vg=${pv#*|}
    test "$pv_uuid" = "${planned_pv_uuid[$i]}" && test "$pv_vg" = "$vg_name" || fail "${selected[$i]} PV identity/membership drift"
  done
  lv_count=$(lvs --noheadings --select "vg_name=$vg_name" -o lv_name | awk 'NF {n++} END {print n+0}')
  test "$lv_count" = 0 || fail "$vg_name still contains $lv_count logical volumes; delete claims and release consumers first"
  vgremove --yes "$vg_name"
else
  test -z "$tag_owners" || fail "receipt ownership tag belongs to a foreign VG"
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
