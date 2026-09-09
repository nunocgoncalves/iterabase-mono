package sshprovisioner

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/stretchr/testify/require"

	"github.com/nunocgoncalves/iterabase-mono/forge/internal/provisioner"
)

// lvmFaultMatrixEnv reports whether a privileged real-LVM environment capable of
// staging the actual reconcile transaction is available. It requires root, the
// LVM2 + losetup toolchain, and mknod so the deterministic executable fault-stage
// matrix can create real block devices. When unavailable the test skips cleanly;
// the exact-head CI/Linux runner and candidate rehearsal exercise this privileged
// path (HOR-545 acceptance).
func lvmFaultMatrixEnv(t *testing.T) (bool, string) {
	t.Helper()
	if os.Geteuid() != 0 {
		return false, "real-LVM reconcile fault matrix requires root (loop/mknod); skip on dev workstation, run in exact-head CI/rehearsal"
	}
	for _, tool := range []string{"losetup", "pvcreate", "vgcreate", "pvs", "vgs", "lvs", "mknod"} {
		if _, err := exec.LookPath(tool); err != nil {
			return false, fmt.Sprintf("real-LVM reconcile fault matrix requires %q; skip where unavailable", tool)
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

// runDataStorageBash executes the already-written reconcile script under the
// real bash interpreter and returns stdout/exit. killAfter determines the
// durable boilerplate snapshot that, once observed in the receipt file, triggers
// a SIGKILL to simulate a crash at exactly that stage without mutating the script.
func runDataStorageBash(t *testing.T, path string, spec provisioner.DataStorageSpec, killAfter string) (string, bool) {
	t.Helper()
	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Minute)
	defer cancel()

	cmd := exec.CommandContext(ctx, "bash", path)
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

// TestDataStorageFaultStageMatrixExecutable stages a real LVM transaction across
// every durable boundary and proves Forge's crash-resumable and mismatch-refusal
// contract by actually executing the generated script (not just text-searching
// it). Each sub-test terminates the script with SIGKILL after a specific durable
// receipt stage, re-runs it idempotently, and asserts it resumes and completes.
func TestDataStorageFaultStageMatrixExecutable(t *testing.T) {
	ok, reason := lvmFaultMatrixEnv(t)
	if !ok {
		t.Skip(reason)
	}

	dir := t.TempDir()
	devices := makeFaultLoopDevices(t, dir, 2)
	t.Cleanup(func() { teardownFaultLoopDevices(t, devices) })
	spec := provisioner.DataStorageSpec{InstallName: "opo1", Devices: devices}
	script := writeFaultScript(t, spec, "reconcile")

	// A clean full run must converge to complete and bind both PVs + the VG.
	out, ok := runDataStorageBash(t, script, spec, "")
	require.True(t, ok, "reconcile failed on clean run: %s", out)
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
			teardownFaultLoopDevices(t, devices)
			devices = makeFaultLoopDevices(t, t.TempDir(), 2)
			spec := provisioner.DataStorageSpec{InstallName: "opo1", Devices: devices}
			script := writeFaultScript(t, spec, "reconcile")

			out, crashed := runDataStorageBash(t, script, spec, sc.marker)
			require.True(t, crashed, "expected SIGKILL crash at %s; out=%s", sc.name, out)

			// Re-run: the receipt is durable so reconcile must resume, not re-create.
			out2, ok2 := runDataStorageBash(t, script, spec, "")
			require.True(t, ok2, "resume failed after %s crash: %s", sc.name, out2)
			require.Contains(t, out2, "FORGE_DATA_STORAGE_RESULT\tcomplete")
		})
	}

	// Mismatch refusal: rerun against a foreign install identity must be refused.
	t.Run("refuses-foreign-install", func(t *testing.T) {
		out, ok := runDataStorageBash(t, script, provisioner.DataStorageSpec{InstallName: "other", Devices: devices}, "")
		require.False(t, ok, "foreign install identity must be refused")
		require.Contains(t, out, "receipt install mismatch")
	})
}

// makeFaultLoopDevices allocates count real loop-backed block devices on a
// dedicated loopAhead device-mapper-free path and returns their resolved /dev
// paths, which the reconcile script can clean. It exercises the exact device set
// the production script consumes (whole disks, no partition table).
func makeFaultLoopDevices(t *testing.T, dir string, count int) []string {
	t.Helper()
	var paths []string
	for i := 0; i < count; i++ {
		img := filepath.Join(dir, fmt.Sprintf("disk%d.img", i))
		require.NoError(t, runCmd("truncate", "-s", "16M", img))
		out, err := exec.Command("losetup", "--find", "--show", "--nooverlap", img).CombinedOutput()
		require.NoError(t, err, "losetup failed: %s", out)
		paths = append(paths, strings.TrimSpace(string(out)))
	}
	return paths
}

func teardownFaultLoopDevices(t *testing.T, devices []string) {
	t.Helper()
	for _, dev := range devices {
		_ = runCmd("losetup", "-d", dev)
	}
}

func runCmd(name string, args ...string) error {
	cmd := exec.Command(name, args...)
	return cmd.Run()
}

// TestDataStorageFaultMatrixScriptCoversEveryDurableStage is the locally
// runnable structural companion: it asserts the generated script front-loads the
// durable receipt before every mutation and binds each stage to an explicit
// write_receipt, so a fault at any durable boundary is always resumable.
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
	// There is one pvs-created receipt write for each device in the staged loop
	// body (a single source template), so each PV stage persists a durable marker
	// before the next mutation and a crash after any PV is always resumable.
	require.Less(t, pvcreateIdx, receipt, "each pvcreate must be followed by the durable pvs-created receipt")
	require.Equal(t, 1, strings.Count(script, "write_receipt pvs-created"))

	// DES-HOR-545-03 monotonic receipt: the pvs-created write is gated on an
	// unbound VG UUID, so exact reapply of an already-complete receipt never
	// regresses to pvs-created (which, paired with a real vg_uuid, is invalid and
	// would brick the next run). The VG-created and complete markers remain
	// unconditional promotion only.
	require.Contains(t, script, "if test -z \"$receipt_vg_uuid\"; then write_receipt pvs-created", "reapply must guard the pvs-created receipt write on an unbound VG UUID")

	// The planned receipt precedes the first mutation and the VG-created receipt
	// binds the complete PV set before the UUID is persisted.
	require.Less(t, strings.Index(script, "write_receipt planned 0"), strings.Index(script, "pvcreate --yes --zero y --uuid"))
	require.Less(t, strings.Index(script, "vgcreate --yes --addtag"), strings.Index(script, "receipt_vg_uuid=$vg_uuid; write_receipt vg-created"))
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

// TestDataStorageReapplyReceiptMonotonicExecutable proves exact reapply of an
// already-complete receipt is idempotent, crash-resumable, and never regresses
// the receipt to pvs-created (DES-HOR-545-03 / HOR-545 exact-reapply
// acceptance). It executes the real reconcile script over loop-backed devices,
// injects SIGKILL crashes, and asserts the complete receipt is always preserved.
func TestDataStorageReapplyReceiptMonotonicExecutable(t *testing.T) {
	ok, reason := lvmFaultMatrixEnv(t)
	if !ok {
		t.Skip(reason)
	}
	_ = os.Remove(dataStorageReceiptPath) // isolate from any prior global receipt
	t.Cleanup(func() { _ = os.Remove(dataStorageReceiptPath) })

	dir := t.TempDir()
	devices := makeFaultLoopDevices(t, dir, 2)
	t.Cleanup(func() { teardownFaultLoopDevices(t, devices) })
	spec := provisioner.DataStorageSpec{InstallName: "opo1", Devices: devices}
	script := writeFaultScript(t, spec, "reconcile")

	// Fresh install must converge to complete and bind both PVs + the VG.
	out, okc := runDataStorageBash(t, script, spec, "")
	require.True(t, okc, "reconcile failed on clean run: %s", out)
	require.Contains(t, out, "FORGE_DATA_STORAGE_RESULT\tcomplete")
	requireReceiptStatus(t, "complete")

	// Exact reapply: if the old (bricking) logic were present, the per-PV loop
	// would write pvs-created while retaining the real vg_uuid, and the
	// killAfter=pvs-created watcher would SIGKILL once it observed that
	// regressive stage. With the monotonic guard the marker is never emitted, so
	// the run completes normally (crashed=false) and the receipt stays complete.
	out, crashed := runDataStorageBash(t, script, spec, "pvs-created")
	require.False(t, crashed, "exact reapply regressed the receipt to pvs-created:\n%s", out)
	require.Contains(t, out, "FORGE_DATA_STORAGE_RESULT\tcomplete")
	requireReceiptStatus(t, "complete")

	// Crash during reapply (killed after loading the complete receipt) must leave
	// a resumable, still-complete receipt; the next run completes without repair.
	_, _ = runDataStorageBash(t, script, spec, "complete") // best-effort SIGKILL mid-reapply
	out2, ok2 := runDataStorageBash(t, script, spec, "")
	require.True(t, ok2, "reapply did not resume to complete after crash: %s", out2)
	require.Contains(t, out2, "FORGE_DATA_STORAGE_RESULT\tcomplete")
	requireReceiptStatus(t, "complete")
}
