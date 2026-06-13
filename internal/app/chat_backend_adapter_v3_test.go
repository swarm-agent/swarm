package app

import (
	"bufio"
	"context"
	"crypto/sha1"
	"encoding/base64"
	"encoding/binary"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"os"
	"strings"
	"testing"

	"swarm-refactor/swarmtui/internal/client"
	"swarm-refactor/swarmtui/internal/ui"
)

func TestAPIChatBackendV3RunTurnConsumesCommittedAssistantStream(t *testing.T) {
	var gotPaths []string
	var gotRealtimeQuery string
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		gotPaths = append(gotPaths, r.Method+" "+r.URL.Path)
		switch {
		case r.Method == http.MethodPost && r.URL.Path == "/v3/sessions/session-v3/messages":
			_ = json.NewEncoder(w).Encode(map[string]any{
				"ok":         true,
				"session":    map[string]any{"id": "session-v3", "workspace_path": "/workspace", "workspace_name": "workspace", "title": "V3", "mode": "auto"},
				"projection": map[string]any{"session_id": "session-v3", "last_event_seq": 2, "projection_high_watermark_seq": 2},
				"message":    map[string]any{"id": "msg-user", "session_id": "session-v3", "global_seq": 2, "role": "user", "content": "hello"},
				"run_intent": map[string]any{"session_id": "session-v3", "run_id": "run-1", "status": "pending_executor", "event_seq": 2},
				"messages":   []any{},
				"events":     []any{},
				"mutation":   map[string]any{"realtime_outbox": map[string]any{"endpoint_cursor": "cursor-2", "endpoint_seq": 2, "session_id": "session-v3"}},
			})
		case r.Method == http.MethodGet && r.URL.Path == "/v3/realtime/stream":
			gotRealtimeQuery = r.URL.RawQuery
			writeTestV3RealtimeFrames(t, w, r,
				map[string]any{"protocol": "v3.realtime", "protocol_version": 1, "kind": "replay.started", "session_id": "session-v3", "subscription_id": "active-turn", "endpoint_cursor": "cursor-2", "last_seq": 2, "high_watermark_seq": 6},
				map[string]any{"protocol": "v3.realtime", "protocol_version": 1, "kind": "event", "session_id": "session-v3", "event_type": "session.assistant.started", "last_seq": 3, "event": map[string]any{"id": "evt-3", "session_id": "session-v3", "seq": 3, "event_type": "session.assistant.started", "ts_unix_ms": 30, "payload": map[string]any{"session_id": "session-v3", "run_id": "run-1", "status": "running"}}},
				map[string]any{"protocol": "v3.realtime", "protocol_version": 1, "kind": "event", "session_id": "session-v3", "event_type": "session.assistant.delta", "last_seq": 4, "event": map[string]any{"id": "evt-4", "session_id": "session-v3", "seq": 4, "event_type": "session.assistant.delta", "ts_unix_ms": 31, "payload": map[string]any{"session_id": "session-v3", "run_id": "run-1", "delta": "hel"}}},
				map[string]any{"protocol": "v3.realtime", "protocol_version": 1, "kind": "event", "session_id": "session-v3", "event_type": "session.assistant.completed", "last_seq": 5, "event": map[string]any{"id": "evt-5", "session_id": "session-v3", "seq": 5, "event_type": "session.assistant.completed", "ts_unix_ms": 32, "payload": map[string]any{"session_id": "session-v3", "run_id": "run-1", "status": "running", "message": map[string]any{"id": "msg-assistant", "session_id": "session-v3", "global_seq": 5, "role": "assistant", "content": "hello", "created_at": 32, "metadata": map[string]any{"run_id": "run-1"}}, "run_intent": map[string]any{"run_id": "run-1", "status": "running"}}}},
				map[string]any{"protocol": "v3.realtime", "protocol_version": 1, "kind": "event", "session_id": "session-v3", "event_type": "session.run_intent.recorded", "last_seq": 6, "event": map[string]any{"id": "evt-6", "session_id": "session-v3", "seq": 6, "event_type": "session.run_intent.recorded", "ts_unix_ms": 33, "payload": map[string]any{"session_id": "session-v3", "run_id": "run-1", "status": "completed", "run_intent": map[string]any{"session_id": "session-v3", "run_id": "run-1", "status": "completed", "event_seq": 6, "updated_at": 33}}}},
			)
		default:
			t.Fatalf("unexpected request: %s %s", r.Method, r.URL.Path)
		}
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
	if resp.AssistantMessage.ID != "msg-assistant" || resp.AssistantMessage.Content != "hello" || resp.NoAssistant {
		t.Fatalf("response = %#v", resp)
	}
	if len(gotPaths) != 2 || gotPaths[0] != "POST /v3/sessions/session-v3/messages" || gotPaths[1] != "GET /v3/realtime/stream" {
		t.Fatalf("paths = %#v", gotPaths)
	}
	if !strings.Contains(gotRealtimeQuery, "endpoint_cursor=cursor-2") {
		t.Fatalf("realtime query = %q, want endpoint_cursor from committed mutation outbox", gotRealtimeQuery)
	}
	if resp.PrimaryRunStatus != "completed" {
		t.Fatalf("primary run status = %q", resp.PrimaryRunStatus)
	}
	if len(events) < 6 {
		t.Fatalf("events len = %d: %#v", len(events), events)
	}
	if events[2].Type != "session.lifecycle.updated" || events[2].Lifecycle == nil || !events[2].Lifecycle.Active || events[2].Lifecycle.Phase != "running" {
		t.Fatalf("assistant started event = %#v", events[2])
	}
	if events[3].Type != "assistant.delta" || events[3].Delta != "hel" {
		t.Fatalf("delta event = %#v", events[3])
	}
	if events[4].Type != "message.stored" || events[4].Message == nil || events[4].Message.ID != "msg-assistant" {
		t.Fatalf("completed message event = %#v", events[4])
	}
	if events[5].Type != "session.run_intent.recorded" {
		t.Fatalf("run intent event = %#v", events[5])
	}
	terminalLifecycle := events[len(events)-1]
	if terminalLifecycle.Type != "session.lifecycle.updated" || terminalLifecycle.Lifecycle == nil || terminalLifecycle.Lifecycle.Active || terminalLifecycle.Lifecycle.Phase != "completed" {
		t.Fatalf("terminal lifecycle event = %#v", terminalLifecycle)
	}
}

func TestAPIChatBackendV3RunTurnIgnoresDiagnosticRealtimeFrames(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch {
		case r.Method == http.MethodPost && r.URL.Path == "/v3/sessions/session-v3/messages":
			_ = json.NewEncoder(w).Encode(map[string]any{
				"ok":         true,
				"session":    map[string]any{"id": "session-v3", "workspace_path": "/workspace", "workspace_name": "workspace", "title": "V3", "mode": "auto"},
				"projection": map[string]any{"session_id": "session-v3", "last_event_seq": 2, "projection_high_watermark_seq": 2},
				"message":    map[string]any{"id": "msg-user", "session_id": "session-v3", "global_seq": 2, "role": "user", "content": "hello"},
				"run_intent": map[string]any{"session_id": "session-v3", "run_id": "run-1", "status": "pending_executor", "event_seq": 2},
				"messages":   []any{},
				"events":     []any{},
				"mutation":   map[string]any{"realtime_outbox": map[string]any{"endpoint_cursor": "cursor-2", "endpoint_seq": 2, "session_id": "session-v3"}},
			})
		case r.Method == http.MethodGet && r.URL.Path == "/v3/realtime/stream":
			writeTestV3RealtimeFrames(t, w, r,
				map[string]any{"protocol": "v3.realtime", "protocol_version": 1, "kind": "replay.started", "session_id": "session-v3", "subscription_id": "active-turn", "endpoint_cursor": "cursor-2", "last_seq": 2, "high_watermark_seq": 8},
				map[string]any{"protocol": "v3.realtime", "protocol_version": 1, "kind": "event", "session_id": "session-v3", "endpoint_cursor": "cursor-3", "event_type": "session.diagnostic.provider.stream", "last_seq": 3, "event": map[string]any{"id": "evt-3", "session_id": "session-v3", "seq": 3, "event_type": "session.diagnostic.provider.stream", "ts_unix_ms": 30, "payload": map[string]any{"diagnostic": true, "session_id": "session-v3", "run_id": "run-1", "payload": map[string]any{"delta": "DIAGNOSTIC PROVIDER TOKEN"}}}},
				map[string]any{"protocol": "v3.realtime", "protocol_version": 1, "kind": "event", "session_id": "session-v3", "endpoint_cursor": "cursor-4", "event_type": "session.assistant.started", "last_seq": 4, "event": map[string]any{"id": "evt-4", "session_id": "session-v3", "seq": 4, "event_type": "session.assistant.started", "ts_unix_ms": 31, "payload": map[string]any{"session_id": "session-v3", "run_id": "run-1", "status": "running", "run_intent": map[string]any{"session_id": "session-v3", "run_id": "run-1", "status": "running", "event_seq": 4}}}},
				map[string]any{"protocol": "v3.realtime", "protocol_version": 1, "kind": "event", "session_id": "session-v3", "endpoint_cursor": "cursor-5", "event_type": "session.assistant.delta", "last_seq": 5, "event": map[string]any{"id": "evt-5", "session_id": "session-v3", "seq": 5, "event_type": "session.assistant.delta", "ts_unix_ms": 32, "payload": map[string]any{"run_id": "run-1", "delta": "real assistant text"}}},
				map[string]any{"protocol": "v3.realtime", "protocol_version": 1, "kind": "event", "session_id": "session-v3", "endpoint_cursor": "cursor-6", "event_type": "session.diagnostic.store.result", "last_seq": 6, "event": map[string]any{"id": "evt-6", "session_id": "session-v3", "seq": 6, "event_type": "session.diagnostic.store.result", "ts_unix_ms": 33, "payload": map[string]any{"diagnostic": true, "session_id": "session-v3", "run_id": "run-1", "payload": map[string]any{"event_type": "session.assistant.delta", "event_payload": map[string]any{"delta": "DUPLICATE FROM STORE DIAGNOSTIC"}}}}},
				map[string]any{"protocol": "v3.realtime", "protocol_version": 1, "kind": "event", "session_id": "session-v3", "endpoint_cursor": "cursor-7", "event_type": "session.assistant.completed", "last_seq": 7, "event": map[string]any{"id": "evt-7", "session_id": "session-v3", "seq": 7, "event_type": "session.assistant.completed", "ts_unix_ms": 34, "payload": map[string]any{"session_id": "session-v3", "run_id": "run-1", "status": "completed", "message": map[string]any{"id": "msg-assistant", "session_id": "session-v3", "global_seq": 7, "role": "assistant", "content": "real assistant text", "created_at": 34, "metadata": map[string]any{"run_id": "run-1"}}, "run_intent": map[string]any{"session_id": "session-v3", "run_id": "run-1", "status": "completed", "event_seq": 7}}}},
				map[string]any{"protocol": "v3.realtime", "protocol_version": 1, "kind": "event", "session_id": "session-v3", "endpoint_cursor": "cursor-8", "event_type": "session.run_intent.recorded", "last_seq": 8, "event": map[string]any{"id": "evt-8", "session_id": "session-v3", "seq": 8, "event_type": "session.run_intent.recorded", "ts_unix_ms": 35, "payload": map[string]any{"session_id": "session-v3", "run_id": "run-1", "status": "completed", "run_intent": map[string]any{"session_id": "session-v3", "run_id": "run-1", "status": "completed", "event_seq": 8}}}},
			)
		default:
			t.Fatalf("unexpected request: %s %s", r.Method, r.URL.Path)
		}
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
	if resp.AssistantMessage.ID != "msg-assistant" || resp.AssistantMessage.Content != "real assistant text" || resp.PrimaryRunStatus != "completed" {
		t.Fatalf("response = %#v", resp)
	}
	var deltaCount int
	for _, event := range events {
		if strings.HasPrefix(event.Type, "session.diagnostic.") {
			t.Fatalf("diagnostic event leaked to TUI stream: %#v", event)
		}
		if strings.Contains(event.Delta, "DIAGNOSTIC") || strings.Contains(event.Output, "DIAGNOSTIC") {
			t.Fatalf("diagnostic payload leaked to TUI stream: %#v", event)
		}
		if event.Type == "assistant.delta" {
			deltaCount++
			if event.Delta != "real assistant text" {
				t.Fatalf("assistant delta = %#v", event)
			}
		}
	}
	if deltaCount != 1 {
		t.Fatalf("assistant delta count = %d, events=%#v", deltaCount, events)
	}
}

func TestAPIChatBackendV3RunTurnDoesNotCompleteOnAssistantMessageStored(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch {
		case r.Method == http.MethodPost && r.URL.Path == "/v3/sessions/session-v3/messages":
			_ = json.NewEncoder(w).Encode(map[string]any{
				"ok":         true,
				"session":    map[string]any{"id": "session-v3", "workspace_path": "/workspace", "workspace_name": "workspace", "title": "V3", "mode": "auto"},
				"projection": map[string]any{"session_id": "session-v3", "last_event_seq": 2, "projection_high_watermark_seq": 2},
				"message":    map[string]any{"id": "msg-user", "session_id": "session-v3", "global_seq": 2, "role": "user", "content": "hello"},
				"run_intent": map[string]any{"session_id": "session-v3", "run_id": "run-1", "status": "pending_executor", "event_seq": 2},
				"messages":   []any{},
				"events":     []any{},
				"mutation":   map[string]any{"realtime_outbox": map[string]any{"endpoint_cursor": "cursor-2", "endpoint_seq": 2, "session_id": "session-v3"}},
			})
		case r.Method == http.MethodGet && r.URL.Path == "/v3/realtime/stream":
			writeTestV3RealtimeFrames(t, w, r,
				map[string]any{"protocol": "v3.realtime", "protocol_version": 1, "kind": "replay.started", "session_id": "session-v3", "subscription_id": "active-turn", "endpoint_cursor": "cursor-2", "last_seq": 2, "high_watermark_seq": 5},
				map[string]any{"protocol": "v3.realtime", "protocol_version": 1, "kind": "event", "session_id": "session-v3", "event_type": "session.message.appended", "last_seq": 3, "event": map[string]any{"id": "evt-3", "session_id": "session-v3", "seq": 3, "event_type": "session.message.appended", "ts_unix_ms": 30, "payload": map[string]any{"session_id": "session-v3", "run_id": "run-1", "status": "running", "message": map[string]any{"id": "msg-assistant", "session_id": "session-v3", "global_seq": 3, "role": "assistant", "content": "hello", "created_at": 30, "metadata": map[string]any{"run_id": "run-1"}}, "run_intent": map[string]any{"session_id": "session-v3", "run_id": "run-1", "status": "running", "event_seq": 3}}}},
				map[string]any{"protocol": "v3.realtime", "protocol_version": 1, "kind": "event", "session_id": "session-v3", "event_type": "session.tool.started", "last_seq": 4, "event": map[string]any{"id": "evt-4", "session_id": "session-v3", "seq": 4, "event_type": "session.tool.started", "ts_unix_ms": 31, "payload": map[string]any{"session_id": "session-v3", "run_id": "run-1", "tool_name": "bash", "call_id": "call-1", "arguments": "{}", "step": 1, "run_intent": map[string]any{"session_id": "session-v3", "run_id": "run-1", "status": "running", "event_seq": 4}}}},
				map[string]any{"protocol": "v3.realtime", "protocol_version": 1, "kind": "event", "session_id": "session-v3", "event_type": "session.run_intent.recorded", "last_seq": 5, "event": map[string]any{"id": "evt-5", "session_id": "session-v3", "seq": 5, "event_type": "session.run_intent.recorded", "ts_unix_ms": 32, "payload": map[string]any{"session_id": "session-v3", "run_id": "run-1", "status": "completed", "run_intent": map[string]any{"session_id": "session-v3", "run_id": "run-1", "status": "completed", "event_seq": 5, "updated_at": 32}}}},
			)
		default:
			t.Fatalf("unexpected request: %s %s", r.Method, r.URL.Path)
		}
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
	if resp.PrimaryRunStatus != "completed" || resp.AssistantMessage.ID != "msg-assistant" {
		t.Fatalf("response = %#v", resp)
	}
	if len(events) < 6 {
		t.Fatalf("events len = %d: %#v", len(events), events)
	}
	if events[2].Type != "message.stored" || events[2].Message == nil || events[2].Message.ID != "msg-assistant" {
		t.Fatalf("assistant message event = %#v", events[2])
	}
	if events[3].Type != "tool.started" {
		t.Fatalf("adapter stopped before durable terminal run intent; event[3] = %#v", events[3])
	}
}

func TestAPIChatBackendV3RunTurnTreatsCancelledRunIntentAsTerminal(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch {
		case r.Method == http.MethodPost && r.URL.Path == "/v3/sessions/session-v3/messages":
			_ = json.NewEncoder(w).Encode(map[string]any{
				"ok":         true,
				"session":    map[string]any{"id": "session-v3", "workspace_path": "/workspace", "workspace_name": "workspace", "title": "V3", "mode": "auto"},
				"projection": map[string]any{"session_id": "session-v3", "last_event_seq": 2, "projection_high_watermark_seq": 2},
				"message":    map[string]any{"id": "msg-user", "session_id": "session-v3", "global_seq": 2, "role": "user", "content": "hello"},
				"run_intent": map[string]any{"session_id": "session-v3", "run_id": "run-1", "status": "pending_executor", "event_seq": 2},
				"messages":   []any{},
				"events":     []any{},
				"mutation":   map[string]any{"realtime_outbox": map[string]any{"endpoint_cursor": "cursor-2", "endpoint_seq": 2, "session_id": "session-v3"}},
			})
		case r.Method == http.MethodGet && r.URL.Path == "/v3/realtime/stream":
			writeTestV3RealtimeFrames(t, w, r,
				map[string]any{"protocol": "v3.realtime", "protocol_version": 1, "kind": "replay.started", "session_id": "session-v3", "subscription_id": "active-turn", "endpoint_cursor": "cursor-2", "last_seq": 2, "high_watermark_seq": 3},
				map[string]any{"protocol": "v3.realtime", "protocol_version": 1, "kind": "event", "session_id": "session-v3", "event_type": "session.run.cancelled", "last_seq": 3, "event": map[string]any{"id": "evt-3", "session_id": "session-v3", "seq": 3, "event_type": "session.run.cancelled", "ts_unix_ms": 30, "payload": map[string]any{"session_id": "session-v3", "run_id": "run-1", "status": "cancelled", "error": "stop from test", "run_intent": map[string]any{"session_id": "session-v3", "run_id": "run-1", "status": "cancelled", "blocked_reason": "stop from test", "event_seq": 3, "updated_at": 30}}}},
			)
		default:
			t.Fatalf("unexpected request: %s %s", r.Method, r.URL.Path)
		}
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
	if resp.PrimaryRunStatus != "cancelled" || !resp.NoAssistant || resp.PrimaryBlockedReason != "stop from test" {
		t.Fatalf("response = %#v", resp)
	}
	terminalLifecycle := events[len(events)-1]
	if terminalLifecycle.Type != "session.lifecycle.updated" || terminalLifecycle.Lifecycle == nil || terminalLifecycle.Lifecycle.Active || terminalLifecycle.Lifecycle.Phase != "cancelled" {
		t.Fatalf("terminal lifecycle = %#v", terminalLifecycle)
	}
}

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

func TestAPIChatBackendV3CompactUsesNativeCompactEndpoint(t *testing.T) {
	var gotPaths []string
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		gotPaths = append(gotPaths, r.Method+" "+r.URL.Path)
		if strings.Contains(r.URL.Path, "/v2/") || strings.Contains(r.URL.Path, "/run/stream") || strings.Contains(r.URL.Path, "/messages") {
			t.Fatalf("v3 compact must not route through v2/messages/run stream: %s %s", r.Method, r.URL.Path)
		}
		if r.Method != http.MethodPost || r.URL.Path != "/v3/sessions/session-v3/compact" {
			t.Fatalf("unexpected request: %s %s", r.Method, r.URL.Path)
		}
		var body map[string]any
		if err := json.NewDecoder(r.Body).Decode(&body); err != nil {
			t.Fatalf("decode compact: %v", err)
		}
		if body["note"] != "keep constraints" {
			t.Fatalf("compact body = %#v", body)
		}
		_ = json.NewEncoder(w).Encode(map[string]any{
			"ok":      true,
			"session": map[string]any{"id": "session-v3", "workspace_path": "/workspace", "workspace_name": "workspace", "title": "V3 (Compact #2)", "mode": "auto"},
			"result": map[string]any{
				"session_id": "session-v3", "agent": "swarm", "model": "gpt-test", "thinking": "medium", "reasoning_summary": "Context compacted into checkpoint #2.", "steps": 1,
				"usage_summary":     map[string]any{"session_id": "session-v3", "provider": "test", "model": "gpt-test", "source": "context_compaction_reset", "context_window": 1000, "remaining_tokens": 1000},
				"assistant_message": map[string]any{"id": "msg-ack", "session_id": "session-v3", "role": "assistant", "content": "Manual context compact complete (Compact #2)."},
			},
			"events": []any{
				map[string]any{"type": "usage.updated", "session_id": "session-v3", "run_id": "run-compact", "usage_summary": map[string]any{"session_id": "session-v3", "provider": "test", "model": "gpt-test", "source": "context_compaction_reset", "context_window": 1000, "remaining_tokens": 1000}},
				map[string]any{"type": "message.stored", "session_id": "session-v3", "run_id": "run-compact", "message": map[string]any{"id": "msg-ack", "session_id": "session-v3", "role": "assistant", "content": "Manual context compact complete (Compact #2)."}},
			},
		})
	}))
	defer server.Close()

	backend := newAPIChatBackend(testAPIWithToken(server.URL), "v3")
	var events []ui.ChatRunStreamEvent
	resp, err := backend.RunTurnStream(context.Background(), "session-v3", ui.ChatRunRequest{Prompt: "keep constraints", Compact: true}, func(event ui.ChatRunStreamEvent) {
		events = append(events, event)
	})
	if err != nil {
		t.Fatalf("RunTurnStream() error = %v", err)
	}
	if len(gotPaths) != 1 || gotPaths[0] != "POST /v3/sessions/session-v3/compact" {
		t.Fatalf("paths = %#v", gotPaths)
	}
	if resp.UsageSummary == nil || resp.UsageSummary.ContextWindow != 1000 || resp.UsageSummary.RemainingTokens != 1000 {
		t.Fatalf("usage summary did not reset context window: %+v", resp.UsageSummary)
	}
	if resp.AssistantMessage.ID != "msg-ack" || !strings.Contains(resp.AssistantMessage.Content, "Manual context compact complete") {
		t.Fatalf("assistant ack = %+v", resp.AssistantMessage)
	}
	if len(events) != 2 || events[0].Type != "usage.updated" || events[1].Type != "message.stored" {
		t.Fatalf("events = %#v", events)
	}
}

func TestAPIChatBackendV3RunTurnRefetchesOnRealtimeCursorGap(t *testing.T) {
	var gotPaths []string
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		gotPaths = append(gotPaths, r.Method+" "+r.URL.Path+"?"+r.URL.RawQuery)
		switch {
		case r.Method == http.MethodPost && r.URL.Path == "/v3/sessions/session-v3/messages":
			_ = json.NewEncoder(w).Encode(map[string]any{
				"ok":         true,
				"session":    map[string]any{"id": "session-v3", "workspace_path": "/workspace", "workspace_name": "workspace", "title": "V3", "mode": "auto"},
				"projection": map[string]any{"session_id": "session-v3", "last_event_seq": 2, "projection_high_watermark_seq": 2},
				"message":    map[string]any{"id": "msg-user", "session_id": "session-v3", "global_seq": 2, "role": "user", "content": "hello"},
				"run_intent": map[string]any{"session_id": "session-v3", "run_id": "run-1", "status": "pending_executor", "event_seq": 2},
				"messages":   []any{},
				"events":     []any{},
				"mutation":   map[string]any{"realtime_outbox": map[string]any{"endpoint_cursor": "cursor-2", "endpoint_seq": 2, "session_id": "session-v3"}},
			})
		case r.Method == http.MethodGet && r.URL.Path == "/v3/realtime/stream":
			writeTestV3RealtimeFrames(t, w, r,
				map[string]any{"protocol": "v3.realtime", "protocol_version": 1, "kind": "replay.started", "session_id": "session-v3", "subscription_id": "active-turn", "endpoint_cursor": "cursor-2", "last_seq": 2, "high_watermark_seq": 6},
				map[string]any{"protocol": "v3.realtime", "protocol_version": 1, "kind": "event", "session_id": "session-v3", "event_type": "session.assistant.delta", "last_seq": 4, "rev": 2, "prevRev": 1, "event": map[string]any{"id": "evt-4", "session_id": "session-v3", "seq": 4, "event_type": "session.assistant.delta", "ts_unix_ms": 31, "payload": json.RawMessage(`{"session_id":"session-v3","run_id":"run-1","delta":"gap"}`)}},
			)
		case r.Method == http.MethodGet && r.URL.Path == "/v3/sessions/session-v3":
			if r.URL.Query().Get("event_limit") != "200" || r.URL.Query().Get("message_limit") != "500" {
				t.Fatalf("refetch query = %q", r.URL.RawQuery)
			}
			_ = json.NewEncoder(w).Encode(map[string]any{
				"ok":         true,
				"session":    map[string]any{"id": "session-v3", "workspace_path": "/workspace", "workspace_name": "workspace", "title": "V3", "mode": "auto"},
				"projection": map[string]any{"session_id": "session-v3", "last_event_seq": 6, "projection_high_watermark_seq": 6},
				"messages": []any{
					map[string]any{"id": "msg-user", "session_id": "session-v3", "global_seq": 2, "role": "user", "content": "hello"},
					map[string]any{"id": "msg-assistant", "session_id": "session-v3", "global_seq": 5, "role": "assistant", "content": "hello after refetch", "metadata": map[string]any{"run_id": "run-1"}},
				},
				"events": []any{
					map[string]any{"id": "evt-5", "session_id": "session-v3", "seq": 5, "event_type": "session.assistant.completed", "ts_unix_ms": 32, "payload": json.RawMessage(`{"session_id":"session-v3","run_id":"run-1","message":{"id":"msg-assistant","session_id":"session-v3","global_seq":5,"role":"assistant","content":"hello after refetch","metadata":{"run_id":"run-1"}}}`)},
					map[string]any{"id": "evt-6", "session_id": "session-v3", "seq": 6, "event_type": "session.run_intent.recorded", "ts_unix_ms": 33, "payload": json.RawMessage(`{"session_id":"session-v3","run_id":"run-1","status":"completed","run_intent":{"session_id":"session-v3","run_id":"run-1","status":"completed","event_seq":6}}`)},
				},
				"pending_permissions": []any{},
			})
		default:
			t.Fatalf("unexpected request: %s %s", r.Method, r.URL.Path)
		}
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
	if resp.PrimaryRunStatus != "completed" || resp.AssistantMessage.Content != "hello after refetch" {
		t.Fatalf("response = %#v", resp)
	}
	if len(gotPaths) != 3 || !strings.HasPrefix(gotPaths[2], "GET /v3/sessions/session-v3?") {
		t.Fatalf("paths = %#v", gotPaths)
	}
	if len(events) < 2 || events[2].Type != "session.refetched" {
		t.Fatalf("events = %#v, want session.refetched after local gap", events)
	}
}

func TestAPIChatBackendV3ActiveTurnUsesLiveWebSocketStream(t *testing.T) {
	body, err := os.ReadFile("chat_backend_adapter.go")
	if err != nil {
		t.Fatalf("read chat_backend_adapter.go: %v", err)
	}
	source := string(body)
	if !strings.Contains(source, "StreamSessionsV3Realtime(") {
		t.Fatalf("V3 active turn consumption must use the native multiplexed V3 realtime websocket")
	}
	for _, forbidden := range []string{"StreamSessionV3(streamCtx", "StreamSessionV3Replay(streamCtx", "ReplaySessionV3Events(streamCtx"} {
		if strings.Contains(source, forbidden) {
			t.Fatalf("V3 active turn consumption must not use %s in place of the native realtime websocket", forbidden)
		}
	}
}

func TestAPIChatBackendV3MapsPermissionAndUsageRealtimeEvents(t *testing.T) {
	requested := v3StreamEventToChatEvent(client.SessionV3Event{
		SessionID: "session-v3",
		EventType: "permission.requested",
		Payload:   json.RawMessage(`{"session_id":"session-v3","run_id":"run-v3","tool_name":"ask-user","call_id":"call-perm","permission":{"id":"perm-1","session_id":"session-v3","run_id":"run-v3","call_id":"call-perm","tool_name":"ask-user","tool_arguments":"{\"question\":\"Continue?\"}","status":"pending","mode":"auto"}}`),
	})
	if requested.Type != "permission.requested" || requested.Permission == nil || requested.Permission.ID != "perm-1" || requested.ToolName != "ask-user" || requested.CallID != "call-perm" {
		t.Fatalf("mapped permission.requested = %#v", requested)
	}

	updated := v3StreamEventToChatEvent(client.SessionV3Event{
		SessionID: "session-v3",
		EventType: "permission.updated",
		Payload:   json.RawMessage(`{"session_id":"session-v3","run_id":"run-v3","permission":{"id":"perm-1","session_id":"session-v3","run_id":"run-v3","call_id":"call-perm","tool_name":"ask-user","status":"approved","decision":"allow_once","reason":"ok"}}`),
	})
	if updated.Type != "permission.updated" || updated.Permission == nil || updated.Permission.Status != "approved" || updated.Permission.Decision != "allow_once" {
		t.Fatalf("mapped permission.updated = %#v", updated)
	}

	usage := v3StreamEventToChatEvent(client.SessionV3Event{
		SessionID: "session-v3",
		EventType: "usage.updated",
		Payload:   json.RawMessage(`{"session_id":"session-v3","run_id":"run-v3","turn_usage":{"session_id":"session-v3","run_id":"run-v3","context_window":1000,"total_tokens":42,"transport":"websocket","connected_via_websocket":true},"usage_summary":{"session_id":"session-v3","context_window":1000,"total_tokens":42,"remaining_tokens":958,"last_run_id":"run-v3"}}`),
	})
	if usage.Type != "usage.updated" || usage.TurnUsage == nil || usage.TurnUsage.TotalTokens != 42 || usage.UsageSummary == nil || usage.UsageSummary.RemainingTokens != 958 {
		t.Fatalf("mapped usage.updated = %#v", usage)
	}
}

func TestAPIChatBackendV3MapsSessionToolEventsToLiveToolStream(t *testing.T) {
	started := v3StreamEventToChatEvent(client.SessionV3Event{
		SessionID: "session-v3",
		EventType: "session.tool.started",
		Payload:   json.RawMessage(`{"session_id":"session-v3","run_id":"run-v3","tool_name":"bash","call_id":"call-1","arguments":"{\"command\":\"echo hi\"}","step":2}`),
	})
	if started.Type != "tool.started" || started.ToolName != "bash" || started.CallID != "call-1" || started.Arguments == "" || started.Step != 2 {
		t.Fatalf("mapped started event = %#v", started)
	}

	delta := v3StreamEventToChatEvent(client.SessionV3Event{
		SessionID: "session-v3",
		EventType: "session.tool.delta",
		Payload:   json.RawMessage(`{"session_id":"session-v3","run_id":"run-v3","tool_name":"bash","call_id":"call-1","output":"chunk"}`),
	})
	if delta.Type != "tool.delta" || delta.Output != "chunk" || delta.ToolName != "bash" || delta.CallID != "call-1" {
		t.Fatalf("mapped delta event = %#v", delta)
	}

	completed := v3StreamEventToChatEvent(client.SessionV3Event{
		SessionID: "session-v3",
		EventType: "session.tool.completed",
		Payload:   json.RawMessage(`{"session_id":"session-v3","run_id":"run-v3","tool_name":"bash","call_id":"call-1","output":"done","raw_output":"raw done","duration_ms":7}`),
	})
	if completed.Type != "tool.completed" || completed.Output != "done" || completed.RawOutput != "raw done" || completed.DurationMS != 7 {
		t.Fatalf("mapped completed event = %#v", completed)
	}
}

func writeTestV3RealtimeFrames(t *testing.T, w http.ResponseWriter, r *http.Request, frames ...map[string]any) {
	t.Helper()
	hijacker, ok := w.(http.Hijacker)
	if !ok {
		t.Fatalf("response writer does not support hijacking")
	}
	conn, rw, err := hijacker.Hijack()
	if err != nil {
		t.Fatalf("hijack websocket: %v", err)
	}
	defer conn.Close()
	key := strings.TrimSpace(r.Header.Get("Sec-WebSocket-Key"))
	if key == "" {
		t.Fatalf("missing websocket key")
	}
	acceptHash := sha1.Sum([]byte(key + "258EAFA5-E914-47DA-95CA-C5AB0DC85B11"))
	accept := base64.StdEncoding.EncodeToString(acceptHash[:])
	if _, err := rw.WriteString("HTTP/1.1 101 Switching Protocols\r\nUpgrade: websocket\r\nConnection: Upgrade\r\nSec-WebSocket-Accept: " + accept + "\r\n\r\n"); err != nil {
		t.Fatalf("write websocket handshake: %v", err)
	}
	if err := rw.Flush(); err != nil {
		t.Fatalf("flush websocket handshake: %v", err)
	}
	_, subscribePayload, err := readTestWebSocketText(rw.Reader)
	if err != nil {
		t.Fatalf("read realtime subscribe: %v", err)
	}
	var subscribe map[string]any
	if err := json.Unmarshal(subscribePayload, &subscribe); err != nil {
		t.Fatalf("decode realtime subscribe: %v", err)
	}
	if subscribe["protocol"] != "v3.realtime" || subscribe["kind"] != "subscribe.session" || subscribe["session_id"] != "session-v3" {
		t.Fatalf("subscribe frame = %#v", subscribe)
	}
	if _, hasAfterSeq := subscribe["after_seq"]; hasAfterSeq {
		t.Fatalf("canonical realtime subscribe must not use after_seq: %#v", subscribe)
	}
	nextRev := uint64(1)
	for _, frame := range frames {
		if frame["kind"] == "event" {
			if _, ok := frame["rev"]; !ok {
				frame["rev"] = nextRev
				frame["prevRev"] = nextRev - 1
				nextRev++
			} else if rev, ok := numericTestFrameValue(frame["rev"]); ok {
				nextRev = rev + 1
			}
		}
		raw, err := json.Marshal(frame)
		if err != nil {
			t.Fatalf("marshal websocket frame: %v", err)
		}
		if err := writeTestWebSocketText(rw.Writer, raw); err != nil {
			t.Fatalf("write websocket frame: %v", err)
		}
		if err := rw.Flush(); err != nil {
			t.Fatalf("flush websocket frame: %v", err)
		}
	}
}

func numericTestFrameValue(value any) (uint64, bool) {
	switch v := value.(type) {
	case uint64:
		return v, true
	case int:
		if v >= 0 {
			return uint64(v), true
		}
	case int64:
		if v >= 0 {
			return uint64(v), true
		}
	case float64:
		if v >= 0 {
			return uint64(v), true
		}
	}
	return 0, false
}

func readTestWebSocketText(r *bufio.Reader) (byte, []byte, error) {
	head := make([]byte, 2)
	if _, err := r.Read(head); err != nil {
		return 0, nil, err
	}
	opcode := head[0] & 0x0F
	masked := head[1]&0x80 != 0
	payloadLength := int(head[1] & 0x7F)
	if payloadLength == 126 {
		ext := make([]byte, 2)
		if _, err := r.Read(ext); err != nil {
			return 0, nil, err
		}
		payloadLength = int(ext[0])<<8 | int(ext[1])
	} else if payloadLength == 127 {
		ext := make([]byte, 8)
		if _, err := r.Read(ext); err != nil {
			return 0, nil, err
		}
		payloadLength = int(binary.BigEndian.Uint64(ext))
	}
	var mask [4]byte
	if masked {
		if _, err := r.Read(mask[:]); err != nil {
			return 0, nil, err
		}
	}
	payload := make([]byte, payloadLength)
	if _, err := r.Read(payload); err != nil {
		return 0, nil, err
	}
	if masked {
		for i := range payload {
			payload[i] ^= mask[i%4]
		}
	}
	return opcode, payload, nil
}

func writeTestWebSocketText(w *bufio.Writer, payload []byte) error {
	header := []byte{0x81}
	switch length := len(payload); {
	case length <= 125:
		header = append(header, byte(length))
	case length <= 65535:
		header = append(header, 126, byte(length>>8), byte(length))
	default:
		header = append(header, 127)
		ext := make([]byte, 8)
		binary.BigEndian.PutUint64(ext, uint64(length))
		header = append(header, ext...)
	}
	if _, err := w.Write(header); err != nil {
		return err
	}
	_, err := w.Write(payload)
	return err
}
