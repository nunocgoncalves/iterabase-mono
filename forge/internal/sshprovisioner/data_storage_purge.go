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
for tool in readlink lsblk findmnt blkid wipefs awk grep stat base64 sync sort dirname tr install mktemp mv chown chmod find seq sleep; do need "$tool"; done
%s
%s
%s
%s
if test "$receipt_present" = 0; then
  if command -v vgs >/dev/null 2>&1 && vgs "$vg_name" >/dev/null 2>&1; then fail "$vg_name exists without its Forge receipt"; fi
  for ((i=0; i<count; i++)); do
    set +e; signatures=$(wipefs -n --noheadings --output TYPE -- "${resolved[$i]}" 2>&1); rc=$?; set -e
    test "$rc" = 0 || fail "signature probe failed for ${selected[$i]}: $signatures"
    test -z "$(printf '%%s' "$signatures" | awk 'NF')" || fail "${selected[$i]} has signatures without a Forge receipt"
  done
  printf 'FORGE_DATA_STORAGE_PURGE_RESULT\talready-clean\t%%s\n' "$count"; exit 0
fi

for tool in pvs vgs lvs vgremove pvremove fuser; do need "$tool"; done
for ((i=0; i<count; i++)); do
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

case "$status" in
  planned|pvs-created|vg-created|complete) normal_status=true ;;
  purge-vg-pending|purge-vg-removed|purge-pv-pending|purge-pvs-removed|purge-receipt-pending) normal_status=false ;;
  *) fail "unsupported purge receipt stage $status" ;;
esac

if test "$vg_record_count" = 1; then
  case "$status" in purge-vg-removed|purge-pv-pending|purge-pvs-removed|purge-receipt-pending) fail "$vg_name reappeared after its durable purge stage" ;; esac
  observed=$(vgs --noheadings --separator '|' -o vg_uuid,vg_tags,pv_count "$vg_name") || fail "cannot inspect $vg_name"
  test "$(printf '%%s\n' "$observed" | awk 'NF {n++} END {print n+0}')" = 1 || fail "$vg_name identity is ambiguous"
  IFS='|' read -r observed_uuid observed_tags observed_pv_count <<<"$observed"
  observed_uuid=$(printf '%%s' "$observed_uuid" | awk '{$1=$1;print}')
  case "$observed_uuid" in ??????-????-????-????-????-????-??????) ;; *) fail "$vg_name reported an invalid VG UUID" ;; esac
  observed_tags=$(printf '%%s' "$observed_tags" | awk '{$1=$1;print}')
  observed_pv_count=$(printf '%%s' "$observed_pv_count" | awk '{$1=$1;print}')
  test "$observed_tags" = "$ownership_tag" || fail "$vg_name ownership tag differs from receipt"
  test "$tag_owners" = "$vg_name|$observed_uuid" || fail "$vg_name ownership tag is not globally unique"
  test "$observed_pv_count" = "$count" && test "$pv_done" = "$count" || fail "$vg_name PV membership count differs from receipt"
  if test -n "$receipt_vg_uuid"; then test "$observed_uuid" = "$receipt_vg_uuid" || fail "$vg_name UUID differs from receipt"; fi
  members=$(pvs --noheadings --select "vg_name=$vg_name" -o pv_name | awk '{$1=$1;if(NF)print}' | sort)
  expected=$(printf '%%s\n' "${resolved[@]}" | sort); test "$members" = "$expected" || fail "$vg_name membership differs from receipt"
  for ((i=0; i<count; i++)); do
    set +e; pv_line=$(pvs --noheadings --separator '|' -o pv_uuid,vg_name -- "${resolved[$i]}" 2>/dev/null); pv_rc=$?; set -e
    test "$pv_rc" = 0 || fail "receipt PV ${selected[$i]} disappeared before VG purge"
    pv=$(printf '%%s' "$pv_line" | `+lvmReportPairParser+`); pv_uuid=${pv%%|*}; pv_vg=${pv#*|}
    test "$pv_uuid" = "${planned_pv_uuid[$i]}" && test "$pv_vg" = "$vg_name" || fail "${selected[$i]} PV identity/membership drift"
  done
  lv_count=$(lvs --noheadings --select "vg_name=$vg_name" -o lv_name | awk 'NF {n++} END {print n+0}')
  test "$lv_count" = 0 || fail "$vg_name still contains $lv_count logical volumes; delete claims and release consumers, including snapshots, first"
  if test "$normal_status" = true; then
    write_receipt purge-vg-pending "$pv_done" 0; status=purge-vg-pending; purge_done=0; storage_stage_barrier purge-vg-pending
  fi
  test "$status" = purge-vg-pending || fail "$vg_name removal lacks its durable pending stage"
  vgremove --yes "$vg_name"
else
  test -z "$tag_owners" || fail "receipt ownership tag belongs to a foreign VG"
  if test "$normal_status" = true; then
    case "$status" in planned|pvs-created) ;; *) fail "$vg_name disappeared before a durable purge intent" ;; esac
    write_receipt purge-vg-pending "$pv_done" 0; status=purge-vg-pending; purge_done=0; storage_stage_barrier purge-vg-pending
  fi
fi
if test "$status" = purge-vg-pending; then
  write_receipt purge-vg-removed "$pv_done" 0; status=purge-vg-removed; purge_done=0; storage_stage_barrier purge-vg-removed
fi

for ((i=0; i<count; i++)); do
  set +e; pv_line=$(pvs --noheadings --separator '|' -o pv_uuid,vg_name -- "${resolved[$i]}" 2>/dev/null); pv_rc=$?; set -e
  if test "$i" -ge "$pv_done"; then
    test "$pv_rc" != 0 || fail "${selected[$i]} gained an unrecorded PV during purge"
    set +e; remaining=$(wipefs -n --noheadings --output TYPE -- "${resolved[$i]}" 2>&1); rc=$?; set -e
    test "$rc" = 0 && test -z "$(printf '%%s' "$remaining" | awk 'NF')" || fail "unrecorded signatures remain on ${selected[$i]}: $remaining"
    continue
  fi
  if test "$i" -lt "$purge_done"; then
    test "$pv_rc" != 0 || fail "receipt PV ${selected[$i]} reappeared after its durable removal stage"
    continue
  fi
  test "$i" = "$purge_done" || fail "data-storage PV purge progress is non-contiguous"
  if test "$pv_rc" = 0; then
    pv=$(printf '%%s' "$pv_line" | `+lvmReportPairParser+`)
    pv_uuid=${pv%%|*}; pv_vg=${pv#*|}
    test "$pv_uuid" = "${planned_pv_uuid[$i]}" && test -z "$pv_vg" || fail "${selected[$i]} PV identity/membership drift"
  fi
  if test "$status" != purge-pv-pending; then
    write_receipt purge-pv-pending "$pv_done" "$i"; status=purge-pv-pending; storage_stage_barrier "purge-pv-$i-pending"
  fi
  if test "$pv_rc" = 0; then pvremove --yes -- "${resolved[$i]}"; fi
  sync
  set +e; remaining=$(wipefs -n --noheadings --output TYPE -- "${resolved[$i]}" 2>&1); rc=$?; set -e
  test "$rc" = 0 && test -z "$(printf '%%s' "$remaining" | awk 'NF')" || fail "signatures remain on ${selected[$i]} after purge: $remaining"
  purge_done=$((i + 1)); write_receipt purge-pvs-removed "$pv_done" "$purge_done"; status=purge-pvs-removed; storage_stage_barrier "purge-pv-$i-removed"
done

test "$purge_done" = "$pv_done" || fail "not every receipt PV reached its durable removal stage"
if test "$status" != purge-receipt-pending; then
  write_receipt purge-receipt-pending "$pv_done" "$purge_done"; status=purge-receipt-pending; storage_stage_barrier purge-receipt-pending
fi
rm -f -- "$receipt"; sync -f "$(dirname "$receipt")"; storage_stage_barrier receipt-removed
printf 'FORGE_DATA_STORAGE_PURGE_RESULT\tpurged\t%%s\n' "$count"
`, shellQuote(spec.InstallName), shellQuote(dataStorageContractVersion), shellQuote(dataStorageReceiptPath), shellQuote(provisioner.DataVolumeGroupName), strings.Join(quoted, " "), dataStorageDeviceResolutionPrelude(), dataStorageReceiptWriterPrelude(), dataStorageStageBarrierPrelude(), dataStorageReceiptPrelude())
}
