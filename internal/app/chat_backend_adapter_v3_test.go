package app

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"testing"

	"swarm-refactor/swarmtui/internal/ui"
)

func TestAPIChatBackendV3RunTurnCommitsPrimaryMessageOnly(t *testing.T) {
	var gotPath string
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		gotPath = r.URL.Path
		if r.Method != http.MethodPost || r.URL.Path != "/v3/sessions/session-v3/messages" {
			t.Fatalf("unexpected request: %s %s", r.Method, r.URL.Path)
		}
		var body map[string]any
		if err := json.NewDecoder(r.Body).Decode(&body); err != nil {
			t.Fatalf("decode message: %v", err)
		}
		if body["content"] != "hello" || body["role"] != "user" {
			t.Fatalf("message body = %#v", body)
		}
		_ = json.NewEncoder(w).Encode(map[string]any{
			"ok":         true,
			"session":    map[string]any{"id": "session-v3", "workspace_path": "/workspace", "workspace_name": "workspace", "title": "V3", "mode": "auto"},
			"projection": map[string]any{"session_id": "session-v3", "last_event_seq": 2, "projection_high_watermark_seq": 2},
			"message":    map[string]any{"id": "msg-1", "session_id": "session-v3", "global_seq": 2, "role": "user", "content": "hello"},
			"run_intent": map[string]any{"session_id": "session-v3", "run_id": "run-1", "status": "dispatch_blocked", "blocked_reason": "invalid dispatch authority", "event_seq": 2},
			"messages":   []any{},
			"events":     []any{},
		})
	}))
	defer server.Close()

	backend := newAPIChatBackend(testAPIWithToken(server.URL), "v3")
	var events []ui.ChatRunStreamEvent
	resp, err := backend.RunTurnStream(context.Background(), "session-v3", ui.ChatRunRequest{Prompt: "hello"}, func(event ui.ChatRunStreamEvent) {
		events = append(events, event)
	})
	if err != nil {
		t.Fatalf("RunTurnStream() error = %v", err)
	}
	if gotPath != "/v3/sessions/session-v3/messages" {
		t.Fatalf("path = %q, want v3 messages", gotPath)
	}
	if !resp.NoAssistant || resp.PrimaryRunStatus != "dispatch_blocked" || resp.PrimaryBlockedReason == "" {
		t.Fatalf("response = %#v", resp)
	}
	if len(events) != 2 {
		t.Fatalf("events len = %d, want 2 (%#v)", len(events), events)
	}
	if events[0].Type != "message.stored" || events[0].Message == nil || events[0].Message.ID != "msg-1" {
		t.Fatalf("message event = %#v", events[0])
	}
	if events[1].Type != "session.lifecycle.updated" || events[1].Lifecycle == nil || events[1].Lifecycle.Phase != "dispatch_blocked" {
		t.Fatalf("lifecycle event = %#v", events[1])
	}
}
