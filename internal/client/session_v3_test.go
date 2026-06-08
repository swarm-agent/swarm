package client

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"testing"
)

func TestSessionV3ClientUsesPrimaryRoutes(t *testing.T) {
	var seen []string
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		seen = append(seen, r.Method+" "+r.URL.Path)
		switch {
		case r.Method == http.MethodPost && r.URL.Path == "/v3/sessions":
			var body map[string]any
			if err := json.NewDecoder(r.Body).Decode(&body); err != nil {
				t.Fatalf("decode create: %v", err)
			}
			if _, ok := body["swarm_id"]; ok {
				t.Fatalf("v3 create included swarm_id: %#v", body)
			}
			if _, ok := body["workspace_binding_id"]; ok {
				t.Fatalf("v3 create included workspace_binding_id: %#v", body)
			}
			_ = json.NewEncoder(w).Encode(map[string]any{
				"ok":                true,
				"session":           map[string]any{"id": "session-v3", "workspace_path": body["workspace_path"], "workspace_name": body["workspace_name"], "title": body["title"], "mode": body["mode"]},
				"projection":        map[string]any{"session_id": "session-v3", "last_event_seq": 1, "projection_high_watermark_seq": 1},
				"messages":          []any{},
				"events":            []any{},
				"active_run_intent": map[string]any{"session_id": "session-v3", "run_id": "run-create", "status": "running", "created_at": 1000, "updated_at": 1001, "event_seq": 1},
			})
		case r.Method == http.MethodGet && r.URL.Path == "/v3/sessions/session-v3":
			_ = json.NewEncoder(w).Encode(map[string]any{
				"ok":                true,
				"session":           map[string]any{"id": "session-v3", "workspace_path": "/workspace", "workspace_name": "workspace", "title": "V3", "mode": "auto"},
				"projection":        map[string]any{"session_id": "session-v3", "last_event_seq": 2, "projection_high_watermark_seq": 2},
				"messages":          []map[string]any{{"id": "msg-1", "session_id": "session-v3", "global_seq": 2, "role": "user", "content": "hi"}},
				"events":            []any{},
				"active_run_intent": map[string]any{"session_id": "session-v3", "run_id": "run-active", "status": "running", "created_at": 2000, "updated_at": 2001, "event_seq": 2},
			})
		case r.Method == http.MethodPost && r.URL.Path == "/v3/sessions/session-v3/messages":
			var body map[string]any
			if err := json.NewDecoder(r.Body).Decode(&body); err != nil {
				t.Fatalf("decode message: %v", err)
			}
			if body["role"] != "user" || body["content"] != "hi" {
				t.Fatalf("message body = %#v", body)
			}
			_ = json.NewEncoder(w).Encode(map[string]any{
				"ok":         true,
				"session":    map[string]any{"id": "session-v3", "workspace_path": "/workspace", "workspace_name": "workspace", "title": "V3", "mode": "auto"},
				"projection": map[string]any{"session_id": "session-v3", "last_event_seq": 3, "projection_high_watermark_seq": 3},
				"message":    map[string]any{"id": "msg-2", "session_id": "session-v3", "global_seq": 3, "role": "user", "content": "hi"},
				"run_intent": map[string]any{"session_id": "session-v3", "run_id": "run-1", "status": "pending_executor", "event_seq": 3},
				"messages":   []any{},
				"events":     []any{},
			})
		case r.Method == http.MethodPost && r.URL.Path == "/v3/sessions/session-v3/run/stop":
			var body map[string]any
			if err := json.NewDecoder(r.Body).Decode(&body); err != nil {
				t.Fatalf("decode stop: %v", err)
			}
			if body["type"] != "run.stop" || body["run_id"] != "run-1" {
				t.Fatalf("stop body = %#v", body)
			}
			_ = json.NewEncoder(w).Encode(map[string]any{"ok": true, "session_id": "session-v3", "run_id": "run-1", "status": "failed"})
		default:
			t.Fatalf("unexpected request: %s %s", r.Method, r.URL.Path)
		}
	}))
	defer server.Close()

	api := New(server.URL)
	api.SetToken("test-token")
	created, err := api.CreateSessionV3WithOptions(context.Background(), SessionCreateOptions{Title: "V3", WorkspacePath: "/workspace", WorkspaceName: "workspace", Mode: "auto"})
	if err != nil {
		t.Fatalf("CreateSessionV3WithOptions() error = %v", err)
	}
	if created.Session.SessionAPI != "v3" || created.Session.ProjectionHighWatermarkSeq != 1 {
		t.Fatalf("created session = %#v", created.Session)
	}
	if created.ActiveRunIntent == nil || created.ActiveRunIntent.RunID != "run-create" || created.ActiveRunIntent.CreatedAt != 1000 {
		t.Fatalf("created active_run_intent = %#v", created.ActiveRunIntent)
	}
	hydrated, err := api.GetSessionV3(context.Background(), "session-v3")
	if err != nil {
		t.Fatalf("GetSessionV3() error = %v", err)
	}
	if hydrated.ActiveRunIntent == nil || hydrated.ActiveRunIntent.RunID != "run-active" || hydrated.ActiveRunIntent.CreatedAt != 2000 {
		t.Fatalf("hydrated active_run_intent = %#v", hydrated.ActiveRunIntent)
	}
	msg, err := api.SendSessionV3Message(context.Background(), "session-v3", SessionV3MessageOptions{Content: "hi"})
	if err != nil {
		t.Fatalf("SendSessionV3Message() error = %v", err)
	}
	if msg.RunIntent.Status != "pending_executor" || msg.Message.ID != "msg-2" {
		t.Fatalf("message result = %#v", msg)
	}
	if err := api.StopSessionV3Run(context.Background(), "session-v3", "run-1", ""); err != nil {
		t.Fatalf("StopSessionV3Run() error = %v", err)
	}
	want := []string{"POST /v3/sessions", "GET /v3/sessions/session-v3", "POST /v3/sessions/session-v3/messages", "POST /v3/sessions/session-v3/run/stop"}
	if len(seen) != len(want) {
		t.Fatalf("requests = %#v, want %#v", seen, want)
	}
	for i := range want {
		if seen[i] != want[i] {
			t.Fatalf("request[%d] = %q, want %q", i, seen[i], want[i])
		}
	}
}

func TestStreamSessionsV3RealtimeMultiplexesSubscriptionsAndIsolatesGaps(t *testing.T) {
	var gotPath string
	var subscribes []map[string]any
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		gotPath = r.URL.Path
		if r.Method != http.MethodGet || r.URL.Path != "/v3/realtime/stream" {
			t.Fatalf("websocket path = %s %s, want GET /v3/realtime/stream", r.Method, r.URL.Path)
		}
		conn, rw, err := hijackLifecycleTestWebsocket(w, r)
		if err != nil {
			t.Fatalf("hijack websocket: %v", err)
		}
		defer conn.Close()
		for i := 0; i < 2; i++ {
			_, payload, err := readClientLifecycleTestFrame(rw)
			if err != nil {
				t.Fatalf("read subscribe %d: %v", i, err)
			}
			var msg map[string]any
			if err := json.Unmarshal(payload, &msg); err != nil {
				t.Fatalf("decode subscribe %d: %v", i, err)
			}
			subscribes = append(subscribes, msg)
		}
		writeServerLifecycleTestFrame(t, conn, map[string]any{"protocol": "v3.realtime", "protocol_version": 1, "kind": "replay.started", "session_id": "session-a", "after_seq": 1})
		writeServerLifecycleTestFrame(t, conn, map[string]any{"protocol": "v3.realtime", "protocol_version": 1, "kind": "replay.complete", "session_id": "session-a", "last_seq": 1})
		writeServerLifecycleTestFrame(t, conn, map[string]any{"protocol": "v3.realtime", "protocol_version": 1, "kind": "replay.started", "session_id": "session-b", "after_seq": 1})
		writeServerLifecycleTestFrame(t, conn, map[string]any{"protocol": "v3.realtime", "protocol_version": 1, "kind": "replay.complete", "session_id": "session-b", "last_seq": 1})
		writeServerLifecycleTestFrame(t, conn, map[string]any{"protocol": "v3.realtime", "protocol_version": 1, "kind": "event", "session_id": "session-a", "event_type": "session.assistant.delta", "last_seq": 3, "high_watermark_seq": 3, "event": map[string]any{"id": "evt-a-3", "session_id": "session-a", "seq": 3, "event_type": "session.assistant.delta", "payload": map[string]any{"session_id": "session-a", "delta": "gap"}}})
		writeServerLifecycleTestFrame(t, conn, map[string]any{"protocol": "v3.realtime", "protocol_version": 1, "kind": "event", "session_id": "session-b", "event_type": "session.assistant.delta", "last_seq": 2, "event": map[string]any{"id": "evt-b-2", "session_id": "session-b", "seq": 2, "event_type": "session.assistant.delta", "payload": map[string]any{"session_id": "session-b", "delta": "ok"}}})
	}))
	defer server.Close()

	api := New(server.URL)
	api.SetToken("test-token")
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	var frames []V3RealtimeFrame
	err := api.StreamSessionsV3Realtime(ctx, []V3RealtimeSubscription{{SessionID: "session-a", AfterSeq: 1, SubscriptionID: "sub-a"}, {SessionID: "session-b", AfterSeq: 1, SubscriptionID: "sub-b"}}, func(frame V3RealtimeFrame) {
		frames = append(frames, frame)
		if frame.Kind == "event" && frame.SessionID == "session-b" && frame.Event != nil && frame.Event.Seq == 2 {
			cancel()
		}
	})
	if err != nil {
		t.Fatalf("StreamSessionsV3Realtime() error = %v", err)
	}
	if gotPath != "/v3/realtime/stream" {
		t.Fatalf("got path = %q", gotPath)
	}
	if len(subscribes) != 2 || subscribes[0]["kind"] != "subscribe.session" || subscribes[0]["session_id"] != "session-a" || subscribes[0]["after_seq"] != float64(1) || subscribes[1]["session_id"] != "session-b" {
		t.Fatalf("subscribes = %#v", subscribes)
	}
	var sawGap, sawB bool
	for _, frame := range frames {
		if frame.Kind == "cursor.error" && frame.SessionID == "session-a" && frame.ErrorCode == "session_cursor_gap" {
			sawGap = true
		}
		if frame.Kind == "event" && frame.SessionID == "session-b" && frame.Event != nil && frame.Event.Seq == 2 {
			sawB = true
		}
		if frame.Kind == "event" && frame.SessionID == "session-a" {
			t.Fatalf("gap event for session-a was delivered: %#v", frame)
		}
	}
	if !sawGap || !sawB {
		t.Fatalf("frames did not prove gap isolation; sawGap=%v sawB=%v frames=%#v", sawGap, sawB, frames)
	}
}

func TestStreamSessionV3ReplayDoesNotStopOnAssistantCompleted(t *testing.T) {
	var replayCalls int
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodGet || r.URL.Path != "/v3/sessions/session-v3/events" {
			t.Fatalf("unexpected request: %s %s", r.Method, r.URL.Path)
		}
		replayCalls++
		lastSeq := uint64(2)
		if replayCalls == 1 {
			lastSeq = 1
		}
		_ = json.NewEncoder(w).Encode(map[string]any{
			"ok":                 true,
			"session_id":         "session-v3",
			"projection":         map[string]any{"session_id": "session-v3", "last_event_seq": lastSeq, "projection_high_watermark_seq": lastSeq},
			"high_watermark_seq": lastSeq,
			"next_seq":           lastSeq + 1,
			"events":             []any{map[string]any{"id": "evt-1", "session_id": "session-v3", "seq": 1, "event_type": "session.assistant.completed", "payload": map[string]any{"session_id": "session-v3", "run_id": "run-1", "status": "running"}}},
		})
	}))
	defer server.Close()

	api := New(server.URL)
	api.SetToken("test-token")
	ctx, cancel := context.WithCancel(context.Background())
	var frames []SessionV3StreamFrame
	err := api.StreamSessionV3Replay(ctx, "session-v3", 0, func(frame SessionV3StreamFrame) {
		frames = append(frames, frame)
		if frame.Type == "replay.complete" && replayCalls >= 2 {
			cancel()
		}
	})
	if err != nil {
		t.Fatalf("StreamSessionV3Replay() error = %v", err)
	}
	if replayCalls < 2 {
		t.Fatalf("replay stopped on assistant.completed after %d calls; frames=%#v", replayCalls, frames)
	}
}

func TestStreamSessionV3WebSocketDeliversCursorErrorWithoutClosing(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodGet || r.URL.Path != "/v3/sessions/session-v3/stream" {
			t.Fatalf("websocket path = %s %s, want GET /v3/sessions/session-v3/stream", r.Method, r.URL.Path)
		}
		conn, rw, err := hijackLifecycleTestWebsocket(w, r)
		if err != nil {
			t.Fatalf("hijack websocket: %v", err)
		}
		defer conn.Close()
		if _, _, err := readClientLifecycleTestFrame(rw); err != nil {
			t.Fatalf("read stream hello: %v", err)
		}
		writeServerLifecycleTestFrame(t, conn, map[string]any{"type": "cursor.error", "session_id": "session-v3", "error": "gap"})
		writeServerLifecycleTestFrame(t, conn, map[string]any{"type": "event", "session_id": "session-v3", "last_seq": 1, "event": map[string]any{"id": "evt-1", "session_id": "session-v3", "seq": 1, "event_type": "session.assistant.delta", "payload": map[string]any{"session_id": "session-v3", "delta": "after"}}})
	}))
	defer server.Close()

	api := New(server.URL)
	api.SetToken("test-token")
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	var sawCursorError, sawEvent bool
	err := api.StreamSessionV3(ctx, "session-v3", 0, func(frame SessionV3StreamFrame) {
		if frame.Type == "cursor.error" {
			sawCursorError = true
		}
		if frame.Type == "event" && frame.Event != nil && frame.Event.Seq == 1 {
			sawEvent = true
			cancel()
		}
	})
	if err != nil {
		t.Fatalf("StreamSessionV3() error = %v", err)
	}
	if !sawCursorError || !sawEvent {
		t.Fatalf("frames did not prove cursor.error was non-terminal; sawCursorError=%v sawEvent=%v", sawCursorError, sawEvent)
	}
}
