package dispatch_test

import (
	"context"
	"encoding/json"
	"errors"
	"sync"
	"testing"
	"time"

	"github.com/jackc/pgx/v5/pgxpool"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/nunocgoncalves/iterabase-mono/control-plane/internal/dispatch"
	"github.com/nunocgoncalves/iterabase-mono/control-plane/internal/runtime"
	"github.com/nunocgoncalves/iterabase-mono/control-plane/internal/testutil"
)

func newTestStore(t *testing.T) (*dispatch.Store, *runtime.Store, *pgxpool.Pool) {
	t.Helper()
	pool := testutil.NewPostgresPool(t)
	rt := runtime.NewStore(pool)
	return dispatch.NewStore(pool, rt), rt, pool
}

func insertIdentity(t *testing.T, pool *pgxpool.Pool, key string) string {
	t.Helper()
	var id string
	require.NoError(t, pool.QueryRow(context.Background(), `
		INSERT INTO identity.identities (key, kind, source, display_name)
		VALUES ($1, 'workflow', 'local', $1) RETURNING id`, key).Scan(&id))
	return id
}

// seedRunTurn creates a chat run + running step + active turn and returns the
// run id, step id, and turn id. The run is left in 'running' state.
func seedRunTurn(t *testing.T, rt *runtime.Store, pool *pgxpool.Pool, sessionID string) (runID, stepID, turnID string) {
	t.Helper()
	ctx := context.Background()
	identID := insertIdentity(t, pool, "ident-"+sessionID)
	run, err := rt.CreateRun(ctx, runtime.CreateRunInput{
		Kind:            runtime.KindChat,
		ScopeIdentityID: identID,
		SessionID:       sessionID,
		SessionDir:      "/sessions/" + sessionID,
		Steps: []runtime.StepInput{
			{Seq: 1, Kind: runtime.StepKindAgentTask, Config: json.RawMessage(`{"prompt":"hi"}`)},
		},
	})
	require.NoError(t, err)
	_, err = rt.StartRun(ctx, run.ID)
	require.NoError(t, err)
	steps, err := rt.ListSteps(ctx, run.ID)
	require.NoError(t, requireLen1(steps))
	stepID = steps[0].ID
	_, err = rt.StartStep(ctx, stepID)
	require.NoError(t, err)
	turn, err := rt.StartTurn(ctx, run.ID, stepID, "m1")
	require.NoError(t, err)
	return run.ID, stepID, turn.ID
}

func requireLen1[T any](s []T) error {
	if len(s) != 1 {
		return errors.New("expected exactly 1 element")
	}
	return nil
}

func mustAllocateTestSessionUID(t *testing.T, store *dispatch.Store, sessionID string) uint32 {
	t.Helper()
	uid, err := store.AllocateSessionUID(context.Background(), sessionID, 10000, 50000, 5*time.Minute)
	require.NoError(t, err)
	return uid
}

func TestStore_WorkspaceCapacityHysteresisIsDurable(t *testing.T) {
	store, _, _ := newTestStore(t)
	ctx := context.Background()

	initial, err := store.LoadWorkspaceCapacityState(ctx)
	require.NoError(t, err)
	assert.False(t, initial.Observed)
	assert.True(t, initial.CreditGated, "missing history starts fail-closed")

	opened, err := store.ObserveWorkspaceCapacity(ctx, 30, 100, 0.30)
	require.NoError(t, err)
	assert.False(t, opened.Warning)
	assert.False(t, opened.CreditGated)

	warning, err := store.ObserveWorkspaceCapacity(ctx, 24, 100, 0.24)
	require.NoError(t, err)
	assert.True(t, warning.Warning)
	assert.False(t, warning.CreditGated, "warning alone does not close fresh credit")

	gated, err := store.ObserveWorkspaceCapacity(ctx, 20, 100, 0.20)
	require.NoError(t, err)
	assert.True(t, gated.CreditGated)

	replacement, err := store.ObserveWorkspaceCapacity(ctx, 24, 100, 0.24)
	require.NoError(t, err)
	assert.True(t, replacement.CreditGated, "replacement observations retain the durable gate in-band")

	reloaded, err := store.LoadWorkspaceCapacityState(ctx)
	require.NoError(t, err)
	assert.True(t, reloaded.CreditGated, "dispatch restart restores the gate")

	reopened, err := store.ObserveWorkspaceCapacity(ctx, 25, 100, 0.25)
	require.NoError(t, err)
	assert.False(t, reopened.CreditGated)
}

func TestStore_AssignRunToPoolAndResolve(t *testing.T) {
	store, rt, pool := newTestStore(t)
	ctx := context.Background()

	// Seed a pool in toolgateway.pools (the AgentPool registry).
	var poolID string
	require.NoError(t, pool.QueryRow(ctx, `
		INSERT INTO toolgateway.pools (key, name, spiffe_id_prefix)
		VALUES ('ns/pool-1', 'pool-1', 'spiffe://iterabase.local/pools/pool-1/') RETURNING id::text`).Scan(&poolID))

	runID, _, _ := seedRunTurn(t, rt, pool, "sess-1")
	require.NoError(t, store.AssignRunToPool(ctx, runID, poolID))

	got, err := store.PoolForRun(ctx, runID)
	require.NoError(t, err)
	assert.Equal(t, poolID, got)

	// Idempotent.
	require.NoError(t, store.AssignRunToPool(ctx, runID, poolID))

	// Resolve by spiffe prefix.
	p, err := store.ResolvePoolBySpiffePrefix(ctx, "spiffe://iterabase.local/pools/pool-1/workers/worker-1")
	require.NoError(t, err)
	assert.Equal(t, poolID, p.ID)
	assert.Equal(t, "ns/pool-1", p.Key)

	_, err = store.PoolForRun(ctx, "00000000-0000-0000-0000-000000000000")
	assert.ErrorIs(t, err, dispatch.ErrNotFound)
}

func TestStore_CreateAssignmentAndResolve(t *testing.T) {
	store, rt, pool := newTestStore(t)
	ctx := context.Background()
	var poolID string
	require.NoError(t, pool.QueryRow(ctx, `
		INSERT INTO toolgateway.pools (key, name, spiffe_id_prefix)
		VALUES ('ns/pool-1', 'pool-1', 'spiffe://iterabase.local/pools/pool-1/') RETURNING id::text`).Scan(&poolID))
	identID := insertIdentity(t, pool, "ident-a")
	runID, _, turnID := seedRunTurn(t, rt, pool, "sess-a")
	uid := mustAllocateTestSessionUID(t, store, "sess-a")

	in := dispatch.AssignmentInput{
		TurnID: turnID, RunID: runID, PoolID: poolID, WorkerID: "worker-1",
		FencingGeneration: 7, AttemptID: runID, ScopeIdentityID: identID,
		AgentPoolKey: "ns/pool-1", SessionID: "sess-a", SandboxUID: uid, SandboxGID: uid,
		ModelPermission: json.RawMessage(`{"id":"m1"}`),
	}
	a, err := store.CreateAssignment(ctx, in)
	require.NoError(t, err)
	assert.Equal(t, dispatch.AssignmentActive, a.State)
	assert.Equal(t, "worker-1", a.WorkerID)
	assert.Equal(t, int64(7), a.FencingGeneration)

	// Resolve active.
	got, err := store.ResolveActiveAssignment(ctx, turnID)
	require.NoError(t, err)
	assert.Equal(t, turnID, got.TurnID)
	assert.Equal(t, runID, got.AttemptID)

	// Active assignment for the worker.
	got, err = store.ActiveAssignmentForWorker(ctx, poolID, "worker-1")
	require.NoError(t, err)
	assert.Equal(t, turnID, got.TurnID)

	// Duplicate create -> ErrAlreadyAssigned.
	_, err = store.CreateAssignment(ctx, in)
	assert.ErrorIs(t, err, dispatch.ErrAlreadyAssigned)

	// A different same-pool worker has no active assignment.
	_, err = store.ActiveAssignmentForWorker(ctx, poolID, "worker-2")
	assert.ErrorIs(t, err, dispatch.ErrNotFound)
}

func TestStore_CreateAssignmentFailsClosedOnSessionUIDDrift(t *testing.T) {
	store, rt, pool := newTestStore(t)
	ctx := context.Background()
	var poolID string
	require.NoError(t, pool.QueryRow(ctx, `
		INSERT INTO toolgateway.pools (key, name, spiffe_id_prefix)
		VALUES ('ns/pool-1', 'pool-1', 'spiffe://iterabase.local/pools/pool-1/') RETURNING id::text`).Scan(&poolID))
	identID := insertIdentity(t, pool, "ident-session-uid-drift")

	inputFor := func(runID, turnID, sessionID string, uid, gid uint32) dispatch.AssignmentInput {
		return dispatch.AssignmentInput{
			TurnID: turnID, RunID: runID, PoolID: poolID, WorkerID: "worker-1",
			FencingGeneration: 1, AttemptID: runID, ScopeIdentityID: identID, AgentPoolKey: "ns/pool-1",
			SessionID: sessionID, SandboxUID: uid, SandboxGID: gid,
		}
	}

	runMissing, _, turnMissing := seedRunTurn(t, rt, pool, "sess-missing")
	_, err := store.CreateAssignment(ctx, inputFor(runMissing, turnMissing, "sess-missing", 10000, 10000))
	assert.ErrorIs(t, err, dispatch.ErrSessionUIDUnavailable, "a missing durable allocation must not produce an assignment")

	runMismatch, _, turnMismatch := seedRunTurn(t, rt, pool, "sess-mismatch")
	uidMismatch := mustAllocateTestSessionUID(t, store, "sess-mismatch")
	_, err = store.CreateAssignment(ctx, inputFor(runMismatch, turnMismatch, "wrong-session", uidMismatch, uidMismatch))
	assert.ErrorIs(t, err, dispatch.ErrSessionIdentityMismatch, "the run and assigned session must agree")
	_, err = store.CreateAssignment(ctx, inputFor(runMismatch, turnMismatch, "sess-mismatch", uidMismatch, uidMismatch+1))
	assert.ErrorIs(t, err, dispatch.ErrSessionIdentityMismatch, "assigned UID and GID must be equal")
	_, err = store.CreateAssignment(ctx, inputFor(runMismatch, turnMismatch, "sess-mismatch", uidMismatch+1, uidMismatch+1))
	assert.ErrorIs(t, err, dispatch.ErrSessionIdentityMismatch, "the assigned UID must equal durable allocator authority")

	runFreed, _, turnFreed := seedRunTurn(t, rt, pool, "sess-freed")
	uidFreed := mustAllocateTestSessionUID(t, store, "sess-freed")
	require.NoError(t, store.ReleaseSessionUID(ctx, "sess-freed"))
	_, err = store.CreateAssignment(ctx, inputFor(runFreed, turnFreed, "sess-freed", uidFreed, uidFreed))
	assert.ErrorIs(t, err, dispatch.ErrSessionUIDUnavailable, "a prematurely freed allocation must not produce an assignment")
}

func TestStore_ReleaseSessionUIDRefusesActiveAssignment(t *testing.T) {
	store, rt, pool := newTestStore(t)
	ctx := context.Background()
	var poolID string
	require.NoError(t, pool.QueryRow(ctx, `
		INSERT INTO toolgateway.pools (key, name, spiffe_id_prefix)
		VALUES ('ns/pool-1', 'pool-1', 'spiffe://iterabase.local/pools/pool-1/') RETURNING id::text`).Scan(&poolID))
	identID := insertIdentity(t, pool, "ident-active-session")
	runID, _, turnID := seedRunTurn(t, rt, pool, "sess-active")
	uid := mustAllocateTestSessionUID(t, store, "sess-active")
	_, err := store.CreateAssignment(ctx, dispatch.AssignmentInput{
		TurnID: turnID, RunID: runID, PoolID: poolID, WorkerID: "worker-1",
		FencingGeneration: 1, AttemptID: runID, ScopeIdentityID: identID, AgentPoolKey: "ns/pool-1",
		SessionID: "sess-active", SandboxUID: uid, SandboxGID: uid,
	})
	require.NoError(t, err)

	assert.ErrorIs(t, store.ReleaseSessionUID(ctx, "sess-active"), dispatch.ErrSessionUIDActive)
	retained, err := store.SessionUID(ctx, "sess-active")
	require.NoError(t, err)
	assert.Equal(t, uid, retained, "an active assignment keeps its UID in use")

	require.NoError(t, store.TerminalizeAssignment(ctx, turnID))
	require.NoError(t, store.ReleaseSessionUID(ctx, "sess-active"))
	_, err = store.SessionUID(ctx, "sess-active")
	assert.ErrorIs(t, err, dispatch.ErrNotFound)
}

func TestStore_ConcurrentAssignmentsRetainDistinctSessionUIDs(t *testing.T) {
	store, rt, pool := newTestStore(t)
	ctx := context.Background()
	var poolID string
	require.NoError(t, pool.QueryRow(ctx, `
		INSERT INTO toolgateway.pools (key, name, spiffe_id_prefix)
		VALUES ('ns/pool-1', 'pool-1', 'spiffe://iterabase.local/pools/pool-1/') RETURNING id::text`).Scan(&poolID))
	identID := insertIdentity(t, pool, "ident-concurrent-session")
	type participant struct {
		sessionID string
		runID     string
		turnID    string
		workerID  string
		uid       uint32
		err       error
	}
	participants := []participant{{sessionID: "sess-concurrent-a", workerID: "worker-a"}, {sessionID: "sess-concurrent-b", workerID: "worker-b"}}
	for index := range participants {
		participants[index].runID, _, participants[index].turnID = seedRunTurn(t, rt, pool, participants[index].sessionID)
	}

	start := make(chan struct{})
	var wg sync.WaitGroup
	for index := range participants {
		wg.Add(1)
		go func(p *participant) {
			defer wg.Done()
			<-start
			p.uid, p.err = store.AllocateSessionUID(ctx, p.sessionID, 10000, 2, 5*time.Minute)
			if p.err != nil {
				return
			}
			_, p.err = store.CreateAssignment(ctx, dispatch.AssignmentInput{
				TurnID: p.turnID, RunID: p.runID, PoolID: poolID, WorkerID: p.workerID,
				FencingGeneration: 1, AttemptID: p.runID, ScopeIdentityID: identID, AgentPoolKey: "ns/pool-1",
				SessionID: p.sessionID, SandboxUID: p.uid, SandboxGID: p.uid,
			})
		}(&participants[index])
	}
	close(start)
	wg.Wait()
	for _, participant := range participants {
		require.NoError(t, participant.err)
	}
	require.NotEqual(t, participants[0].uid, participants[1].uid, "concurrent sessions must not collide")

	var total, distinctUIDs, distinctWorkers int
	require.NoError(t, pool.QueryRow(ctx, `
		SELECT count(*), count(DISTINCT u.uid), count(DISTINCT ta.worker_id)
		FROM runtime.turn_assignments ta
		JOIN runtime.workflow_runs wr ON wr.id=ta.run_id
		JOIN runtime.session_uid_allocations u ON u.session_id=wr.session_id AND u.state='in_use'
		WHERE ta.attempt_id IN ($1,$2) AND ta.state='active'`, participants[0].runID, participants[1].runID).
		Scan(&total, &distinctUIDs, &distinctWorkers))
	assert.Equal(t, 2, total)
	assert.Equal(t, 2, distinctUIDs)
	assert.Equal(t, 2, distinctWorkers)
}

func TestStore_FenceAndTerminalize(t *testing.T) {
	store, rt, pool := newTestStore(t)
	ctx := context.Background()
	var poolID string
	require.NoError(t, pool.QueryRow(ctx, `
		INSERT INTO toolgateway.pools (key, name, spiffe_id_prefix)
		VALUES ('ns/pool-1', 'pool-1', 'spiffe://iterabase.local/pools/pool-1/') RETURNING id::text`).Scan(&poolID))
	identID := insertIdentity(t, pool, "ident-b")
	runID, _, turnID := seedRunTurn(t, rt, pool, "sess-b")
	uid := mustAllocateTestSessionUID(t, store, "sess-b")
	_, err := store.CreateAssignment(ctx, dispatch.AssignmentInput{
		TurnID: turnID, RunID: runID, PoolID: poolID, WorkerID: "worker-1",
		FencingGeneration: 1, AttemptID: runID, ScopeIdentityID: identID, AgentPoolKey: "ns/pool-1",
		SessionID: "sess-b", SandboxUID: uid, SandboxGID: uid,
	})
	require.NoError(t, err)

	// Fence the worker (reconnect/loss): active -> fenced. The turn is no longer
	// actively assigned.
	fenced, err := store.FenceWorkerGeneration(ctx, poolID, "worker-1")
	require.NoError(t, err)
	assert.Equal(t, dispatch.AssignmentFenced, fenced.State)
	_, err = store.ResolveActiveAssignment(ctx, turnID)
	assert.ErrorIs(t, err, dispatch.ErrAssignmentNotActive)

	// Terminalize (idempotent): fenced -> terminal.
	require.NoError(t, store.TerminalizeAssignment(ctx, turnID))
	require.NoError(t, store.TerminalizeAssignment(ctx, turnID)) // idempotent
	_, err = store.ResolveActiveAssignment(ctx, turnID)
	assert.ErrorIs(t, err, dispatch.ErrAssignmentNotActive)

	// Fencing a worker with no active assignment -> ErrNotFound.
	_, err = store.FenceWorkerGeneration(ctx, poolID, "worker-1")
	assert.ErrorIs(t, err, dispatch.ErrNotFound)
}

func TestStore_AppendTurnEventDedupAndWatermark(t *testing.T) {
	store, rt, pool := newTestStore(t)
	ctx := context.Background()
	var poolID string
	require.NoError(t, pool.QueryRow(ctx, `
		INSERT INTO toolgateway.pools (key, name, spiffe_id_prefix)
		VALUES ('ns/pool-1', 'pool-1', 'spiffe://iterabase.local/pools/pool-1/') RETURNING id::text`).Scan(&poolID))
	identID := insertIdentity(t, pool, "ident-c")
	runID, _, turnID := seedRunTurn(t, rt, pool, "sess-c")
	uid := mustAllocateTestSessionUID(t, store, "sess-c")
	_, err := store.CreateAssignment(ctx, dispatch.AssignmentInput{
		TurnID: turnID, RunID: runID, PoolID: poolID, WorkerID: "worker-1",
		FencingGeneration: 1, AttemptID: runID, ScopeIdentityID: identID, AgentPoolKey: "ns/pool-1",
		SessionID: "sess-c", SandboxUID: uid, SandboxGID: uid,
	})
	require.NoError(t, err)

	// Apply seq 1 + 2.
	applied, err := store.AppendTurnEvent(ctx, turnID, 1, runtime.EvAssistantMessage, json.RawMessage(`{"text":"a"}`))
	require.NoError(t, err)
	assert.True(t, applied)
	applied, err = store.AppendTurnEvent(ctx, turnID, 2, runtime.EvAssistantMessage, json.RawMessage(`{"text":"b"}`))
	require.NoError(t, err)
	assert.True(t, applied)

	// Watermark is 2.
	wm, err := store.AckWatermark(ctx, turnID)
	require.NoError(t, err)
	assert.Equal(t, uint64(2), wm)

	// Replay seq 2 (dedup) -> not applied.
	applied, err = store.AppendTurnEvent(ctx, turnID, 2, runtime.EvAssistantMessage, json.RawMessage(`{"text":"b"}`))
	require.NoError(t, err)
	assert.False(t, applied)

	// Events were appended to the run's audit log (turn_started from StartTurn +
	// 2 assistant_message).
	evs, err := rt.ListEvents(ctx, runID)
	require.NoError(t, err)
	kinds := make(map[string]int)
	for _, e := range evs {
		kinds[e.Kind]++
	}
	assert.Equal(t, 2, kinds[runtime.EvAssistantMessage], "two assistant_message events appended")
	assert.Equal(t, 1, kinds[runtime.EvTurnStarted])

	// After fencing, AppendTurnEvent -> ErrAssignmentNotActive (late events are
	// after-terminal audit, handled by the service).
	_, err = store.FenceWorkerGeneration(ctx, poolID, "worker-1")
	require.NoError(t, err)
	_, err = store.AppendTurnEvent(ctx, turnID, 3, runtime.EvAssistantMessage, json.RawMessage(`{}`))
	assert.ErrorIs(t, err, dispatch.ErrAssignmentNotActive)
}

// TestStore_AppendAfterTerminalEventDedup: after a turn is fenced/terminal, late
// worker observations are durably appended as after-terminal audit with
// (turn, sequence) dedup + watermark advance in one transaction, so an ACK lost
// after the commit dedups the event on the next replay (HOR-381 cumulative ACK
// only after Postgres commit).
func TestStore_AppendAfterTerminalEventDedup(t *testing.T) {
	store, rt, pool := newTestStore(t)
	ctx := context.Background()
	var poolID string
	require.NoError(t, pool.QueryRow(ctx, `
		INSERT INTO toolgateway.pools (key, name, spiffe_id_prefix)
		VALUES ('ns/pool-1', 'pool-1', 'spiffe://iterabase.local/pools/pool-1/') RETURNING id::text`).Scan(&poolID))
	identID := insertIdentity(t, pool, "ident-at")
	runID, _, turnID := seedRunTurn(t, rt, pool, "sess-at")
	uid := mustAllocateTestSessionUID(t, store, "sess-at")
	_, err := store.CreateAssignment(ctx, dispatch.AssignmentInput{
		TurnID: turnID, RunID: runID, PoolID: poolID, WorkerID: "worker-1",
		FencingGeneration: 1, AttemptID: runID, ScopeIdentityID: identID, AgentPoolKey: "ns/pool-1",
		SessionID: "sess-at", SandboxUID: uid, SandboxGID: uid,
	})
	require.NoError(t, err)
	// Apply seq 1 while active, then fence (turn becomes non-active).
	_, err = store.AppendTurnEvent(ctx, turnID, 1, runtime.EvAssistantMessage, json.RawMessage(`{"text":"a"}`))
	require.NoError(t, err)
	_, err = store.FenceWorkerGeneration(ctx, poolID, "worker-1")
	require.NoError(t, err)

	// Late seq 2 (after-terminal audit): applied + watermark advances to 2.
	applied, err := store.AppendAfterTerminalEvent(ctx, turnID, 2, runtime.EvAssistantMessage, json.RawMessage(`{"text":"b"}`), poolID, "worker-1")
	require.NoError(t, err)
	assert.True(t, applied)

	// Replay seq 2 (ACK lost after commit): deduped, not re-appended.
	applied, err = store.AppendAfterTerminalEvent(ctx, turnID, 2, runtime.EvAssistantMessage, json.RawMessage(`{"text":"b"}`), poolID, "worker-1")
	require.NoError(t, err)
	assert.False(t, applied)

	// Late seq 3 applied; watermark is now 3.
	applied, err = store.AppendAfterTerminalEvent(ctx, turnID, 3, runtime.EvAssistantMessage, json.RawMessage(`{"text":"c"}`), poolID, "worker-1")
	require.NoError(t, err)
	assert.True(t, applied)

	// Exactly two after-terminal audit events were appended (seq 2, 3); no
	// duplicate for the replayed seq 2.
	evs, err := rt.ListEvents(ctx, runID)
	require.NoError(t, err)
	count := 0
	for _, e := range evs {
		if e.Kind == runtime.EvAssistantMessage {
			count++
		}
	}
	assert.Equal(t, 3, count, "one active + two after-terminal assistant_message events, no duplicate")

	// Gap (seq jumps to 5 while highest is 3): rejected without advancing.
	_, err = store.AppendAfterTerminalEvent(ctx, turnID, 5, runtime.EvAssistantMessage, json.RawMessage(`{}`), poolID, "worker-1")
	assert.ErrorIs(t, err, dispatch.ErrOutOfOrderSequence)

	// A different worker does not own this assignment: it cannot append forged
	// audit events or advance the watermark (HOR-381 certificate-authoritative
	// replay; HOR-249 persisted worker identity). ErrNotFound, not applied.
	applied, err = store.AppendAfterTerminalEvent(ctx, turnID, 4, runtime.EvAssistantMessage, json.RawMessage(`{"text":"forged"}`), poolID, "worker-2")
	assert.ErrorIs(t, err, dispatch.ErrNotFound)
	assert.False(t, applied)

	// A turn with no assignment row at all -> ErrNotFound (nothing to audit).
	_, err = store.AppendAfterTerminalEvent(ctx, "00000000-0000-0000-0000-000000000000", 1, runtime.EvAssistantMessage, json.RawMessage(`{}`), poolID, "worker-1")
	assert.ErrorIs(t, err, dispatch.ErrNotFound)
}
