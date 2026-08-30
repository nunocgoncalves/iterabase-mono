package lifecycle

import (
	"context"
	"errors"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestPlanInspectsSelectedWorkspaceWithoutMutation(t *testing.T) {
	p := &fakeProv{pf: readyPf()}
	plan, err := Plan(context.Background(), testConfig(), p)
	require.NoError(t, err)
	assert.Equal(t, 1, p.workspaceInspectCalls)
	assert.Zero(t, p.workspaceApplyCalls)
	assert.Equal(t, "/dev/disk/by-id/scsi-workspace", plan.AgentPoolWorkspace.Device)
	assert.Equal(t, "scsi", plan.AgentPoolWorkspace.Transport)
	assert.Equal(t, "ext4", plan.AgentPoolWorkspace.Filesystem)
}

func TestPlanWorkspaceUncertaintyFailsClosed(t *testing.T) {
	p := &fakeProv{pf: readyPf(), workspaceInspectErr: errors.New("signature probe read error")}
	_, err := Plan(context.Background(), testConfig(), p)
	require.ErrorContains(t, err, "signature probe read error")
	assert.Empty(t, p.installs)
}

func TestApplyReconcilesWorkspaceBeforeK3sAndConfiguresLocalPath(t *testing.T) {
	useTempHome(t)
	p := &fakeProv{pf: readyPf(), readyAfterInstall: true, kubeconfig: []byte(minKubeconfig)}
	res, err := Apply(context.Background(), testConfig(), p, nil, nil, nil, ApplyOpts{ReadyTimeout: time.Second, ReadyInterval: time.Millisecond})
	require.NoError(t, err)
	assert.Equal(t, 1, p.workspaceInspectCalls)
	assert.Equal(t, []string{"ext4"}, p.workspaceToolsCalls)
	assert.Equal(t, 1, p.workspaceApplyCalls)
	assert.Len(t, p.installs, 1)
	assert.Equal(t, 1, p.localPathCalls)
	assert.True(t, res.AgentPoolLocalPathReady)
	assert.Equal(t, "complete", res.AgentPoolWorkspace.State)
}

func TestApplyWorkspaceToolingFailurePreventsFormatAndK3sMutation(t *testing.T) {
	p := &fakeProv{pf: readyPf(), workspaceToolsErr: errors.New("xfsprogs install failed")}
	cfg := testConfig()
	cfg.Spec.AgentPoolWorkspace.Filesystem = "xfs"
	_, err := Apply(context.Background(), cfg, p, nil, nil, nil, ApplyOpts{})
	require.ErrorContains(t, err, "xfsprogs install failed")
	assert.Equal(t, []string{"xfs"}, p.workspaceToolsCalls)
	assert.Zero(t, p.workspaceApplyCalls)
	assert.Empty(t, p.installs)
}

func TestApplyWorkspaceRefusalPreventsK3sMutation(t *testing.T) {
	p := &fakeProv{pf: readyPf(), workspaceReconcileErr: errors.New("root backing device")}
	_, err := Apply(context.Background(), testConfig(), p, nil, nil, nil, ApplyOpts{})
	require.ErrorContains(t, err, "root backing device")
	assert.Empty(t, p.installs)
	assert.Zero(t, p.localPathCalls)
}
