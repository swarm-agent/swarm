package client

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"reflect"
	"strings"
	"testing"
	"time"
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
			if body["agent_name"] != "swarm" {
				t.Fatalf("v3 create agent_name = %#v, want swarm", body["agent_name"])
			}
			if body["mode"] != "auto" {
				t.Fatalf("v3 create mode = %#v, want auto", body["mode"])
			}
			profile, _ := body["model_profile"].(map[string]any)
			if profile["saved_profile_id"] != "profile-1" {
				t.Fatalf("v3 create model_profile = %#v", body["model_profile"])
			}
			if body["worktree_mode"] != "on" || body["worktree_base_branch"] != "dev" || body["worktree_branch_name"] != "agent/client-v3" {
				t.Fatalf("v3 create worktree fields = %#v", body)
			}
			_ = json.NewEncoder(w).Encode(map[string]any{
				"ok":                true,
				"session":           map[string]any{"id": "session-v3", "workspace_path": body["workspace_path"], "workspace_name": body["workspace_name"], "title": body["title"], "mode": body["mode"]},
				"projection":        map[string]any{"session_id": "session-v3", "last_event_seq": 1, "projection_high_watermark_seq": 1},
				"messages":          []any{},
				"events":            []any{},
				"active_run_intent": map[string]any{"session_id": "session-v3", "run_id": "run-create", "status": "running", "created_at": 1000, "started_at": 1001, "cumulative_duration_ms": 60000, "updated_at": 1001, "event_seq": 1},
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
			if body["type"] != "run.stop" || body["run_id"] != "run-1" || body["target_swarm_id"] != "host-swarm" {
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
	created, err := api.CreateSessionV3WithOptions(context.Background(), SessionCreateOptions{Title: "V3", WorkspacePath: "/workspace", WorkspaceName: "workspace", WorkspaceBindingID: "binding-primary", SwarmID: "host-swarm", TargetKind: "host", TargetRelationship: "self", Mode: "auto", AgentName: "swarm", ModelProfile: &SessionV3ModelProfileChoice{SavedProfileID: "profile-1"}, WorktreeMode: "on", WorktreeBaseBranch: "dev", WorktreeBranchName: "agent/client-v3"})
	if err != nil {
		t.Fatalf("CreateSessionV3WithOptions() error = %v", err)
	}
	if created.Session.SessionAPI != "v3" || created.Session.ProjectionHighWatermarkSeq != 1 {
		t.Fatalf("created session = %#v", created.Session)
	}
	if created.ActiveRunIntent == nil || created.ActiveRunIntent.RunID != "run-create" || created.ActiveRunIntent.CreatedAt != 1000 || created.ActiveRunIntent.StartedAt != 1001 || created.ActiveRunIntent.CumulativeDurationMS != 60000 {
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
	if err := api.StopSessionV3Run(context.Background(), "session-v3", "run-1", "host-swarm", ""); err != nil {
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

func TestSessionV3TUICreateCarriesSelectedModelProfile(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodPost || r.URL.Path != "/v3/tui/sessions" {
			t.Fatalf("unexpected request: %s %s", r.Method, r.URL.Path)
		}
		var body map[string]any
		if err := json.NewDecoder(r.Body).Decode(&body); err != nil {
			t.Fatal(err)
		}
		if body["mode"] != "plan" {
			t.Fatalf("tui create mode = %#v, want plan", body["mode"])
		}
		profile, _ := body["model_profile"].(map[string]any)
		if profile["saved_profile_id"] != "profile-1" {
			t.Fatalf("tui create model_profile = %#v", body["model_profile"])
		}
		_ = json.NewEncoder(w).Encode(map[string]any{
			"session":    map[string]any{"id": "session-v3", "title": "V3", "mode": "auto"},
			"projection": map[string]any{"session_id": "session-v3"},
		})
	}))
	defer server.Close()

	api := New(server.URL)
	api.SetToken("test-token")
	_, err := api.CreateSessionV3TUIWithOptions(context.Background(), SessionCreateOptions{CWDPath: "/workspace", Mode: "plan", AgentName: "swarm", ModelProfile: &SessionV3ModelProfileChoice{SavedProfileID: "profile-1"}})
	if err != nil {
		t.Fatal(err)
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

func TestSessionV3TUIWorksetClientUsesTUIRouteAndScope(t *testing.T) {
	var gotPath string
	var gotRequest SessionV3TUIWorksetRequest
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		gotPath = r.Method + " " + r.URL.Path
		if r.Method != http.MethodPost || r.URL.Path != "/v3/tui/sessions:workset" {
			t.Fatalf("unexpected request: %s %s", r.Method, r.URL.Path)
		}
		if err := json.NewDecoder(r.Body).Decode(&gotRequest); err != nil {
			t.Fatalf("decode tui workset request: %v", err)
		}
		_ = json.NewEncoder(w).Encode(map[string]any{
			"ok":                       true,
			"snapshot_endpoint_cursor": "endpoint-cursor-1",
			"sessions_by_id": map[string]any{
				"session-a": map[string]any{"id": "session-a", "workspace_path": "/workspace-a", "title": "A", "mode": "plan", "updated_at": 2000},
			},
			"projections_by_session": map[string]any{
				"session-a": map[string]any{"session_id": "session-a", "last_event_seq": 9, "projection_high_watermark_seq": 10, "updated_at": 2000},
			},
			"messages_by_session":          map[string]any{},
			"events_by_session":            map[string]any{},
			"plans_by_session":             map[string]any{},
			"plan_revisions_by_session":    map[string]any{},
			"permissions_by_session":       map[string]any{},
			"usage_by_session":             map[string]any{},
			"preferences_by_session":       map[string]any{},
			"run_intents_by_session":       map[string]any{},
			"history_manifests_by_session": map[string]any{},
			"history_chunks_by_id":         map[string]any{},
			"pagination":                   map[string]any{},
			"watermarks":                   map[string]any{"loaded_at": 3000},
			"session_order":                []string{"session-a"},
		})
	}))
	defer server.Close()

	api := New(server.URL)
	api.SetToken("test-token")
	beforeUpdatedAt := int64(5000)
	workset, err := api.GetSessionV3TUIWorkset(context.Background(), SessionV3TUIWorksetRequest{
		SessionIDs: []string{"session-a"},
		Scope: SessionV3TUIWorksetScope{
			WorkspacePath:  "/workspace-a",
			WorkspacePaths: []string{"/workspace-b"},
			CWDPath:        "/cwd-only",
		},
		Recent: SessionV3WorksetRecent{Limit: 25, BeforeUpdatedAt: &beforeUpdatedAt, BeforeSessionID: "session-z"},
		History: SessionV3WorksetHistory{
			Mode:                  "tail",
			MaxMessagesPerSession: 5,
			MaxEventsPerSession:   6,
			ManifestPolicy:        "manifest",
			IncludeEvents:         true,
		},
	})
	if err != nil {
		t.Fatalf("GetSessionV3TUIWorkset() error = %v", err)
	}
	if gotPath != "POST /v3/tui/sessions:workset" {
		t.Fatalf("request path = %q", gotPath)
	}
	if gotRequest.Scope.WorkspacePath != "/workspace-a" || len(gotRequest.Scope.WorkspacePaths) != 1 || gotRequest.Scope.WorkspacePaths[0] != "/workspace-b" || gotRequest.Scope.CWDPath != "/cwd-only" {
		t.Fatalf("scope not encoded: %#v", gotRequest.Scope)
	}
	if gotRequest.Recent.BeforeUpdatedAt == nil || *gotRequest.Recent.BeforeUpdatedAt != beforeUpdatedAt || gotRequest.Recent.BeforeSessionID != "session-z" || gotRequest.Recent.Limit != 25 {
		t.Fatalf("recent not encoded: %#v", gotRequest.Recent)
	}
	if gotRequest.History.Mode != "tail" || !gotRequest.History.IncludeEvents || gotRequest.History.MaxMessagesPerSession != 5 || gotRequest.History.MaxEventsPerSession != 6 {
		t.Fatalf("history not encoded: %#v", gotRequest.History)
	}
	if workset.SessionOrder[0] != "session-a" || workset.SessionsByID["session-a"].SessionAPI != "v3" || workset.SessionsByID["session-a"].LastEventSeq != 9 || workset.SessionsByID["session-a"].ProjectionHighWatermarkSeq != 10 {
		t.Fatalf("session not marked from projection: %#v", workset.SessionsByID["session-a"])
	}
	if workset.SnapshotEndpointCursor != "endpoint-cursor-1" {
		t.Fatalf("SnapshotEndpointCursor = %q, want endpoint-cursor-1", workset.SnapshotEndpointCursor)
	}
}

func TestStreamV3RealtimeStartAtCurrentUsesHelloCursorForNewSessionSubscription(t *testing.T) {
	var gotPath string
	var gotResume, gotSubscribe V3RealtimeFrame
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		gotPath = r.URL.RequestURI()
		conn, rw, err := hijackLifecycleTestWebsocket(w, r)
		if err != nil {
			t.Fatalf("hijack websocket: %v", err)
		}
		defer conn.Close()
		writeServerLifecycleTestFrame(t, conn, map[string]any{"protocol": "v3.realtime", "protocol_version": 1, "kind": "hello", "endpoint_cursor": "signed-current"})
		_, payload, err := readClientLifecycleTestFrame(rw)
		if err != nil {
			t.Fatalf("read current-head resume: %v", err)
		}
		if err := json.Unmarshal(payload, &gotResume); err != nil {
			t.Fatalf("decode current-head resume: %v", err)
		}
		_, payload, err = readClientLifecycleTestFrame(rw)
		if err != nil {
			t.Fatalf("read subscribe: %v", err)
		}
		if err := json.Unmarshal(payload, &gotSubscribe); err != nil {
			t.Fatalf("decode subscribe: %v", err)
		}
		writeServerLifecycleTestFrame(t, conn, map[string]any{"protocol": "v3.realtime", "protocol_version": 1, "kind": "replay.complete", "session_id": "session-new", "endpoint_cursor": "signed-current", "last_seq": 0})
	}))
	defer server.Close()

	api := New(server.URL)
	api.SetToken("test-token")
	ctx, cancel := context.WithCancel(context.Background())
	ready := false
	var frames []V3RealtimeFrame
	err := api.StreamV3Realtime(ctx, V3RealtimeResumeOptions{
		Surface:        "tui",
		StartAtCurrent: true,
		Subscriptions:  []V3RealtimeSubscription{{SessionID: "session-new", SubscriptionID: "sub-new"}},
		Capabilities:   []string{" ", V3RealtimeCapabilityLivePatchV1, V3RealtimeCapabilityLivePatchV1},
		OnResumeSent:   func() { ready = true },
	}, func(frame V3RealtimeFrame) {
		frames = append(frames, frame)
		if frame.Kind == "replay.complete" {
			cancel()
		}
	})
	if err != nil {
		t.Fatalf("StreamV3Realtime() error = %v", err)
	}
	if gotPath != "/v3/realtime/stream?surface=tui" {
		t.Fatalf("stream path = %q", gotPath)
	}
	if !ready || len(frames) != 2 || frames[0].Kind != "hello" || frames[0].EndpointCursor != "signed-current" {
		t.Fatalf("ready=%v frames=%#v", ready, frames)
	}
	if gotResume.Kind != "resume" || gotResume.EndpointCursor != "signed-current" || !reflect.DeepEqual(gotResume.Capabilities, []string{V3RealtimeCapabilityLivePatchV1}) || len(gotResume.Subscriptions) != 0 {
		t.Fatalf("current-head resume = %#v", gotResume)
	}
	if gotSubscribe.Kind != "subscribe.session" || gotSubscribe.SessionID != "session-new" || gotSubscribe.SubscriptionID != "sub-new" || gotSubscribe.EndpointCursor != "signed-current" {
		t.Fatalf("subscribe = %#v", gotSubscribe)
	}
}

func TestStreamV3RealtimeResumeIncludesRequestedCapabilities(t *testing.T) {
	var gotResume V3RealtimeFrame
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		conn, rw, err := hijackLifecycleTestWebsocket(w, r)
		if err != nil {
			t.Fatalf("hijack websocket: %v", err)
		}
		defer conn.Close()
		_, payload, err := readClientLifecycleTestFrame(rw)
		if err != nil {
			t.Fatalf("read resume: %v", err)
		}
		if err := json.Unmarshal(payload, &gotResume); err != nil {
			t.Fatalf("decode resume: %v", err)
		}
		writeServerLifecycleTestFrame(t, conn, map[string]any{"protocol": "v3.realtime", "protocol_version": 1, "kind": "replay.complete", "session_id": "session-a", "endpoint_cursor": "cursor-1", "last_seq": 0})
	}))
	defer server.Close()

	api := New(server.URL)
	api.SetToken("test-token")
	ctx, cancel := context.WithCancel(context.Background())
	err := api.StreamV3Realtime(ctx, V3RealtimeResumeOptions{
		EndpointCursor: "cursor-1",
		Subscriptions:  []V3RealtimeSubscription{{SessionID: "session-a", SubscriptionID: "sub-a"}},
		Capabilities:   []string{" ", V3RealtimeCapabilityLivePatchV1, V3RealtimeCapabilityLivePatchV1},
	}, func(frame V3RealtimeFrame) { cancel() })
	if err != nil {
		t.Fatalf("StreamV3Realtime() error = %v", err)
	}
	if gotResume.Kind != "resume" || gotResume.EndpointCursor != "cursor-1" || !reflect.DeepEqual(gotResume.Capabilities, []string{V3RealtimeCapabilityLivePatchV1}) {
		t.Fatalf("resume = %#v", gotResume)
	}
}

func TestNormalizeV3RealtimeResumeStillRequiresCursorOutsideNewSessionStart(t *testing.T) {
	_, _, err := normalizeV3RealtimeResumeOptions(V3RealtimeResumeOptions{Subscriptions: []V3RealtimeSubscription{{SessionID: "session-a", SubscriptionID: "sub-a"}}})
	if err == nil || err.Error() != "v3 realtime endpoint cursor is required" {
		t.Fatalf("normalize error = %v", err)
	}
}

func TestStreamV3RealtimeDeliversProviderToolStartPatch(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		conn, rw, err := hijackLifecycleTestWebsocket(w, r)
		if err != nil {
			t.Fatalf("hijack websocket: %v", err)
		}
		defer conn.Close()
		if _, _, err := readClientLifecycleTestFrame(rw); err != nil {
			t.Fatalf("read resume: %v", err)
		}
		text := `{"type":"session.provider_tool_call.started","run_id":"run-1","step":1,"step_id":"step-1","event_index":1,"output_index":0,"call_id":"call-edit","tool_name":"edit","status":"started","recorded_at":100}`
		writeServerLifecycleTestFrame(t, conn, map[string]any{"protocol": "v3.realtime", "protocol_version": 1, "kind": "live.patch", "session_id": "session-a", "live": map[string]any{"session_id": "session-a", "run_id": "run-1", "stream_id": "provider-tool:run-1:step:1:event:1", "stream_kind": "provider_tool_call", "operation": "append", "step": 1, "step_id": "step-1", "live_seq_start": 1, "live_seq_end": 1, "offset_start": 0, "offset_end": len([]byte(text)), "text": text, "recorded_at": 100}})
	}))
	defer server.Close()

	api := New(server.URL)
	api.SetToken("test-token")
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	var frames []V3RealtimeFrame
	err := api.StreamV3Realtime(ctx, V3RealtimeResumeOptions{
		EndpointCursor: "cursor-1",
		Subscriptions:  []V3RealtimeSubscription{{SessionID: "session-a", EndpointCursor: "cursor-1", SubscriptionID: "sub-a"}},
		Capabilities:   []string{V3RealtimeCapabilityLivePatchV1},
	}, func(frame V3RealtimeFrame) {
		frames = append(frames, frame)
		cancel()
	})
	if err != nil {
		t.Fatalf("StreamV3Realtime() error = %v", err)
	}
	if len(frames) != 1 || frames[0].Live == nil || frames[0].Live.StreamKind != "provider_tool_call" || !strings.Contains(frames[0].Live.Text, "session.provider_tool_call.started") {
		t.Fatalf("frames = %#v", frames)
	}
}

func TestStreamSessionsV3RealtimeDeliversLiveAssistantPatchWithoutAdvancingDurableOrder(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		conn, rw, err := hijackLifecycleTestWebsocket(w, r)
		if err != nil {
			t.Fatalf("hijack websocket: %v", err)
		}
		defer conn.Close()
		if _, _, err := readClientLifecycleTestFrame(rw); err != nil {
			t.Fatalf("read resume: %v", err)
		}
		writeServerLifecycleTestFrame(t, conn, map[string]any{"protocol": "v3.realtime", "protocol_version": 1, "kind": "live.patch", "session_id": "session-a", "live": map[string]any{"session_id": "session-a", "run_id": "run-1", "stream_id": "assistant:run-1:step:1", "stream_kind": "assistant_text", "operation": "append", "step": 1, "step_id": "step-1", "live_seq_start": 1, "live_seq_end": 1, "offset_start": 0, "offset_end": 5, "text": "hello", "recorded_at": 100}})
		writeServerLifecycleTestFrame(t, conn, map[string]any{"protocol": "v3.realtime", "protocol_version": 1, "kind": "event", "session_id": "session-a", "event_type": "session.title.updated", "last_seq": 2, "rev": 2, "prevRev": 1, "event": map[string]any{"id": "evt-2", "session_id": "session-a", "seq": 2, "event_type": "session.title.updated", "payload": map[string]any{"title": "after live"}}})
	}))
	defer server.Close()

	api := New(server.URL)
	api.SetToken("test-token")
	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
	defer cancel()
	var frames []V3RealtimeFrame
	err := api.StreamSessionsV3Realtime(ctx, []V3RealtimeSubscription{{SessionID: "session-a", EndpointCursor: "cursor-1", LastSeq: 1, SubscriptionID: "sub-a"}}, func(frame V3RealtimeFrame) {
		frames = append(frames, frame)
		if len(frames) == 2 {
			cancel()
		}
	})
	if err != nil {
		t.Fatalf("StreamSessionsV3Realtime() error = %v", err)
	}
	if len(frames) != 2 || frames[0].Kind != "live.patch" || frames[0].Live == nil || frames[0].Live.Text != "hello" || frames[1].Event == nil || frames[1].Event.Seq != 2 {
		t.Fatalf("frames = %#v", frames)
	}
}

func TestStreamSessionsV3RealtimeAcceptsSparseFilteredSessionSequences(t *testing.T) {
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
		for i := 0; i < 1; i++ {
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
		writeServerLifecycleTestFrame(t, conn, map[string]any{"protocol": "v3.realtime", "protocol_version": 1, "kind": "replay.started", "session_id": "session-a", "endpoint_cursor": "cursor-10", "last_seq": 1})
		writeServerLifecycleTestFrame(t, conn, map[string]any{"protocol": "v3.realtime", "protocol_version": 1, "kind": "replay.complete", "session_id": "session-a", "last_seq": 1})
		writeServerLifecycleTestFrame(t, conn, map[string]any{"protocol": "v3.realtime", "protocol_version": 1, "kind": "replay.started", "session_id": "session-b", "endpoint_cursor": "cursor-10", "last_seq": 1})
		writeServerLifecycleTestFrame(t, conn, map[string]any{"protocol": "v3.realtime", "protocol_version": 1, "kind": "replay.complete", "session_id": "session-b", "last_seq": 1})
		writeServerLifecycleTestFrame(t, conn, map[string]any{"protocol": "v3.realtime", "protocol_version": 1, "kind": "event", "session_id": "session-a", "event_type": "session.assistant.delta", "last_seq": 3, "high_watermark_seq": 3, "rev": 2, "prevRev": 1, "event": map[string]any{"id": "evt-a-3", "session_id": "session-a", "seq": 3, "event_type": "session.assistant.delta", "payload": map[string]any{"session_id": "session-a", "delta": "gap"}}})
		writeServerLifecycleTestFrame(t, conn, map[string]any{"protocol": "v3.realtime", "protocol_version": 1, "kind": "event", "session_id": "session-b", "event_type": "session.assistant.delta", "last_seq": 2, "rev": 3, "prevRev": 2, "event": map[string]any{"id": "evt-b-2", "session_id": "session-b", "seq": 2, "event_type": "session.assistant.delta", "payload": map[string]any{"session_id": "session-b", "delta": "ok"}}})
	}))
	defer server.Close()

	api := New(server.URL)
	api.SetToken("test-token")
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	var frames []V3RealtimeFrame
	err := api.StreamSessionsV3Realtime(ctx, []V3RealtimeSubscription{{SessionID: "session-a", EndpointCursor: "cursor-10", LastSeq: 1, SubscriptionID: "sub-a"}, {SessionID: "session-b", EndpointCursor: "cursor-10", LastSeq: 1, SubscriptionID: "sub-b"}}, func(frame V3RealtimeFrame) {
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
	if len(subscribes) != 1 || subscribes[0]["kind"] != "resume" || subscribes[0]["endpoint_cursor"] != "cursor-10" {
		t.Fatalf("subscribes = %#v", subscribes)
	}
	rawSubs, ok := subscribes[0]["subscriptions"].([]any)
	if !ok || len(rawSubs) != 2 {
		t.Fatalf("resume subscriptions = %#v", subscribes[0]["subscriptions"])
	}
	firstSub, _ := rawSubs[0].(map[string]any)
	secondSub, _ := rawSubs[1].(map[string]any)
	if firstSub["session_id"] != "session-a" || firstSub["endpoint_cursor"] != "cursor-10" || secondSub["session_id"] != "session-b" || secondSub["endpoint_cursor"] != "cursor-10" {
		t.Fatalf("resume subscriptions = %#v", rawSubs)
	}
	if _, ok := subscribes[0]["after_seq"]; ok {
		t.Fatalf("canonical realtime resume must not use after_seq: %#v", subscribes[0])
	}
	var sawSparseA, sawB bool
	for _, frame := range frames {
		if frame.Kind == "cursor.error" && frame.SessionID == "session-a" {
			t.Fatalf("sparse filtered sequence produced a false cursor gap: %#v", frame)
		}
		if frame.Kind == "event" && frame.SessionID == "session-a" && frame.Event != nil && frame.Event.Seq == 3 {
			sawSparseA = true
		}
		if frame.Kind == "event" && frame.SessionID == "session-b" && frame.Event != nil && frame.Event.Seq == 2 {
			sawB = true
		}
	}
	if !sawSparseA || !sawB {
		t.Fatalf("frames did not preserve sparse per-session order; sawSparseA=%v sawB=%v frames=%#v", sawSparseA, sawB, frames)
	}
}

func TestStreamSessionsV3RealtimeDeliversEndpointWatermarkWithoutChangingSessionOrder(t *testing.T) {
	var gotPath, gotQuery string
	var subscribe map[string]any
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		gotPath = r.URL.Path
		gotQuery = r.URL.RawQuery
		if r.Method != http.MethodGet || r.URL.Path != "/v3/realtime/stream" {
			t.Fatalf("websocket path = %s %s, want GET /v3/realtime/stream", r.Method, r.URL.Path)
		}
		conn, rw, err := hijackLifecycleTestWebsocket(w, r)
		if err != nil {
			t.Fatalf("hijack websocket: %v", err)
		}
		defer conn.Close()
		_, payload, err := readClientLifecycleTestFrame(rw)
		if err != nil {
			t.Fatalf("read resume: %v", err)
		}
		if err := json.Unmarshal(payload, &subscribe); err != nil {
			t.Fatalf("decode resume: %v", err)
		}
		writeServerLifecycleTestFrame(t, conn, map[string]any{"protocol": "v3.realtime", "protocol_version": 1, "kind": "replay.started", "session_id": "session-a", "endpoint_cursor": "cursor-1", "last_seq": 12})
		writeServerLifecycleTestFrame(t, conn, map[string]any{"protocol": "v3.realtime", "protocol_version": 1, "kind": "replay.complete", "session_id": "session-a", "endpoint_cursor": "cursor-1", "last_seq": 12})
		writeServerLifecycleTestFrame(t, conn, map[string]any{"protocol": "v3.realtime", "protocol_version": 1, "kind": "endpoint.watermark", "endpoint_cursor": "cursor-2", "high_watermark_seq": 99, "rev": 99, "prevRev": 98})
	}))
	defer server.Close()

	api := New(server.URL)
	api.SetToken("test-token")
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	var frames []V3RealtimeFrame
	err := api.StreamSessionsV3Realtime(ctx, []V3RealtimeSubscription{{SessionID: "session-a", EndpointCursor: "cursor-1", LastSeq: 12, SubscriptionID: "sub-a"}}, func(frame V3RealtimeFrame) {
		frames = append(frames, frame)
		if frame.Kind == v3RealtimeKindEndpointWatermark {
			cancel()
		}
	})
	if err != nil {
		t.Fatalf("StreamSessionsV3Realtime() error = %v", err)
	}
	if gotPath != "/v3/realtime/stream" || gotQuery != "endpoint_cursor=cursor-1" {
		t.Fatalf("realtime request path/query = %q?%q", gotPath, gotQuery)
	}
	if subscribe["kind"] != "resume" || subscribe["endpoint_cursor"] != "cursor-1" {
		t.Fatalf("resume = %#v", subscribe)
	}
	rawSubs, ok := subscribe["subscriptions"].([]any)
	if !ok || len(rawSubs) != 1 {
		t.Fatalf("resume subscriptions = %#v", subscribe["subscriptions"])
	}
	firstSub, _ := rawSubs[0].(map[string]any)
	if firstSub["session_id"] != "session-a" || firstSub["endpoint_cursor"] != "cursor-1" {
		t.Fatalf("resume subscription = %#v", firstSub)
	}
	if len(frames) != 3 || frames[2].Kind != v3RealtimeKindEndpointWatermark || frames[2].EndpointCursor != "cursor-2" || frames[2].LastSeq != 0 || frames[2].SessionID != "" {
		t.Fatalf("frames = %#v, want endpoint watermark delivered without session last_seq", frames)
	}
}

func TestSendSessionV3MessageIncludesExplicitOperationIDs(t *testing.T) {
	var body map[string]any
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodPost || r.URL.Path != "/v3/sessions/session-v3/messages" {
			t.Fatalf("unexpected request: %s %s", r.Method, r.URL.Path)
		}
		if err := json.NewDecoder(r.Body).Decode(&body); err != nil {
			t.Fatalf("decode message request: %v", err)
		}
		_ = json.NewEncoder(w).Encode(map[string]any{
			"ok":         true,
			"session":    map[string]any{"id": "session-v3", "workspace_path": "/workspace", "workspace_name": "workspace", "title": "V3", "mode": "auto"},
			"projection": map[string]any{"session_id": "session-v3", "last_event_seq": 3, "projection_high_watermark_seq": 3},
			"message":    map[string]any{"id": "msg-explicit", "session_id": "session-v3", "global_seq": 3, "role": "user", "content": "hi"},
			"run_intent": map[string]any{"session_id": "session-v3", "run_id": "run-explicit", "status": "pending_executor", "event_seq": 3},
		})
	}))
	defer server.Close()

	api := New(server.URL)
	api.SetToken("test-token")
	_, err := api.SendSessionV3Message(context.Background(), "session-v3", SessionV3MessageOptions{
		ClientRequestID: "client-request-explicit",
		MessageID:       "message-explicit",
		RunID:           "run-explicit",
		Content:         "hi",
	})
	if err != nil {
		t.Fatalf("SendSessionV3Message() error = %v", err)
	}
	if body["client_request_id"] != "client-request-explicit" || body["message_id"] != "message-explicit" || body["run_id"] != "run-explicit" {
		t.Fatalf("explicit operation IDs not serialized: %#v", body)
	}
}

func TestStreamV3RealtimeOnResumeSentFiresAfterResumeWrite(t *testing.T) {
	resumeRead := make(chan struct{})
	resumeCallback := make(chan struct{})
	releaseServer := make(chan struct{})
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodGet || r.URL.Path != "/v3/realtime/stream" {
			t.Fatalf("websocket path = %s %s, want GET /v3/realtime/stream", r.Method, r.URL.Path)
		}
		conn, rw, err := hijackLifecycleTestWebsocket(w, r)
		if err != nil {
			t.Fatalf("hijack websocket: %v", err)
		}
		defer conn.Close()
		_, payload, err := readClientLifecycleTestFrame(rw)
		if err != nil {
			t.Fatalf("read resume frame: %v", err)
		}
		var resume map[string]any
		if err := json.Unmarshal(payload, &resume); err != nil {
			t.Fatalf("decode resume: %v", err)
		}
		if resume["kind"] != "resume" || resume["endpoint_cursor"] != "cursor-workset" {
			t.Fatalf("resume frame = %#v", resume)
		}
		close(resumeRead)
		<-releaseServer
	}))
	defer server.Close()

	api := New(server.URL)
	api.SetToken("test-token")
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	done := make(chan error, 1)
	go func() {
		done <- api.StreamV3Realtime(ctx, V3RealtimeResumeOptions{
			EndpointCursor: "cursor-workset",
			Surface:        "tui",
			Worksets: []V3RealtimeWorksetSubscription{{
				WorksetID:             "tui:workspace:/repo",
				SubscriptionID:        "tui:test:workset:workspace:/repo",
				Surface:               "tui",
				Selector:              V3RealtimeWorksetSelector{Kind: "workspace", WorkspacePath: "/repo"},
				AutoSubscribeSessions: true,
			}},
			OnResumeSent: func() { close(resumeCallback) },
		}, func(frame V3RealtimeFrame) {})
	}()

	select {
	case <-resumeCallback:
	case <-time.After(time.Second):
		t.Fatal("OnResumeSent did not fire after writing the resume frame")
	}
	select {
	case <-resumeRead:
	case <-time.After(time.Second):
		t.Fatal("server did not receive resume frame before OnResumeSent fired")
	}
	cancel()
	close(releaseServer)
	select {
	case err := <-done:
		if err != nil {
			t.Fatalf("StreamV3Realtime() error = %v", err)
		}
	case <-time.After(time.Second):
		t.Fatal("StreamV3Realtime did not return after cancel")
	}
}

func TestStreamSessionsV3RealtimeResumeFrameIncludesWorksetSelector(t *testing.T) {
	var gotPath, gotQuery string
	var resume map[string]any
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		gotPath = r.URL.Path
		gotQuery = r.URL.RawQuery
		if r.Method != http.MethodGet || r.URL.Path != "/v3/realtime/stream" {
			t.Fatalf("websocket path = %s %s, want GET /v3/realtime/stream", r.Method, r.URL.Path)
		}
		conn, rw, err := hijackLifecycleTestWebsocket(w, r)
		if err != nil {
			t.Fatalf("hijack websocket: %v", err)
		}
		defer conn.Close()
		_, payload, err := readClientLifecycleTestFrame(rw)
		if err != nil {
			t.Fatalf("read resume: %v", err)
		}
		if err := json.Unmarshal(payload, &resume); err != nil {
			t.Fatalf("decode resume: %v", err)
		}
		writeServerLifecycleTestFrame(t, conn, map[string]any{"protocol": "v3.realtime", "protocol_version": 1, "kind": "endpoint.watermark", "endpoint_cursor": "cursor-next"})
	}))
	defer server.Close()

	api := New(server.URL)
	api.SetToken("test-token")
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	err := api.StreamV3Realtime(ctx, V3RealtimeResumeOptions{
		EndpointCursor: "cursor-workset",
		Surface:        "tui",
		Worksets: []V3RealtimeWorksetSubscription{{
			WorksetID:             "tui:cwd:/repo/subdir",
			SubscriptionID:        "tui:test:workset:cwd:/repo/subdir",
			Surface:               "tui",
			Selector:              V3RealtimeWorksetSelector{Kind: "workspace", WorkspacePath: "/repo/subdir"},
			Resources:             []string{"membership", "sessions"},
			AutoSubscribeSessions: true,
		}},
	}, func(frame V3RealtimeFrame) {
		if frame.Kind == v3RealtimeKindEndpointWatermark {
			cancel()
		}
	})
	if err != nil {
		t.Fatalf("StreamV3Realtime() error = %v", err)
	}
	if gotPath != "/v3/realtime/stream" || gotQuery != "endpoint_cursor=cursor-workset&surface=tui" {
		t.Fatalf("realtime request path/query = %q?%q", gotPath, gotQuery)
	}
	if resume["protocol"] != "v3.realtime" || resume["protocol_version"] != float64(1) || resume["kind"] != "resume" || resume["endpoint_cursor"] != "cursor-workset" {
		t.Fatalf("resume frame header = %#v", resume)
	}
	if _, hasAfterSeq := resume["after_seq"]; hasAfterSeq {
		t.Fatalf("canonical realtime resume must not use after_seq: %#v", resume)
	}
	rawWorksets, ok := resume["worksets"].([]any)
	if !ok || len(rawWorksets) != 1 {
		t.Fatalf("resume worksets = %#v", resume["worksets"])
	}
	workset, ok := rawWorksets[0].(map[string]any)
	if !ok || workset["workset_id"] != "tui:cwd:/repo/subdir" || workset["subscription_id"] != "tui:test:workset:cwd:/repo/subdir" || workset["surface"] != "tui" || workset["auto_subscribe_sessions"] != true {
		t.Fatalf("workset resume entry = %#v", rawWorksets[0])
	}
	selector, ok := workset["selector"].(map[string]any)
	if !ok || selector["kind"] != "workspace" || selector["workspace_path"] != "/repo/subdir" {
		t.Fatalf("workset selector = %#v", workset["selector"])
	}
	if cursor := resume["endpoint_cursor"].(string); cursor == "" {
		t.Fatalf("resume endpoint cursor is empty: %#v", resume)
	}
}

func TestNormalizeV3RealtimeResumeOptionsPreservesOnResumeSent(t *testing.T) {
	called := false
	callback := func() { called = true }
	normalized, cursor, err := normalizeV3RealtimeResumeOptions(V3RealtimeResumeOptions{
		EndpointCursor: " cursor-1 ",
		Surface:        " tui ",
		Subscriptions: []V3RealtimeSubscription{{
			SessionID:      " session-1 ",
			EndpointCursor: " cursor-1 ",
			SubscriptionID: " sub-1 ",
		}},
		OnResumeSent: callback,
	})
	if err != nil {
		t.Fatalf("normalizeV3RealtimeResumeOptions() error = %v", err)
	}
	if cursor != "cursor-1" || normalized.EndpointCursor != "cursor-1" || normalized.Surface != "tui" {
		t.Fatalf("normalized cursor/surface = %q/%q/%q", cursor, normalized.EndpointCursor, normalized.Surface)
	}
	if normalized.OnResumeSent == nil {
		t.Fatalf("OnResumeSent was not preserved")
	}
	normalized.OnResumeSent()
	if !called {
		t.Fatalf("normalized OnResumeSent callback did not invoke original callback")
	}
}
