package lifecycle

import (
	"context"
	"errors"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestPlanInspectsSelectedDataStorageSetWithoutMutation(t *testing.T) {
	p := &fakeProv{pf: readyPf()}
	plan, err := Plan(context.Background(), testConfig(), p)
	require.NoError(t, err)
	assert.Equal(t, 1, p.workspaceInspectCalls)
	assert.Zero(t, p.workspaceApplyCalls)
	require.Len(t, plan.DataStorage.Devices, 2)
	assert.Equal(t, "/dev/disk/by-id/scsi-data-a", plan.DataStorage.Devices[0].Path)
	assert.Equal(t, "scsi", plan.DataStorage.Devices[0].Transport)
	assert.Equal(t, "blank-candidate", plan.DataStorage.State)
}

func TestPlanDataStorageUncertaintyFailsClosed(t *testing.T) {
	p := &fakeProv{pf: readyPf(), workspaceInspectErr: errors.New("signature probe read error")}
	_, err := Plan(context.Background(), testConfig(), p)
	require.ErrorContains(t, err, "signature probe read error")
	assert.Empty(t, p.installs)
}

func TestApplyReconcilesDataStorageBeforeK3s(t *testing.T) {
	useTempHome(t)
	p := &fakeProv{pf: readyPf(), readyAfterInstall: true, kubeconfig: []byte(minKubeconfig)}
	res, err := Apply(context.Background(), testConfig(), p, nil, nil, nil, ApplyOpts{ReadyTimeout: time.Second, ReadyInterval: time.Millisecond})
	require.NoError(t, err)
	assert.Equal(t, 1, p.workspaceInspectCalls)
	assert.Equal(t, 1, p.workspaceToolsCalls)
	assert.Equal(t, 1, p.workspaceApplyCalls)
	assert.Len(t, p.installs, 1)
	assert.Equal(t, "complete", res.DataStorage.State)
	assert.Equal(t, "iterabase-data", res.DataStorage.VGName)
}

func TestApplyDataStorageToolingFailurePreventsPVAndK3sMutation(t *testing.T) {
	p := &fakeProv{pf: readyPf(), workspaceToolsErr: errors.New("lvm2 install failed")}
	_, err := Apply(context.Background(), testConfig(), p, nil, nil, nil, ApplyOpts{})
	require.ErrorContains(t, err, "lvm2 install failed")
	assert.Equal(t, 1, p.workspaceToolsCalls)
	assert.Zero(t, p.workspaceApplyCalls)
	assert.Empty(t, p.installs)
}

func TestApplyDataStorageRefusalPreventsK3sMutation(t *testing.T) {
	p := &fakeProv{pf: readyPf(), workspaceReconcileErr: errors.New("root backing device")}
	_, err := Apply(context.Background(), testConfig(), p, nil, nil, nil, ApplyOpts{})
	require.ErrorContains(t, err, "root backing device")
	assert.Empty(t, p.installs)
	assert.Zero(t, p.lvmReadinessCalls)
}

func TestPlanRefusesInstalledK3sWithLocalStorageFallback(t *testing.T) {
	p := &fakeProv{pf: readyPf(), state: inSyncState()}
	p.pf.Installed = true
	p.state.LocalStorageDisabled = false
	plan, err := Plan(context.Background(), testConfig(), p)
	require.NoError(t, err)
	assert.Equal(t, ActionRefuseImmutable, plan.Action)
	assert.Contains(t, plan.ImmutableDiff, "k3s.disable[local-storage]")
}
