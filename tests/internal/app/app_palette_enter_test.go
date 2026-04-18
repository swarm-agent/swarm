package app

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"slices"
	"strings"
	"testing"
	"time"

	"github.com/gdamore/tcell/v2"

	"swarm-refactor/swarmtui/internal/client"
	"swarm-refactor/swarmtui/internal/model"
	"swarm-refactor/swarmtui/internal/ui"
)

func TestHandleHomeKey_EnterExecutesPaletteSelection(t *testing.T) {
	a := &App{
		home:   ui.NewHomePage(model.EmptyHome()),
		route:  "home",
		config: defaultAppConfig(),
	}
	a.home.SetCommandSuggestions(homeCommandSuggestions)
	a.home.SetPrompt("/them")

	if handled := a.handleHomeKey(tcell.NewEventKey(tcell.KeyEnter, 0, tcell.ModNone)); !handled {
		t.Fatalf("handleHomeKey() handled = false, want true")
	}
	if !a.home.ThemeModalVisible() {
		t.Fatalf("ThemeModalVisible() = false, want true")
	}
}

func TestHomeCommandSuggestionsAreAlphabetical(t *testing.T) {
	for i := 1; i < len(homeCommandSuggestions); i++ {
		prev := strings.ToLower(homeCommandSuggestions[i-1].Command)
		curr := strings.ToLower(homeCommandSuggestions[i].Command)
		if prev > curr {
			t.Fatalf("homeCommandSuggestions not alphabetical at %q then %q", homeCommandSuggestions[i-1].Command, homeCommandSuggestions[i].Command)
		}
	}
}

func TestHomeCommandSuggestionsCondensePlanAndExposeWorktreeAliasTip(t *testing.T) {
	var planSuggestion *ui.CommandSuggestion
	var worktreesSuggestion *ui.CommandSuggestion
	var codexSuggestion *ui.CommandSuggestion
	var thinkingSuggestion *ui.CommandSuggestion
	for i := range homeCommandSuggestions {
		suggestion := &homeCommandSuggestions[i]
		switch suggestion.Command {
		case "/plan":
			planSuggestion = suggestion
		case "/worktrees":
			worktreesSuggestion = suggestion
		case "/codex":
			codexSuggestion = suggestion
		case "/thinking":
			thinkingSuggestion = suggestion
		}
		if suggestion.Command == "/plan exit" {
			t.Fatalf("unexpected direct /plan exit suggestion in condensed palette")
		}
	}
	if planSuggestion == nil {
		t.Fatalf("missing /plan suggestion")
	}
	if !slices.Contains(planSuggestion.QuickTips, "/plan exit") {
		t.Fatalf("/plan suggestion missing /plan exit quick tip: %v", planSuggestion.QuickTips)
	}
	var agentsSuggestion *ui.CommandSuggestion
	for i := range homeCommandSuggestions {
		suggestion := &homeCommandSuggestions[i]
		if suggestion.Command == "/agents" {
			agentsSuggestion = suggestion
			break
		}
	}
	if agentsSuggestion == nil {
		t.Fatalf("missing /agents suggestion")
	}
	if !slices.Contains(agentsSuggestion.QuickTips, "/agents reset") {
		t.Fatalf("/agents suggestion missing /agents reset quick tip: %v", agentsSuggestion.QuickTips)
	}
	if !slices.Contains(agentsSuggestion.QuickTips, "/agents restore") {
		t.Fatalf("/agents suggestion missing /agents restore quick tip: %v", agentsSuggestion.QuickTips)
	}
	if worktreesSuggestion == nil {
		t.Fatalf("missing /worktrees suggestion")
	}
	if !slices.Contains(worktreesSuggestion.QuickTips, "/wt") {
		t.Fatalf("/worktrees suggestion missing /wt alias tip: %v", worktreesSuggestion.QuickTips)
	}
	if codexSuggestion == nil {
		t.Fatalf("missing /codex suggestion")
	}
	if !slices.Contains(codexSuggestion.QuickTips, "/codex status") {
		t.Fatalf("/codex suggestion missing /codex status quick tip: %v", codexSuggestion.QuickTips)
	}
	if thinkingSuggestion == nil {
		t.Fatalf("missing /thinking suggestion")
	}
	if !slices.Contains(thinkingSuggestion.QuickTips, "/thinking off") {
		t.Fatalf("/thinking suggestion missing /thinking off quick tip: %v", thinkingSuggestion.QuickTips)
	}
}

func TestHandleChatKey_EnterExecutesPaletteSelection(t *testing.T) {
	a := &App{
		home: ui.NewHomePage(model.EmptyHome()),
		chat: ui.NewChatPage(ui.ChatPageOptions{
			SessionID:          "session-1",
			SessionTitle:       "Session",
			CommandSuggestions: homeCommandSuggestions,
			ShowHeader:         true,
			AuthConfigured:     true,
		}),
		route:  "chat",
		config: defaultAppConfig(),
	}

	for _, r := range []rune("/them") {
		a.chat.HandleKey(tcell.NewEventKey(tcell.KeyRune, r, tcell.ModNone))
	}

	if handled := a.handleChatKey(tcell.NewEventKey(tcell.KeyEnter, 0, tcell.ModNone)); !handled {
		t.Fatalf("handleChatKey() handled = false, want true")
	}
	if a.route != "chat" {
		t.Fatalf("route = %q, want chat", a.route)
	}
	if a.chat == nil {
		t.Fatalf("chat != nil, want active chat")
	}
	if !a.home.ThemeModalVisible() {
		t.Fatalf("ThemeModalVisible() = false, want true")
	}
}

func TestHandleHomeKey_EnterExecutesAgentsModalSelection(t *testing.T) {
	a := &App{
		home:   ui.NewHomePage(model.EmptyHome()),
		route:  "home",
		config: defaultAppConfig(),
	}
	a.home.SetCommandSuggestions(homeCommandSuggestions)
	a.home.SetPrompt("/agents")

	if handled := a.handleHomeKey(tcell.NewEventKey(tcell.KeyEnter, 0, tcell.ModNone)); !handled {
		t.Fatalf("handleHomeKey() handled = false, want true")
	}
	if !a.home.AgentsModalVisible() {
		t.Fatalf("AgentsModalVisible() = false, want true")
	}
}

func TestHandleHomeKey_EnterExecutesKeybindsModalSelection(t *testing.T) {
	a := &App{
		home:   ui.NewHomePage(model.EmptyHome()),
		route:  "home",
		config: defaultAppConfig(),
	}
	a.home.SetCommandSuggestions(homeCommandSuggestions)
	a.home.SetPrompt("/keyb")

	if handled := a.handleHomeKey(tcell.NewEventKey(tcell.KeyEnter, 0, tcell.ModNone)); !handled {
		t.Fatalf("handleHomeKey() handled = false, want true")
	}
	if !a.home.KeybindsModalVisible() {
		t.Fatalf("KeybindsModalVisible() = false, want true")
	}
}

func TestHandleHomeKey_EnterExecutesVoiceModalSelection(t *testing.T) {
	a := &App{
		home:   ui.NewHomePage(model.EmptyHome()),
		route:  "home",
		config: defaultAppConfig(),
		api:    client.New("http://127.0.0.1:7782"),
	}
	a.home.SetCommandSuggestions(homeCommandSuggestions)
	a.home.SetPrompt("/voice")

	if handled := a.handleHomeKey(tcell.NewEventKey(tcell.KeyEnter, 0, tcell.ModNone)); !handled {
		t.Fatalf("handleHomeKey() handled = false, want true")
	}
	if !a.home.VoiceModalVisible() {
		t.Fatalf("VoiceModalVisible() = false, want true")
	}
}

func TestHandleChatKey_EnterExecutesAgentsModalSelection(t *testing.T) {
	a := &App{
		home: ui.NewHomePage(model.EmptyHome()),
		chat: ui.NewChatPage(ui.ChatPageOptions{
			SessionID:          "session-1",
			SessionTitle:       "Session",
			CommandSuggestions: homeCommandSuggestions,
			ShowHeader:         true,
			AuthConfigured:     true,
		}),
		route:  "chat",
		config: defaultAppConfig(),
	}

	for _, r := range []rune("/agents") {
		a.chat.HandleKey(tcell.NewEventKey(tcell.KeyRune, r, tcell.ModNone))
	}

	if handled := a.handleChatKey(tcell.NewEventKey(tcell.KeyEnter, 0, tcell.ModNone)); !handled {
		t.Fatalf("handleChatKey() handled = false, want true")
	}
	if a.route != "chat" {
		t.Fatalf("route = %q, want chat", a.route)
	}
	if a.chat == nil {
		t.Fatalf("chat == nil, want active chat")
	}
	if !a.home.AgentsModalVisible() {
		t.Fatalf("AgentsModalVisible() = false, want true")
	}
}

func TestHandleChatKey_EnterExecutesVoiceModalSelection(t *testing.T) {
	a := &App{
		home: ui.NewHomePage(model.EmptyHome()),
		chat: ui.NewChatPage(ui.ChatPageOptions{
			SessionID:          "session-1",
			SessionTitle:       "Session",
			CommandSuggestions: homeCommandSuggestions,
			ShowHeader:         true,
			AuthConfigured:     true,
		}),
		route:  "chat",
		config: defaultAppConfig(),
		api:    client.New("http://127.0.0.1:7782"),
	}

	for _, r := range []rune("/voice") {
		a.chat.HandleKey(tcell.NewEventKey(tcell.KeyRune, r, tcell.ModNone))
	}

	if handled := a.handleChatKey(tcell.NewEventKey(tcell.KeyEnter, 0, tcell.ModNone)); !handled {
		t.Fatalf("handleChatKey() handled = false, want true")
	}
	if a.route != "chat" {
		t.Fatalf("route = %q, want chat", a.route)
	}
	if a.chat == nil {
		t.Fatalf("chat == nil, want active chat")
	}
	if !a.home.VoiceModalVisible() {
		t.Fatalf("VoiceModalVisible() = false, want true")
	}
}

func TestHandleChatKey_PlanExitOpensExitPlanModal(t *testing.T) {
	a := &App{
		home: ui.NewHomePage(model.EmptyHome()),
		chat: ui.NewChatPage(ui.ChatPageOptions{
			SessionID:          "session-1",
			SessionTitle:       "Session",
			CommandSuggestions: homeCommandSuggestions,
			ShowHeader:         true,
			AuthConfigured:     true,
			SessionMode:        "plan",
		}),
		route:  "chat",
		config: defaultAppConfig(),
	}

	for _, r := range []rune("/plan exit") {
		a.chat.HandleKey(tcell.NewEventKey(tcell.KeyRune, r, tcell.ModNone))
	}

	if handled := a.handleChatKey(tcell.NewEventKey(tcell.KeyEnter, 0, tcell.ModNone)); !handled {
		t.Fatalf("handleChatKey() handled = false, want true")
	}
	if a.route != "chat" {
		t.Fatalf("route = %q, want chat", a.route)
	}
	if a.chat == nil {
		t.Fatalf("chat = nil, want active chat")
	}
	if !a.chat.ExitPlanModalVisible() {
		t.Fatalf("ExitPlanModalVisible() = false, want true")
	}
}

func TestHandleHomeKey_NewSessionAppliesHomeMode(t *testing.T) {
	var (
		created  bool
		setMode  bool
		lastMode string
	)

	handler := http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch {
		case r.Method == http.MethodPost && r.URL.Path == "/v1/sessions":
			created = true
			_ = json.NewEncoder(w).Encode(map[string]any{
				"ok": true,
				"session": map[string]any{
					"id":             "session-test",
					"workspace_path": "/tmp/ws",
					"workspace_name": "ws",
					"title":          "New Session",
					"mode":           "plan",
				},
			})
		case r.Method == http.MethodPost && r.URL.Path == "/v1/sessions/session-test/mode":
			setMode = true
			var req struct {
				Mode string `json:"mode"`
			}
			if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
				t.Fatalf("decode set-mode request: %v", err)
			}
			lastMode = strings.TrimSpace(req.Mode)
			_ = json.NewEncoder(w).Encode(map[string]any{
				"ok":         true,
				"session_id": "session-test",
				"mode":       lastMode,
			})
		case r.Method == http.MethodGet && strings.HasPrefix(r.URL.Path, "/v1/sessions/") && strings.HasSuffix(r.URL.Path, "/messages"):
			_ = json.NewEncoder(w).Encode(map[string]any{"ok": true, "session_id": "session-test", "messages": []any{}})
		case r.Method == http.MethodGet && r.URL.Path == "/v1/sessions/session-test/mode":
			_ = json.NewEncoder(w).Encode(map[string]any{"ok": true, "session_id": "session-test", "mode": "auto"})
		case r.Method == http.MethodGet && strings.HasPrefix(r.URL.Path, "/v1/sessions/session-test/permissions"):
			_ = json.NewEncoder(w).Encode(map[string]any{"ok": true, "session_id": "session-test", "permissions": []any{}})
		default:
			w.WriteHeader(http.StatusNotFound)
			_ = json.NewEncoder(w).Encode(map[string]any{"ok": false, "error": "not found"})
		}
	})
	server := httptest.NewServer(handler)
	defer server.Close()

	a := &App{
		home:       ui.NewHomePage(model.EmptyHome()),
		route:      "home",
		config:     defaultAppConfig(),
		api:        client.New(server.URL),
		startupCWD: "/tmp/ws",
		activePath: "/tmp/ws",
		homeModel:  model.HomeModel{CWD: "/tmp/ws", ServerMode: "single", ActiveAgent: "swarm", ContextWindow: 200000},
	}
	a.home.SetSessionMode("auto")
	a.home.SetPrompt("new session")

	if handled := a.handleHomeKey(tcell.NewEventKey(tcell.KeyEnter, 0, tcell.ModNone)); !handled {
		t.Fatalf("handleHomeKey() handled = false, want true")
	}
	if !created {
		t.Fatalf("expected CreateSession call")
	}
	if !setMode {
		t.Fatalf("expected SetSessionMode call")
	}
	if lastMode != "auto" {
		t.Fatalf("set mode = %q, want auto", lastMode)
	}
	if a.route != "chat" || a.chat == nil {
		t.Fatalf("expected chat route after opening session")
	}
	if got := a.home.SessionMode(); got != "auto" {
		t.Fatalf("home session mode = %q, want auto", got)
	}
}

func TestHandleHomeKey_NewSessionModeErrorIsSurfaced(t *testing.T) {
	handler := http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch {
		case r.Method == http.MethodPost && r.URL.Path == "/v1/sessions":
			_ = json.NewEncoder(w).Encode(map[string]any{
				"ok": true,
				"session": map[string]any{
					"id":             "session-test",
					"workspace_path": "/tmp/ws",
					"workspace_name": "ws",
					"title":          "New Session",
					"mode":           "plan",
				},
			})
		case r.Method == http.MethodPost && r.URL.Path == "/v1/sessions/session-test/mode":
			w.WriteHeader(http.StatusBadRequest)
			_ = json.NewEncoder(w).Encode(map[string]any{"ok": false, "error": "invalid mode"})
		default:
			w.WriteHeader(http.StatusNotFound)
			_ = json.NewEncoder(w).Encode(map[string]any{"ok": false, "error": "not found"})
		}
	})
	server := httptest.NewServer(handler)
	defer server.Close()

	a := &App{
		home:       ui.NewHomePage(model.EmptyHome()),
		route:      "home",
		config:     defaultAppConfig(),
		api:        client.New(server.URL),
		startupCWD: "/tmp/ws",
		activePath: "/tmp/ws",
		homeModel:  model.HomeModel{CWD: "/tmp/ws", ServerMode: "single", ActiveAgent: "swarm", ContextWindow: 200000},
	}
	a.home.SetSessionMode("auto")
	a.home.SetPrompt("new session")

	if handled := a.handleHomeKey(tcell.NewEventKey(tcell.KeyEnter, 0, tcell.ModNone)); !handled {
		t.Fatalf("handleHomeKey() handled = false, want true")
	}
	if got := a.home.Status(); !strings.Contains(got, "set session mode") {
		t.Fatalf("status = %q, want mode error surfaced", got)
	}
	if a.route != "home" {
		t.Fatalf("route = %q, want home after mode set failure", a.route)
	}
	if a.chat != nil {
		t.Fatalf("chat != nil, want nil after mode set failure")
	}
}

func TestApplySessionStreamEventSessionModeUpdatedIsSessionBound(t *testing.T) {
	a := &App{
		home: ui.NewHomePage(model.EmptyHome()),
		chat: ui.NewChatPage(ui.ChatPageOptions{
			SessionID:      "session-2",
			SessionTitle:   "Session Two",
			ShowHeader:     true,
			AuthConfigured: true,
			SessionMode:    "plan",
			Backend:        &chatBackendNoop{},
		}),
		route:  "chat",
		config: defaultAppConfig(),
		homeModel: model.HomeModel{
			RecentSessions: []model.SessionSummary{
				{ID: "session-1", Title: "Session One", Mode: "plan"},
				{ID: "session-2", Title: "Session Two", Mode: "plan"},
			},
		},
	}
	a.home.SetModel(a.homeModel)
	a.home.SetSessionMode("read")

	payload := []byte(`{"session_id":"session-2","mode":"auto"}`)
	changed := a.applySessionStreamEvent(client.StreamEventEnvelope{
		EventType: "session.mode.updated",
		Payload:   payload,
	})
	if !changed {
		t.Fatalf("applySessionStreamEvent() = false, want true")
	}
	if got := a.homeModel.RecentSessions[0].Mode; got != "plan" {
		t.Fatalf("session-1 mode = %q, want plan", got)
	}
	if got := a.homeModel.RecentSessions[1].Mode; got != "auto" {
		t.Fatalf("session-2 mode = %q, want auto", got)
	}
	if got := a.chat.SessionMode(); got != "auto" {
		t.Fatalf("chat session mode = %q, want auto", got)
	}
	if got := a.home.SessionMode(); got != "read" {
		t.Fatalf("home draft mode = %q, want read", got)
	}
}

func TestHandleGlobalKey_EscapeReturnsHomeWithoutOverwritingDraftMode(t *testing.T) {
	a := &App{
		home: ui.NewHomePage(model.EmptyHome()),
		chat: ui.NewChatPage(ui.ChatPageOptions{
			SessionID:      "session-1",
			SessionTitle:   "Session",
			ShowHeader:     true,
			AuthConfigured: true,
			SessionMode:    "readwrite",
			Backend:        &chatBackendNoop{},
		}),
		route:  "chat",
		config: defaultAppConfig(),
	}
	a.home.SetSessionMode("plan")

	if done := a.handleGlobalKey(tcell.NewEventKey(tcell.KeyEsc, 0, tcell.ModNone)); done {
		t.Fatalf("first Esc done = true, want false")
	}
	if done := a.handleGlobalKey(tcell.NewEventKey(tcell.KeyEsc, 0, tcell.ModNone)); done {
		t.Fatalf("second Esc done = true, want false")
	}
	if got := a.home.SessionMode(); got != "plan" {
		t.Fatalf("home draft mode after exiting chat = %q, want plan", got)
	}
}

type chatBackendNoop struct{}

func (chatBackendNoop) LoadMessages(context.Context, string, uint64, int) ([]ui.ChatMessageRecord, error) {
	return nil, nil
}
func (chatBackendNoop) GetSessionUsageSummary(context.Context, string) (*ui.ChatUsageSummary, error) {
	return nil, nil
}
func (chatBackendNoop) GetSessionMode(context.Context, string) (string, error) {
	return "readwrite", nil
}
func (chatBackendNoop) SetSessionMode(context.Context, string, string) (string, error) {
	return "readwrite", nil
}
func (chatBackendNoop) ListPermissions(context.Context, string, int) ([]ui.ChatPermissionRecord, error) {
	return nil, nil
}
func (chatBackendNoop) ListPendingPermissions(context.Context, string, int) ([]ui.ChatPermissionRecord, error) {
	return nil, nil
}
func (chatBackendNoop) ResolvePermission(context.Context, string, string, string, string) (ui.ChatPermissionRecord, error) {
	return ui.ChatPermissionRecord{}, nil
}

func (chatBackendNoop) ResolvePermissionWithArguments(context.Context, string, string, string, string, string) (ui.ChatPermissionRecord, error) {
	return ui.ChatPermissionRecord{}, nil
}
func (chatBackendNoop) ResolveAllPermissions(context.Context, string, string, string) ([]ui.ChatPermissionRecord, error) {
	return nil, nil
}
func (chatBackendNoop) RunTurn(context.Context, string, ui.ChatRunRequest) (ui.ChatRunResponse, error) {
	return ui.ChatRunResponse{}, nil
}
func (chatBackendNoop) RunTurnStream(context.Context, string, ui.ChatRunRequest, func(ui.ChatRunStreamEvent)) (ui.ChatRunResponse, error) {
	return ui.ChatRunResponse{}, nil
}

func TestHandleSessionsCommandHomeUsesScopedAPIResultsOnly(t *testing.T) {
	var requestedPath string
	var exactPath string
	handler := http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch {
		case r.Method == http.MethodGet && r.URL.Path == "/v1/sessions":
			requestedPath = strings.TrimSpace(r.URL.Query().Get("cwd"))
			exactPath = strings.TrimSpace(r.URL.Query().Get("exact_path"))
			_ = json.NewEncoder(w).Encode(map[string]any{
				"ok": true,
				"sessions": []map[string]any{{
					"id":             "scoped-1",
					"workspace_path": "/tmp/current",
					"workspace_name": "current",
					"title":          "Scoped Session",
					"mode":           "plan",
					"updated_at":     time.Now().UnixMilli(),
				}},
			})
		default:
			w.WriteHeader(http.StatusNotFound)
			_ = json.NewEncoder(w).Encode(map[string]any{"ok": false, "error": "not found"})
		}
	})
	server := httptest.NewServer(handler)
	defer server.Close()

	a := &App{
		home:       ui.NewHomePage(model.EmptyHome()),
		route:      "home",
		config:     defaultAppConfig(),
		api:        client.New(server.URL),
		startupCWD: "/tmp/current",
		activePath: "/tmp/current",
		homeModel: model.HomeModel{
			CWD: "/tmp/current",
			RecentSessions: []model.SessionSummary{{
				ID:            "stale-1",
				WorkspacePath: "/tmp/other",
				WorkspaceName: "other",
				Title:         "Stale Session",
				Mode:          "auto",
			}},
		},
	}

	a.handleSessionsCommand(nil)
	if requestedPath != "/tmp/current" {
		t.Fatalf("cwd = %q, want /tmp/current", requestedPath)
	}
	if exactPath != "" {
		t.Fatalf("exact_path = %q, want empty for palette fetch", exactPath)
	}
	if !a.home.SessionsModalVisible() {
		t.Fatalf("SessionsModalVisible() = false, want true")
	}
	item, ok := a.home.SelectedSessionsModalItem()
	if !ok {
		t.Fatalf("SelectedSessionsModalItem() ok = false, want true")
	}
	if item.ID != "scoped-1" {
		t.Fatalf("selected session id = %q, want scoped-1", item.ID)
	}
	items := a.home.SessionsModalItems()
	if len(items) != 1 {
		t.Fatalf("sessions modal items = %+v, want exactly one scoped item", items)
	}
	if items[0].ID != "scoped-1" {
		t.Fatalf("sessions modal first item = %q, want scoped-1", items[0].ID)
	}
}

func TestHandleSessionsCommandChatUsesCurrentSessionPath(t *testing.T) {
	var requestedPath string
	handler := http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch {
		case r.Method == http.MethodGet && r.URL.Path == "/v1/sessions":
			requestedPath = strings.TrimSpace(r.URL.Query().Get("cwd"))
			_ = json.NewEncoder(w).Encode(map[string]any{
				"ok": true,
				"sessions": []map[string]any{{
					"id":             "chat-session",
					"workspace_path": "/tmp/chat-scope",
					"workspace_name": "chat-scope",
					"title":          "Chat Session",
					"mode":           "plan",
					"updated_at":     time.Now().UnixMilli(),
				}},
			})
		default:
			w.WriteHeader(http.StatusNotFound)
			_ = json.NewEncoder(w).Encode(map[string]any{"ok": false, "error": "not found"})
		}
	})
	server := httptest.NewServer(handler)
	defer server.Close()

	a := &App{
		home:          ui.NewHomePage(model.EmptyHome()),
		route:         "chat",
		config:        defaultAppConfig(),
		api:           client.New(server.URL),
		startupCWD:    "/tmp/start",
		activePath:    "/tmp/start",
		workspacePath: "/tmp/start",
		homeModel: model.HomeModel{
			CWD: "/tmp/start",
			RecentSessions: []model.SessionSummary{{
				ID:            "stale-chat",
				WorkspacePath: "/tmp/unrelated",
				WorkspaceName: "unrelated",
				Title:         "Unrelated Session",
				Mode:          "auto",
			}},
		},
		chat: ui.NewChatPage(ui.ChatPageOptions{
			SessionID:      "session-1",
			SessionTitle:   "Session One",
			ShowHeader:     true,
			AuthConfigured: true,
			SessionMode:    "plan",
			Backend:        &chatBackendNoop{},
		}),
	}

	if err := a.openChatView("session-1", "Session One", "/tmp/chat-scope", "chat-scope", "plan", "", false, "", ""); err != nil {
		t.Fatalf("openChatView() error = %v", err)
	}
	if got := a.activePath; got != "/tmp/chat-scope" {
		t.Fatalf("activePath = %q, want /tmp/chat-scope", got)
	}
	if got := a.workspacePath; got != "/tmp/start" {
		t.Fatalf("workspacePath = %q, want /tmp/start", got)
	}
	if err := a.openChatSessionsPalette(""); err != nil {
		t.Fatalf("openChatSessionsPalette() error = %v", err)
	}
	if requestedPath != "/tmp/chat-scope" {
		t.Fatalf("cwd = %q, want /tmp/chat-scope", requestedPath)
	}
	items := a.chat.SessionPaletteItems()
	if len(items) != 1 {
		t.Fatalf("session palette items = %+v, want exactly one scoped item", items)
	}
	if items[0].ID != "chat-session" {
		t.Fatalf("session palette first item = %q, want chat-session", items[0].ID)
	}
}

func TestHealthStatus_BypassPermissionsJSON(t *testing.T) {
	status := HealthStatus{OK: true, Mode: "single", BypassPermissions: true}
	body, err := json.Marshal(status)
	if err != nil {
		t.Fatalf("marshal health status: %v", err)
	}
	if !strings.Contains(string(body), "bypass_permissions") {
		t.Fatalf("expected bypass_permissions in payload: %s", string(body))
	}
}
