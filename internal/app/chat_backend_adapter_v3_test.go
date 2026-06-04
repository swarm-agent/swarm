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
			})
		case r.Method == http.MethodGet && r.URL.Path == "/v3/sessions/session-v3/stream":
			writeTestV3StreamFrames(t, w, r,
				map[string]any{"type": "replay.started", "ok": true, "session_id": "session-v3", "after_seq": 2, "high_watermark_seq": 5},
				map[string]any{"type": "event", "ok": true, "session_id": "session-v3", "last_seq": 3, "event": map[string]any{"id": "evt-3", "session_id": "session-v3", "seq": 3, "event_type": "session.assistant.started", "ts_unix_ms": 30, "payload": map[string]any{"session_id": "session-v3", "run_id": "run-1", "status": "running"}}},
				map[string]any{"type": "event", "ok": true, "session_id": "session-v3", "last_seq": 4, "event": map[string]any{"id": "evt-4", "session_id": "session-v3", "seq": 4, "event_type": "session.assistant.delta", "ts_unix_ms": 31, "payload": map[string]any{"session_id": "session-v3", "run_id": "run-1", "delta": "hel"}}},
				map[string]any{"type": "event", "ok": true, "session_id": "session-v3", "last_seq": 5, "event": map[string]any{"id": "evt-5", "session_id": "session-v3", "seq": 5, "event_type": "session.assistant.completed", "ts_unix_ms": 32, "payload": map[string]any{"session_id": "session-v3", "run_id": "run-1", "status": "completed", "message": map[string]any{"id": "msg-assistant", "session_id": "session-v3", "global_seq": 5, "role": "assistant", "content": "hello", "created_at": 32, "metadata": map[string]any{"run_id": "run-1"}}, "run_intent": map[string]any{"run_id": "run-1", "status": "completed"}}}},
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
	if len(gotPaths) != 2 || gotPaths[0] != "POST /v3/sessions/session-v3/messages" || gotPaths[1] != "GET /v3/sessions/session-v3/stream" {
		t.Fatalf("paths = %#v", gotPaths)
	}
	if len(events) < 5 {
		t.Fatalf("events len = %d: %#v", len(events), events)
	}
	if events[2].Type != "session.lifecycle.updated" || events[2].Lifecycle == nil || !events[2].Lifecycle.Active || events[2].Lifecycle.Phase != "running" {
		t.Fatalf("assistant started event = %#v", events[2])
	}
	if events[3].Type != "assistant.delta" || events[3].Delta != "hel" {
		t.Fatalf("delta event = %#v", events[3])
	}
	if events[4].Type != "message.stored" || events[4].Message == nil || events[4].Message.ID != "msg-assistant" {
		t.Fatalf("completed event = %#v", events[4])
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

func TestAPIChatBackendV3ActiveTurnUsesLiveWebSocketStream(t *testing.T) {
	body, err := os.ReadFile("chat_backend_adapter.go")
	if err != nil {
		t.Fatalf("read chat_backend_adapter.go: %v", err)
	}
	source := string(body)
	if !strings.Contains(source, "StreamSessionV3(") {
		t.Fatalf("V3 active turn consumption must use live StreamSessionV3 websocket")
	}
	if strings.Contains(source, "StreamSessionV3Replay(streamCtx") {
		t.Fatalf("V3 active turn consumption must not use replay polling in place of the live websocket")
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

func writeTestV3StreamFrames(t *testing.T, w http.ResponseWriter, r *http.Request, frames ...map[string]any) {
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
	for _, frame := range frames {
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
