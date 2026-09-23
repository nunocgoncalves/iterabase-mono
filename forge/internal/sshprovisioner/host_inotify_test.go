package sshprovisioner

import (
	"context"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestInspectHostInotifyRunsOneReadOnlyRemoteScript(t *testing.T) {
	var commands []string
	addr, cfg, cleanup := startFakeSSH(t, func(cmd string) (string, int) {
		commands = append(commands, cmd)
		return "128|none|false|false|none|none|false\n", 0
	})
	defer cleanup()

	p := newProvisioner(t, addr, cfg)
	defer p.Close()
	state, err := p.InspectHostInotify(context.Background())
	require.NoError(t, err)
	require.Len(t, commands, 1)
	assert.True(t, strings.HasPrefix(commands[0], "sudo bash -ceu "))
	assert.Contains(t, commands[0], hostInotifyProcPath)
	assert.Contains(t, commands[0], hostInotifyDropInPath)
	assert.NotContains(t, commands[0], "-q -w", "observation must not mutate live state")
	assert.NotContains(t, commands[0], "mktemp", "observation must not write")
	assert.Equal(t, 128, state.Effective)
	assert.False(t, state.Ready())
}

func TestReconcileHostInotifyRunsOneFailClosedRemoteScript(t *testing.T) {
	var commands []string
	addr, cfg, cleanup := startFakeSSH(t, func(cmd string) (string, int) {
		commands = append(commands, cmd)
		return "8192|8192|true|true|0:0|644|true\n", 0
	})
	defer cleanup()

	p := newProvisioner(t, addr, cfg)
	defer p.Close()
	state, err := p.ReconcileHostInotify(context.Background())
	require.NoError(t, err)
	require.Len(t, commands, 1)
	assert.True(t, strings.HasPrefix(commands[0], "sudo bash -ceu "))
	assert.Contains(t, commands[0], hostInotifyProcPath)
	assert.Contains(t, commands[0], hostInotifyDropInPath)
	assert.Contains(t, commands[0], "sysctl")
	assert.Contains(t, commands[0], "mktemp")
	assert.True(t, state.Ready())
}

func TestReconcileHostInotifyRefusesUnreadyReadBack(t *testing.T) {
	addr, cfg, cleanup := startFakeSSH(t, func(string) (string, int) {
		return "128|none|false|false|none|none|false\n", 0
	})
	defer cleanup()

	p := newProvisioner(t, addr, cfg)
	defer p.Close()
	state, err := p.ReconcileHostInotify(context.Background())
	require.ErrorContains(t, err, "read-back is not ready")
	require.NotNil(t, state)
	assert.False(t, state.Ready())
}

func TestReconcileHostInotifyPropagatesRemoteRefusalEvidence(t *testing.T) {
	addr, cfg, cleanup := startFakeSSHWithResult(t, func(string) sshCommandResult {
		return sshCommandResult{stdout: "", stderr: "host-inotify refusal: read-back effective value 128 is below the required 8192\n", code: 42}
	})
	defer cleanup()

	p := newProvisioner(t, addr, cfg)
	defer p.Close()
	_, err := p.ReconcileHostInotify(context.Background())
	require.ErrorContains(t, err, "read-back effective value 128 is below the required 8192")
}

func TestParseHostInotifyState(t *testing.T) {
	t.Run("canonical", func(t *testing.T) {
		state, err := parseHostInotifyState("8192|8192|true|true|0:0|644|true\n")
		require.NoError(t, err)
		assert.Equal(t, 8192, state.Effective)
		assert.Equal(t, 8192, state.Persisted)
		assert.Equal(t, "0:0", state.DropInOwner)
		assert.Equal(t, "644", state.DropInMode)
		assert.True(t, state.Ready())
	})

	t.Run("missing", func(t *testing.T) {
		state, err := parseHostInotifyState("128|none|false|false|none|none|false\n")
		require.NoError(t, err)
		assert.Equal(t, 128, state.Effective)
		assert.Zero(t, state.Persisted)
		assert.Empty(t, state.DropInOwner)
		assert.False(t, state.Ready())
	})

	t.Run("symlink", func(t *testing.T) {
		state, err := parseHostInotifyState("8192|none|true|false|none|none|false\n")
		require.NoError(t, err)
		assert.True(t, state.DropInPresent)
		assert.False(t, state.DropInRegular)
		assert.False(t, state.Ready())
	})

	t.Run("higher-live-value", func(t *testing.T) {
		state, err := parseHostInotifyState("16384|8192|true|true|0:0|644|true\n")
		require.NoError(t, err)
		assert.Equal(t, 16384, state.Effective)
		assert.True(t, state.Ready(), "a live value above the requirement is ready and must never be lowered")
	})

	for name, observation := range map[string]string{
		"wrong-field-count":        "8192|8192|true|true|0:0|644",
		"non-numeric-effective":    "many|none|false|false|none|none|false",
		"non-numeric-persisted":    "8192|lots|true|true|0:0|644|true",
		"non-boolean-presence":     "8192|8192|yes|true|0:0|644|true",
		"non-boolean-regular":      "8192|8192|true|yes|0:0|644|true",
		"non-boolean-canonical":    "8192|8192|true|true|0:0|644|maybe",
		"canonical-without-file":   "8192|8192|false|false|none|none|true",
		"empty-observation":        "",
		"unexpected-pipe-content":  "8192|8192|true|true|0:0|644|true|extra",
		"negative-effective-value": "-1|none|false|false|none|none|false",
	} {
		t.Run(name, func(t *testing.T) {
			_, err := parseHostInotifyState(observation)
			require.Error(t, err)
		})
	}
}

func TestHostInotifyScriptsAreValidBash(t *testing.T) {
	for name, script := range map[string]string{
		"inspect":   hostInotifyInspectScript(hostInotifyProcPath, hostInotifyDropInPath, hostInotifyOwner),
		"reconcile": hostInotifyReconcileScript(hostInotifyProcPath, hostInotifyDropInPath, hostInotifySysctlCommand, hostInotifyOwner),
	} {
		t.Run(name, func(t *testing.T) {
			cmd := exec.Command("bash", "-n")
			cmd.Stdin = strings.NewReader(script)
			out, err := cmd.CombinedOutput()
			require.NoError(t, err, string(out))
		})
	}
}

// hostInotifyFixture runs the exact production scripts against unprivileged
// local files with deterministic shims for the privileged tools.
type hostInotifyFixture struct {
	dir             string
	procPath        string
	dropinPath      string
	inspectScript   string
	reconcileScript string
	binDir          string
	owner           string
	mvLog           string
	syncLog         string
	chownLog        string
	sysctlLog       string
}

func newHostInotifyFixture(t *testing.T, effective string, dropin *string, mode os.FileMode) *hostInotifyFixture {
	t.Helper()
	dir := t.TempDir()
	fixture := &hostInotifyFixture{
		dir:        dir,
		procPath:   filepath.Join(dir, "proc-inotify-instances"),
		dropinPath: filepath.Join(dir, "90-iterabase-k3s-inotify.conf"),
		binDir:     filepath.Join(dir, "bin"),
		owner:      fmt.Sprintf("%d:%d", os.Getuid(), os.Getgid()),
		mvLog:      filepath.Join(dir, "mv.log"),
		syncLog:    filepath.Join(dir, "sync.log"),
		chownLog:   filepath.Join(dir, "chown.log"),
		sysctlLog:  filepath.Join(dir, "sysctl.log"),
	}
	require.NoError(t, os.Mkdir(fixture.binDir, 0o700))
	require.NoError(t, os.WriteFile(fixture.procPath, []byte(effective+"\n"), 0o600))
	if dropin != nil {
		require.NoError(t, os.WriteFile(fixture.dropinPath, []byte(*dropin), mode))
		require.NoError(t, os.Chmod(fixture.dropinPath, mode))
	}

	writeHostInotifyFixtureTools(t, fixture)
	fixture.inspectScript = writeFixtureScript(t, dir, "inspect.sh",
		"set -eu\n"+hostInotifyInspectScript(fixture.procPath, fixture.dropinPath, fixture.owner)+"\n")
	fixture.reconcileScript = writeFixtureScript(t, dir, "reconcile.sh",
		"set -eu\n"+hostInotifyReconcileScript(fixture.procPath, fixture.dropinPath, filepath.Join(fixture.binDir, "sysctl"), fixture.owner)+"\n")
	return fixture
}

func writeFixtureScript(t *testing.T, dir, name, content string) string {
	t.Helper()
	path := filepath.Join(dir, name)
	require.NoError(t, os.WriteFile(path, []byte(content), 0o700))
	return path
}

func writeHostInotifyFixtureTools(t *testing.T, fixture *hostInotifyFixture) {
	t.Helper()
	realStat, err := exec.LookPath("stat")
	require.NoError(t, err)
	realMV, err := exec.LookPath("mv")
	require.NoError(t, err)

	writeExecutable := func(name, content string) {
		t.Helper()
		require.NoError(t, os.WriteFile(filepath.Join(fixture.binDir, name), []byte(content), 0o700))
	}
	writeExecutable("stat", fmt.Sprintf(`#!/bin/sh
real=%s
format=$2
file=$4
if "$real" -c "$format" -- "$file" >/dev/null 2>&1; then
  exec "$real" -c "$format" -- "$file"
fi
case "$format" in
  '%%u') bsd_format='%%u' ;;
  '%%g') bsd_format='%%g' ;;
  '%%a') bsd_format='%%Lp' ;;
  '%%u:%%g') bsd_format='%%u:%%g' ;;
  *) exit 64 ;;
esac
exec "$real" -f "$bsd_format" "$file"
`, shellQuote(realStat)))
	writeExecutable("sync", `#!/bin/sh
printf '%s\n' "$1" >> "$FORGE_INOTIFY_SYNC_LOG"
case "${FORGE_INOTIFY_SYNC_MODE:-success}" in
  success) exit 0 ;;
  fail) exit 8 ;;
  *) exit 64 ;;
esac
`)
	writeExecutable("mv", fmt.Sprintf(`#!/bin/sh
real=%s
printf 'mv\n' >> "$FORGE_INOTIFY_MV_LOG"
case "${FORGE_INOTIFY_MV_MODE:-real}" in
  fail) exit 9 ;;
  real-fail) "$real" "$@"; exit 9 ;;
  noop) exit 0 ;;
  real) exec "$real" "$@" ;;
  *) exit 64 ;;
esac
`, shellQuote(realMV)))
	writeExecutable("chown", `#!/bin/sh
printf 'chown\n' >> "$FORGE_INOTIFY_CHOWN_LOG"
case "${FORGE_INOTIFY_CHOWN_MODE:-success}" in
  success) exit 0 ;;
  fail) exit 7 ;;
  *) exit 64 ;;
esac
`)
	writeExecutable("sysctl", `#!/bin/sh
printf 'sysctl\n' >> "$FORGE_INOTIFY_SYSCTL_LOG"
case "${FORGE_INOTIFY_SYSCTL_MODE:-success}" in
  success)
    test "$#" = 3 && test "$1" = -q && test "$2" = -w || exit 64
    case "$3" in fs.inotify.max_user_instances=*) ;; *) exit 64 ;; esac
    printf '%s\n' "${3#*=}" > "$FORGE_INOTIFY_PROC_FILE"
    ;;
  noop) exit 0 ;;
  fail) exit 7 ;;
  *) exit 64 ;;
esac
`)
}

// run executes one fixture script. Empty modes use the success defaults.
func (f *hostInotifyFixture) run(t *testing.T, script, mvMode, syncMode, chownMode, sysctlMode string) (string, error) {
	t.Helper()
	values := map[string]string{
		"PATH":                      f.binDir + string(os.PathListSeparator) + os.Getenv("PATH"),
		"FORGE_INOTIFY_MV_LOG":      f.mvLog,
		"FORGE_INOTIFY_MV_MODE":     mvMode,
		"FORGE_INOTIFY_SYNC_LOG":    f.syncLog,
		"FORGE_INOTIFY_SYNC_MODE":   syncMode,
		"FORGE_INOTIFY_CHOWN_LOG":   f.chownLog,
		"FORGE_INOTIFY_CHOWN_MODE":  chownMode,
		"FORGE_INOTIFY_SYSCTL_LOG":  f.sysctlLog,
		"FORGE_INOTIFY_SYSCTL_MODE": sysctlMode,
		"FORGE_INOTIFY_PROC_FILE":   f.procPath,
	}
	cmd := exec.Command("bash", script)
	cmd.Env = replaceHostSwapFixtureEnv(os.Environ(), values)
	out, err := cmd.CombinedOutput()
	return string(out), err
}

func (f *hostInotifyFixture) inspect(t *testing.T) (string, error) {
	t.Helper()
	return f.run(t, f.inspectScript, "real", "success", "success", "success")
}

func (f *hostInotifyFixture) reconcile(t *testing.T, mvMode, syncMode, chownMode, sysctlMode string) (string, error) {
	t.Helper()
	return f.run(t, f.reconcileScript, mvMode, syncMode, chownMode, sysctlMode)
}

func hostInotifyFixtureLogCount(t *testing.T, path string) int {
	t.Helper()
	data, err := os.ReadFile(path)
	if os.IsNotExist(err) {
		return 0
	}
	require.NoError(t, err)
	return len(strings.Fields(string(data)))
}

func hostInotifyFixtureEffective(t *testing.T, fixture *hostInotifyFixture) string {
	t.Helper()
	data, err := os.ReadFile(fixture.procPath)
	require.NoError(t, err)
	return strings.TrimSpace(string(data))
}

// canonicalDropIn returns the exact persistent content Forge owns.
func canonicalDropIn() string { return hostInotifyCanonicalBody + "\n" }

func TestHostInotifyInspectReportsEveryState(t *testing.T) {
	t.Run("default-low-missing-drop-in", func(t *testing.T) {
		fixture := newHostInotifyFixture(t, "128", nil, 0o644)
		out, err := fixture.inspect(t)
		require.NoError(t, err)
		assert.Equal(t, "128|none|false|false|none|none|false", strings.TrimSpace(out))
	})

	t.Run("canonical", func(t *testing.T) {
		body := canonicalDropIn()
		fixture := newHostInotifyFixture(t, "8192", &body, 0o644)
		out, err := fixture.inspect(t)
		require.NoError(t, err)
		assert.Equal(t, "8192|8192|true|true|"+fixture.owner+"|644|true", strings.TrimSpace(out))
	})

	t.Run("drifted-content", func(t *testing.T) {
		body := "fs.inotify.max_user_instances = 4096\n"
		fixture := newHostInotifyFixture(t, "4096", &body, 0o644)
		out, err := fixture.inspect(t)
		require.NoError(t, err)
		assert.Equal(t, "4096|4096|true|true|"+fixture.owner+"|644|false", strings.TrimSpace(out))
	})

	t.Run("mode-drift", func(t *testing.T) {
		body := canonicalDropIn()
		fixture := newHostInotifyFixture(t, "8192", &body, 0o600)
		out, err := fixture.inspect(t)
		require.NoError(t, err)
		assert.Equal(t, "8192|8192|true|true|"+fixture.owner+"|600|false", strings.TrimSpace(out))
	})

	t.Run("symlink", func(t *testing.T) {
		dir := t.TempDir()
		target := filepath.Join(dir, "real.conf")
		require.NoError(t, os.WriteFile(target, []byte(canonicalDropIn()), 0o644))
		fixture := newHostInotifyFixture(t, "8192", nil, 0o644)
		require.NoError(t, os.Symlink(target, fixture.dropinPath))
		out, err := fixture.inspect(t)
		require.NoError(t, err)
		assert.Equal(t, "8192|none|true|false|none|none|false", strings.TrimSpace(out))
	})

	t.Run("unreadable-effective-state", func(t *testing.T) {
		fixture := newHostInotifyFixture(t, "128", nil, 0o644)
		require.NoError(t, os.Remove(fixture.procPath))
		out, err := fixture.inspect(t)
		require.Error(t, err)
		assert.Contains(t, out, "effective inotify state")
	})

	t.Run("malformed-effective-state", func(t *testing.T) {
		fixture := newHostInotifyFixture(t, "many", nil, 0o644)
		out, err := fixture.inspect(t)
		require.Error(t, err)
		assert.Contains(t, out, "not a decimal integer")
	})
}

func TestHostInotifyReconcileHealsDefaultLowHost(t *testing.T) {
	fixture := newHostInotifyFixture(t, "128", nil, 0o644)
	out, err := fixture.reconcile(t, "real", "success", "success", "success")
	require.NoError(t, err, out)
	assert.Equal(t, "8192|8192|true|true|"+fixture.owner+"|644|true", strings.TrimSpace(out))
	assert.Equal(t, "8192", hostInotifyFixtureEffective(t, fixture))

	body, err := os.ReadFile(fixture.dropinPath)
	require.NoError(t, err)
	assert.Equal(t, canonicalDropIn(), string(body))
	info, err := os.Stat(fixture.dropinPath)
	require.NoError(t, err)
	assert.Equal(t, os.FileMode(0o644), info.Mode().Perm())
	assert.Equal(t, 1, hostInotifyFixtureLogCount(t, fixture.mvLog))
	assert.Equal(t, 1, hostInotifyFixtureLogCount(t, fixture.sysctlLog))
	assert.Equal(t, 1, hostInotifyFixtureLogCount(t, fixture.chownLog))
	assert.Equal(t, 3, hostInotifyFixtureLogCount(t, fixture.syncLog), "staged file, drop-in, and directory are synced")
}

func TestHostInotifyReconcileIsIdempotent(t *testing.T) {
	fixture := newHostInotifyFixture(t, "128", nil, 0o644)
	out, err := fixture.reconcile(t, "real", "success", "success", "success")
	require.NoError(t, err, out)

	out, err = fixture.reconcile(t, "real", "success", "success", "success")
	require.NoError(t, err, out)
	assert.Equal(t, "8192|8192|true|true|"+fixture.owner+"|644|true", strings.TrimSpace(out))
	assert.Equal(t, 1, hostInotifyFixtureLogCount(t, fixture.mvLog), "a converged run must not rewrite the drop-in")
	assert.Equal(t, 1, hostInotifyFixtureLogCount(t, fixture.sysctlLog), "a converged run must not touch live state")
	assert.Equal(t, 5, hostInotifyFixtureLogCount(t, fixture.syncLog), "a converged run still proves drop-in durability")
}

func TestHostInotifyReconcileAlreadyConformingIsNoOp(t *testing.T) {
	body := canonicalDropIn()
	fixture := newHostInotifyFixture(t, "8192", &body, 0o644)
	out, err := fixture.reconcile(t, "real", "success", "success", "success")
	require.NoError(t, err, out)
	assert.Equal(t, "8192|8192|true|true|"+fixture.owner+"|644|true", strings.TrimSpace(out))
	assert.Zero(t, hostInotifyFixtureLogCount(t, fixture.mvLog))
	assert.Zero(t, hostInotifyFixtureLogCount(t, fixture.sysctlLog))
	assert.Zero(t, hostInotifyFixtureLogCount(t, fixture.chownLog))
	assert.Equal(t, 2, hostInotifyFixtureLogCount(t, fixture.syncLog))
}

func TestHostInotifyReconcilePersistsMissingDropInWithoutLoweringLiveCapacity(t *testing.T) {
	fixture := newHostInotifyFixture(t, "16384", nil, 0o644)
	out, err := fixture.reconcile(t, "real", "success", "success", "success")
	require.NoError(t, err, out)
	assert.Equal(t, "16384|8192|true|true|"+fixture.owner+"|644|true", strings.TrimSpace(out))
	assert.Equal(t, "16384", hostInotifyFixtureEffective(t, fixture), "a sufficient live value must never be lowered")
	assert.Zero(t, hostInotifyFixtureLogCount(t, fixture.sysctlLog))
}

func TestHostInotifyReconcileConvergesPersistedAndLiveDrift(t *testing.T) {
	t.Run("persisted-lower-than-live", func(t *testing.T) {
		body := "fs.inotify.max_user_instances = 4096\n"
		fixture := newHostInotifyFixture(t, "8192", &body, 0o644)
		out, err := fixture.reconcile(t, "real", "success", "success", "success")
		require.NoError(t, err, out)
		assert.Equal(t, "8192|8192|true|true|"+fixture.owner+"|644|true", strings.TrimSpace(out))
		assert.Equal(t, 1, hostInotifyFixtureLogCount(t, fixture.mvLog))
		assert.Zero(t, hostInotifyFixtureLogCount(t, fixture.sysctlLog))
	})

	t.Run("live-lower-than-persisted", func(t *testing.T) {
		body := canonicalDropIn()
		fixture := newHostInotifyFixture(t, "128", &body, 0o644)
		out, err := fixture.reconcile(t, "real", "success", "success", "success")
		require.NoError(t, err, out)
		assert.Equal(t, "8192|8192|true|true|"+fixture.owner+"|644|true", strings.TrimSpace(out))
		assert.Zero(t, hostInotifyFixtureLogCount(t, fixture.mvLog))
		assert.Equal(t, 1, hostInotifyFixtureLogCount(t, fixture.sysctlLog))
	})

	t.Run("mode-drift", func(t *testing.T) {
		body := canonicalDropIn()
		fixture := newHostInotifyFixture(t, "8192", &body, 0o600)
		out, err := fixture.reconcile(t, "real", "success", "success", "success")
		require.NoError(t, err, out)
		assert.Equal(t, "8192|8192|true|true|"+fixture.owner+"|644|true", strings.TrimSpace(out))
		assert.Equal(t, 1, hostInotifyFixtureLogCount(t, fixture.mvLog))
	})
}

func TestHostInotifyReconcileFailureIsActionableAndFailClosed(t *testing.T) {
	t.Run("staged-write-failure-keeps-original-and-retries", func(t *testing.T) {
		body := "fs.inotify.max_user_instances = 4096\n"
		fixture := newHostInotifyFixture(t, "4096", &body, 0o644)
		out, err := fixture.reconcile(t, "fail", "success", "success", "success")
		require.Error(t, err)
		assert.Contains(t, out, "cannot atomically replace the inotify drop-in")
		got, readErr := os.ReadFile(fixture.dropinPath)
		require.NoError(t, readErr)
		assert.Equal(t, body, string(got), "the previous drop-in must survive a failed replacement")
		assert.Equal(t, "4096", hostInotifyFixtureEffective(t, fixture))

		out, err = fixture.reconcile(t, "real", "success", "success", "success")
		require.NoError(t, err, out)
		assert.Equal(t, "8192|8192|true|true|"+fixture.owner+"|644|true", strings.TrimSpace(out))
		assert.Equal(t, 2, hostInotifyFixtureLogCount(t, fixture.mvLog), "the retry legitimately rewrites the drifted drop-in")
		assert.Equal(t, 4, hostInotifyFixtureLogCount(t, fixture.syncLog), "the retry syncs the staged file, the drop-in, and its directory")
	})

	t.Run("completed-rename-with-lost-response-recovers", func(t *testing.T) {
		fixture := newHostInotifyFixture(t, "128", nil, 0o644)
		out, err := fixture.reconcile(t, "real-fail", "success", "success", "success")
		require.Error(t, err)
		assert.Contains(t, out, "cannot atomically replace the inotify drop-in")
		body, readErr := os.ReadFile(fixture.dropinPath)
		require.NoError(t, readErr)
		assert.Equal(t, canonicalDropIn(), string(body), "rename completed before its response was lost")

		out, err = fixture.reconcile(t, "real", "success", "success", "success")
		require.NoError(t, err, out)
		assert.Equal(t, 1, hostInotifyFixtureLogCount(t, fixture.mvLog), "the retry must not rewrite the converged drop-in")
		assert.Equal(t, 1, hostInotifyFixtureLogCount(t, fixture.sysctlLog))
	})

	t.Run("chown-failure-persists-nothing", func(t *testing.T) {
		fixture := newHostInotifyFixture(t, "128", nil, 0o644)
		out, err := fixture.reconcile(t, "real", "success", "fail", "success")
		require.Error(t, err)
		assert.Contains(t, out, "cannot set inotify drop-in ownership")
		if _, statErr := os.Stat(fixture.dropinPath); !os.IsNotExist(statErr) {
			t.Fatalf("failed ownership staging must not publish a drop-in: %v", statErr)
		}
		assert.Equal(t, "128", hostInotifyFixtureEffective(t, fixture))

		out, err = fixture.reconcile(t, "real", "success", "success", "success")
		require.NoError(t, err, out)
		assert.Equal(t, "8192|8192|true|true|"+fixture.owner+"|644|true", strings.TrimSpace(out))
	})

	t.Run("live-apply-failure-keeps-persisted-drop-in", func(t *testing.T) {
		fixture := newHostInotifyFixture(t, "128", nil, 0o644)
		out, err := fixture.reconcile(t, "real", "success", "success", "fail")
		require.Error(t, err)
		assert.Contains(t, out, "cannot apply the live inotify instance limit 8192")
		body, readErr := os.ReadFile(fixture.dropinPath)
		require.NoError(t, readErr)
		assert.Equal(t, canonicalDropIn(), string(body))
		assert.Equal(t, "128", hostInotifyFixtureEffective(t, fixture))

		out, err = fixture.reconcile(t, "real", "success", "success", "success")
		require.NoError(t, err, out)
		assert.Equal(t, "8192|8192|true|true|"+fixture.owner+"|644|true", strings.TrimSpace(out))
		assert.Equal(t, 1, hostInotifyFixtureLogCount(t, fixture.mvLog), "the persisted drop-in must not be rewritten on retry")
	})

	t.Run("failed-read-back-fails-closed", func(t *testing.T) {
		fixture := newHostInotifyFixture(t, "128", nil, 0o644)
		out, err := fixture.reconcile(t, "real", "success", "success", "noop")
		require.Error(t, err)
		assert.Contains(t, out, "read-back effective inotify value 128 is below the required 8192")
		assert.Equal(t, "128", hostInotifyFixtureEffective(t, fixture))

		out, err = fixture.reconcile(t, "real", "success", "success", "success")
		require.NoError(t, err, out)
		assert.Equal(t, "8192|8192|true|true|"+fixture.owner+"|644|true", strings.TrimSpace(out))
	})

	t.Run("symlink-drop-in-is-refused", func(t *testing.T) {
		dir := t.TempDir()
		target := filepath.Join(dir, "real.conf")
		require.NoError(t, os.WriteFile(target, []byte("fs.inotify.max_user_instances = 128\n"), 0o644))
		fixture := newHostInotifyFixture(t, "128", nil, 0o644)
		require.NoError(t, os.Symlink(target, fixture.dropinPath))
		out, err := fixture.reconcile(t, "real", "success", "success", "success")
		require.Error(t, err)
		assert.Contains(t, out, "is not a regular file")
		got, readErr := os.ReadFile(target)
		require.NoError(t, readErr)
		assert.Equal(t, "fs.inotify.max_user_instances = 128\n", string(got), "the symlink target must never be followed or rewritten")
		assert.Equal(t, "128", hostInotifyFixtureEffective(t, fixture))
	})
}
