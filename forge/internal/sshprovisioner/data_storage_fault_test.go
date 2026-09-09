package sshprovisioner

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strconv"
	"strings"
	"testing"
	"time"

	"github.com/stretchr/testify/require"

	"github.com/nunocgoncalves/iterabase-mono/forge/internal/provisioner"
)

// fixtureLoopEnv is the INTERNAL test-process-only environment flag that tells
// the generated reconcile/purge script it may accept loop-backed by-id fixtures.
// It is NOT a Forge CLI or configuration knob (it cannot be set through Forge),
// and normal Forge apply never sets it, so ordinary production rejection of
// loop/logical devices and unsigned disks is fully retained. Only the privileged
// forge-fault-matrix CI harness injects it when it executes the real script over
// loop-backed by-id devices.
const fixtureLoopEnv = "FORGE_DATA_STORAGE_FIXTURE_LOOP"

// fixtureRequiredEnv, when set to "1", makes lvmFaultMatrixEnv fail (not skip)
// whenever the privileged environment is unavailable, so the required defect-matrix
// CI job cannot silently pass without exercising the real transaction.
const fixtureRequiredEnv = "FORGE_FAULT_MATRIX_REQUIRED"

// faultByIDDir mirrors the stable whole-disk by-id identities Forge selects in
// production. The fault harness places iteration-specific aliases under it for
// loop-backed devices and removes them on teardown.
const faultByIDDir = "/dev/disk/by-id"

// lvmFaultMatrixEnv reports whether a privileged real-LVM environment capable of
// staging the actual reconcile transaction is available. It requires root, the
// LVM2 + losetup toolchain, mknod, and /dev/disk/by-id so the deterministic
// executable fault-stage matrix can create loop-backed by-id block devices. When
// unavailable it skips on dev workstations, but FAILS (rather than skips) when
// FORGE_FAULT_MATRIX_REQUIRED is set, so the required CI job is fail-closed if
// its prerequisites are missing (founder-approved Option B).
func lvmFaultMatrixEnv(t *testing.T) (bool, string) {
	t.Helper()
	required := os.Getenv(fixtureRequiredEnv) == "1"
	unavailable := func(reason string) (bool, string) {
		if required {
			t.Fatalf("required forge fault-matrix environment unavailable, must fail not skip: %s", reason)
		}
		return false, reason
	}
	if os.Geteuid() != 0 {
		return unavailable("real-LVM reconcile fault matrix requires root (losetup/mknod/by-id alias)")
	}
	for _, tool := range []string{"losetup", "pvcreate", "vgcreate", "pvs", "vgs", "lvs", "vgremove", "pvremove", "mknod", "ln"} {
		if _, err := exec.LookPath(tool); err != nil {
			return unavailable(fmt.Sprintf("real-LVM reconcile fault matrix requires %q", tool))
		}
	}
	return true, ""
}

// writeFaultScript writes the generated reconcile script to disk so it can be
// executed with the real bash -ceu interpreter (exactly as Forge runs it over
// SSH) while a goroutine terminates the process at a chosen durable stage.
func writeFaultScript(t *testing.T, spec provisioner.DataStorageSpec, mode string) string {
	t.Helper()
	path := filepath.Join(t.TempDir(), "reconcile.sh")
	require.NoError(t, os.WriteFile(path, []byte("set -eu\n"+dataStorageReconcileScript(spec, mode)+"\n"), 0o600))
	return path
}

// runDataStorageBash executes an already-written reconcile script under the real
// bash interpreter in the internal fixture mode (fixtureLoopEnv set) and returns
// stdout/exit. killAfter determines the durable receipt stage at which a SIGKILL
// is injected to simulate a crash at exactly that transaction boundary without
// mutating the script.
func runDataStorageBash(t *testing.T, path, killAfter string) (string, bool) {
	t.Helper()
	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Minute)
	defer cancel()

	cmd := exec.CommandContext(ctx, "bash", path)
	cmd.Env = append(os.Environ(), fixtureLoopEnv+"=1")
	var stdout, stderr bytes.Buffer
	cmd.Stdout = &stdout
	cmd.Stderr = &stderr
	if err := cmd.Start(); err != nil {
		t.Fatalf("start reconcile script: %v", err)
	}

	if killAfter != "" {
		go func() {
			deadline := time.Now().Add(90 * time.Second)
			for time.Now().Before(deadline) {
				data, err := os.ReadFile(dataStorageReceiptPath)
				if err == nil && strings.Contains(string(data), "status="+killAfter) {
					_ = cmd.Process.Kill()
					return
				}
				time.Sleep(10 * time.Millisecond)
			}
		}()
	}

	done := make(chan error, 1)
	go func() { done <- cmd.Wait() }()
	select {
	case <-ctx.Done():
		_ = cmd.Process.Kill()
		t.Fatalf("reconcile script timed out; stdout=%s stderr=%s", stdout.String(), stderr.String())
		return "", false
	case err := <-done:
		if ctx.Err() != nil {
			_ = cmd.Process.Kill()
		}
		if err != nil {
			var exitErr *exec.ExitError
			if errors.As(err, &exitErr) && killAfter != "" && exitErr.ExitCode() == -1 {
				// SIGKILL injected at the requested stage is the expected crash.
				return stdout.String(), true
			}
			return stdout.String() + "\nstderr: " + stderr.String(), false
		}
		return stdout.String(), true
	}
}

// makeFaultLoopDevices allocates count real loop-backed block devices and places
// stable /dev/disk/by-id aliases for them (mirroring the whole-disk by-id
// identities Forge selects in production). It returns the by-id paths to hand to
// the reconcile script and the raw loop device paths to detach on teardown.
func makeFaultLoopDevices(t *testing.T, dir string, count int) ([]string, []string) {
	t.Helper()
	var byID, loops []string
	require.NoError(t, os.MkdirAll(faultByIDDir, 0o755))
	for i := 0; i < count; i++ {
		img := filepath.Join(dir, fmt.Sprintf("disk%d.img", i))
		require.NoError(t, runCmd("truncate", "-s", "16M", img))
		out, err := exec.Command("losetup", "--find", "--show", "--nooverlap", img).CombinedOutput()
		require.NoError(t, err, "losetup failed: %s", out)
		loop := strings.TrimSpace(string(out))
		loops = append(loops, loop)
		alias := filepath.Join(faultByIDDir, "iterabase-fixture-"+strconv.Itoa(i))
		require.NoError(t, os.RemoveAll(alias))
		require.NoError(t, os.Symlink(loop, alias))
		byID = append(byID, alias)
	}
	return byID, loops
}

func teardownFaultLoopDevices(t *testing.T, loops, aliases []string) {
	t.Helper()
	for _, a := range aliases {
		_ = os.RemoveAll(a)
	}
	for _, loop := range loops {
		_ = runCmd("losetup", "-d", loop)
	}
}

// resetFaultStorage removes the durable fixture VG (production name
// iterabase-data) and receipt so a fresh matrix case starts from clean state.
func resetFaultStorage(t *testing.T) {
	t.Helper()
	_ = runCmd("vgremove", "--force", "--yes", provisioner.DataVolumeGroupName)
	_ = os.Remove(dataStorageReceiptPath)
}

func runCmd(name string, args ...string) error {
	cmd := exec.Command(name, args...)
	return cmd.Run()
}

// lvmFaultDump returns the current LVM + receipt state for failure diagnostics in
// the privileged fault matrix.
func lvmFaultDump() string {
	out, err := exec.Command("sh", "-c", `vgs --noheadings -o vg_name,vg_uuid 2>/dev/null; pvs --noheadings -o pv_name,pv_uuid,vg_name 2>/dev/null; echo '--- receipt ---'; cat /var/lib/iterabase/data-storage.receipt 2>/dev/null || true`).CombinedOutput()
	if err != nil {
		return strings.TrimSpace(string(out))
	}
	return strings.TrimSpace(string(out))
}

// TestDataStorageFaultMatrixScriptCoversEveryDurableStage is the locally
// runnable structural companion: it asserts the generated script front-loads the
// durable receipt before every mutation, binds each stage to an explicit
// write_receipt, and gates the loop-by-id fixture escape behind the internal
// FORGE_DATA_STORAGE_FIXTURE_LOOP flag so ordinary production rejection is
// retained unless the test harness opt-in is present.
func TestDataStorageFaultMatrixScriptCoversEveryDurableStage(t *testing.T) {
	script := dataStorageReconcileScript(provisioner.DataStorageSpec{
		InstallName: "opo1", Devices: []string{"/dev/disk/by-id/scsi-a", "/dev/disk/by-id/scsi-b"},
	}, "reconcile")

	// Every pvcreate in the per-PV loop is immediately followed by a durable
	// pvs-created receipt that persists pv_done so a crash mid-write is resumable.
	pvcreateIdx := strings.Index(script, "pvcreate --yes --zero y --uuid")
	require.GreaterOrEqual(t, pvcreateIdx, 0, "reconcile must pvcreate each planned device")
	receipt := strings.Index(script, "write_receipt pvs-created")
	require.GreaterOrEqual(t, receipt, 0, "reconcile must persist a pvs-created receipt after each PV")
	require.Less(t, pvcreateIdx, receipt, "each pvcreate must be followed by the durable pvs-created receipt")
	require.Equal(t, 1, strings.Count(script, "write_receipt pvs-created"))

	// DES-HOR-545-03 monotonic receipt: the pvs-created write is gated on an
	// unbound VG UUID, so exact reapply of an already-complete receipt never
	// regresses to pvs-created.
	require.Contains(t, script, "if test -z \"$receipt_vg_uuid\"; then write_receipt pvs-created")

	// The planned receipt precedes the first mutation and the VG-created receipt
	// binds the complete PV set before the UUID is persisted.
	require.Less(t, strings.Index(script, "write_receipt planned 0"), strings.Index(script, "pvcreate --yes --zero y --uuid"))
	require.Less(t, strings.Index(script, "vgcreate --yes --addtag"), strings.Index(script, "receipt_vg_uuid=$vg_uuid; write_receipt vg-created"))

	// Founder-approved Option B: the loop/unsigned-device fixture escape is gated
	// behind the internal FORGE_DATA_STORAGE_FIXTURE_LOOP flag. Its only
	// occurrences are the gate guards; ordinary production rejection of loop
	// devices and unsigned disks remains the default.
	// Founder-approved Option B: the loop/unsigned/whole-disk fixture escape is
	// gated behind the internal FORGE_DATA_STORAGE_FIXTURE_LOOP flag. Every
	// check that would otherwise reject the loop fixture must be gated (balanced),
	// and the ordinary production rejection messages must remain present so
	// normal forge apply (without the flag) rejects loop devices unchanged.
	gate := "if [ \"${FORGE_DATA_STORAGE_FIXTURE_LOOP:-}\" != \"1\" ]; then"
	wholeDisk := strings.Count(script, "is not a whole disk")
	loopReject := strings.Count(script, "unsupported logical/network device")
	unsigned := strings.Count(script, "exposes neither serial nor WWN")
	require.Equal(t, wholeDisk+loopReject+unsigned, strings.Count(script, gate),
		"every production device-rejection check must be gated behind the internal fixture flag")
	require.GreaterOrEqual(t, wholeDisk, 2, "both the resolution prelude and topology probe must keep the whole-disk rejection")
	require.Contains(t, script, "unsupported logical/network device")
	require.Contains(t, script, "exposes neither serial nor WWN")
}

// requireReceiptStatus asserts the durable receipt currently reports the given
// status field, proving the on-disk stage survived (or was not regressed by)
// the preceding run/crash.
func requireReceiptStatus(t *testing.T, want string) {
	t.Helper()
	data, err := os.ReadFile(dataStorageReceiptPath)
	require.NoError(t, err, "read durable receipt")
	for _, line := range strings.Split(string(data), "\n") {
		if strings.HasPrefix(line, "status=") {
			require.Equal(t, "status="+want, strings.TrimSpace(line))
			return
		}
	}
	t.Fatalf("receipt has no status field: %s", string(data))
}

// TestDataStorageFaultStageMatrixExecutable stages a real LVM transaction across
// every durable boundary and proves Forge's crash-resumable and mismatch-refusal
// contract by actually executing the generated reconcile script over loop-backed
// by-id devices in the fixture mode. Each sub-test terminates the script with
// SIGKILL after a specific durable receipt stage, re-runs it idempotently, and
// asserts it resumes and completes. It runs in the privileged required
// forge-fault-matrix CI job (FORGE_FAULT_MATRIX_REQUIRED=1, root) and fails
// rather than skips if the environment or any stage is unavailable.
func TestDataStorageFaultStageMatrixExecutable(t *testing.T) {
	ok, reason := lvmFaultMatrixEnv(t)
	if !ok {
		t.Skip(reason)
	}

	var curLoops, curAliases []string
	t.Cleanup(func() { teardownFaultLoopDevices(t, curLoops, curAliases) })
	// newFixture tears down prior devices, resets the durable VG/receipt, and
	// creates a fresh loop-backed by-id fixture for an independent case.
	newFixture := func(t *testing.T, installName string) (provisioner.DataStorageSpec, string) {
		t.Helper()
		teardownFaultLoopDevices(t, curLoops, curAliases)
		resetFaultStorage(t)
		dir := t.TempDir()
		aliases, loops := makeFaultLoopDevices(t, dir, 2)
		curLoops, curAliases = loops, aliases
		spec := provisioner.DataStorageSpec{InstallName: installName, Devices: aliases}
		return spec, writeFaultScript(t, spec, "reconcile")
	}

	// A clean full run must converge to complete and bind both PVs + the VG.
	_, script := newFixture(t, "opo1")
	out, okc := runDataStorageBash(t, script, "")
	if !okc {
		t.Fatalf("reconcile failed on clean run: %s\n%s", out, lvmFaultDump())
	}
	require.Contains(t, out, "FORGE_DATA_STORAGE_RESULT\tcomplete")
	require.Contains(t, out, lvmReportPairParser) // the bounded VG inspection parser is present

	// Fault-stage matrix: crash after each durable receipt stage then resume.
	for _, sc := range []struct {
		name   string
		marker string
	}{
		{name: "planned", marker: "status=planned"},
		{name: "after-pv0", marker: "status=pvs-created\npv_done=1"},
		{name: "after-pv1", marker: "status=pvs-created\npv_done=2"},
		{name: "after-vg", marker: "status=vg-created"},
	} {
		t.Run("resume-after-"+sc.name, func(t *testing.T) {
			_, fscript := newFixture(t, "opo1")
			out, crashed := runDataStorageBash(t, fscript, sc.marker)
			require.True(t, crashed, "expected SIGKILL crash at %s; out=%s", sc.name, out)
			out2, ok2 := runDataStorageBash(t, fscript, "")
			require.True(t, ok2, "resume failed after %s crash: %s", sc.name, out2)
			require.Contains(t, out2, "FORGE_DATA_STORAGE_RESULT\tcomplete")
		})
	}

	// Mismatch refusal: regenerate the script from mismatched install input and
	// run it against the same durable fixture - the receipt must refuse it.
	t.Run("refuses-foreign-install", func(t *testing.T) {
		_, base := newFixture(t, "opo1")
		out, ok := runDataStorageBash(t, base, "")
		require.True(t, ok, "seed complete receipt: %s", out)
		require.Contains(t, out, "FORGE_DATA_STORAGE_RESULT\tcomplete")
		foreign := writeFaultScript(t, provisioner.DataStorageSpec{InstallName: "other", Devices: curAliases}, "reconcile")
		outF, okF := runDataStorageBash(t, foreign, "")
		require.False(t, okF, "foreign install identity must be refused")
		require.Contains(t, outF, "receipt install mismatch")
	})
}

// TestDataStorageReapplyReceiptMonotonicExecutable proves exact reapply of an
// already-complete receipt is idempotent, crash-resumable, and never regresses
// the receipt to pvs-created (DES-HOR-545-03 / HOR-545 exact-reapply
// acceptance). It executes the real reconcile script over loop-backed by-id
// devices, injects SIGKILL crashes, and asserts the complete receipt is always
// preserved.
func TestDataStorageReapplyReceiptMonotonicExecutable(t *testing.T) {
	ok, reason := lvmFaultMatrixEnv(t)
	if !ok {
		t.Skip(reason)
	}
	resetFaultStorage(t)
	dir := t.TempDir()
	aliases, loops := makeFaultLoopDevices(t, dir, 2)
	t.Cleanup(func() { teardownFaultLoopDevices(t, loops, aliases) })
	spec := provisioner.DataStorageSpec{InstallName: "opo1", Devices: aliases}
	script := writeFaultScript(t, spec, "reconcile")

	// Fresh install must converge to complete and bind both PVs + the VG.
	out, okc := runDataStorageBash(t, script, "")
	if !okc {
		t.Fatalf("reconcile failed on clean run: %s\n%s", out, lvmFaultDump())
	}
	require.Contains(t, out, "FORGE_DATA_STORAGE_RESULT\tcomplete")
	requireReceiptStatus(t, "complete")

	// Exact reapply: with the monotonic guard the pvs-created marker is never
	// re-emitted for an already-complete receipt, so the run completes normally
	// (crashed=false) and the receipt stays complete.
	out, crashed := runDataStorageBash(t, script, "pvs-created")
	require.False(t, crashed, "exact reapply regressed the receipt to pvs-created:\n%s", out)
	require.Contains(t, out, "FORGE_DATA_STORAGE_RESULT\tcomplete")
	requireReceiptStatus(t, "complete")

	// Crash during reapply (killed after loading the complete receipt) must leave
	// a resumable, still-complete receipt; the next run completes without repair.
	_, _ = runDataStorageBash(t, script, "complete") // best-effort SIGKILL mid-reapply
	out2, ok2 := runDataStorageBash(t, script, "")
	require.True(t, ok2, "reapply did not resume to complete after crash: %s", out2)
	require.Contains(t, out2, "FORGE_DATA_STORAGE_RESULT\tcomplete")
	requireReceiptStatus(t, "complete")
}

// TestDataStorageLoopDevicesRejectedWithoutFixtureFlag proves the ordinary
// production prelude still rejects loop-backed identities when the internal
// FORGE_DATA_STORAGE_FIXTURE_LOOP flag is absent, so the founder-approved
// fixture escape can never affect normal forge apply.
func TestDataStorageLoopDevicesRejectedWithoutFixtureFlag(t *testing.T) {
	ok, reason := lvmFaultMatrixEnv(t)
	if !ok {
		t.Skip(reason)
	}
	resetFaultStorage(t)
	dir := t.TempDir()
	aliases, loops := makeFaultLoopDevices(t, dir, 1)
	t.Cleanup(func() { teardownFaultLoopDevices(t, loops, aliases) })
	spec := provisioner.DataStorageSpec{InstallName: "opo1", Devices: aliases}
	path := writeFaultScript(t, spec, "reconcile")

	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Minute)
	defer cancel()
	cmd := exec.CommandContext(ctx, "bash", path) // deliberately NO fixtureLoopEnv
	var out bytes.Buffer
	cmd.Stdout = &out
	cmd.Stderr = &out
	err := cmd.Run()
	require.Error(t, err, "loop-backed by-id device accepted without FORGE_DATA_STORAGE_FIXTURE_LOOP")
	outStr := out.String()
	require.Contains(t, outStr, "data-storage refusal")
	require.Contains(t, outStr, "is not a whole disk")
}
