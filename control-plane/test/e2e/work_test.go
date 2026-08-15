package e2e_test

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"net/url"
	"reflect"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/nunocgoncalves/iterabase-mono/testkit/e2e/poll"
)

const privateSourceMarker = "PRIVATE-SOURCE-MUST-NOT-LEAK-478"

type workItemResponse struct {
	ID               string `json:"id"`
	CurrentAttemptID string `json:"currentAttemptId"`
	State            string `json:"state"`
	Title            string `json:"title"`
	Source           struct {
		Kind  string `json:"kind"`
		Title string `json:"title"`
	} `json:"source"`
	Blocker *struct {
		ID   string `json:"id"`
		Kind string `json:"kind"`
	} `json:"blocker,omitempty"`
}

type blockerResponse struct {
	ID      string `json:"id"`
	State   string `json:"state"`
	Attempt string `json:"attemptId"`
}

type feedbackResponse struct {
	ID               string  `json:"id"`
	AttemptID        string  `json:"attemptId"`
	RevisedAttemptID *string `json:"revisedAttemptId,omitempty"`
}

func setupWorkJourneyStage(t *testing.T, state *deployedState) {
	t.Helper()
	state.captureBootstrapKeys(t)
	state.createWorkIdentity(t, "work-journey@local")
	state.applyWorkFixture(t)

	status, body := state.request(t, http.MethodGet, "/v1/work-items", state.adminKey, nil, nil)
	requireStatus(t, status, http.StatusForbidden, body)
	status, body = state.requestJSON(t, http.MethodPost, "/v1/work-items", state.workKey, workStartBody("Case without key"))
	requireStatus(t, status, http.StatusBadRequest, body)
}

func concurrentIdempotentStartStage(t *testing.T, state *deployedState) {
	t.Helper()
	payload, err := json.Marshal(workStartBody("Case 478 — concurrent start"))
	if err != nil {
		t.Fatal(err)
	}
	const concurrency = 8
	type result struct {
		status int
		body   []byte
		err    error
	}
	results := make(chan result, concurrency)
	var wait sync.WaitGroup
	for range concurrency {
		wait.Add(1)
		go func() {
			defer wait.Done()
			status, body, requestErr := state.doRequest(http.MethodPost, "/v1/work-items", state.workKey, payload, map[string]string{
				"Content-Type": "application/json", "Idempotency-Key": "hor-478-concurrent-1",
			})
			results <- result{status: status, body: body, err: requestErr}
		}()
	}
	wait.Wait()
	close(results)

	created := 0
	ids := make(map[string]struct{})
	for current := range results {
		if current.err != nil {
			t.Fatalf("concurrent work start: %v", current.err)
		}
		state.recordRequest(t, requestEvidence{Method: http.MethodPost, Path: "/v1/work-items", Status: current.status,
			Fields: map[string]any{"idempotencyFixture": "hor-478-concurrent-1"}})
		if current.status == http.StatusCreated {
			created++
		} else if current.status != http.StatusOK {
			t.Fatalf("concurrent work start status=%d body=%s", current.status, safeResponse(current.body))
		}
		var item workItemResponse
		mustDecode(t, current.body, &item)
		if item.ID == "" {
			t.Fatal("concurrent start returned an empty work item ID")
		}
		ids[item.ID] = struct{}{}
		state.workItemID = item.ID
	}
	if created != 1 || len(ids) != 1 {
		t.Fatalf("concurrent starts created=%d uniqueIDs=%d, want 1/1", created, len(ids))
	}

	changed := workStartBody("Changed payload")
	status, body := state.requestJSONWithHeaders(t, http.MethodPost, "/v1/work-items", state.workKey, changed,
		map[string]string{"Idempotency-Key": "hor-478-concurrent-1"})
	requireStatus(t, status, http.StatusConflict, body)

	waitForBlockedWorkItem(t, state)

	for _, path := range []string{
		"/v1/work-items?state=blocked",
		"/v1/work-items?search=" + url.QueryEscape("concurrent start"),
		"/v1/work-items?search=" + url.QueryEscape(privateSourceMarker),
	} {
		status, listBody := state.request(t, http.MethodGet, path, state.workKey, nil, nil)
		requireStatus(t, status, http.StatusOK, listBody)
		assertCustomerSafeJSON(t, listBody, privateSourceMarker)
		var items []workItemResponse
		mustDecode(t, listBody, &items)
		if strings.Contains(path, "PRIVATE-SOURCE") {
			if len(items) != 0 {
				t.Fatalf("private source is searchable through customer API: %s", safeResponse(listBody))
			}
		} else if len(items) != 1 || items[0].ID != state.workItemID {
			t.Fatalf("work filter %s returned %s", path, safeResponse(listBody))
		}
	}

	initialTimeline := getTimeline(t, state)
	if len(initialTimeline) == 0 {
		t.Fatal("work start produced no customer timeline")
	}
	assertStrictCursorOrder(t, initialTimeline, 0)
	attributed := false
	// Decode the actor field separately because customer-safe timeline events
	// intentionally expose attribution but no credentials.
	status, timelineBody := state.request(t, http.MethodGet, "/v1/work-items/"+state.workItemID+"/timeline", state.workKey, nil, nil)
	requireStatus(t, status, http.StatusOK, timelineBody)
	var attributedEvents []struct {
		Code            string  `json:"code"`
		ActorIdentityID *string `json:"actorIdentityId"`
	}
	mustDecode(t, timelineBody, &attributedEvents)
	for _, event := range attributedEvents {
		if event.Code == "work_item_created" && event.ActorIdentityID != nil && *event.ActorIdentityID == state.workIdentityID {
			attributed = true
		}
	}
	if !attributed {
		t.Fatal("work_item_created event is not attributable to the authenticated starter")
	}
	initialSSE := state.streamEventsAfter(t, 0, len(initialTimeline))
	assertStrictCursorOrder(t, initialSSE, 0)
	state.initialCursor = initialSSE[len(initialSSE)-1].Cursor
}

func workCommandsAndHistoryStage(t *testing.T, state *deployedState) {
	t.Helper()
	status, body := state.request(t, http.MethodGet, "/v1/work-items/"+state.workItemID+"/blocker", state.workKey, nil, nil)
	requireStatus(t, status, http.StatusOK, body)
	assertCustomerSafeJSON(t, body, privateSourceMarker)
	var blocker blockerResponse
	mustDecode(t, body, &blocker)
	if blocker.ID == "" {
		t.Fatal("open blocker has no ID")
	}

	status, denied := state.requestJSON(t, http.MethodPost, "/v1/work-blockers/"+blocker.ID+"/responses", state.adminKey,
		map[string]any{"outcome": "accepted", "response": map[string]any{"approved": true}})
	requireStatus(t, status, http.StatusForbidden, denied)
	status, body = state.requestJSON(t, http.MethodPost, "/v1/work-blockers/"+blocker.ID+"/responses", state.workKey,
		map[string]any{"outcome": "accepted", "response": map[string]any{"approved": true}})
	requireStatus(t, status, http.StatusOK, body)

	status, body = state.request(t, http.MethodGet, "/v1/work-items/"+state.workItemID, state.workKey, nil, nil)
	requireStatus(t, status, http.StatusOK, body)
	assertCustomerSafeJSON(t, body, privateSourceMarker)
	var completed workItemResponse
	mustDecode(t, body, &completed)
	if completed.State != "done" {
		t.Fatalf("resolved human gate state=%q want done", completed.State)
	}

	status, attemptsBody := state.request(t, http.MethodGet, "/v1/work-items/"+state.workItemID+"/attempts", state.workKey, nil, nil)
	requireStatus(t, status, http.StatusOK, attemptsBody)
	assertCustomerSafeJSON(t, attemptsBody, privateSourceMarker)
	var attempts []map[string]any
	mustDecode(t, attemptsBody, &attempts)
	if len(attempts) != 1 {
		t.Fatalf("attempts=%d want 1", len(attempts))
	}
	state.firstAttemptJSON, _ = json.Marshal(attempts[0])
	firstAttemptID, _ := attempts[0]["id"].(string)
	if firstAttemptID == "" {
		t.Fatal("first attempt has no ID")
	}
	status, nodesBody := state.request(t, http.MethodGet, "/v1/work-attempts/"+firstAttemptID+"/nodes", state.workKey, nil, nil)
	requireStatus(t, status, http.StatusOK, nodesBody)
	assertCustomerSafeJSON(t, nodesBody, privateSourceMarker)

	status, body = state.requestJSON(t, http.MethodPost, "/v1/work-items/"+state.workItemID+"/feedback", state.workKey,
		map[string]any{"attemptId": firstAttemptID, "category": "poor_output", "explanation": "Use the reviewed business evidence."})
	requireStatus(t, status, http.StatusCreated, body)
	var feedback feedbackResponse
	mustDecode(t, body, &feedback)
	if feedback.ID == "" || feedback.AttemptID != firstAttemptID || feedback.RevisedAttemptID != nil {
		t.Fatalf("feedback unexpectedly started work: %s", safeResponse(body))
	}
	status, attemptsBody = state.request(t, http.MethodGet, "/v1/work-items/"+state.workItemID+"/attempts", state.workKey, nil, nil)
	requireStatus(t, status, http.StatusOK, attemptsBody)
	mustDecode(t, attemptsBody, &attempts)
	if len(attempts) != 1 {
		t.Fatalf("saving feedback created %d attempts, want 1", len(attempts))
	}

	status, body = state.requestJSON(t, http.MethodPost, "/v1/work-items/"+state.workItemID+"/revisions", state.workKey,
		map[string]any{"feedbackId": feedback.ID, "actionableGuidance": "Review the supplied evidence and confirm again."})
	requireStatus(t, status, http.StatusCreated, body)
	var revised workItemResponse
	mustDecode(t, body, &revised)
	if revised.CurrentAttemptID == firstAttemptID {
		t.Fatalf("revision did not create a new attempt: %s", safeResponse(body))
	}
	revised = waitForBlockedWorkItem(t, state)
	status, body = state.requestJSON(t, http.MethodPost, "/v1/work-blockers/"+revised.Blocker.ID+"/responses", state.workKey,
		map[string]any{"outcome": "accepted", "response": map[string]any{"approved": true}})
	requireStatus(t, status, http.StatusOK, body)

	status, attemptsBody = state.request(t, http.MethodGet, "/v1/work-items/"+state.workItemID+"/attempts", state.workKey, nil, nil)
	requireStatus(t, status, http.StatusOK, attemptsBody)
	assertCustomerSafeJSON(t, attemptsBody, privateSourceMarker)
	mustDecode(t, attemptsBody, &attempts)
	if len(attempts) != 2 {
		t.Fatalf("revision attempts=%d want 2", len(attempts))
	}
	currentFirst, _ := json.Marshal(attempts[0])
	if !bytes.Equal(currentFirst, state.firstAttemptJSON) {
		t.Fatalf("first attempt history mutated\nbefore=%s\nafter=%s", state.firstAttemptJSON, currentFirst)
	}
	if attempts[1]["revisedFromAttemptId"] != firstAttemptID {
		t.Fatalf("revised attempt is not linked to original: %v", attempts[1])
	}
}

func restartAndReconnectStage(t *testing.T, state *deployedState) {
	t.Helper()
	beforeRestart := getTimeline(t, state)
	assertStrictCursorOrder(t, beforeRestart, 0)
	expected := make([]timelineEvent, 0)
	for _, event := range beforeRestart {
		if event.Cursor > state.initialCursor {
			expected = append(expected, event)
		}
	}
	if len(expected) == 0 {
		t.Fatal("work commands produced no events after the initial SSE cursor")
	}

	state.restartAPI(t)
	reconnected := state.streamEventsAfter(t, state.initialCursor, len(expected))
	assertStrictCursorOrder(t, reconnected, state.initialCursor)
	if cursors(reconnected) == nil || !reflect.DeepEqual(cursors(reconnected), cursors(expected)) {
		t.Fatalf("SSE reconnect cursors=%v want=%v", cursors(reconnected), cursors(expected))
	}

	status, body := state.request(t, http.MethodGet, "/v1/work-items/"+state.workItemID, state.workKey, nil, nil)
	requireStatus(t, status, http.StatusOK, body)
	assertCustomerSafeJSON(t, body, privateSourceMarker)
	var item workItemResponse
	mustDecode(t, body, &item)
	if item.State != "done" {
		t.Fatalf("work item state after restart=%q want done", item.State)
	}
	status, body = state.request(t, http.MethodGet, "/v1/work-items/"+state.workItemID+"/attempts", state.workKey, nil, nil)
	requireStatus(t, status, http.StatusOK, body)
	var attempts []map[string]any
	mustDecode(t, body, &attempts)
	if len(attempts) != 2 {
		t.Fatalf("API restart duplicated or lost attempts: %d", len(attempts))
	}
	status, doneList := state.request(t, http.MethodGet, "/v1/work-items?state=done", state.workKey, nil, nil)
	requireStatus(t, status, http.StatusOK, doneList)
	var items []workItemResponse
	mustDecode(t, doneList, &items)
	if len(items) != 1 || items[0].ID != state.workItemID {
		t.Fatalf("done filter after restart returned %s", safeResponse(doneList))
	}
}

func workStartBody(title string) map[string]any {
	return map[string]any{
		"workflowKey": "e2e/manual-review",
		"title":       title,
		"source": map[string]any{
			"privateMarker": privateSourceMarker,
			"prompt":        "private fixture prompt",
			"credentials":   map[string]any{"token": "synthetic-private-value"},
			"workerId":      "private-worker-id",
			"rawToolTrace":  []any{"must remain private"},
		},
		"sourcePresentation": map[string]any{
			"kind": "api", "title": "Reviewed case", "subtitle": "Synthetic deployed fixture",
			"evidence": []any{map[string]any{"label": map[string]any{"en": "Customer", "pt": "Cliente"}, "value": "Example Industries"}},
		},
	}
}

func waitForBlockedWorkItem(t *testing.T, state *deployedState) workItemResponse {
	t.Helper()
	var item workItemResponse
	err := poll.Until(state.ctx, 30*time.Second, 250*time.Millisecond, func(_ context.Context) (bool, string, error) {
		status, body, requestErr := state.doRequest(http.MethodGet, "/v1/work-items/"+state.workItemID, state.workKey, nil, nil)
		if requestErr != nil {
			return false, "request failed", requestErr
		}
		if status != http.StatusOK {
			return false, fmt.Sprintf("status=%d body=%s", status, safeResponse(body)), fmt.Errorf("observe human blocker: status %d", status)
		}
		if bytes.Contains(body, []byte(privateSourceMarker)) {
			return false, "private source marker leaked", fmt.Errorf("private source reached customer work item")
		}
		if err := json.Unmarshal(body, &item); err != nil {
			return false, safeResponse(body), err
		}
		return item.State == "blocked" && item.Blocker != nil && item.Blocker.ID != "", safeResponse(body), nil
	})
	if err != nil {
		t.Fatalf("wait for deployed dispatch to materialize human blocker: %v", err)
	}
	state.recordRequest(t, requestEvidence{Method: http.MethodGet, Path: "/v1/work-items/" + state.workItemID, Status: http.StatusOK})
	return item
}

func (state *deployedState) requestJSONWithHeaders(t *testing.T, method, path, key string, value any, headers map[string]string) (int, []byte) {
	t.Helper()
	body, err := json.Marshal(value)
	if err != nil {
		t.Fatal(err)
	}
	if headers == nil {
		headers = make(map[string]string)
	}
	headers["Content-Type"] = "application/json"
	return state.request(t, method, path, key, body, headers)
}

func getTimeline(t *testing.T, state *deployedState) []timelineEvent {
	t.Helper()
	status, body := state.request(t, http.MethodGet, "/v1/work-items/"+state.workItemID+"/timeline?limit=500", state.workKey, nil, nil)
	requireStatus(t, status, http.StatusOK, body)
	assertCustomerSafeJSON(t, body, privateSourceMarker)
	var events []timelineEvent
	mustDecode(t, body, &events)
	return events
}

func cursors(events []timelineEvent) []int64 {
	result := make([]int64, len(events))
	for index, event := range events {
		result[index] = event.Cursor
	}
	return result
}
