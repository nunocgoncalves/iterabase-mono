package cli

import (
	"bufio"
	"bytes"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/nunocgoncalves/iterabase-mono/forge/internal/config"
	"github.com/nunocgoncalves/iterabase-mono/forge/internal/provisioner"
)

func TestResolveWorkspaceDeviceSources(t *testing.T) {
	device := "/dev/disk/by-id/scsi-workspace"
	got, err := resolveWorkspaceDeviceSources(device, device)
	require.NoError(t, err)
	assert.Equal(t, device, got)

	_, err = resolveWorkspaceDeviceSources(device, "/dev/disk/by-id/scsi-other")
	require.ErrorContains(t, err, "conflicting AgentPool workspace devices")
}

func TestInitNonInteractiveMaterializesEnvironmentDevice(t *testing.T) {
	device := "/dev/disk/by-id/scsi-workspace"
	t.Setenv(agentPoolWorkspaceDeviceEnv, device)
	path := filepath.Join(t.TempDir(), "forge.yaml")
	cmd := NewRootCmd()
	cmd.SetArgs([]string{"init", "--non-interactive", "--path", path, "--address", "192.0.2.10"})
	cmd.SetOut(&bytes.Buffer{})
	require.NoError(t, cmd.Execute())

	cfg, err := config.Load(path)
	require.NoError(t, err)
	assert.Equal(t, device, cfg.Spec.AgentPoolWorkspace.Device)
	info, err := os.Stat(path)
	require.NoError(t, err)
	assert.Equal(t, os.FileMode(0o600), info.Mode().Perm())
}

func TestInitNonInteractiveRequiresSingleDeviceSource(t *testing.T) {
	t.Setenv(agentPoolWorkspaceDeviceEnv, "")
	cmd := NewRootCmd()
	cmd.SetArgs([]string{"init", "--non-interactive", "--path", filepath.Join(t.TempDir(), "forge.yaml"), "--address", "192.0.2.10"})
	err := cmd.Execute()
	require.ErrorContains(t, err, agentPoolWorkspaceDeviceEnv)
}

func TestSelectAgentPoolWorkspaceDeviceShowsConsequenceBeforeSelection(t *testing.T) {
	var out bytes.Buffer
	selected, err := selectAgentPoolWorkspaceDevice(bufio.NewReader(strings.NewReader("2\n")), &out, []provisioner.WorkspaceDevice{
		{Path: "/dev/disk/by-id/scsi-a", Model: "model-a", Serial: "serial-a", SizeBytes: 100 << 30},
		{Path: "/dev/disk/by-id/scsi-b", Model: "model-b", Serial: "serial-b", SizeBytes: 200 << 30},
	})
	require.NoError(t, err)
	assert.Equal(t, "/dev/disk/by-id/scsi-b", selected)
	text := out.String()
	assert.Contains(t, text, "format the selected whole disk as ext4")
	assert.Contains(t, text, "sole destructive authorization")
	assert.Contains(t, text, "model-b")
	assert.Contains(t, text, "serial-b")
	assert.Contains(t, text, "200.0 GiB")
}
