package dispatch

import (
	"testing"

	v1 "github.com/nunocgoncalves/iterabase-mono/control-plane/internal/harnessrpc/iterabase/harness/v1"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestWorkspaceCapacityGateRevokesOnlyUnspentCredit(t *testing.T) {
	w := &workerConn{}
	w.updateWorkspaceStatus(30, 100, 0.30, false, false)
	granted, valid := w.grantCreditIfIdle()
	require.True(t, valid)
	require.True(t, granted)
	require.True(t, w.idle)

	w.updateWorkspaceStatus(20, 100, 0.20, true, true)
	assert.False(t, w.idle, "an unspent credit is revoked at the floor")
	granted, valid = w.grantCreditIfIdle()
	assert.True(t, valid)
	assert.False(t, granted)

	w.updateWorkspaceStatus(25, 100, 0.25, false, false)
	granted, valid = w.grantCreditIfIdle()
	assert.True(t, valid)
	assert.True(t, granted)
}

func TestWorkspaceCapacityCrossingDoesNotAbortActiveTurn(t *testing.T) {
	w := &workerConn{}
	w.updateWorkspaceStatus(30, 100, 0.30, false, false)
	granted, valid := w.grantCreditIfIdle()
	require.True(t, valid)
	require.True(t, granted)
	require.True(t, w.tryConsumeCredit("turn-1"))

	w.updateWorkspaceStatus(20, 100, 0.20, true, true)
	assert.Equal(t, "turn-1", w.activeTurn, "threshold-only gating preserves active ownership")
	w.releaseTurn()
	granted, valid = w.grantCreditIfIdle()
	assert.True(t, valid)
	assert.False(t, granted, "the next fresh credit is withheld after terminalization")
}

func TestValidateWorkspaceStatusThresholdContract(t *testing.T) {
	assert.NoError(t, validateWorkspaceStatus(&v1.WorkspaceStatus{FreeBytes: 24, CapacityBytes: 100, FreeRatio: 0.24, Warning: true}))
	assert.NoError(t, validateWorkspaceStatus(&v1.WorkspaceStatus{FreeBytes: 20, CapacityBytes: 100, FreeRatio: 0.20, Warning: true, CreditGated: true}))
	assert.Error(t, validateWorkspaceStatus(&v1.WorkspaceStatus{FreeBytes: 20, CapacityBytes: 100, FreeRatio: 0.20, Warning: true, CreditGated: false}))
	assert.Error(t, validateWorkspaceStatus(&v1.WorkspaceStatus{FreeBytes: 30, CapacityBytes: 100, FreeRatio: 0.20, Warning: true, CreditGated: true}))
}
