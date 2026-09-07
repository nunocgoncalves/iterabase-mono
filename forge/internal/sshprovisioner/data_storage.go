package sshprovisioner

import (
	"context"
	"fmt"
	"sort"
	"strconv"
	"strings"
	"time"

	"github.com/nunocgoncalves/iterabase-mono/forge/internal/provisioner"
)

const (
	dataStorageContractVersion = "HOR-545/v1"
	dataStorageReceiptPath     = "/var/lib/iterabase/data-storage.receipt"
	k3sKubeletDirectory        = "/var/lib/rancher/k3s/agent/kubelet"
	lvmReportPairParser        = `awk -F'|' '{for(i=1;i<=2;i++){gsub(/^[[:space:]]+|[[:space:]]+$/, "", $i)}; print $1 "|" $2}'`
)

// ListDataStorageDevices lists blank stable whole-disk candidates without
// reading arbitrary device bytes or mutating the host.
func (p *SSHProvisioner) ListDataStorageDevices(ctx context.Context) ([]provisioner.DataStorageDevice, error) {
	cmd := `sudo bash -ceu '
for tool in readlink lsblk wipefs sort tr awk; do command -v "$tool" >/dev/null; done
for selected in /dev/disk/by-id/*; do
  test -L "$selected" || continue
  case "$selected" in *-part[0-9]*) continue ;; esac
  device=$(readlink -f -- "$selected")
  test -b "$device" || continue
  test "$(lsblk -dnro TYPE -- "$device")" = disk || continue
  test "$(lsblk -dnro RM -- "$device")" = 0 || continue
  test "$(lsblk -nrpo PATH -- "$device" | awk "NF {n++} END {print n+0}")" = 1 || continue
  test -z "$(lsblk -dnro PTTYPE -- "$device")" || continue
  signatures=$(wipefs -n --noheadings --output TYPE -- "$device") || exit 42
  test -z "$(printf "%s" "$signatures" | awk "NF")" || continue
  model=$(lsblk -dnro MODEL -- "$device" | tr "\t\r\n" "   ")
  serial=$(lsblk -dnro SERIAL -- "$device" | tr "\t\r\n" "   ")
  size=$(lsblk -bdnro SIZE -- "$device")
  transport=$(lsblk -dnro TRAN -- "$device" | tr "[:upper:]" "[:lower:]" | tr "\t\r\n" "   ")
  printf "FORGE_DATA_STORAGE_DEVICE\t%s\t%s\t%s\t%s\t%s\n" "$selected" "$model" "$serial" "$size" "$transport"
done | sort -u
'`
	out, err := p.run(ctx, cmd)
	if err != nil {
		return nil, fmt.Errorf("list stable blank data-storage devices: %w", err)
	}
	var devices []provisioner.DataStorageDevice
	for _, line := range strings.Split(out, "\n") {
		parts := strings.Split(line, "\t")
		if len(parts) != 6 || parts[0] != "FORGE_DATA_STORAGE_DEVICE" {
			continue
		}
		size, parseErr := strconv.ParseUint(strings.TrimSpace(parts[4]), 10, 64)
		if parseErr != nil {
			return nil, fmt.Errorf("parse data-storage device size from %q: %w", line, parseErr)
		}
		devices = append(devices, provisioner.DataStorageDevice{
			Path: parts[1], Model: strings.TrimSpace(parts[2]), Serial: strings.TrimSpace(parts[3]),
			SizeBytes: size, Transport: strings.TrimSpace(parts[5]),
		})
	}
	return devices, nil
}

// EnsureDataStorageTools installs/verifies the host-owned LVM/XFS tools and
// loads and persists dm-snapshot without touching selected disks.
func (p *SSHProvisioner) EnsureDataStorageTools(ctx context.Context) error {
	const verify = "command -v pvcreate >/dev/null && command -v pvremove >/dev/null && command -v vgcreate >/dev/null && command -v vgremove >/dev/null && command -v pvs >/dev/null && command -v vgs >/dev/null && command -v lvs >/dev/null && command -v mkfs.xfs >/dev/null && command -v xfs_info >/dev/null && command -v fuser >/dev/null"
	if _, err := p.run(ctx, verify); err != nil {
		cmd := "sudo apt-get update && sudo apt-get install -y lvm2 xfsprogs psmisc"
		for attempt := 0; ; attempt++ {
			out, installErr := p.run(ctx, cmd)
			if installErr == nil {
				break
			}
			if (!isAptLockHeld(installErr.Error()) && !isAptLockHeld(out)) || attempt >= 20 {
				return fmt.Errorf("install required data-storage tooling (lvm2 xfsprogs psmisc): %w", installErr)
			}
			select {
			case <-ctx.Done():
				return ctx.Err()
			case <-time.After(aptLockRetryInterval):
			}
		}
	}
	if _, err := p.run(ctx, verify); err != nil {
		return fmt.Errorf("verify required data-storage tooling: %w", err)
	}
	moduleScript := `sudo bash -ceu '
modprobe dm-snapshot
install -d -o root -g root -m 0755 /etc/modules-load.d
tmp=$(mktemp /etc/modules-load.d/.iterabase-data.conf.XXXXXX)
printf "dm-snapshot\n" > "$tmp"
chown root:root "$tmp"; chmod 0644 "$tmp"; sync -f "$tmp"
mv -f "$tmp" /etc/modules-load.d/iterabase-data.conf
sync -f /etc/modules-load.d
test "$(cat /etc/modules-load.d/iterabase-data.conf)" = dm-snapshot
grep -q "^dm_snapshot " /proc/modules
'`
	if _, err := p.run(ctx, moduleScript); err != nil {
		return fmt.Errorf("load and persist dm-snapshot: %w", err)
	}
	return nil
}

func (p *SSHProvisioner) InspectDataStorage(ctx context.Context, spec provisioner.DataStorageSpec) (*provisioner.DataStorageState, error) {
	return p.runDataStorage(ctx, spec, false)
}

func (p *SSHProvisioner) ReconcileDataStorage(ctx context.Context, spec provisioner.DataStorageSpec) (*provisioner.DataStorageState, error) {
	return p.runDataStorage(ctx, spec, true)
}

func (p *SSHProvisioner) runDataStorage(ctx context.Context, spec provisioner.DataStorageSpec, reconcile bool) (*provisioner.DataStorageState, error) {
	mode := "inspect"
	if reconcile {
		mode = "reconcile"
	}
	out, err := p.run(ctx, "sudo bash -ceu "+shellQuote(dataStorageReconcileScript(spec, mode)))
	if err != nil {
		return nil, fmt.Errorf("%s data storage %v: %w", mode, spec.Devices, err)
	}
	state, err := parseDataStorageResult(out)
	if err != nil {
		return nil, err
	}
	return state, nil
}

func parseDataStorageResult(out string) (*provisioner.DataStorageState, error) {
	state := &provisioner.DataStorageState{}
	declaredDevices := -1
	for _, line := range strings.Split(out, "\n") {
		parts := strings.Split(line, "\t")
		switch {
		case len(parts) == 9 && parts[0] == "FORGE_DATA_STORAGE_DEVICE_RESULT":
			size, err := strconv.ParseUint(parts[7], 10, 64)
			if err != nil {
				return nil, fmt.Errorf("parse data-storage device result size: %w", err)
			}
			state.Devices = append(state.Devices, provisioner.DataStorageDevice{
				Path: parts[1], Resolved: parts[2], Model: parts[3], Serial: parts[4], WWN: parts[5],
				PVUUID: parts[6], SizeBytes: size, Transport: parts[8],
			})
		case len(parts) == 7 && parts[0] == "FORGE_DATA_STORAGE_RESULT":
			size, err := strconv.ParseUint(parts[4], 10, 64)
			if err != nil {
				return nil, fmt.Errorf("parse data-storage VG size: %w", err)
			}
			free, err := strconv.ParseUint(parts[5], 10, 64)
			if err != nil {
				return nil, fmt.Errorf("parse data-storage VG free size: %w", err)
			}
			declaredDevices, err = strconv.Atoi(parts[6])
			if err != nil {
				return nil, fmt.Errorf("parse data-storage device count: %w", err)
			}
			state.State, state.VGName, state.VGUUID, state.SizeBytes, state.FreeBytes = parts[1], parts[2], parts[3], size, free
		}
	}
	if declaredDevices < 0 {
		return nil, fmt.Errorf("data-storage reconciliation returned no bounded result")
	}
	if declaredDevices != len(state.Devices) {
		return nil, fmt.Errorf("data-storage reconciliation returned %d device records, expected %d", len(state.Devices), declaredDevices)
	}
	return state, nil
}

func dataStorageReconcileScript(spec provisioner.DataStorageSpec, mode string) string {
	devices := append([]string(nil), spec.Devices...)
	sort.Strings(devices)
	quoted := make([]string, len(devices))
	for i, device := range devices {
		quoted[i] = shellQuote(device)
	}
	return fmt.Sprintf(`
install_name=%s
mode=%s
contract=%s
receipt=%s
vg_name=%s
selected=(%s)

fail() { printf 'data-storage refusal: %%s\n' "$*" >&2; exit 42; }
need() { command -v "$1" >/dev/null 2>&1 || fail "required probe/tool $1 is unavailable"; }
for tool in readlink lsblk findmnt blkid wipefs awk grep stat base64 sync find tr head sort dirname mktemp mv chown chmod cat; do need "$tool"; done
count=${#selected[@]}
test "$count" -gt 0 || fail "selected device set is empty"

sanitize() { printf '%%s' "$1" | tr '\t|\r\n' '    '; }
resolved=(); model=(); serial=(); wwn=(); size=(); transport=(); kname=(); planned_pv_uuid=()
for ((i=0; i<count; i++)); do
  path=${selected[$i]}
  case "$path" in /dev/disk/by-id/*) ;; *) fail "selected device $path is not a stable /dev/disk/by-id identity" ;; esac
  case "$path" in *-part[0-9]*) fail "selected device $path is a partition identity" ;; esac
  test -L "$path" || fail "selected stable identity $path is missing or not a symlink"
  dev=$(readlink -f -- "$path")
  test -b "$dev" || fail "selected identity $path does not resolve to a block device"
  resolved[$i]=$dev
  kname[$i]=$(lsblk -dnro KNAME -- "$dev")
  test -n "${kname[$i]}" || fail "cannot determine kernel identity for $path"
  case "${kname[$i]}" in loop*|dm-*|md*|zd*|nbd*) fail "selected device $path is an unsupported logical/network device" ;; esac
  model[$i]=$(sanitize "$(lsblk -dnro MODEL -- "$dev")")
  serial[$i]=$(sanitize "$(lsblk -dnro SERIAL -- "$dev")")
  wwn[$i]=$(sanitize "$(lsblk -dnro WWN -- "$dev")")
  size[$i]=$(lsblk -bdnro SIZE -- "$dev")
  case "${size[$i]}" in ''|*[!0-9]*) fail "selected disk $path size probe is invalid" ;; esac
  test -n "${serial[$i]}${wwn[$i]}" || fail "selected disk $path exposes neither serial nor WWN"
  transport[$i]=$(lsblk -dnro TRAN -- "$dev" | tr '[:upper:]' '[:lower:]' | tr '\t\r\n' '   ' | awk '{$1=$1; print}')
  transport[$i]=${transport[$i]:-unknown}
  for ((j=0; j<i; j++)); do
    test "${resolved[$j]}" != "$dev" || fail "selected stable identities ${selected[$j]} and $path resolve to the same disk"
  done
done

list_process_ids() {
  process_list_error=
  for process_list_attempt in 1 2 3; do
    set +e
    process_list=$(set -o pipefail; LC_ALL=C ls -1U -- /proc 2>&1 | head -n 65537)
    process_list_rc=$?
    set -e
    if test "$process_list_rc" = 0; then
      test -n "$process_list" || fail "active-open probe found an empty /proc process table"
      test "$(printf '%%s\n' "$process_list" | awk 'END {print NR+0}')" -le 65536 || fail "active-open probe exceeded its bounded /proc-entry limit"
      printf '%%s\n' "$process_list" | awk '/^[0-9]+$/'
      return 0
    fi
    process_list_error=$process_list
  done
  fail "active-open probe could not enumerate /proc: $process_list_error"
}

list_process_fds() {
  process_path=$1
  fd_list_error=
  for fd_list_attempt in 1 2 3; do
    set +e
    fd_list=$(set -o pipefail; LC_ALL=C ls -1U -- "$process_path/fd" 2>&1 | head -n 65537)
    fd_list_rc=$?
    set -e
    if test "$fd_list_rc" = 0; then printf '%%s' "$fd_list"; return 0; fi
    fd_list_error=$fd_list
    current_process_ids=$(list_process_ids)
    process_id=${process_path##*/}
    if ! printf '%%s\n' "$current_process_ids" | grep -Fxq "$process_id"; then return 0; fi
  done
  fail "active-open probe could not enumerate $process_path/fd: $fd_list_error"
}

probe_active_raw_consumer() {
  dev=$1
  device_number=$(stat -Lc '%%t:%%T' "$dev" 2>/dev/null) || fail "cannot determine $dev device number"
  scanned_fds=0
  process_ids=$(list_process_ids)
  test -n "$process_ids" || fail "active-open probe found no visible processes"
  while IFS= read -r process_id; do
    process=/proc/$process_id
    fd_names=$(list_process_fds "$process")
    test -n "$fd_names" || continue
    while IFS= read -r fd_name; do
      case "$fd_name" in ''|*[!0-9]*) fail "active-open probe found invalid descriptor $process/fd/$fd_name" ;; esac
      scanned_fds=$((scanned_fds + 1)); test "$scanned_fds" -le 65536 || fail "active-open probe exceeded its descriptor bound"
      fd=$process/fd/$fd_name; fd_rc=1; fd_number=; fd_gone=false
      for fd_round in 1 2 3; do
        for fd_attempt in 1 2 3; do
          set +e; fd_number=$(stat -Lc '%%t:%%T' "$fd" 2>&1); fd_rc=$?; set -e
          test "$fd_rc" = 0 && break
        done
        test "$fd_rc" = 0 && break
        remaining_fds=$(list_process_fds "$process")
        if ! printf '%%s\n' "$remaining_fds" | grep -Fxq "$fd_name"; then fd_gone=true; break; fi
      done
      test "$fd_gone" = false || continue
      test "$fd_rc" = 0 || fail "active-open probe could not inspect $fd: $fd_number"
      test "$fd_number" != "$device_number" || fail "$dev is held open as a raw block device by process $process_id"
    done <<EOF
$fd_names
EOF
  done <<EOF
$process_ids
EOF
}

probe_identity_topology() {
  i=$1; path=${selected[$i]}; dev=${resolved[$i]}; kernel=${kname[$i]}
  test -L "$path" && test "$(readlink -f -- "$path")" = "$dev" || fail "$path identity drifted"
  test "$(lsblk -dnro TYPE -- "$dev")" = disk || fail "$path is not a whole disk"
  test "$(lsblk -dnro RM -- "$dev")" = 0 || fail "$path is removable"
  test -z "$(lsblk -dnro PTTYPE -- "$dev")" || fail "$path has a partition table"
  test "$(sanitize "$(lsblk -dnro MODEL -- "$dev")")" = "${model[$i]}" || fail "$path model identity drifted"
  test "$(sanitize "$(lsblk -dnro SERIAL -- "$dev")")" = "${serial[$i]}" || fail "$path serial identity drifted"
  test "$(sanitize "$(lsblk -dnro WWN -- "$dev")")" = "${wwn[$i]}" || fail "$path WWN identity drifted"
  test "$(lsblk -bdnro SIZE -- "$dev")" = "${size[$i]}" || fail "$path size identity drifted"
  for target in / /boot /boot/efi /var /var/lib/rancher/k3s /var/lib/kubelet; do
    source=$(findmnt -n -o SOURCE --target "$target" 2>/dev/null || true); source=${source%%[*}
    test -n "$source" || continue; source=$(readlink -f -- "$source" 2>/dev/null || true); test -b "$source" || continue
    if lsblk -snro PATH -- "$source" | grep -Fxq "$dev"; then fail "$path backs system path $target"; fi
  done
  while read -r source _; do
    test "$source" != Filename || continue; source=$(readlink -f -- "$source" 2>/dev/null || true); test -b "$source" || continue
    if lsblk -snro PATH -- "$source" | grep -Fxq "$dev"; then fail "$path backs active swap"; fi
  done < /proc/swaps
  direct_mounts=$(findmnt -rn -S "$dev" -o TARGET 2>/dev/null || true)
  test -z "$direct_mounts" || fail "$path is directly mounted at $direct_mounts"
  probe_active_raw_consumer "$dev"
}

probe_blank() {
  i=$1; probe_identity_topology "$i"; dev=${resolved[$i]}; kernel=${kname[$i]}
  test "$(lsblk -nrpo PATH -- "$dev" | awk 'NF {n++} END {print n+0}')" = 1 || fail "${selected[$i]} has partitions or child devices"
  test ! -d "/sys/class/block/$kernel/holders" || test -z "$(find "/sys/class/block/$kernel/holders" -mindepth 1 -maxdepth 1 -print -quit)" || fail "${selected[$i]} has active holders"
  set +e; wipe_types=$(wipefs -n --noheadings --output TYPE -- "$dev" 2>&1); wipe_rc=$?; set -e
  test "$wipe_rc" = 0 || fail "wipefs probe failed for ${selected[$i]}: $wipe_types"
  test -z "$(printf '%%s' "$wipe_types" | awk 'NF')" || fail "${selected[$i]} has a recognized partition/filesystem/RAID/LVM/crypt signature"
  set +e; blk_type=$(blkid -p -s TYPE -o value -- "$dev" 2>&1); blk_rc=$?; set -e
  test "$blk_rc" = 2 || { test "$blk_rc" = 0 && fail "${selected[$i]} has recognized signature $blk_type"; fail "blkid probe failed or was ambiguous for ${selected[$i]}: $blk_type"; }
}

receipt_value() { awk -F= -v wanted="$1" '$1 == wanted {sub(/^[^=]*=/, ""); print; found=1} END {if (!found) exit 3}' "$receipt"; }
decode_receipt() { receipt_value "$1" | base64 -d; }
lvm_uuid() { raw=$(tr -d - < /proc/sys/kernel/random/uuid); printf '%%s-%%s-%%s-%%s-%%s-%%s-%%s\n' "${raw:0:6}" "${raw:6:4}" "${raw:10:4}" "${raw:14:4}" "${raw:18:4}" "${raw:22:4}" "${raw:26:6}"; }
write_receipt() {
  status_value=$1; pv_done_value=$2; receipt_dir=$(dirname "$receipt")
  install -d -o root -g root -m 0700 "$receipt_dir"
  tmp=$(mktemp "$receipt_dir/.data-storage.receipt.XXXXXX"); umask 077
  {
    printf 'contract=%%s\nstatus=%%s\npv_done=%%s\n' "$contract" "$status_value" "$pv_done_value"
    printf 'install_b64=%%s\n' "$(printf '%%s' "$install_name" | base64 -w0)"
    printf 'device_count=%%s\nvg_name=%%s\nvg_uuid=%%s\n' "$count" "$vg_name" "$planned_vg_uuid"
    for ((r=0; r<count; r++)); do
      printf 'device_%%s_b64=%%s\n' "$r" "$(printf '%%s' "${selected[$r]}" | base64 -w0)"
      printf 'resolved_%%s_b64=%%s\n' "$r" "$(printf '%%s' "${resolved[$r]}" | base64 -w0)"
      printf 'model_%%s_b64=%%s\n' "$r" "$(printf '%%s' "${model[$r]}" | base64 -w0)"
      printf 'serial_%%s_b64=%%s\n' "$r" "$(printf '%%s' "${serial[$r]}" | base64 -w0)"
      printf 'wwn_%%s_b64=%%s\n' "$r" "$(printf '%%s' "${wwn[$r]}" | base64 -w0)"
      printf 'transport_%%s_b64=%%s\n' "$r" "$(printf '%%s' "${transport[$r]}" | base64 -w0)"
      printf 'size_%%s=%%s\npv_uuid_%%s=%%s\n' "$r" "${size[$r]}" "$r" "${planned_pv_uuid[$r]}"
    done
  } > "$tmp"
  chown root:root "$tmp"; chmod 0600 "$tmp"; sync -f "$tmp"; mv -f "$tmp" "$receipt"; sync -f "$receipt_dir"
}

status=; pv_done=0; planned_vg_uuid=
if test -e "$receipt"; then
  test -f "$receipt" && test ! -L "$receipt" || fail "data-storage receipt is not a regular file"
  test "$(stat -c '%%u:%%g:%%a' "$receipt")" = 0:0:600 || fail "data-storage receipt ownership/mode drift"
  test "$(receipt_value contract)" = "$contract" || fail "data-storage receipt contract mismatch"
  status=$(receipt_value status); case "$status" in planned|pvs-created|vg-created|complete) ;; *) fail "data-storage receipt status is invalid" ;; esac
  pv_done=$(receipt_value pv_done); case "$pv_done" in ''|*[!0-9]*) fail "data-storage receipt pv_done is invalid" ;; esac
  test "$pv_done" -le "$count" || fail "data-storage receipt pv_done exceeds device count"
  test "$(decode_receipt install_b64)" = "$install_name" || fail "data-storage receipt install mismatch"
  test "$(receipt_value device_count)" = "$count" || fail "data-storage configured device-set size differs from the receipt"
  test "$(receipt_value vg_name)" = "$vg_name" || fail "data-storage VG name mismatch"
  planned_vg_uuid=$(receipt_value vg_uuid)
  case "$planned_vg_uuid" in ??????-????-????-????-????-????-??????) ;; *) fail "data-storage receipt VG UUID is invalid" ;; esac
  for ((i=0; i<count; i++)); do
    test "$(decode_receipt device_${i}_b64)" = "${selected[$i]}" || fail "data-storage device order/set differs from the receipt"
    test "$(decode_receipt resolved_${i}_b64)" = "${resolved[$i]}" || fail "data-storage resolved device identity drift"
    test "$(decode_receipt model_${i}_b64)" = "${model[$i]}" || fail "data-storage model identity drift"
    test "$(decode_receipt serial_${i}_b64)" = "${serial[$i]}" || fail "data-storage serial identity drift"
    test "$(decode_receipt wwn_${i}_b64)" = "${wwn[$i]}" || fail "data-storage WWN identity drift"
    test "$(decode_receipt transport_${i}_b64)" = "${transport[$i]}" || fail "data-storage transport identity drift"
    test "$(receipt_value size_$i)" = "${size[$i]}" || fail "data-storage size identity drift"
    planned_pv_uuid[$i]=$(receipt_value pv_uuid_$i)
    case "${planned_pv_uuid[$i]}" in ??????-????-????-????-????-????-??????) ;; *) fail "data-storage receipt PV UUID is invalid" ;; esac
  done
else
  for ((i=0; i<count; i++)); do probe_blank "$i"; done
  if command -v vgs >/dev/null 2>&1 && vgs "$vg_name" >/dev/null 2>&1; then fail "fixed VG $vg_name already exists without the Forge receipt"; fi
  if test "$mode" = inspect; then
    for ((i=0; i<count; i++)); do printf 'FORGE_DATA_STORAGE_DEVICE_RESULT\t%%s\t%%s\t%%s\t%%s\t%%s\t\t%%s\t%%s\n' "${selected[$i]}" "${resolved[$i]}" "${model[$i]}" "${serial[$i]}" "${wwn[$i]}" "${size[$i]}" "${transport[$i]}"; done
    printf 'FORGE_DATA_STORAGE_RESULT\tblank-candidate\t%%s\t\t0\t0\t%%s\n' "$vg_name" "$count"; exit 0
  fi
  need pvcreate; need pvs; need vgcreate; need vgs; need lvs
  for ((i=0; i<count; i++)); do planned_pv_uuid[$i]=$(lvm_uuid); done
  planned_vg_uuid=$(lvm_uuid); write_receipt planned 0; status=planned
fi

need pvs; need vgs; need lvs
read_pv() {
  set +e; pv_line=$(pvs --noheadings --separator '|' -o pv_uuid,vg_name -- "$1" 2>&1); pv_rc=$?; set -e
  test "$pv_rc" = 0 || return 1
  printf '%%s' "$pv_line" | `+lvmReportPairParser+`
}
verify_or_blank_set() {
  for ((q=0; q<count; q++)); do
    probe_identity_topology "$q"
    if pv=$(read_pv "${resolved[$q]}"); then
      pv_uuid=${pv%%|*}; pv_vg=${pv#*|}
      test "$pv_uuid" = "${planned_pv_uuid[$q]}" || fail "${selected[$q]} PV UUID is foreign"
      test -z "$pv_vg" || test "$pv_vg" = "$vg_name" || fail "${selected[$q]} belongs to foreign VG $pv_vg"
    else
      probe_blank "$q"
    fi
  done
}

if test "$mode" = inspect; then
  verify_or_blank_set
else
  for ((i=0; i<count; i++)); do
    if pv=$(read_pv "${resolved[$i]}"); then
      pv_uuid=${pv%%|*}; pv_vg=${pv#*|}
      test "$pv_uuid" = "${planned_pv_uuid[$i]}" || fail "${selected[$i]} PV UUID differs from the planned receipt identity"
      test -z "$pv_vg" || test "$pv_vg" = "$vg_name" || fail "${selected[$i]} belongs to foreign VG $pv_vg"
    else
      verify_or_blank_set
      pvcreate --yes --zero y --uuid "${planned_pv_uuid[$i]}" --norestorefile -- "${resolved[$i]}"
      pv=$(read_pv "${resolved[$i]}") || fail "pvcreate did not produce a readable PV for ${selected[$i]}"
      test "${pv%%|*}" = "${planned_pv_uuid[$i]}" && test -z "${pv#*|}" || fail "pvcreate identity mismatch for ${selected[$i]}"
    fi
    pv_done=$((i + 1)); write_receipt pvs-created "$pv_done"; status=pvs-created
  done
fi

vg_exists=false
if vgs "$vg_name" >/dev/null 2>&1; then vg_exists=true; fi
if test "$vg_exists" = false; then
  test "$mode" = inspect && { final_state="resumable-$status"; vg_size=0; vg_free=0; vg_uuid=; }
  if test "$mode" = reconcile; then
    test "$pv_done" = "$count" || fail "not every receipt PV is complete before vgcreate"
    verify_or_blank_set
    vgcreate --yes --uuid "$planned_vg_uuid" "$vg_name" "${resolved[@]}"
    write_receipt vg-created "$count"; status=vg-created; vg_exists=true
  fi
fi

if test "$vg_exists" = true; then
  vg_line=$(vgs --noheadings --separator '|' --units b --nosuffix -o vg_uuid,vg_size,vg_free,lv_count,pv_count "$vg_name") || fail "cannot inspect $vg_name"
  IFS='|' read -r vg_uuid vg_size vg_free lv_count pv_count <<<"$vg_line"
  vg_uuid=$(printf '%%s' "$vg_uuid" | awk '{$1=$1;print}'); vg_size=$(printf '%%s' "$vg_size" | awk '{$1=$1;printf "%%.0f",$1}'); vg_free=$(printf '%%s' "$vg_free" | awk '{$1=$1;printf "%%.0f",$1}')
  lv_count=$(printf '%%s' "$lv_count" | awk '{$1=$1;print}'); pv_count=$(printf '%%s' "$pv_count" | awk '{$1=$1;print}')
  unexpected_segments=$(lvs --noheadings --select "vg_name=$vg_name" -o segtype | awk '{$1=$1; if(NF && $1 != "linear") print}')
  test -z "$unexpected_segments" || fail "$vg_name contains unsupported thin/snapshot/non-linear logical volumes: $unexpected_segments"
  test "$vg_uuid" = "$planned_vg_uuid" || fail "$vg_name UUID differs from the receipt"
  test "$pv_count" = "$count" || fail "$vg_name PV membership count differs from the receipt"
  actual_members=$(pvs --noheadings --select "vg_name=$vg_name" -o pv_name | awk '{$1=$1; if(NF)print}' | sort)
  expected_members=$(printf '%%s\n' "${resolved[@]}" | sort)
  test "$actual_members" = "$expected_members" || fail "$vg_name PV membership differs from the selected set"
  for ((i=0; i<count; i++)); do
    pv=$(read_pv "${resolved[$i]}") || fail "receipt PV ${selected[$i]} disappeared"
    test "${pv%%|*}" = "${planned_pv_uuid[$i]}" && test "${pv#*|}" = "$vg_name" || fail "receipt PV ${selected[$i]} identity/membership drift"
  done
  if test "$mode" = reconcile && test "$status" != complete; then write_receipt complete "$count"; status=complete; fi
  if test "$status" = complete; then final_state=complete; else final_state="resumable-$status"; fi
fi

for ((i=0; i<count; i++)); do printf 'FORGE_DATA_STORAGE_DEVICE_RESULT\t%%s\t%%s\t%%s\t%%s\t%%s\t%%s\t%%s\t%%s\n' "${selected[$i]}" "${resolved[$i]}" "${model[$i]}" "${serial[$i]}" "${wwn[$i]}" "${planned_pv_uuid[$i]}" "${size[$i]}" "${transport[$i]}"; done
printf 'FORGE_DATA_STORAGE_RESULT\t%%s\t%%s\t%%s\t%%s\t%%s\t%%s\n' "$final_state" "$vg_name" "${vg_uuid:-}" "${vg_size:-0}" "${vg_free:-0}" "$count"
`, shellQuote(spec.InstallName), shellQuote(mode), shellQuote(dataStorageContractVersion), shellQuote(dataStorageReceiptPath), shellQuote(provisioner.DataVolumeGroupName), strings.Join(quoted, " "))
}

// WaitForLVMStorageReady validates the chart-owned substrate and exact VG
// discovery after the companion release is applied.
func (p *SSHProvisioner) WaitForLVMStorageReady(ctx context.Context, namespace string, host *provisioner.DataStorageState) (*provisioner.LVMStorageReadiness, error) {
	if host == nil || host.VGName != provisioner.DataVolumeGroupName || host.VGUUID == "" {
		return nil, fmt.Errorf("host data-storage identity is incomplete")
	}
	script := fmt.Sprintf(`
namespace=%s
expected_vg=%s
expected_uuid=%s
expected_size=%s
expected_free=%s
fail() { printf 'lvm-storage readiness refusal: %%s\n' "$*" >&2; exit 42; }
for attempt in $(seq 1 150); do
  if k3s kubectl get crd lvmnodes.local.openebs.io lvmvolumes.local.openebs.io lvmsnapshots.local.openebs.io >/dev/null 2>&1 &&
     k3s kubectl wait --for=condition=Established crd/lvmnodes.local.openebs.io crd/lvmvolumes.local.openebs.io crd/lvmsnapshots.local.openebs.io --timeout=10s >/dev/null 2>&1 &&
     k3s kubectl wait -n "$namespace" --for=condition=Available deployment -l app=openebs-lvm-controller --timeout=10s >/dev/null 2>&1 &&
     k3s kubectl rollout status -n "$namespace" daemonset -l app=openebs-lvm-node --timeout=10s >/dev/null 2>&1 &&
     k3s kubectl get csidriver local.csi.openebs.io >/dev/null 2>&1; then
    break
  fi
  test "$attempt" -lt 150 || fail "OpenEBS LVM LocalPV controller/node/CRD/CSI registration did not become Ready"
  sleep 2
done
classes=$(k3s kubectl get storageclass -o jsonpath='{range .items[*]}{.metadata.name}{"\n"}{end}' | sort)
test "$classes" = %s || fail "managed StorageClass set is not exactly the two approved classes: $classes"
test -z "$(k3s kubectl get storageclass -o jsonpath='{range .items[?(@.metadata.annotations.storageclass\\.kubernetes\\.io/is-default-class=="true")]}{.metadata.name}{"\n"}{end}')" || fail "a default StorageClass exists"
if k3s kubectl get deployment local-path-provisioner -n kube-system >/dev/null 2>&1 || k3s kubectl get configmap local-path-config -n kube-system >/dev/null 2>&1; then fail "K3s local-path provisioner still exists"; fi
verify_class() {
  name=$1; shared=$2
  observed=$(k3s kubectl get storageclass "$name" -o jsonpath='{.provisioner}|{.reclaimPolicy}|{.volumeBindingMode}|{.allowVolumeExpansion}|{.parameters.storage}|{.parameters.vgpattern}|{.parameters.fsType}|{.parameters.thinProvision}|{.parameters.shared}')
  test "$observed" = "local.csi.openebs.io|Delete|WaitForFirstConsumer|false|lvm|^iterabase-data$|xfs|no|$shared" || fail "StorageClass $name contract drift: $observed"
}
verify_class %s no
verify_class %s yes
node_count=$(k3s kubectl get lvmnodes.local.openebs.io -n "$namespace" -o jsonpath='{.items[*].metadata.name}' | awk '{print NF}')
test "$node_count" = 1 || fail "expected exactly one LVMNode, observed $node_count"
node_name=$(k3s kubectl get lvmnodes.local.openebs.io -n "$namespace" -o jsonpath='{.items[0].metadata.name}')
vg=$(k3s kubectl get lvmnodes.local.openebs.io -n "$namespace" -o jsonpath='{range .items[0].volumeGroups[?(@.name=="iterabase-data")]}{.name}|{.uuid}|{.size}|{.free}|{.lvCount}|{.pvCount}{"\n"}{end}')
test -n "$vg" || fail "LVMNode does not report iterabase-data"
IFS='|' read -r vg_name vg_uuid vg_size vg_free lv_count pv_count <<<"$vg"
test "$vg_name" = "$expected_vg" && test "$vg_uuid" = "$expected_uuid" || fail "LVMNode VG identity differs from the Forge receipt"
test -n "$vg_size" && test -n "$vg_free" || fail "LVMNode VG capacity is missing: $vg"
for number in "$lv_count" "$pv_count"; do case "$number" in ''|*[!0-9]*) fail "LVMNode VG count is invalid: $vg" ;; esac; done
printf 'FORGE_LVM_STORAGE_READY\t%%s\t%%s\t%%s\t%%s\t%%s\t%%s\t%%s\n' "$node_name" "$vg_name" "$vg_uuid" "$expected_size" "$expected_free" "$lv_count" "$pv_count"
`, shellQuote(namespace), shellQuote(host.VGName), shellQuote(host.VGUUID), shellQuote(strconv.FormatUint(host.SizeBytes, 10)), shellQuote(strconv.FormatUint(host.FreeBytes, 10)), shellQuote(provisioner.AgentPoolStorageClass+"\n"+provisioner.PlatformStorageClass), shellQuote(provisioner.PlatformStorageClass), shellQuote(provisioner.AgentPoolStorageClass))
	out, err := p.run(ctx, "sudo bash -ceu "+shellQuote(script))
	if err != nil {
		return nil, fmt.Errorf("wait for LVM storage substrate: %w", err)
	}
	for _, line := range strings.Split(out, "\n") {
		parts := strings.Split(line, "\t")
		if len(parts) != 8 || parts[0] != "FORGE_LVM_STORAGE_READY" {
			continue
		}
		size, parseErr := strconv.ParseUint(parts[4], 10, 64)
		if parseErr != nil {
			return nil, fmt.Errorf("parse LVMNode size: %w", parseErr)
		}
		free, parseErr := strconv.ParseUint(parts[5], 10, 64)
		if parseErr != nil {
			return nil, fmt.Errorf("parse LVMNode free size: %w", parseErr)
		}
		lvCount, parseErr := strconv.Atoi(parts[6])
		if parseErr != nil {
			return nil, fmt.Errorf("parse LVMNode LV count: %w", parseErr)
		}
		pvCount, parseErr := strconv.Atoi(parts[7])
		if parseErr != nil {
			return nil, fmt.Errorf("parse LVMNode PV count: %w", parseErr)
		}
		return &provisioner.LVMStorageReadiness{Ready: true, NodeName: parts[1], VGName: parts[2], VGUUID: parts[3], SizeBytes: size, FreeBytes: free, LVCount: lvCount, PVCount: pvCount}, nil
	}
	return nil, fmt.Errorf("LVM storage readiness returned no bounded result")
}
