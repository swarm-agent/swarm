package app

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"os"
	"strings"
	"testing"
	"time"

	"github.com/gdamore/tcell/v2"

	"swarm-refactor/swarmtui/internal/client"
	"swarm-refactor/swarmtui/internal/model"
	"swarm-refactor/swarmtui/internal/ui"
)

func TestTUIRejectsRetiredSessionAPIsOnOpenAndBackend(t *testing.T) {
	app := &App{home: ui.NewHomePage(model.HomeModel{})}
	err := app.openSessionSummary(model.SessionSummary{ID: "legacy-session", Title: "Legacy", SessionAPI: "v2"}, "")
	if err == nil || !strings.Contains(err.Error(), tuiRetiredSessionAPIMessage) {
		t.Fatalf("openSessionSummary() error = %v, want retired TUI session API error", err)
	}

	backend := newAPIChatBackend(testAPIWithToken("http://127.0.0.1"), "v2")
	if _, err := backend.LoadMessages(context.Background(), "legacy-session", 0, 10); err == nil || !strings.Contains(err.Error(), tuiRetiredSessionAPIMessage) {
		t.Fatalf("LoadMessages() error = %v, want retired TUI session API error", err)
	}
}

func TestTUIOpenSessionHydratesFromV3BeforeOpeningChat(t *testing.T) {
	t.Setenv("SWARMD_LOCAL_TRANSPORT_SOCKET", "")
	t.Setenv("DATA_DIR", "")

	var hydrated bool
	fake := newTUIRealtimeFakeStreamer()
	fake.handler = func(index int, ctx context.Context, options client.V3RealtimeResumeOptions, onFrame func(client.V3RealtimeFrame)) error {
		<-ctx.Done()
		return nil
	}
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch {
		case r.Method == http.MethodGet && r.URL.Path == "/ws":
			return
		case r.Method == http.MethodGet && r.URL.Path == "/v3/tui/sessions/session-1":
			hydrated = true
			if got := r.URL.Query().Get("workspace_path"); got != testWorkspacePath {
				t.Fatalf("workspace_path = %q, want %q", got, testWorkspacePath)
			}
			_ = json.NewEncoder(w).Encode(map[string]any{
				"ok": true,
				"session": map[string]any{
					"id":             "session-1",
					"workspace_path": testWorkspacePath,
					"workspace_name": "swarm-go",
					"title":          "Hydrated Session",
					"mode":           "auto",
					"metadata": map[string]any{
						"swarm_v3_runtime_swarm_id": "host-swarm",
					},
				},
				"projection":               map[string]any{"session_id": "session-1", "last_event_seq": 7, "projection_high_watermark_seq": 8},
				"snapshot_endpoint_cursor": "cursor-hydrated",
				"preference":               map[string]any{"provider": "anthropic", "model": "claude", "thinking": "auto", "service_tier": "standard", "context_mode": "full"},
				"context_window":           1000,
				"usage_summary": map[string]any{
					"session_id":        "session-1",
					"provider":          "anthropic",
					"model":             "claude",
					"source":            "provider",
					"context_window":    1000,
					"total_tokens":      250,
					"remaining_tokens":  750,
					"cache_read_tokens": 0,
				},
				"messages": []any{},
				"events":   []any{},
			})
		case r.Method == http.MethodGet && r.URL.Path == "/v1/providers":
			_ = json.NewEncoder(w).Encode(map[string]any{"ok": true, "providers": []any{}})
		case r.Method == http.MethodGet && r.URL.Path == "/v1/models/favorites":
			_ = json.NewEncoder(w).Encode(map[string]any{"ok": true, "favorites": []any{}})
		case r.Method == http.MethodGet && r.URL.Path == "/v1/model/catalog":
			_ = json.NewEncoder(w).Encode(map[string]any{"ok": true, "models": []any{}})
		case r.Method == http.MethodGet && r.URL.Path == "/v3/sessions/session-1/messages":
			_ = json.NewEncoder(w).Encode(map[string]any{"ok": true, "session_id": "session-1", "messages": []any{}})
		case r.Method == http.MethodGet && r.URL.Path == "/v3/sessions/session-1":
			_ = json.NewEncoder(w).Encode(map[string]any{
				"ok":         true,
				"session":    map[string]any{"id": "session-1", "workspace_path": testWorkspacePath, "workspace_name": "swarm-go", "title": "Hydrated Session", "mode": "auto"},
				"projection": map[string]any{"session_id": "session-1", "last_event_seq": 7, "projection_high_watermark_seq": 8},
			})
		case r.Method == http.MethodGet && r.URL.Path == "/v3/sessions/session-1/permissions":
			_ = json.NewEncoder(w).Encode(map[string]any{"ok": true, "session_id": "session-1", "permissions": []any{}})
		default:
			t.Fatalf("unexpected request: %s %s", r.Method, r.URL.String())
		}
	}))
	defer server.Close()

	homeModel := model.HomeModel{RecentSessions: []model.SessionSummary{{ID: "session-1", Title: "stale", WorkspacePath: testWorkspacePath}}}
	home := ui.NewHomePage(homeModel)
	app := &App{
		api:                 testAPIWithToken(server.URL),
		startupCWD:          testWorkspacePath,
		workspacePath:       testWorkspacePath,
		home:                home,
		homeModel:           homeModel,
		streamEvents:        make(chan client.StreamEventEnvelope, 1),
		tuiRealtimeFrames:   make(chan client.V3RealtimeFrame, 256),
		tuiRealtimeStatuses: make(chan tuiRealtimeStatus, 32),
		tuiRealtimeClientID: "tui:test",
	}
	if err := app.openSessionSummary(model.SessionSummary{ID: "session-1", Title: "stale", WorkspacePath: testWorkspacePath}, ""); err != nil {
		t.Fatalf("openSessionSummary() error = %v", err)
	}
	if !hydrated {
		t.Fatalf("openSessionSummary() did not hydrate through v3 TUI endpoint")
	}
	if app.chat == nil || app.chat.SessionID() != "session-1" {
		t.Fatalf("chat session id = %q, want session-1", app.chat.SessionID())
	}
	if got := app.chat.SessionMode(); got != "auto" {
		t.Fatalf("chat session mode = %q, want auto", got)
	}
	if summary, ok := app.sessionSummaryByID("session-1"); !ok || summary.SessionAPI != "v3" || summary.Title != "Hydrated Session" || summary.LastEventSeq != 7 || summary.ProjectionHighWatermarkSeq != 8 {
		t.Fatalf("hydrated summary = %+v, ok=%v", summary, ok)
	}
	subs := app.tuiSessionStore.DesiredSubscriptions(app.tuiRealtimeClientID)
	if len(subs) != 1 || subs[0].SessionID != "session-1" || subs[0].EndpointCursor != "cursor-hydrated" || subs[0].LastSeq != 7 || subs[0].SubscriptionID != "tui:test:session:session-1" {
		t.Fatalf("realtime direct session subscriptions = %+v", subs)
	}
	fakeController := newTestTUIRealtimeController(fake, app.tuiRealtimeFrames, app.tuiRealtimeStatuses)
	app.tuiRealtime = fakeController
	if err := app.reconcileTUIRealtime(); err != nil {
		t.Fatalf("reconcileTUIRealtime() error = %v", err)
	}
	call := waitTUIRealtimeCall(t, fake)
	if call.EndpointCursor != "cursor-hydrated" || len(call.Subscriptions) != 1 || call.Subscriptions[0].SessionID != "session-1" || call.Subscriptions[0].LastSeq != 7 {
		t.Fatalf("direct realtime subscription call = %#v", call)
	}
	if len(call.Worksets) != 1 || call.Worksets[0].Selector.WorkspacePath != testWorkspacePath {
		t.Fatalf("workset realtime subscription call = %#v", call)
	}
	if app.tuiRealtime != nil {
		app.tuiRealtime.Stop()
	}
	text := renderPageText(t, app.chat)
	if !strings.Contains(text, "75% left") {
		t.Fatalf("hydrated chat footer missing context usage: %q", text)
	}
}

func TestTUIChatPageSendUsesNativeV3MessageMutationAndRealtimeStore(t *testing.T) {
	messageRequests := make(chan map[string]any, 1)
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch {
		case r.Method == http.MethodPost && r.URL.Path == "/v3/sessions/session-1/messages":
			var req map[string]any
			if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
				t.Fatalf("decode v3 message request: %v", err)
			}
			messageRequests <- req
			_ = json.NewEncoder(w).Encode(map[string]any{
				"ok": true,
				"session": map[string]any{
					"id":             "session-1",
					"workspace_path": testWorkspacePath,
					"workspace_name": "swarm-go",
					"title":          "Native V3",
					"mode":           "auto",
				},
				"projection": map[string]any{"session_id": "session-1", "last_event_seq": 1, "projection_high_watermark_seq": 1},
				"message":    map[string]any{"id": "msg-user", "session_id": "session-1", "global_seq": 1, "role": "user", "content": "hello v3", "created_at": time.Now().UnixMilli()},
				"run_intent": map[string]any{"session_id": "session-1", "run_id": "run-1", "status": "pending_executor", "event_seq": 1, "created_at": time.Now().UnixMilli(), "updated_at": time.Now().UnixMilli()},
				"realtime_outbox": map[string]any{
					"endpoint_seq":    1,
					"endpoint_cursor": "cursor-1",
					"session_id":      "session-1",
				},
			})
		case strings.Contains(r.URL.Path, "/run") || strings.Contains(r.URL.Path, "/stream") || strings.Contains(r.URL.Path, "/v1/sessions") || strings.Contains(r.URL.Path, "/v2/sessions"):
			t.Fatalf("ChatPage send used retired run/stream route: %s %s", r.Method, r.URL.String())
		default:
			t.Fatalf("unexpected request during ChatPage send: %s %s", r.Method, r.URL.String())
		}
	}))
	defer server.Close()

	app := &App{
		api:                 testAPIWithToken(server.URL),
		tuiSessionStore:     newTUISessionStore(),
		tuiRealtimeClientID: "tui:test",
		homeModel:           model.HomeModel{RecentSessions: []model.SessionSummary{{ID: "session-1", Title: "Native V3", WorkspacePath: testWorkspacePath, SessionAPI: "v3"}}},
	}
	app.chat = ui.NewChatPage(ui.ChatPageOptions{
		SessionID:      "session-1",
		SessionTitle:   "Native V3",
		SessionMode:    "auto",
		AuthConfigured: true,
		Meta:           ui.ChatSessionMeta{Agent: "swarm", Workspace: "swarm-go", Path: testWorkspacePath},
		Send: func(ctx context.Context, sessionID string, req ui.ChatSendRequest) error {
			_, err := app.sendTUIV3ChatMessage(ctx, sessionID, req)
			return err
		},
	})

	app.chat.SetInput("hello v3")
	app.chat.HandleKey(tcell.NewEventKey(tcell.KeyEnter, 0, tcell.ModNone))

	select {
	case req := <-messageRequests:
		if req["content"] != "hello v3" || req["role"] != "user" {
			t.Fatalf("v3 message request = %#v", req)
		}
		if metadata, ok := req["metadata"].(map[string]any); ok {
			for _, key := range []string{"agent_name", "target_kind", "target_name"} {
				if _, exists := metadata[key]; exists {
					t.Fatalf("v3 message metadata contains reserved key %q: %#v", key, metadata)
				}
			}
		}
	case <-time.After(2 * time.Second):
		t.Fatal("ChatPage submit did not call native v3 message mutation")
	}
	deadline := time.Now().Add(2 * time.Second)
	for app.chat.Status() != "message sent" && time.Now().Before(deadline) {
		app.chat.HandleAsync()
		time.Sleep(time.Millisecond)
	}
	if app.chat.Status() != "message sent" {
		t.Fatalf("chat status after send = %q", app.chat.Status())
	}

	if snapshot, ok := app.tuiSessionStore.ChatSnapshot("session-1"); !ok || len(snapshot.Messages) != 1 || snapshot.Messages[0].ID != "msg-user" {
		t.Fatalf("store snapshot after send = %+v, ok=%v", snapshot, ok)
	}

	assistantPayload, err := json.Marshal(map[string]any{
		"message": map[string]any{"id": "msg-assistant", "session_id": "session-1", "global_seq": 2, "role": "assistant", "content": "assistant via realtime store", "created_at": time.Now().UnixMilli()},
	})
	if err != nil {
		t.Fatalf("marshal assistant realtime payload: %v", err)
	}
	if !app.applyTUIRealtimeFrame(client.V3RealtimeFrame{
		Kind:           "event",
		SessionID:      "session-1",
		LastSeq:        2,
		EndpointCursor: "cursor-2",
		Event:          &client.SessionV3Event{ID: "evt-assistant", SessionID: "session-1", Seq: 2, EventType: "message.stored", Payload: assistantPayload, TsUnixMS: time.Now().UnixMilli()},
		Projection:     &client.SessionV3Projection{SessionID: "session-1", LastEventSeq: 2, ProjectionHighWatermarkSeq: 2},
	}) {
		t.Fatal("assistant realtime frame did not update TUI session store")
	}
	text := renderPageText(t, app.chat)
	if !strings.Contains(text, "assistant via realtime store") {
		t.Fatalf("chat did not render assistant update from realtime store:\n%s", text)
	}
}

func TestTUIAppSessionContractDoesNotCallLegacySessionAPIs(t *testing.T) {
	assertSourceDoesNotContain(t, "app.go", map[string]string{
		"ListSessionsForWorkspaceBinding(": "TUI session lists must use the v3 TUI workset endpoint until durable sync parity is complete, not v2 workspace binding lists",
		"ListSessionsForExactCWD(":         "TUI session lists must use the v3 TUI workset endpoint until durable sync parity is complete, not v2 cwd lists",
		"CreateSessionWithOptions(":        "TUI session create must be v3-only",
		"/v1/sessions":                     "TUI app code must not call v1 session routes",
		"/v2/sessions":                     "TUI app code must not call v2 session routes",
	})
}

func TestTUIChatBackendContractDoesNotCallLegacySessionAPIs(t *testing.T) {
	assertSourceDoesNotContain(t, "chat_backend_adapter.go", map[string]string{
		"ListSessionMessages(":         "TUI chat message loads must use v3 messages/workset hydration",
		"RunSessionWithOptions(":       "TUI chat turns must use v3 message commit plus realtime",
		"RunSessionStreamWithOptions(": "TUI chat turns must use v3 realtime, not legacy run stream",
		"StopSessionRun(":              "TUI chat stop must use v3 primary stop directly",
		"sessionV2LifecyclePath(":      "TUI chat permissions/plans must use v3 primary routes, not v2 lifecycle routes",
		"RunTurnStream(":               "apiChatBackend must not implement V3 send/run through the old RunTurnStream contract",
		"RunTurn(":                     "apiChatBackend must not implement V3 send/run through the old RunTurn contract",
		"ChatRunResponse":              "apiChatBackend must not produce legacy ChatRunResponse for native V3 send/run",
		"ChatRunRequest":               "apiChatBackend must not accept legacy ChatRunRequest for native V3 send/run",
		"StreamSessionsV3Realtime(":    "apiChatBackend must not wrap V3 realtime as a legacy chat run stream",
		"StreamV3Realtime(":            "apiChatBackend must not wrap V3 realtime as a legacy chat run stream",
		"V3RealtimeFrame":              "apiChatBackend must not convert V3 realtime frames into legacy chat run responses",
		"SessionV3Event":               "apiChatBackend must not convert V3 session events into legacy chat run responses",
		"/v1/sessions":                 "TUI chat backend must not call v1 session routes",
		"/v2/sessions":                 "TUI chat backend must not call v2 session routes",
	})
}

func TestTUIChatPageContractDoesNotUseLegacyRunTurnForSend(t *testing.T) {
	assertSourceDoesNotContain(t, "../ui/chat_page.go", map[string]string{
		"RunTurnStream(":  "ChatPage send must call native V3 send function, not ChatBackend.RunTurnStream",
		"RunTurn(":        "ChatPage send must call native V3 send function, not ChatBackend.RunTurn",
		"ChatRunResponse": "ChatPage send must not depend on legacy ChatRunResponse",
		"ChatRunRequest":  "ChatPage send must not build legacy ChatRunRequest",
	})
}

func TestTUIV3EndpointNamesAreCanonical(t *testing.T) {
	const primary = "/v3/sessions"
	const workset = "/v3/sessions:workset"
	const tuiSessions = "/v3/tui/sessions"
	const tuiWorkset = "/v3/tui/sessions:workset"
	const syncBootstrap = "/v3/sync/bootstrap"
	const syncHydrate = "/v3/sync/hydrate"
	const syncStream = "/v3/sync/stream"
	for name, route := range map[string]string{"primary": primary, "workset": workset, "tuiSessions": tuiSessions, "tuiWorkset": tuiWorkset, "syncBootstrap": syncBootstrap, "syncHydrate": syncHydrate, "syncStream": syncStream} {
		if !strings.HasPrefix(route, "/v3/") {
			t.Fatalf("%s route = %q, want v3 route", name, route)
		}
	}
	if workset == tuiWorkset {
		t.Fatalf("TUI workset route must be distinct from main fail-closed workset route")
	}
	if !strings.HasPrefix(syncBootstrap, "/v3/sync/") || !strings.HasPrefix(syncHydrate, "/v3/sync/") || !strings.HasPrefix(syncStream, "/v3/sync/") {
		t.Fatalf("durable sync endpoints must stay under /v3/sync")
	}
}

func assertSourceDoesNotContain(t *testing.T, path string, forbidden map[string]string) {
	t.Helper()
	body, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("read %s: %v", path, err)
	}
	source := string(body)
	for needle, message := range forbidden {
		if strings.Contains(source, needle) {
			t.Fatalf("%s: found forbidden %q in %s", message, needle, path)
		}
	}
}
