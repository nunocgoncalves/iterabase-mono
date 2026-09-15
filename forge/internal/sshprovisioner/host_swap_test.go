package sshprovisioner

import (
	"context"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"syscall"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestEnsureHostSwapDisabledRunsOneFailClosedRemoteScript(t *testing.T) {
	var commands []string
	addr, cfg, cleanup := startFakeSSH(t, func(cmd string) (string, int) {
		commands = append(commands, cmd)
		return "", 0
	})
	defer cleanup()

	p := newProvisioner(t, addr, cfg)
	defer p.Close()
	require.NoError(t, p.EnsureHostSwapDisabled(context.Background()))
	require.Len(t, commands, 1)
	assert.True(t, strings.HasPrefix(commands[0], "sudo bash -ceu "))
	assert.Contains(t, commands[0], hostActiveSwapsPath)
	assert.Contains(t, commands[0], hostFstabPath)
	assert.Contains(t, commands[0], "swapoff")
	assert.Contains(t, commands[0], "forge-disabled-swap")
}

func TestHostSwapScriptIsValidBash(t *testing.T) {
	cmd := exec.Command("bash", "-n")
	cmd.Stdin = strings.NewReader(hostSwapScript(hostActiveSwapsPath, hostFstabPath, "swapoff"))
	out, err := cmd.CombinedOutput()
	require.NoError(t, err, string(out))
}

type hostSwapFixture struct {
	activePath string
	fstabPath  string
	scriptPath string
	binDir     string
	swapLog    string
	mvLog      string
}

func newHostSwapFixture(t *testing.T, active, fstab string, mode os.FileMode) *hostSwapFixture {
	t.Helper()
	dir := t.TempDir()
	fixture := &hostSwapFixture{
		activePath: filepath.Join(dir, "proc-swaps"),
		fstabPath:  filepath.Join(dir, "fstab"),
		scriptPath: filepath.Join(dir, "host-swap.sh"),
		binDir:     filepath.Join(dir, "bin"),
		swapLog:    filepath.Join(dir, "swapoff.log"),
		mvLog:      filepath.Join(dir, "mv.log"),
	}
	require.NoError(t, os.Mkdir(fixture.binDir, 0o700))
	require.NoError(t, os.WriteFile(fixture.activePath, []byte(active), 0o600))
	require.NoError(t, os.WriteFile(fixture.fstabPath, []byte(fstab), mode))
	require.NoError(t, os.Chmod(fixture.fstabPath, mode))

	writeHostSwapFixtureTools(t, fixture)
	script := "set -eu\n" + hostSwapScript(fixture.activePath, fixture.fstabPath, filepath.Join(fixture.binDir, "swapoff")) + "\n"
	require.NoError(t, os.WriteFile(fixture.scriptPath, []byte(script), 0o600))
	return fixture
}

func writeHostSwapFixtureTools(t *testing.T, fixture *hostSwapFixture) {
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
  '%%u:%%g:%%a') bsd_format='%%u:%%g:%%Lp' ;;
  *) exit 64 ;;
esac
exec "$real" -f "$bsd_format" "$file"
`, shellQuote(realStat)))
	writeExecutable("sync", "#!/bin/sh\nexit 0\n")
	writeExecutable("mv", fmt.Sprintf(`#!/bin/sh
printf 'mv\n' >> "$FORGE_SWAP_MV_LOG"
case "${FORGE_SWAP_MV_MODE:-real}" in
  fail) exit 9 ;;
  noop) exit 0 ;;
  real) exec %s "$@" ;;
  *) exit 64 ;;
esac
`, shellQuote(realMV)))
	writeExecutable("swapoff", `#!/bin/sh
printf 'swapoff\n' >> "$FORGE_SWAPOFF_LOG"
test "$#" = 1 && test "$1" = --all || exit 64
case "${FORGE_SWAPOFF_MODE:-clear}" in
  fail) exit 7 ;;
  noop) exit 0 ;;
  clear) printf 'Filename\tType\tSize\tUsed\tPriority\n' > "$FORGE_SWAP_ACTIVE_FILE" ;;
  *) exit 64 ;;
esac
`)
}

func (f *hostSwapFixture) run(t *testing.T, swapoffMode, mvMode string) (string, error) {
	t.Helper()
	cmd := exec.Command("bash", f.scriptPath)
	values := map[string]string{
		"PATH":                   f.binDir + string(os.PathListSeparator) + os.Getenv("PATH"),
		"FORGE_SWAP_ACTIVE_FILE": f.activePath,
		"FORGE_SWAPOFF_LOG":      f.swapLog,
		"FORGE_SWAPOFF_MODE":     swapoffMode,
		"FORGE_SWAP_MV_LOG":      f.mvLog,
		"FORGE_SWAP_MV_MODE":     mvMode,
	}
	cmd.Env = replaceHostSwapFixtureEnv(os.Environ(), values)
	out, err := cmd.CombinedOutput()
	return string(out), err
}

func replaceHostSwapFixtureEnv(base []string, values map[string]string) []string {
	env := make([]string, 0, len(base)+len(values))
	for _, item := range base {
		key, _, _ := strings.Cut(item, "=")
		if _, replaced := values[key]; !replaced {
			env = append(env, item)
		}
	}
	for key, value := range values {
		env = append(env, key+"="+value)
	}
	return env
}

func hostSwapFixtureLogCount(t *testing.T, path string) int {
	t.Helper()
	data, err := os.ReadFile(path)
	if os.IsNotExist(err) {
		return 0
	}
	require.NoError(t, err)
	return len(strings.Fields(string(data)))
}

func hostSwapFileOwner(t *testing.T, path string) (uint32, uint32) {
	t.Helper()
	info, err := os.Stat(path)
	require.NoError(t, err)
	stat, ok := info.Sys().(*syscall.Stat_t)
	require.True(t, ok)
	return stat.Uid, stat.Gid
}

func TestHostSwapScriptDisablesUbuntuSwapAndIsIdempotent(t *testing.T) {
	const initialFstab = "# /etc/fstab: static file system information.\n" +
		"UUID=root / ext4 defaults 0 1\n" +
		"/swap.img none swap sw 0 0\n" +
		"/dev/sdb2\tnone\tswap\tsw,discard\t0\t0 # second swap\n" +
		"   # /dev/commented none swap sw 0 0\n" +
		"#forge-existing-comment none swap sw 0 0\n" +
		"/dev/data /srv/data ext4 defaults 0 2\n"
	const expectedFstab = "# /etc/fstab: static file system information.\n" +
		"UUID=root / ext4 defaults 0 1\n" +
		"# forge-disabled-swap /swap.img none swap sw 0 0\n" +
		"# forge-disabled-swap /dev/sdb2\tnone\tswap\tsw,discard\t0\t0 # second swap\n" +
		"   # /dev/commented none swap sw 0 0\n" +
		"#forge-existing-comment none swap sw 0 0\n" +
		"/dev/data /srv/data ext4 defaults 0 2\n"
	fixture := newHostSwapFixture(t,
		"Filename\tType\tSize\tUsed\tPriority\n/swap.img file 2097148 0 -2\n",
		initialFstab, 0o640)
	beforeUID, beforeGID := hostSwapFileOwner(t, fixture.fstabPath)

	out, err := fixture.run(t, "clear", "real")
	require.NoError(t, err, out)
	fstab, err := os.ReadFile(fixture.fstabPath)
	require.NoError(t, err)
	assert.Equal(t, expectedFstab, string(fstab))
	active, err := os.ReadFile(fixture.activePath)
	require.NoError(t, err)
	assert.Equal(t, "Filename\tType\tSize\tUsed\tPriority\n", string(active))
	info, err := os.Stat(fixture.fstabPath)
	require.NoError(t, err)
	assert.Equal(t, os.FileMode(0o640), info.Mode().Perm())
	afterUID, afterGID := hostSwapFileOwner(t, fixture.fstabPath)
	assert.Equal(t, beforeUID, afterUID)
	assert.Equal(t, beforeGID, afterGID)
	assert.Equal(t, 1, hostSwapFixtureLogCount(t, fixture.swapLog))
	assert.Equal(t, 1, hostSwapFixtureLogCount(t, fixture.mvLog))

	out, err = fixture.run(t, "clear", "real")
	require.NoError(t, err, out)
	fstab, err = os.ReadFile(fixture.fstabPath)
	require.NoError(t, err)
	assert.Equal(t, expectedFstab, string(fstab))
	assert.Equal(t, 1, hostSwapFixtureLogCount(t, fixture.swapLog), "repeat run must not call swapoff")
	assert.Equal(t, 1, hostSwapFixtureLogCount(t, fixture.mvLog), "repeat run must not rewrite fstab")
}

func TestHostSwapScriptAlreadyDisabledIsNoOp(t *testing.T) {
	const fstab = "UUID=root / ext4 defaults 0 1\n# forge-disabled-swap /swap.img none swap sw 0 0\n  # /dev/sdb2 none swap sw 0 0\n"
	fixture := newHostSwapFixture(t, "Filename\tType\tSize\tUsed\tPriority\n", fstab, 0o600)
	require.NoError(t, os.Remove(filepath.Join(fixture.binDir, "swapoff")), "a swap-free no-op must not require swapoff")
	out, err := fixture.run(t, "clear", "real")
	require.NoError(t, err, out)
	got, err := os.ReadFile(fixture.fstabPath)
	require.NoError(t, err)
	assert.Equal(t, fstab, string(got))
	assert.Zero(t, hostSwapFixtureLogCount(t, fixture.swapLog))
	assert.Zero(t, hostSwapFixtureLogCount(t, fixture.mvLog))
}

func TestHostSwapScriptFailuresAreActionableAndFailClosed(t *testing.T) {
	t.Run("malformed active swap state", func(t *testing.T) {
		fixture := newHostSwapFixture(t, "not the proc swaps header\n", "UUID=root / ext4 defaults 0 1\n", 0o600)
		out, err := fixture.run(t, "clear", "real")
		require.Error(t, err)
		assert.Contains(t, out, "cannot inspect active swap state")
	})

	t.Run("configured swap state unavailable", func(t *testing.T) {
		fixture := newHostSwapFixture(t, "Filename\tType\tSize\tUsed\tPriority\n", "UUID=root / ext4 defaults 0 1\n", 0o600)
		require.NoError(t, os.Remove(fixture.fstabPath))
		out, err := fixture.run(t, "clear", "real")
		require.Error(t, err)
		assert.Contains(t, out, "configured swap state")
	})

	t.Run("swapoff failure", func(t *testing.T) {
		fixture := newHostSwapFixture(t,
			"Filename\tType\tSize\tUsed\tPriority\n/swap.img file 1024 0 -2\n",
			"UUID=root / ext4 defaults 0 1\n", 0o600)
		out, err := fixture.run(t, "fail", "real")
		require.Error(t, err)
		assert.Contains(t, out, "swapoff --all failed")
		assert.Equal(t, 1, hostSwapFixtureLogCount(t, fixture.swapLog))
	})

	t.Run("raced active swap remains", func(t *testing.T) {
		fixture := newHostSwapFixture(t,
			"Filename\tType\tSize\tUsed\tPriority\n/swap.img file 1024 0 -2\n",
			"UUID=root / ext4 defaults 0 1\n", 0o600)
		out, err := fixture.run(t, "noop", "real")
		require.Error(t, err)
		assert.Contains(t, out, "active swap remains after swapoff: /swap.img")
	})

	t.Run("configured swap write failure is retry safe", func(t *testing.T) {
		const initial = "/swap.img none swap sw 0 0\n"
		const expected = "# forge-disabled-swap /swap.img none swap sw 0 0\n"
		fixture := newHostSwapFixture(t, "Filename\tType\tSize\tUsed\tPriority\n", initial, 0o600)
		out, err := fixture.run(t, "clear", "fail")
		require.Error(t, err)
		assert.Contains(t, out, "cannot atomically replace configured swap state")
		got, readErr := os.ReadFile(fixture.fstabPath)
		require.NoError(t, readErr)
		assert.Equal(t, initial, string(got))

		out, err = fixture.run(t, "clear", "real")
		require.NoError(t, err, out)
		got, readErr = os.ReadFile(fixture.fstabPath)
		require.NoError(t, readErr)
		assert.Equal(t, expected, string(got))
	})

	t.Run("residual configured swap is rejected", func(t *testing.T) {
		fixture := newHostSwapFixture(t, "Filename\tType\tSize\tUsed\tPriority\n", "/swap.img none swap sw 0 0\n", 0o600)
		out, err := fixture.run(t, "clear", "noop")
		require.Error(t, err)
		assert.Contains(t, out, "1 uncommented swap entries remain")
	})
}
