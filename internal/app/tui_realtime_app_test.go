package app

import (
	"bufio"
	"context"
	"crypto/sha1"
	"encoding/base64"
	"encoding/json"
	"io"
	"net"
	"net/http"
	"net/http/httptest"
	"reflect"
	"strings"
	"testing"
	"time"

	"github.com/gdamore/tcell/v2"

	"swarm-refactor/swarmtui/internal/client"
	"swarm-refactor/swarmtui/internal/model"
	"swarm-refactor/swarmtui/internal/ui"
)

func TestTUIV3ToolEventsProjectInAuthoritativeOrderWithoutStoredDuplicate(t *testing.T) {
	toolInstanceID := "step-1:call-read"
	messages := []client.SessionMessage{
		{ID: "msg-user", SessionID: "session-1", GlobalSeq: 1, Role: "user", Content: "inspect it", CreatedAt: 100},
		{ID: "msg-assistant-progress", SessionID: "session-1", GlobalSeq: 3, Role: "assistant", Content: "checking", CreatedAt: 300},
		{
			ID:        "msg-tool",
			SessionID: "session-1",
			GlobalSeq: 5,
			Role:      "tool",
			Content:   `{"path_id":"run.v3.provider-tool-result.v1","tool_name":"read","call_id":"call-read","tool_instance_id":"step-1:call-read","completed_output":"actual result"}`,
			Metadata:  map[string]any{"tool_instance_id": toolInstanceID},
			CreatedAt: 500,
		},
		{ID: "msg-assistant-done", SessionID: "session-1", GlobalSeq: 6, Role: "assistant", Content: "done", CreatedAt: 600},
	}
	events := []client.SessionV3Event{
		toolRealtimeEvent("event-started", 2, 200, "session.tool.started", map[string]any{
			"run_id": "run-1", "tool_name": "read", "call_id": "call-read", "tool_instance_id": toolInstanceID, "arguments": `{"path":"facts.go"}`, "status": "started",
		}),
		toolRealtimeEvent("event-delta", 4, 400, "session.tool.delta", map[string]any{
			"run_id": "run-1", "tool_name": "read", "call_id": "call-read", "tool_instance_id": toolInstanceID, "output": "partial result",
		}),
		toolRealtimeEvent("event-completed", 5, 500, "session.tool.completed", map[string]any{
			"run_id": "run-1", "tool_name": "read", "call_id": "call-read", "tool_instance_id": toolInstanceID, "output": "actual result", "raw_output": "actual result", "status": "completed", "duration_ms": 27,
		}),
	}

	got := chatMessagesFromClient(messages, events)
	if ids := chatMessageRecordIDs(got); !reflect.DeepEqual(ids, []string{
		"msg-user",
		"v3-tool-event:event-started",
		"msg-assistant-progress",
		"v3-tool-event:event-delta",
		"v3-tool-event:event-completed",
		"msg-assistant-done",
	}) {
		t.Fatalf("projected timeline ids = %#v", ids)
	}
	for _, message := range got {
		if message.ID == "msg-tool" {
			t.Fatalf("durable terminal tool message duplicated projected lifecycle: %#v", got)
		}
	}
	if !strings.Contains(got[4].Content, "actual result") {
		t.Fatalf("completed event omitted actual output: %#v", got[4])
	}
	if replay := chatMessagesFromClient(messages, events); !reflect.DeepEqual(replay, got) {
		t.Fatalf("idempotent projection changed across replay:\nfirst=%#v\nreplay=%#v", got, replay)
	}
}

func toolRealtimeEvent(id string, seq uint64, at int64, eventType string, payload map[string]any) client.SessionV3Event {
	raw, _ := json.Marshal(payload)
	return client.SessionV3Event{ID: id, SessionID: "session-1", Seq: seq, EventType: eventType, Payload: raw, TsUnixMS: at}
}

func chatMessageRecordIDs(messages []ui.ChatMessageRecord) []string {
	ids := make([]string, 0, len(messages))
	for _, message := range messages {
		ids = append(ids, message.ID)
	}
	return ids
}

func TestTUIV3ReasoningEventsProjectAtAuthoritativeSequencePosition(t *testing.T) {
	messages := []client.SessionMessage{
		{ID: "msg-user", SessionID: "session-1", GlobalSeq: 1, Role: "user", Content: "inspect", CreatedAt: 100},
		{ID: "msg-assistant", SessionID: "session-1", GlobalSeq: 5, Role: "assistant", Content: "done", CreatedAt: 500},
	}
	events := []client.SessionV3Event{
		toolRealtimeEvent("reasoning-start", 2, 200, "session.reasoning.started", map[string]any{"run_id": "run-1", "reasoning_id": "reason-1"}),
		toolRealtimeEvent("reasoning-delta", 3, 300, "session.reasoning.delta", map[string]any{"run_id": "run-1", "reasoning_id": "reason-1", "delta": "checking files", "delta_mode": "replace"}),
		toolRealtimeEvent("reasoning-complete", 4, 400, "session.reasoning.completed", map[string]any{"run_id": "run-1", "reasoning_id": "reason-1", "summary": "checked files"}),
	}

	got := chatMessagesFromClient(messages, events)
	if ids := chatMessageRecordIDs(got); !reflect.DeepEqual(ids, []string{"msg-user", "v3-reasoning:reason-1:reasoning-start", "v3-reasoning:reason-1:reasoning-delta", "v3-reasoning:reason-1:reasoning-complete", "msg-assistant"}) {
		t.Fatalf("reasoning projection order = %#v", ids)
	}
	if got[1].Content != "Thinking" || got[2].Content != "checking files" || got[3].Content != "checked files" {
		t.Fatalf("reasoning projection content = %#v", got[1:4])
	}
}

func TestTUIRealtimeLivePatchUpdatesAssistantWithoutDurableTurnBoundary(t *testing.T) {
	store := newTUISessionStore()
	store.MergeHydrated(client.SessionV3Hydrated{Session: client.SessionSummary{ID: "session-1", SessionAPI: "v3"}})
	app := &App{tuiSessionStore: store, homeModel: model.EmptyHome()}
	app.chat = ui.NewChatPage(ui.ChatPageOptions{SessionID: "session-1", SessionMode: "auto", AuthConfigured: true})
	app.chat.ApplySessionLifecycle(ui.ChatSessionLifecycle{SessionID: "session-1", RunID: "run-1", Active: true, Phase: "running"})

	frame := client.V3RealtimeFrame{Kind: "live.patch", SessionID: "session-1", Live: &client.V3RealtimeLivePatch{SessionID: "session-1", RunID: "run-1", StreamID: "assistant:run-1:step:1", StreamKind: "assistant_text", Operation: "append", Step: 1, StepID: "step-1", LiveSeqStart: 1, LiveSeqEnd: 1, OffsetStart: 0, OffsetEnd: 7, Text: "partial", RecordedAt: 100}}
	if !app.applyTUIRealtimeFrame(frame) {
		t.Fatal("live patch did not update chat")
	}
	if text := app.chat.LiveAssistantText(); text != "partial" {
		t.Fatalf("chat live assistant = %q, want partial", text)
	}
	snapshot, _ := store.ChatSnapshot("session-1")
	if len(snapshot.Messages) != 0 {
		t.Fatalf("live patch incorrectly mutated durable messages: %#v", snapshot.Messages)
	}
}

func TestTUIHydratedPendingPermissionBecomesVisibleInChat(t *testing.T) {
	store := newTUISessionStore()
	store.MergeHydrated(client.SessionV3Hydrated{
		Session: client.SessionSummary{ID: "session-1", SessionAPI: "v3", Mode: "auto"},
		PendingPermissions: []client.PermissionRecord{{
			ID: "perm-bash", SessionID: "session-1", RunID: "run-1", CallID: "call-bash",
			ToolName: "bash", ToolArguments: `{"command":"git status"}`, Requirement: "bash", Status: "pending",
		}},
	})
	app := &App{tuiSessionStore: store, homeModel: model.EmptyHome()}
	app.chat = ui.NewChatPage(ui.ChatPageOptions{SessionID: "session-1", SessionMode: "auto", AuthConfigured: true})

	app.applyTUISessionStoreToChat("session-1")

	if !app.chat.OrdinaryPermissionComposerVisible() {
		t.Fatal("hydrated pending Bash permission did not activate the visible inline composer")
	}
	text := renderedChatScreenText(t, app.chat, 100, 32)
	for _, want := range []string{"permission", "bash", "git status"} {
		if !strings.Contains(strings.ToLower(text), strings.ToLower(want)) {
			t.Fatalf("hydrated permission UI missing %q:\n%s", want, text)
		}
	}
}

func TestTUIRealtimeManageSessionsPermissionBecomesVisibleInChat(t *testing.T) {
	store := newTUISessionStore()
	store.MergeHydrated(client.SessionV3Hydrated{
		Session:    client.SessionSummary{ID: "session-1", SessionAPI: "v3", Mode: "auto"},
		Projection: client.SessionV3Projection{SessionID: "session-1"},
	})
	app := &App{tuiSessionStore: store, homeModel: model.EmptyHome()}
	app.chat = ui.NewChatPage(ui.ChatPageOptions{SessionID: "session-1", SessionMode: "auto", AuthConfigured: true})

	payload, err := json.Marshal(map[string]any{
		"permission": client.PermissionRecord{
			ID: "perm-archive", SessionID: "session-1", RunID: "run-1", CallID: "call-archive",
			ToolName: "manage_sessions", Requirement: "session_archive", Status: "pending",
			ToolArguments: `{"action":"archive","sessions":[{"session_id":"child-1","title":"Child session","workspace_name":"Workspace","state":"inactive"}],"approved_arguments":{"action":"archive","session_ids":["child-1"],"expected_updated_at_by_id":{"child-1":10}}}`,
		},
	})
	if err != nil {
		t.Fatal(err)
	}
	frame := client.V3RealtimeFrame{
		Kind: "event", SessionID: "session-1", LastSeq: 1,
		Event: &client.SessionV3Event{ID: "event-permission", SessionID: "session-1", Seq: 1, EventType: "permission.requested", Payload: payload, TsUnixMS: 10},
	}
	if !app.applyTUIRealtimeFrame(frame) {
		t.Fatal("realtime permission frame did not update chat")
	}
	if !app.chat.PermissionModalVisible() {
		t.Fatal("realtime manage-sessions permission did not activate an approval surface")
	}
	text := renderedChatScreenText(t, app.chat, 100, 32)
	for _, want := range []string{"Archive sessions?", "Child session", "Approve", "Deny"} {
		if !strings.Contains(text, want) {
			t.Fatalf("realtime manage-sessions UI missing %q:\n%s", want, text)
		}
	}
}

func renderedChatScreenText(t *testing.T, page *ui.ChatPage, width, height int) string {
	t.Helper()
	screen := tcell.NewSimulationScreen("UTF-8")
	if err := screen.Init(); err != nil {
		t.Fatal(err)
	}
	defer screen.Fini()
	screen.SetSize(width, height)
	page.Draw(screen)
	screen.Show()
	cells, screenWidth, _ := screen.GetContents()
	var out strings.Builder
	for i, cell := range cells {
		if i > 0 && i%screenWidth == 0 {
			out.WriteByte('\n')
		}
		if len(cell.Runes) > 0 {
			out.WriteRune(cell.Runes[0])
		} else {
			out.WriteByte(' ')
		}
	}
	return out.String()
}

func TestTUIRealtimeModePreferenceSurvivesPendingResponseProjection(t *testing.T) {
	store := newTUISessionStore()
	store.MergeHydrated(client.SessionV3Hydrated{
		Session:    client.SessionSummary{ID: "session-1", SessionAPI: "v3", Mode: "plan"},
		Preference: client.ModelPreference{Provider: "codex", Model: "plan-model", Thinking: "xhigh"},
		AgentModelPolicy: client.SessionV3AgentModelPolicy{
			Preference:    client.ModelPreference{Provider: "codex", Model: "plan-model", Thinking: "xhigh"},
			ContextWindow: 200000,
		},
	})
	store.ApplyModePreference("session-1", "auto", client.ModelPreference{Provider: "codex", Model: "auto-model", Thinking: "medium"}, 240000)

	app := &App{tuiSessionStore: store, homeModel: model.EmptyHome()}
	app.chat = ui.NewChatPage(ui.ChatPageOptions{SessionID: "session-1", SessionMode: "auto"})
	app.applyTUISessionStoreToChat("session-1")

	if got := app.chat.SessionMode(); got != "auto" {
		t.Fatalf("SessionMode = %q, want auto", got)
	}
	provider, modelName, thinking, _, _ := app.chat.ModelState()
	if provider != "codex" || modelName != "auto-model" || thinking != "medium" {
		t.Fatalf("ModelState = (%q, %q, %q), want auto-mode preference", provider, modelName, thinking)
	}
	if got := app.chat.ContextWindow(); got != 240000 {
		t.Fatalf("ContextWindow = %d, want 240000", got)
	}
}

func TestTUIRealtimeWorksetSubscriptionUsesBackendAcceptedWorkspaceSelector(t *testing.T) {
	app := &App{tuiRealtimeClientID: "tui:test"}
	state := tuiRealtimeWorksetState{ScopeKey: "workspace:/repo", WorkspacePaths: []string{"/repo"}}

	workset := app.tuiRealtimeWorksetSubscription(state)

	if workset.WorksetID != "tui:workspace:/repo" {
		t.Fatalf("workset id = %q", workset.WorksetID)
	}
	if workset.SubscriptionID != "tui:test:workset:workspace:/repo" {
		t.Fatalf("subscription id = %q", workset.SubscriptionID)
	}
	if workset.Surface != "tui" || !workset.AutoSubscribeSessions {
		t.Fatalf("workset surface/auto = %q/%v", workset.Surface, workset.AutoSubscribeSessions)
	}
	if workset.Selector.Kind != "workspace" || workset.Selector.WorkspacePath != "/repo" || len(workset.Selector.WorkspacePaths) != 0 {
		t.Fatalf("selector = %+v", workset.Selector)
	}
	if !reflect.DeepEqual(workset.Resources, tuiRealtimeWorksetResources) {
		t.Fatalf("resources = %+v, want %+v", workset.Resources, tuiRealtimeWorksetResources)
	}
}

func TestTUIRealtimeWorksetSubscriptionUsesCWDAsWorkspaceSelector(t *testing.T) {
	app := &App{tuiRealtimeClientID: "tui:test"}
	state := tuiRealtimeWorksetState{ScopeKey: "cwd:/repo/subdir", CWDPath: "/repo/subdir"}

	workset := app.tuiRealtimeWorksetSubscription(state)

	if workset.Selector.Kind != "workspace" || workset.Selector.WorkspacePath != "/repo/subdir" || len(workset.Selector.WorkspacePaths) != 0 {
		t.Fatalf("selector = %+v", workset.Selector)
	}
}

func TestTUIRealtimeWorksetSubscriptionUsesWorkspacePathsForMultipleRoots(t *testing.T) {
	app := &App{tuiRealtimeClientID: "tui:test"}
	state := tuiRealtimeWorksetState{ScopeKey: "workspace:/repo-a|/repo-b", WorkspacePaths: []string{"/repo-a", "/repo-b"}}

	workset := app.tuiRealtimeWorksetSubscription(state)

	if workset.Selector.Kind != "workspace" || workset.Selector.WorkspacePath != "" || !reflect.DeepEqual(workset.Selector.WorkspacePaths, []string{"/repo-a", "/repo-b"}) {
		t.Fatalf("selector = %+v", workset.Selector)
	}
}

func TestEnsureTUIRealtimeWorksetReadyBootstrapsWhenCursorMissing(t *testing.T) {
	var worksetRequests int
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodPost || r.URL.Path != "/v3/tui/sessions:workset" {
			t.Fatalf("unexpected request: %s %s", r.Method, r.URL.Path)
		}
		worksetRequests++
		_ = json.NewEncoder(w).Encode(client.SessionV3Workset{
			OK:                     true,
			SnapshotEndpointCursor: "cursor-workset",
			SessionsByID:           map[string]client.SessionSummary{"session-a": {ID: "session-a", WorkspacePath: "/repo", Title: "From Workset", SessionAPI: "v3"}},
			ProjectionsBySession:   map[string]client.SessionV3Projection{"session-a": {SessionID: "session-a", LastEventSeq: 3}},
			SessionOrder:           []string{"session-a"},
		})
	}))
	defer server.Close()

	app := &App{
		api:                 testAPIWithToken(server.URL),
		tuiSessionStore:     newTUISessionStore(),
		tuiRealtimeWorkset:  tuiRealtimeWorksetState{ScopeKey: "workspace:/repo", WorkspacePaths: []string{"/repo"}},
		tuiRealtimeClientID: "tui:test",
		homeModel:           model.EmptyHome(),
	}
	app.tuiSessionStore.MergeHydrated(client.SessionV3Hydrated{
		Session:                client.SessionSummary{ID: "session-a", WorkspacePath: "/repo", Title: "Hydrated", SessionAPI: "v3"},
		Projection:             client.SessionV3Projection{SessionID: "session-a", LastEventSeq: 2},
		SnapshotEndpointCursor: "hydrated-cursor-must-not-be-used",
	})
	if got := app.tuiSessionStore.EndpointCursor(); got != "" {
		t.Fatalf("setup endpoint cursor = %q, want empty", got)
	}

	if err := app.ensureTUIRealtimeWorksetReady(context.Background(), tuiSessionWorksetLoadOptions{Limit: 25, WorkspacePaths: []string{"/repo"}}); err != nil {
		t.Fatalf("ensureTUIRealtimeWorksetReady() error = %v", err)
	}
	if worksetRequests != 1 {
		t.Fatalf("workset requests = %d, want 1", worksetRequests)
	}
	if got := app.tuiSessionStore.EndpointCursor(); got != "cursor-workset" {
		t.Fatalf("endpoint cursor = %q, want TUI workset cursor", got)
	}
	if app.tuiRealtimeWorkset.ScopeKey != "workspace:/repo" {
		t.Fatalf("realtime workset state = %#v", app.tuiRealtimeWorkset)
	}

	if err := app.ensureTUIRealtimeWorksetReady(context.Background(), tuiSessionWorksetLoadOptions{Limit: 25, WorkspacePaths: []string{"/repo"}}); err != nil {
		t.Fatalf("second ensureTUIRealtimeWorksetReady() error = %v", err)
	}
	if worksetRequests != 1 {
		t.Fatalf("cached workset issued requests = %d, want 1", worksetRequests)
	}
}

func TestTUIHomeWorksetBootstrapThenRealtimeUpdatesWithoutPolling(t *testing.T) {
	var worksetRequests int
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodPost || r.URL.Path != "/v3/tui/sessions:workset" {
			t.Fatalf("unexpected request: %s %s", r.Method, r.URL.Path)
		}
		worksetRequests++
		if worksetRequests > 1 {
			t.Fatalf("TUI must not poll workset after startup bootstrap; request %d", worksetRequests)
		}
		_ = json.NewEncoder(w).Encode(client.SessionV3Workset{
			OK:                     true,
			SnapshotEndpointCursor: "cursor-bootstrap",
			SessionsByID:           map[string]client.SessionSummary{"session-a": {ID: "session-a", WorkspacePath: "/repo", Title: "Initial", SessionAPI: "v3"}},
			ProjectionsBySession:   map[string]client.SessionV3Projection{"session-a": {SessionID: "session-a", LastEventSeq: 1}},
			SessionOrder:           []string{"session-a"},
		})
	}))
	defer server.Close()

	fake := newTUIRealtimeFakeStreamer()
	controller := newTestTUIRealtimeController(fake, make(chan client.V3RealtimeFrame, 8), make(chan tuiRealtimeStatus, 16))
	app := &App{
		api:                 testAPIWithToken(server.URL),
		workspacePath:       "/repo",
		tuiSessionStore:     newTUISessionStore(),
		tuiRealtime:         controller,
		tuiRealtimeClientID: "tui:test",
		tuiRealtimeFrames:   make(chan client.V3RealtimeFrame, 8),
		tuiRealtimeStatuses: make(chan tuiRealtimeStatus, 8),
		homeModel:           model.EmptyHome(),
	}
	app.home = ui.NewHomePage(app.homeModel)

	workset, state, err := app.bootstrapTUIRealtimeWorkset(context.Background(), tuiSessionWorksetLoadOptions{Limit: 25, WorkspacePaths: []string{"/repo"}})
	if err != nil {
		t.Fatalf("bootstrapTUIRealtimeWorkset() error = %v", err)
	}
	app.applyTUISessionWorksetSnapshot(workset, state)
	app.applyTUISessionStoreToHome()
	if err := app.reconcileTUIRealtime(); err != nil {
		t.Fatalf("reconcileTUIRealtime() error = %v", err)
	}
	call := waitTUIRealtimeCall(t, fake)
	if call.EndpointCursor != "cursor-bootstrap" || len(call.Worksets) != 1 || call.Worksets[0].Selector.WorkspacePath != "/repo" {
		t.Fatalf("realtime workset resume = %#v", call)
	}

	for _, frame := range []client.V3RealtimeFrame{
		{Kind: "workset.session.discovered", EndpointCursor: "cursor-2", SessionID: "session-b", Session: &client.SessionSummary{ID: "session-b", WorkspacePath: "/repo", Title: "Discovered", SessionAPI: "v3"}, Projection: &client.SessionV3Projection{SessionID: "session-b", LastEventSeq: 2}},
		{Kind: "workset.session.updated", EndpointCursor: "cursor-3", SessionID: "session-b", Session: &client.SessionSummary{ID: "session-b", WorkspacePath: "/repo", Title: "Updated", SessionAPI: "v3"}, Projection: &client.SessionV3Projection{SessionID: "session-b", LastEventSeq: 3}},
		{Kind: "workset.session.removed", EndpointCursor: "cursor-4", SessionID: "session-a"},
	} {
		if !app.applyTUIRealtimeFrame(frame) {
			t.Fatalf("frame did not update home state: %#v", frame)
		}
	}
	if worksetRequests != 1 {
		t.Fatalf("workset requests = %d, want exactly one bootstrap request", worksetRequests)
	}
	if got := app.tuiSessionStore.EndpointCursor(); got != "cursor-4" {
		t.Fatalf("endpoint cursor = %q, want cursor-4", got)
	}
	ids := sessionIDs(app.tuiSessionStore.HomeSessions())
	if !reflect.DeepEqual(ids, []string{"session-b"}) || app.tuiSessionStore.HomeSessions()[0].Title != "Updated" {
		t.Fatalf("home sessions after realtime frames = %#v", app.tuiSessionStore.HomeSessions())
	}
}

func TestTUIRealtimeEndToEndHitsWorksetAndRealtimeAPIsAndRehydratesOnCursorError(t *testing.T) {
	var worksetRequests int
	var sessionHydrateRequests int
	var realtimeRequests int
	var resume map[string]any
	rehydrateReady := make(chan struct{})
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch {
		case r.Method == http.MethodPost && r.URL.Path == "/v3/tui/sessions:workset":
			worksetRequests++
			var req client.SessionV3TUIWorksetRequest
			if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
				t.Fatalf("decode workset request: %v", err)
			}
			if len(req.SessionIDs) != 0 {
				t.Fatalf("initial workset bootstrap session_ids = %#v, want none", req.SessionIDs)
			}
			if req.Scope.WorkspacePaths[0] != "/repo" || req.History.Mode != "tail" || !req.History.IncludeEvents {
				t.Fatalf("workset request = %#v", req)
			}
			_ = json.NewEncoder(w).Encode(client.SessionV3Workset{
				OK:                     true,
				SnapshotEndpointCursor: "cursor-bootstrap",
				SessionsByID:           map[string]client.SessionSummary{"session-a": {ID: "session-a", WorkspacePath: "/repo", Title: "Initial", SessionAPI: "v3"}},
				ProjectionsBySession:   map[string]client.SessionV3Projection{"session-a": {SessionID: "session-a", LastEventSeq: uint64(worksetRequests)}},
				SessionOrder:           []string{"session-a"},
			})
		case r.Method == http.MethodGet && r.URL.Path == "/v3/tui/sessions/session-a":
			sessionHydrateRequests++
			if got := r.URL.Query().Get("workspace_path"); got != "/repo" {
				t.Fatalf("session hydrate workspace_path query = %q, want /repo", got)
			}
			if sessionHydrateRequests > 1 {
				t.Fatalf("cursor.error should trigger exactly one session rehydrate; request %d", sessionHydrateRequests)
			}
			_ = json.NewEncoder(w).Encode(client.SessionV3Hydrated{
				Session:                client.SessionSummary{ID: "session-a", WorkspacePath: "/repo", Title: "Rehydrated", SessionAPI: "v3"},
				Projection:             client.SessionV3Projection{SessionID: "session-a", LastEventSeq: 2},
				SnapshotEndpointCursor: "cursor-rehydrated",
			})
			close(rehydrateReady)
		case r.Method == http.MethodGet && r.URL.Path == "/v1/vault":
			_ = json.NewEncoder(w).Encode(map[string]any{"ok": true, "enabled": false})
		case r.Method == http.MethodGet && r.URL.Path == "/healthz":
			_ = json.NewEncoder(w).Encode(map[string]any{"ok": true, "mode": "dev"})
		case r.Method == http.MethodGet && r.URL.Path == "/v1/worktrees":
			_ = json.NewEncoder(w).Encode(map[string]any{"ok": true, "enabled": false})
		case r.Method == http.MethodGet && r.URL.Path == "/v1/providers":
			_ = json.NewEncoder(w).Encode(map[string]any{"ok": true, "providers": []any{}})
		case r.Method == http.MethodGet && r.URL.Path == "/v1/model":
			_ = json.NewEncoder(w).Encode(map[string]any{"ok": true})
		case r.Method == http.MethodGet && r.URL.Path == "/v1/update/status":
			_ = json.NewEncoder(w).Encode(map[string]any{"ok": true})
		case r.Method == http.MethodGet && r.URL.Path == "/v2/agents":
			_ = json.NewEncoder(w).Encode(map[string]any{"ok": true, "agents": []any{}})
		case r.Method == http.MethodGet && r.URL.Path == "/v1/context/sources":
			_ = json.NewEncoder(w).Encode(map[string]any{"ok": true, "sources": []any{}})
		case r.Method == http.MethodGet && r.URL.Path == "/v1/workspace/overview":
			_ = json.NewEncoder(w).Encode(map[string]any{"ok": true, "workspaces": []any{map[string]any{"path": "/repo", "workspace_path": "/repo", "workspace_name": "repo"}}, "directories": []any{}})
		case r.Method == http.MethodGet && r.URL.Path == "/v1/workspace/cwd/resolve":
			_ = json.NewEncoder(w).Encode(map[string]any{"ok": true, "cwd": "/repo", "workspace": map[string]any{"workspace_path": "/repo", "workspace_name": "repo"}, "chat_routes": []any{}})
		case r.Method == http.MethodGet && r.URL.Path == "/v3/realtime/stream":
			realtimeRequests++
			if got := r.URL.Query().Get("endpoint_cursor"); got != "cursor-bootstrap" {
				t.Fatalf("realtime endpoint_cursor query = %q, want cursor-bootstrap", got)
			}
			if got := r.URL.Query().Get("surface"); got != "tui" {
				t.Fatalf("realtime surface query = %q, want tui", got)
			}
			conn, rw, err := hijackTUITestWebsocket(w, r)
			if err != nil {
				t.Fatalf("hijack realtime websocket: %v", err)
			}
			defer conn.Close()
			_, payload, err := readTUITestWebsocketFrame(rw)
			if err != nil {
				t.Fatalf("read realtime resume: %v", err)
			}
			if err := json.Unmarshal(payload, &resume); err != nil {
				t.Fatalf("decode realtime resume: %v", err)
			}
			writeTUITestWebsocketFrame(t, conn, map[string]any{"protocol": "v3.realtime", "protocol_version": 1, "kind": "cursor.error", "session_id": "session-a", "endpoint_cursor": "cursor-error", "error": "forced cursor gap; refetch required"})
			<-r.Context().Done()
		default:
			t.Fatalf("unexpected request: %s %s", r.Method, r.URL.String())
		}
	}))
	defer server.Close()

	app := &App{
		api:                 testAPIWithToken(server.URL),
		workspacePath:       "/repo",
		startupCWD:          "/repo",
		tuiSessionStore:     newTUISessionStore(),
		tuiRealtimeClientID: "tui:test",
		tuiRealtimeFrames:   make(chan client.V3RealtimeFrame, 8),
		tuiRealtimeStatuses: make(chan tuiRealtimeStatus, 8),
		reloadCh:            make(chan homeReloadResult, 1),
		homeModel:           model.EmptyHome(),
	}
	app.home = ui.NewHomePage(app.homeModel)

	workset, state, err := app.bootstrapTUIRealtimeWorkset(context.Background(), tuiSessionWorksetLoadOptions{Limit: 25, WorkspacePaths: []string{"/repo"}})
	if err != nil {
		t.Fatalf("bootstrapTUIRealtimeWorkset() error = %v", err)
	}
	app.applyTUISessionWorksetSnapshot(workset, state)
	app.applyTUISessionStoreToHome()
	if err := app.reconcileTUIRealtime(); err != nil {
		t.Fatalf("reconcileTUIRealtime() error = %v", err)
	}

	deadline := time.After(2 * time.Second)
	for app.tuiSessionStore.StaleState().Reason == "" {
		select {
		case <-deadline:
			t.Fatalf("cursor.error was not applied; resume=%#v", resume)
		default:
			app.consumeTUIRealtimeEvents()
			time.Sleep(time.Millisecond)
		}
	}
	if stale := app.tuiSessionStore.StaleState(); stale.Reason != "forced cursor gap; refetch required" || stale.SessionID != "session-a" {
		t.Fatalf("stale state = %#v", stale)
	}
	select {
	case <-rehydrateReady:
	case <-time.After(2 * time.Second):
		t.Fatalf("cursor.error did not trigger second /v3/tui/sessions:workset request")
	}
	if got := app.tuiSessionStore.EndpointCursor(); got != "cursor-bootstrap" {
		t.Fatalf("endpoint cursor after cursor.error rehydrate = %q, want original TUI workset cursor", got)
	}
	if worksetRequests != 1 || sessionHydrateRequests != 1 || realtimeRequests != 1 {
		t.Fatalf("api calls: workset=%d sessionHydrate=%d realtime=%d resume=%#v", worksetRequests, sessionHydrateRequests, realtimeRequests, resume)
	}
	if resume["protocol"] != "v3.realtime" || resume["protocol_version"] != float64(1) || resume["kind"] != "resume" || resume["endpoint_cursor"] != "cursor-bootstrap" {
		t.Fatalf("resume frame = %#v", resume)
	}
	rawWorksets, ok := resume["worksets"].([]any)
	if !ok || len(rawWorksets) != 1 {
		t.Fatalf("resume worksets = %#v", resume["worksets"])
	}
	worksetResume, ok := rawWorksets[0].(map[string]any)
	if !ok || worksetResume["surface"] != "tui" || worksetResume["auto_subscribe_sessions"] != true {
		t.Fatalf("resume workset = %#v", rawWorksets[0])
	}
	rawSubs, ok := resume["subscriptions"].([]any)
	if !ok || len(rawSubs) != 1 {
		t.Fatalf("resume subscriptions = %#v", resume["subscriptions"])
	}
	sub, ok := rawSubs[0].(map[string]any)
	if !ok || sub["session_id"] != "session-a" || sub["endpoint_cursor"] != "cursor-bootstrap" || sub["last_seq"] != float64(1) {
		t.Fatalf("resume subscription = %#v", rawSubs[0])
	}
}

func hijackTUITestWebsocket(w http.ResponseWriter, r *http.Request) (net.Conn, *bufio.ReadWriter, error) {
	hijacker, ok := w.(http.Hijacker)
	if !ok {
		return nil, nil, http.ErrNotSupported
	}
	conn, rw, err := hijacker.Hijack()
	if err != nil {
		return nil, nil, err
	}
	accept := tuiTestWebsocketAccept(r.Header.Get("Sec-WebSocket-Key"))
	if _, err := rw.WriteString("HTTP/1.1 101 Switching Protocols\r\nUpgrade: websocket\r\nConnection: Upgrade\r\nSec-WebSocket-Accept: " + accept + "\r\n\r\n"); err != nil {
		_ = conn.Close()
		return nil, nil, err
	}
	if err := rw.Flush(); err != nil {
		_ = conn.Close()
		return nil, nil, err
	}
	return conn, rw, nil
}

func tuiTestWebsocketAccept(key string) string {
	hash := sha1.New()
	_, _ = hash.Write([]byte(key + "258EAFA5-E914-47DA-95CA-C5AB0DC85B11"))
	return base64.StdEncoding.EncodeToString(hash.Sum(nil))
}

func readTUITestWebsocketFrame(r io.Reader) (byte, []byte, error) {
	head := make([]byte, 2)
	if _, err := io.ReadFull(r, head); err != nil {
		return 0, nil, err
	}
	opcode := head[0] & 0x0F
	masked := head[1]&0x80 != 0
	payloadLength := int(head[1] & 0x7F)
	if payloadLength == 126 {
		ext := make([]byte, 2)
		if _, err := io.ReadFull(r, ext); err != nil {
			return 0, nil, err
		}
		payloadLength = int(ext[0])<<8 | int(ext[1])
	} else if payloadLength == 127 {
		return 0, nil, http.ErrNotSupported
	}
	var mask [4]byte
	if masked {
		if _, err := io.ReadFull(r, mask[:]); err != nil {
			return 0, nil, err
		}
	}
	payload := make([]byte, payloadLength)
	if _, err := io.ReadFull(r, payload); err != nil {
		return 0, nil, err
	}
	if masked {
		for i := range payload {
			payload[i] ^= mask[i%4]
		}
	}
	return opcode, payload, nil
}

func writeTUITestWebsocketFrame(t *testing.T, conn io.Writer, payload any) {
	t.Helper()
	raw, err := json.Marshal(payload)
	if err != nil {
		t.Fatalf("marshal server frame: %v", err)
	}
	header := []byte{0x80 | 0x1}
	if len(raw) <= 125 {
		header = append(header, byte(len(raw)))
	} else if len(raw) <= 65535 {
		header = append(header, 126, byte(len(raw)>>8), byte(len(raw)))
	} else {
		t.Fatalf("test frame too large")
	}
	if _, err := conn.Write(append(header, raw...)); err != nil {
		t.Fatalf("write server frame: %v", err)
	}
}
