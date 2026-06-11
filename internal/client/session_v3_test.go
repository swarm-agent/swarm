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
			if body["swarm_id"] != "host-swarm" {
				t.Fatalf("v3 create swarm_id = %#v, want host-swarm", body["swarm_id"])
			}
			if body["workspace_binding_id"] != "binding-primary" {
				t.Fatalf("v3 create workspace_binding_id = %#v, want binding-primary", body["workspace_binding_id"])
			}
			if body["target_kind"] != "host" || body["target_relationship"] != "self" {
				t.Fatalf("v3 create target = %#v/%#v, want host/self", body["target_kind"], body["target_relationship"])
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
	created, err := api.CreateSessionV3WithOptions(context.Background(), SessionCreateOptions{Title: "V3", WorkspacePath: "/workspace", WorkspaceName: "workspace", WorkspaceBindingID: "binding-primary", SwarmID: "host-swarm", TargetKind: "host", TargetRelationship: "self", Mode: "auto"})
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

func TestSessionV3WorksetClientPreservesNormalizedContract(t *testing.T) {
	var gotPath string
	var gotRequest SessionV3WorksetRequest
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		gotPath = r.Method + " " + r.URL.Path
		if r.Method != http.MethodPost || r.URL.Path != "/v3/sessions:workset" {
			t.Fatalf("unexpected request: %s %s", r.Method, r.URL.Path)
		}
		if err := json.NewDecoder(r.Body).Decode(&gotRequest); err != nil {
			t.Fatalf("decode workset request: %v", err)
		}
		_ = json.NewEncoder(w).Encode(map[string]any{
			"ok": true,
			"sessions_by_id": map[string]any{
				"session-a": map[string]any{"id": "session-a", "workspace_path": "/workspace", "workspace_name": "workspace", "title": "A", "mode": "auto", "updated_at": 2000, "message_count": 2},
			},
			"projections_by_session": map[string]any{
				"session-a": map[string]any{"session_id": "session-a", "last_event_seq": 7, "projection_high_watermark_seq": 7, "updated_at": 2000},
			},
			"messages_by_session": map[string]any{
				"session-a": []map[string]any{{"id": "msg-1", "session_id": "session-a", "global_seq": 1, "role": "user", "content": "hi", "created_at": 1000}},
			},
			"events_by_session": map[string]any{
				"session-a": []map[string]any{{"id": "evt-1", "session_id": "session-a", "seq": 1, "event_type": "session.message.appended", "payload": map[string]any{"session_id": "session-a"}, "ts_unix_ms": 1000}},
			},
			"plans_by_session":              map[string]any{"session-a": []any{}},
			"plan_revisions_by_session":     map[string]any{"session-a": []any{}},
			"permissions_by_session":        map[string]any{"session-a": []any{}},
			"usage_by_session":              map[string]any{"session-a": map[string]any{"session_id": "session-a", "provider": "openai", "model": "gpt", "source": "session", "context_window": 100, "turn_count": 1, "updated_at": 1000}},
			"preferences_by_session":        map[string]any{"session-a": map[string]any{"provider": "openai", "model": "gpt", "thinking": "medium", "service_tier": "auto", "context_mode": "standard"}},
			"agent_model_policy_by_session": map[string]any{"session-a": map[string]any{"agent_name": "swarm", "resolved_agent_name": "swarm", "source": "agent_preset", "locked": true, "preference": map[string]any{"provider": "openai", "model": "gpt"}, "context_window": 100, "max_output_tokens": 20}},
			"run_intents_by_session":        map[string]any{"session-a": []map[string]any{{"session_id": "session-a", "run_id": "run-1", "status": "running", "created_at": 1001, "updated_at": 1002, "event_seq": 2}}},
			"history_manifests_by_session": map[string]any{
				"session-a": []map[string]any{{"chunk_id": "session-a:messages:1-1", "resource": "messages", "from_seq": 1, "to_seq": 1, "message_count": 1, "event_count": 0, "complete": true}},
			},
			"history_chunks_by_id": map[string]any{
				"session-a:messages:1-1": map[string]any{"chunk_id": "session-a:messages:1-1", "resource": "messages", "messages": []map[string]any{{"id": "msg-1", "session_id": "session-a", "global_seq": 1, "role": "user", "content": "hi", "created_at": 1000}}},
			},
			"omissions":     []map[string]any{{"session_id": "session-a", "resource": "messages", "reason": "requires_manifest", "next_cursor": "session-a:messages:2", "manifest_ref": "session-a:messages"}},
			"pagination":    map[string]any{"next_before_updated_at": 1234, "next_before_session_id": "session-a", "has_more": true},
			"watermarks":    map[string]any{"loaded_at": 3000, "max_updated_at": 2000},
			"session_order": []string{"session-a"},
		})
	}))
	defer server.Close()

	api := New(server.URL)
	api.SetToken("test-token")
	beforeUpdatedAt := int64(5000)
	workset, err := api.GetSessionV3Workset(context.Background(), SessionV3WorksetRequest{
		SessionIDs: []string{"session-a"},
		Workspace:  SessionV3WorksetWorkspace{WorkspacePath: "/workspace"},
		Recent:     SessionV3WorksetRecent{Limit: 10, BeforeUpdatedAt: &beforeUpdatedAt, BeforeSessionID: "session-z"},
		History: SessionV3WorksetHistory{
			Mode:                  "full",
			MaxMessagesPerSession: 1,
			MaxEventsPerSession:   2,
			ManifestPolicy:        "manifest",
		},
	})
	if err != nil {
		t.Fatalf("GetSessionV3Workset() error = %v", err)
	}
	if gotPath != "POST /v3/sessions:workset" {
		t.Fatalf("request path = %q", gotPath)
	}
	if gotRequest.Recent.BeforeUpdatedAt == nil || *gotRequest.Recent.BeforeUpdatedAt != beforeUpdatedAt || gotRequest.Recent.BeforeSessionID != "session-z" {
		t.Fatalf("recent cursor not encoded: %#v", gotRequest.Recent)
	}
	if gotRequest.History.Mode != "full" || gotRequest.History.ManifestPolicy != "manifest" || gotRequest.History.MaxMessagesPerSession != 1 || gotRequest.History.MaxEventsPerSession != 2 {
		t.Fatalf("history request not encoded: %#v", gotRequest.History)
	}
	if workset.SessionOrder[0] != "session-a" || workset.SessionsByID["session-a"].SessionAPI != "v3" || workset.SessionsByID["session-a"].LastEventSeq != 7 {
		t.Fatalf("sessions not decoded/marked: %#v", workset.SessionsByID["session-a"])
	}
	if len(workset.MessagesBySession["session-a"]) != 1 || len(workset.EventsBySession["session-a"]) != 1 {
		t.Fatalf("history maps not decoded: messages=%#v events=%#v", workset.MessagesBySession, workset.EventsBySession)
	}
	if workset.Pagination.NextBeforeUpdatedAt == nil || *workset.Pagination.NextBeforeUpdatedAt != 1234 || workset.Pagination.NextBeforeSessionID != "session-a" || !workset.Pagination.HasMore {
		t.Fatalf("pagination not decoded: %#v", workset.Pagination)
	}
	if workset.Watermarks.LoadedAt != 3000 {
		t.Fatalf("watermarks not decoded: watermarks=%#v", workset.Watermarks)
	}
	manifest := workset.HistoryManifestsBySession["session-a"]
	if len(manifest) != 1 || manifest[0].ChunkID != "session-a:messages:1-1" || manifest[0].MessageCount != 1 || !manifest[0].Complete {
		t.Fatalf("manifest not decoded: %#v", manifest)
	}
	chunk := workset.HistoryChunksByID["session-a:messages:1-1"]
	if chunk.ChunkID == "" || len(chunk.Messages) != 1 || chunk.Messages[0].ID != "msg-1" {
		t.Fatalf("chunk not decoded: %#v", chunk)
	}
	if len(workset.Omissions) != 1 || workset.Omissions[0].Reason != "requires_manifest" || workset.Omissions[0].NextCursor != "session-a:messages:2" || workset.Omissions[0].ManifestRef != "session-a:messages" {
		t.Fatalf("omission not decoded: %#v", workset.Omissions)
	}
	if workset.AgentModelPolicyBySession["session-a"].Preference.Model != "gpt" || !workset.AgentModelPolicyBySession["session-a"].Locked {
		t.Fatalf("agent model policy not decoded: %#v", workset.AgentModelPolicyBySession)
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
