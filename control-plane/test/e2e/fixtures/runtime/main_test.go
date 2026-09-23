package main

import (
	"context"
	"strings"
	"testing"
	"time"
)

func barrierExpectedParticipants() []workspaceExpectedParticipant {
	return []workspaceExpectedParticipant{
		{Participant: "session-a", WorkItemID: "work-a", AttemptID: "attempt-a", SessionID: "durable-a"},
		{Participant: "session-b", WorkItemID: "work-b", AttemptID: "attempt-b", SessionID: "durable-b"},
	}
}

func barrierArrival(participant, attempt, session string, uid uint32) workspaceBarrierArrival {
	marker := "a"
	if participant == "session-b" {
		marker = "b"
	}
	return workspaceBarrierArrival{
		Participant: participant, AttemptID: attempt, SessionID: session, UID: uid, GID: uid,
		MarkerSHA256: strings.Repeat(marker, 64),
	}
}

func waitBarrierStatus(t *testing.T, barrier *workspaceTwoPartyBarrier, predicate func(workspaceBarrierStatus) bool) workspaceBarrierStatus {
	t.Helper()
	deadline := time.Now().Add(time.Second)
	for time.Now().Before(deadline) {
		status := barrier.status()
		if predicate(status) {
			return status
		}
		time.Sleep(time.Millisecond)
	}
	status := barrier.status()
	t.Fatalf("workspace barrier state did not converge: %+v", status)
	return workspaceBarrierStatus{}
}

func TestWorkspaceTwoPartyBarrierHoldsExactlyOneExpectedArrivalEach(t *testing.T) {
	barrier := newWorkspaceTwoPartyBarrier(time.Second)
	if err := barrier.configure(barrierExpectedParticipants()); err != nil {
		t.Fatal(err)
	}

	done := make(chan error, 2)
	go func() {
		done <- barrier.arriveAndWait(context.Background(), barrierArrival("session-a", "attempt-a", "durable-a", 10000))
	}()
	waitBarrierStatus(t, barrier, func(status workspaceBarrierStatus) bool { return len(status.Arrivals) == 1 && !status.Ready })
	go func() {
		done <- barrier.arriveAndWait(context.Background(), barrierArrival("session-b", "attempt-b", "durable-b", 10001))
	}()
	status := waitBarrierStatus(t, barrier, func(status workspaceBarrierStatus) bool { return status.Ready })
	if status.State != "ready" || len(status.Arrivals) != 2 {
		t.Fatalf("unexpected ready state: %+v", status)
	}
	select {
	case err := <-done:
		t.Fatalf("participant escaped before validated release: %v", err)
	default:
	}
	if err := barrier.release(); err != nil {
		t.Fatal(err)
	}
	for range 2 {
		if err := <-done; err != nil {
			t.Fatalf("released participant failed: %v", err)
		}
	}
}

func TestWorkspaceTwoPartyBarrierDuplicateFailsAllParticipants(t *testing.T) {
	barrier := newWorkspaceTwoPartyBarrier(time.Second)
	if err := barrier.configure(barrierExpectedParticipants()); err != nil {
		t.Fatal(err)
	}
	first := make(chan error, 1)
	go func() {
		first <- barrier.arriveAndWait(context.Background(), barrierArrival("session-a", "attempt-a", "durable-a", 10000))
	}()
	waitBarrierStatus(t, barrier, func(status workspaceBarrierStatus) bool { return len(status.Arrivals) == 1 })

	err := barrier.arriveAndWait(context.Background(), barrierArrival("session-a", "attempt-a", "durable-a", 10000))
	if err == nil || !strings.Contains(err.Error(), "duplicate participant") {
		t.Fatalf("duplicate arrival did not fail explicitly: %v", err)
	}
	if err := <-first; err == nil || !strings.Contains(err.Error(), "duplicate participant") {
		t.Fatalf("held participant was not failed causally: %v", err)
	}
	if status := barrier.status(); status.State != "failed" || status.Ready {
		t.Fatalf("duplicate arrival left a successful barrier: %+v", status)
	}
}

func TestWorkspaceTwoPartyBarrierRejectsChildIdentityCollapse(t *testing.T) {
	barrier := newWorkspaceTwoPartyBarrier(time.Second)
	if err := barrier.configure(barrierExpectedParticipants()); err != nil {
		t.Fatal(err)
	}
	first := make(chan error, 1)
	go func() {
		first <- barrier.arriveAndWait(context.Background(), barrierArrival("session-a", "attempt-a", "durable-a", 10000))
	}()
	waitBarrierStatus(t, barrier, func(status workspaceBarrierStatus) bool { return len(status.Arrivals) == 1 })

	collapsed := barrierArrival("session-b", "attempt-b", "durable-b", 10000)
	err := barrier.arriveAndWait(context.Background(), collapsed)
	if err == nil || !strings.Contains(err.Error(), "UID collision") {
		t.Fatalf("collapsed child identity did not fail causally: %v", err)
	}
	if err := <-first; err == nil || !strings.Contains(err.Error(), "UID collision") {
		t.Fatalf("held participant did not observe identity collapse: %v", err)
	}
	if barrier.status().Ready {
		t.Fatal("collapsed child identity satisfied the barrier")
	}
}

func TestWorkspaceTwoPartyBarrierMissingParticipantCannotProveSequentialWork(t *testing.T) {
	barrier := newWorkspaceTwoPartyBarrier(20 * time.Millisecond)
	if err := barrier.configure(barrierExpectedParticipants()); err != nil {
		t.Fatal(err)
	}
	err := barrier.arriveAndWait(context.Background(), barrierArrival("session-a", "attempt-a", "durable-a", 10000))
	if err == nil || !strings.Contains(err.Error(), "missing participants: session-b") {
		t.Fatalf("one participant did not fail closed: %v", err)
	}
	if status := barrier.status(); status.State != "failed" || status.Ready || len(status.Arrivals) != 1 {
		t.Fatalf("one-worker/sequential arrival satisfied the barrier: %+v", status)
	}
}

func TestWorkspaceTwoPartyBarrierRejectsUnattributedAndMismatchedIdentity(t *testing.T) {
	tests := []struct {
		name    string
		arrival workspaceBarrierArrival
		want    string
	}{
		{name: "unattributed", arrival: barrierArrival("session-c", "attempt-c", "durable-c", 10002), want: "unattributed participant"},
		{name: "attempt mismatch", arrival: barrierArrival("session-a", "wrong-attempt", "durable-a", 10000), want: "identity mismatch"},
		{name: "session mismatch", arrival: barrierArrival("session-a", "attempt-a", "wrong-session", 10000), want: "identity mismatch"},
		{name: "uid gid mismatch", arrival: func() workspaceBarrierArrival {
			arrival := barrierArrival("session-a", "attempt-a", "durable-a", 10000)
			arrival.GID = 10001
			return arrival
		}(), want: "child identity is invalid"},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			barrier := newWorkspaceTwoPartyBarrier(time.Second)
			if err := barrier.configure(barrierExpectedParticipants()); err != nil {
				t.Fatal(err)
			}
			err := barrier.arriveAndWait(context.Background(), test.arrival)
			if err == nil || !strings.Contains(err.Error(), test.want) {
				t.Fatalf("arrival was not rejected with %q: %v", test.want, err)
			}
			if barrier.status().Ready {
				t.Fatal("invalid identity satisfied the barrier")
			}
		})
	}
}

func TestParseWorkspaceBarrierArrivalUsesAttemptAndChildEmittedIdentity(t *testing.T) {
	digest := strings.Repeat("b", 64)
	messages := []map[string]any{
		{"role": "user", "content": `<workflow_node_task>E2E_MODE:workspace-barrier E2E_WORKSPACE_PARTICIPANT:session-a</workflow_node_task>
<workflow_context_json>{"executionHistoryRef":{"attemptId":"attempt-a"}}</workflow_context_json>`},
		{"role": "tool", "content": "workspace-child-proof participant=session-a session=durable-a uid=10000 gid=10000 marker_sha256=" + digest + " invariants=groups-cleared,caps-cleared,no-new-privs,umask-0077,pool-root-0:0:0711,session-tree-0700,sibling-EACCES,tls-key-EACCES"},
	}
	arrival, err := parseWorkspaceBarrierArrival(messages)
	if err != nil {
		t.Fatal(err)
	}
	if arrival.Participant != "session-a" || arrival.AttemptID != "attempt-a" || arrival.SessionID != "durable-a" || arrival.UID != 10000 || arrival.GID != 10000 || arrival.MarkerSHA256 != digest {
		t.Fatalf("unexpected parsed arrival: %+v", arrival)
	}
}
