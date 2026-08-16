package main

import (
	"encoding/base64"
	"encoding/json"
	"fmt"
	"log"
	"net/http"
	"os"
	"regexp"
	"strings"
	"sync/atomic"
	"time"
)

var (
	requests  atomic.Int64
	cancelled atomic.Int64
	uuidRE    = regexp.MustCompile(`[0-9a-f]{8}-[0-9a-f]{4}-[0-9a-f]{4}-[0-9a-f]{4}-[0-9a-f]{12}`)
	bashRE    = regexp.MustCompile(`E2E_BASH:([A-Za-z0-9+/=]+)`)
)

type completionRequest struct {
	Model    string           `json:"model"`
	Messages []map[string]any `json:"messages"`
	Stream   bool             `json:"stream"`
}

func main() {
	mux := http.NewServeMux()
	mux.HandleFunc("/health", func(w http.ResponseWriter, _ *http.Request) { w.WriteHeader(http.StatusOK) })
	mux.HandleFunc("/stats", stats)
	mux.HandleFunc("/v1/chat/completions", completions)
	server := &http.Server{Addr: ":8080", Handler: mux, ReadHeaderTimeout: 5 * time.Second}
	log.Printf("deterministic E2E model listening on %s", server.Addr)
	if err := server.ListenAndServe(); err != nil && err != http.ErrServerClosed {
		log.Fatal(err)
	}
}

func stats(w http.ResponseWriter, _ *http.Request) {
	w.Header().Set("Content-Type", "application/json")
	_ = json.NewEncoder(w).Encode(map[string]int64{"requests": requests.Load(), "cancelled": cancelled.Load()})
}

func completions(w http.ResponseWriter, r *http.Request) {
	requests.Add(1)
	var input completionRequest
	if err := json.NewDecoder(http.MaxBytesReader(w, r.Body, 1<<20)).Decode(&input); err != nil {
		http.Error(w, "invalid request", http.StatusBadRequest)
		return
	}
	transcriptBytes, _ := json.Marshal(input.Messages)
	transcript := string(transcriptBytes)
	if strings.Contains(transcript, "E2E_SLOW") {
		select {
		case <-r.Context().Done():
			cancelled.Add(1)
			return
		case <-time.After(30 * time.Second):
		}
	}

	choice := nextChoice(transcript)
	if !input.Stream {
		w.Header().Set("Content-Type", "application/json")
		_ = json.NewEncoder(w).Encode(map[string]any{
			"id": "fixture-completion", "object": "chat.completion", "model": input.Model,
			"choices": []any{map[string]any{
				"index": 0, "message": map[string]any{"role": "assistant", "content": "deterministic completion"}, "finish_reason": "stop",
			}},
			"usage": map[string]int{"prompt_tokens": 4, "completion_tokens": 2, "total_tokens": 6},
		})
		return
	}

	w.Header().Set("Content-Type", "text/event-stream")
	w.Header().Set("Cache-Control", "no-cache")
	w.WriteHeader(http.StatusOK)
	frame := map[string]any{"id": "fixture-completion", "object": "chat.completion.chunk", "model": input.Model, "choices": []any{choice}}
	data, _ := json.Marshal(frame)
	_, _ = fmt.Fprintf(w, "data: %s\n\n", data)
	_, _ = fmt.Fprint(w, "data: [DONE]\n\n")
	if flush, ok := w.(http.Flusher); ok {
		flush.Flush()
	}
}

func nextChoice(transcript string) map[string]any {
	currentTurn := transcript
	if index := strings.LastIndex(transcript, "E2E_MODE:"); index >= 0 {
		currentTurn = transcript[index:]
	}
	if strings.Contains(currentTurn, "Workflow step completion recorded") {
		return textChoice("Deterministic workflow step complete.")
	}
	toolResults := strings.Count(currentTurn, `"role":"tool"`)
	if strings.Contains(currentTurn, "E2E_MODE:outcome-unknown") && toolResults > 0 {
		return textChoice("The write outcome is unknown; do not repeat it.")
	}
	if toolResults == 0 {
		if choice := initialToolChoice(currentTurn); choice != nil {
			return choice
		}
	}

	artifactID := ""
	if strings.Contains(currentTurn, "artifact_refs") {
		if matches := uuidRE.FindAllString(currentTurn, -1); len(matches) > 0 {
			// The latest tool result is appended after assignment/run context. Its
			// published artifact is therefore the final UUID in the transcript.
			artifactID = matches[len(matches)-1]
		}
	}
	args := map[string]any{
		"outcome": "completed",
		"summary": "Deterministic deployed execution completed.",
		"output":  map[string]any{"result": "verified"},
	}
	if artifactID != "" {
		args["artifact_refs"] = []any{map[string]any{"artifact_id": artifactID, "role": "evidence", "metadata": map[string]any{"label": "Deterministic evidence"}}}
	}
	encoded, _ := json.Marshal(args)
	return toolChoice("fixture-complete", "complete_step", string(encoded))
}

func initialToolChoice(currentTurn string) map[string]any {
	switch {
	case strings.Contains(currentTurn, "E2E_MODE:read-artifact"):
		return toolChoice("fixture-read", "platform.fixture_read", `{"message":"produce attributable evidence"}`)
	case strings.Contains(currentTurn, "E2E_MODE:consequence"):
		return toolChoice("fixture-write", "platform.fixture_write", `{"target":"synthetic-record","mode":"success"}`)
	case strings.Contains(currentTurn, "E2E_MODE:outcome-unknown"):
		return toolChoice("fixture-write-unknown", "platform.fixture_write", `{"target":"ambiguous-record","mode":"crash"}`)
	case strings.Contains(currentTurn, "E2E_MODE:idempotent-race"):
		return duplicateToolChoice("fixture-upsert", "platform.fixture_upsert", `{"record":"same-logical-write"}`)
	case strings.Contains(currentTurn, "E2E_MODE:isolation"):
		match := bashRE.FindStringSubmatch(currentTurn)
		command := "printf isolation-default"
		if len(match) == 2 {
			if decoded, err := base64.StdEncoding.DecodeString(match[1]); err == nil {
				command = string(decoded)
			}
		}
		args, _ := json.Marshal(map[string]string{"command": command})
		return toolChoice("fixture-bash", "bash", string(args))
	default:
		return nil
	}
}

func duplicateToolChoice(id, name, arguments string) map[string]any {
	calls := make([]any, 0, 2)
	for index := range 2 {
		calls = append(calls, map[string]any{
			"index": index, "id": id, "type": "function",
			"function": map[string]string{"name": name, "arguments": arguments},
		})
	}
	return map[string]any{
		"index":         0,
		"delta":         map[string]any{"role": "assistant", "tool_calls": calls},
		"finish_reason": "tool_calls",
	}
}

func textChoice(content string) map[string]any {
	return map[string]any{
		"index":         0,
		"delta":         map[string]any{"role": "assistant", "content": content},
		"finish_reason": "stop",
	}
}

func toolChoice(id, name, arguments string) map[string]any {
	return map[string]any{
		"index": 0,
		"delta": map[string]any{"role": "assistant", "tool_calls": []any{map[string]any{
			"index": 0, "id": id, "type": "function", "function": map[string]string{"name": name, "arguments": arguments},
		}}},
		"finish_reason": "tool_calls",
	}
}

func init() {
	log.SetFlags(log.LstdFlags | log.LUTC)
	_ = os.Setenv("TZ", "UTC")
}
