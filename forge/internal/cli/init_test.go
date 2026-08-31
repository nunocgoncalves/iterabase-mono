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
	t.Setenv(agentPoolWorkspaceFilesystemEnv, "")
	path := filepath.Join(t.TempDir(), "forge.yaml")
	cmd := NewRootCmd()
	cmd.SetArgs([]string{"init", "--non-interactive", "--path", path, "--address", "192.0.2.10"})
	cmd.SetOut(&bytes.Buffer{})
	require.NoError(t, cmd.Execute())

	cfg, err := config.Load(path)
	require.NoError(t, err)
	assert.Equal(t, device, cfg.Spec.AgentPoolWorkspace.Device)
	assert.Equal(t, config.WorkspaceFilesystemAuto, cfg.Spec.AgentPoolWorkspace.Filesystem)
	info, err := os.Stat(path)
	require.NoError(t, err)
	assert.Equal(t, os.FileMode(0o600), info.Mode().Perm())
}

func TestInitNonInteractiveMaterializesEnvironmentFilesystem(t *testing.T) {
	device := "/dev/disk/by-id/nvme-workspace"
	t.Setenv(agentPoolWorkspaceDeviceEnv, device)
	t.Setenv(agentPoolWorkspaceFilesystemEnv, config.WorkspaceFilesystemXFS)
	path := filepath.Join(t.TempDir(), "forge.yaml")
	cmd := NewRootCmd()
	cmd.SetArgs([]string{"init", "--non-interactive", "--path", path, "--address", "192.0.2.10"})
	cmd.SetOut(&bytes.Buffer{})
	require.NoError(t, cmd.Execute())

	cfg, err := config.Load(path)
	require.NoError(t, err)
	assert.Equal(t, config.WorkspaceFilesystemXFS, cfg.Spec.AgentPoolWorkspace.Filesystem)
}

func TestResolveWorkspaceFilesystemSources(t *testing.T) {
	got, err := resolveWorkspaceFilesystemSources("", "")
	require.NoError(t, err)
	assert.Equal(t, config.WorkspaceFilesystemAuto, got)

	_, err = resolveWorkspaceFilesystemSources(config.WorkspaceFilesystemExt4, config.WorkspaceFilesystemXFS)
	require.ErrorContains(t, err, "conflicting AgentPool workspace filesystems")
	_, err = resolveWorkspaceFilesystemSources("btrfs", "")
	require.ErrorContains(t, err, "auto|ext4|xfs")
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
		{Path: "/dev/disk/by-id/scsi-a", Model: "model-a", Serial: "serial-a", Transport: "sata", SizeBytes: 100 << 30},
		{Path: "/dev/disk/by-id/nvme-b", Model: "model-b", Serial: "serial-b", Transport: "nvme", SizeBytes: 200 << 30},
	})
	require.NoError(t, err)
	assert.Equal(t, "/dev/disk/by-id/nvme-b", selected.Path)
	text := out.String()
	assert.Contains(t, text, "resolved ext4/XFS filesystem")
	assert.Contains(t, text, "sole destructive authorization")
	assert.Contains(t, text, "filesystem selection is configuration")
	assert.Contains(t, text, `transport="nvme"`)
	assert.Contains(t, text, "recommended=xfs")
	assert.Contains(t, text, "model-b")
	assert.Contains(t, text, "serial-b")
	assert.Contains(t, text, "200.0 GiB")
}

func TestSelectAgentPoolWorkspaceFilesystemShowsRecommendationAndOverride(t *testing.T) {
	device := provisioner.WorkspaceDevice{Path: "/dev/disk/by-id/nvme-workspace", Transport: "nvme"}
	var out bytes.Buffer
	selection, err := selectAgentPoolWorkspaceFilesystem(bufio.NewReader(strings.NewReader("ext4\n")), &out, device, config.WorkspaceFilesystemAuto, false)
	require.NoError(t, err)
	assert.Equal(t, config.WorkspaceFilesystemExt4, selection)
	assert.Contains(t, out.String(), `transport is "nvme"`)
	assert.Contains(t, out.String(), "auto recommends and resolves to xfs")
	assert.Contains(t, out.String(), "Resolved workspace filesystem: ext4")
	assert.Contains(t, out.String(), "does not add another destructive confirmation")
}

func TestSelectAgentPoolWorkspaceFilesystemPreservesExplicitSource(t *testing.T) {
	device := provisioner.WorkspaceDevice{Path: "/dev/disk/by-id/nvme-workspace", Transport: "nvme"}
	var out bytes.Buffer
	selection, err := selectAgentPoolWorkspaceFilesystem(bufio.NewReader(strings.NewReader("xfs\n")), &out, device, config.WorkspaceFilesystemExt4, true)
	require.NoError(t, err)
	assert.Equal(t, config.WorkspaceFilesystemExt4, selection)
	assert.Contains(t, out.String(), "Preserving the explicit flag/environment source without an interactive override")
	assert.NotContains(t, out.String(), "selection=xfs")
}
