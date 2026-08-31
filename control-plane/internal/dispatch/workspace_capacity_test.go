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

	restored := w.updateWorkspaceStatus(25, 100, 0.25, false, false)
	assert.True(t, restored, "the server restores the already-advertised unspent credit")
	assert.True(t, w.idle)
	assert.True(t, w.creditAdvertised)
}

func TestWorkspaceCapacityGateAppliesAcrossPoolsAndReplacement(t *testing.T) {
	workers := newWorkerPool()
	first := &workerConn{poolID: "pool-a", workerID: "worker-a"}
	second := &workerConn{poolID: "pool-b", workerID: "worker-b"}
	workers.add(first)
	workers.add(second)
	first.updateWorkspaceStatus(30, 100, 0.30, false, false)
	second.updateWorkspaceStatus(30, 100, 0.30, false, false)
	_, _ = first.grantCreditIfIdle()
	_, _ = second.grantCreditIfIdle()

	workers.applyWorkspaceStatus(first, 20, 100, 0.20, true, true)
	assert.False(t, first.idle)
	assert.False(t, second.idle, "one filesystem observation gates every pool")
	assert.True(t, second.workspaceGated)

	replacement := &workerConn{poolID: "pool-b", workerID: "worker-b"}
	workers.add(replacement)
	workers.applyWorkspaceStatus(replacement, 24, 100, 0.24, true, true)
	granted, valid := replacement.grantCreditIfIdle()
	assert.True(t, valid)
	assert.False(t, granted, "replacement remains gated inside the 20-25 percent band")

	restored := workers.applyWorkspaceStatus(replacement, 25, 100, 0.25, false, false)
	assert.True(t, restored)
	assert.True(t, replacement.idle, "durable reopen restores the retained Ready without a duplicate advertisement")
	assert.True(t, replacement.creditAdvertised)
}

func TestWorkspaceCapacityObservationAfterCreditConsumptionDoesNotRegrant(t *testing.T) {
	w := &workerConn{}
	w.updateWorkspaceStatus(30, 100, 0.30, false, false)
	granted, valid := w.grantCreditIfIdle()
	require.True(t, valid)
	require.True(t, granted)
	require.True(t, w.tryConsumeCredit("turn-in-flight"))

	restored := w.updateWorkspaceStatus(30, 100, 0.30, false, false)
	assert.False(t, restored, "an observation between server-side consumption and AssignTurn delivery cannot mint another credit")
	assert.False(t, w.idle)
	assert.False(t, w.creditAdvertised)
	assert.Equal(t, "turn-in-flight", w.activeTurn)
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
