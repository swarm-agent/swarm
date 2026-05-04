package app

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"swarm-refactor/swarmtui/internal/client"
	"swarm-refactor/swarmtui/internal/model"
	"swarm-refactor/swarmtui/internal/ui"
)

func newCommandTestApp() *App {
	a := &App{
		home:   ui.NewHomePage(model.EmptyHome()),
		route:  "home",
		config: defaultAppConfig(),
	}
	a.home.SetCommandSuggestions(homeCommandSuggestions)
	return a
}

func TestExecuteCommand_HelpPopulatesOverlay(t *testing.T) {
	a := newCommandTestApp()
	a.executeCommand("/help")

	lines := a.home.CommandOverlayLines()
	if len(lines) == 0 {
		t.Fatalf("overlay lines empty after /help")
	}
	if got := a.home.Status(); got != "command palette loaded" {
		t.Fatalf("status = %q, want %q", got, "command palette loaded")
	}
}

func TestExecuteCommand_UnknownCommandSetsStatus(t *testing.T) {
	a := newCommandTestApp()
	a.executeCommand("/definitely-not-a-command")

	if got := a.home.Status(); got != "unknown command: /definitely-not-a-command" {
		t.Fatalf("status = %q", got)
	}
}

func TestExecuteCommand_ThinkingTypoSuggestsCanonicalCommand(t *testing.T) {
	a := newCommandTestApp()
	a.executeCommand("/thinkingn")

	if got := a.home.Status(); got != "unknown command: /thinkingn (did you mean /thinking?)" {
		t.Fatalf("status = %q", got)
	}
}

func TestExecuteCommand_ThinkingOffPersistsToBackend(t *testing.T) {
	var saved client.UISettings
	handler := http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch r.URL.Path {
		case "/v1/ui/settings":
			if r.Method != http.MethodPost {
				w.WriteHeader(http.StatusMethodNotAllowed)
				return
			}
			if err := json.NewDecoder(r.Body).Decode(&saved); err != nil {
				w.WriteHeader(http.StatusBadRequest)
				_ = json.NewEncoder(w).Encode(map[string]any{"error": err.Error()})
				return
			}
			_ = json.NewEncoder(w).Encode(saved)
		default:
			w.WriteHeader(http.StatusNotFound)
		}
	})
	server := httptest.NewServer(handler)
	defer server.Close()

	a := newCommandTestApp()
	a.api = client.New(server.URL)

	a.executeCommand("/thinking off")

	if got := a.home.Status(); got != "thinking tags off" {
		t.Fatalf("status = %q", got)
	}
	if a.config.Chat.ThinkingTags {
		t.Fatalf("config.Chat.ThinkingTags = true, want false")
	}
	if saved.Chat.ThinkingTags {
		t.Fatalf("saved.Chat.ThinkingTags = true, want false")
	}
}

func TestExecuteCommand_AgentsDefaultRestoresDefaults(t *testing.T) {
	var restoreCalls int
	var resetCalls int
	handler := http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch r.URL.Path {
		case "/v2/agents/defaults/restore":
			if r.Method != http.MethodPost {
				w.WriteHeader(http.StatusMethodNotAllowed)
				return
			}
			restoreCalls++
			_ = json.NewEncoder(w).Encode(map[string]any{
				"ok":             true,
				"profiles":       []map[string]any{{"name": "swarm", "mode": "primary", "enabled": true}},
				"active_primary": "swarm",
				"version":        7,
			})
		case "/v2/agents/defaults/reset":
			if r.Method != http.MethodPost {
				w.WriteHeader(http.StatusMethodNotAllowed)
				return
			}
			resetCalls++
			_ = json.NewEncoder(w).Encode(map[string]any{
				"ok":             true,
				"profiles":       []map[string]any{{"name": "swarm", "mode": "primary", "enabled": true}},
				"active_primary": "swarm",
				"version":        8,
			})
		default:
			w.WriteHeader(http.StatusNotFound)
		}
	})
	server := httptest.NewServer(handler)
	defer server.Close()

	a := newCommandTestApp()
	a.api = client.New(server.URL)

	a.executeCommand("/agents default")

	if restoreCalls != 1 {
		t.Fatalf("restoreCalls = %d, want 1", restoreCalls)
	}
	if got := a.home.Status(); got != "restored default agents: 1 profiles" {
		t.Fatalf("status = %q", got)
	}

	a.executeCommand("/agents reset")

	if resetCalls != 1 {
		t.Fatalf("resetCalls = %d, want 1", resetCalls)
	}
	if got := a.home.Status(); got != "reset agents to built-in defaults: 1 profiles" {
		t.Fatalf("status after reset = %q", got)
	}

	a.executeCommand("/agents nope")

	if got := a.home.Status(); got != "usage: /agents [open|restore|reset|use|prompt|delete]" {
		t.Fatalf("status after invalid agents subcommand = %q", got)
	}
}

func TestExecuteCommand_WorkspaceWithoutArgsOpensModal(t *testing.T) {
	a := newCommandTestApp()
	a.api = client.New("http://127.0.0.1:7782")
	a.executeCommand("/workspace")

	if !a.home.WorkspaceModalVisible() {
		t.Fatalf("WorkspaceModalVisible() = false, want true")
	}
}

func TestExecuteCommand_WorktreesAliasOpensModal(t *testing.T) {
	a := newCommandTestApp()
	a.api = client.New("http://127.0.0.1:7782")
	a.executeCommand("/wt")

	if !a.home.WorktreesModalVisible() {
		t.Fatalf("WorktreesModalVisible() = false, want true")
	}
}

func TestExecuteCommand_MouseUsageOnInvalidArg(t *testing.T) {
	a := newCommandTestApp()
	a.executeCommand("/mouse nope")

	if got := a.home.Status(); got != "usage: /mouse [on|off|toggle|status]" {
		t.Fatalf("status = %q", got)
	}
}

func TestHandlePlanCommandWithNoArgsOpensCurrentPlanModal(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch r.URL.Path {
		case "/v1/sessions/session-1/plans/active":
			_ = json.NewEncoder(w).Encode(map[string]any{
				"ok":         true,
				"session_id": "session-1",
				"has_active": true,
				"active_plan": map[string]any{
					"id":             "plan_123",
					"title":          "Release Plan",
					"plan":           "# Plan\n\n- [ ] ship",
					"status":         "draft",
					"approval_state": "approved",
				},
			})
		default:
			w.WriteHeader(http.StatusNotFound)
		}
	}))
	defer server.Close()

	a := newCommandTestApp()
	a.api = client.New(server.URL)
	a.route = "chat"
	a.chat = ui.NewChatPage(ui.ChatPageOptions{SessionID: "session-1", AuthConfigured: true, SessionMode: "plan"})

	a.executeCommand("/plan")

	if got := a.home.Status(); got != "current plan: Release Plan" {
		t.Fatalf("status = %q", got)
	}
	if consumed := a.chat.HandleEscape(); !consumed {
		t.Fatalf("HandleEscape() consumed = false, want true after opening current plan modal")
	}
}

func TestExecuteCommand_ThemeStatusSetsOverlay(t *testing.T) {
	a := newCommandTestApp()
	a.executeCommand("/themes status")

	lines := a.home.CommandOverlayLines()
	if len(lines) == 0 {
		t.Fatalf("overlay lines empty after /themes status")
	}
	if !strings.HasPrefix(a.home.Status(), "theme ") {
		t.Fatalf("status = %q, want prefix %q", a.home.Status(), "theme ")
	}
}

func TestExecuteCommand_ThemesSetPersistsWorkspaceTheme(t *testing.T) {
	handler := http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodPost || r.URL.Path != "/v1/workspace/theme" {
			w.WriteHeader(http.StatusNotFound)
			_ = json.NewEncoder(w).Encode(map[string]any{"ok": false, "error": "not found"})
			return
		}
		var req struct {
			Path    string `json:"path"`
			ThemeID string `json:"theme_id"`
		}
		if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
			t.Fatalf("decode workspace theme request: %v", err)
		}
		if req.Path != "/tmp/ws" {
			t.Fatalf("workspace theme path = %q, want /tmp/ws", req.Path)
		}
		if req.ThemeID != "crimson" {
			t.Fatalf("workspace theme id = %q, want crimson", req.ThemeID)
		}
		_ = json.NewEncoder(w).Encode(map[string]any{
			"ok": true,
			"workspace": map[string]any{
				"resolved_path":  "/tmp/ws",
				"workspace_path": "/tmp/ws",
				"workspace_name": "ws",
				"theme_id":       "crimson",
			},
		})
	})
	server := httptest.NewServer(handler)
	defer server.Close()

	a := newCommandTestApp()
	a.api = client.New(server.URL)
	a.activePath = "/tmp/ws"
	a.workspacePath = "/tmp/ws"
	a.homeModel.Workspaces = []model.Workspace{{Name: "ws", Path: "/tmp/ws", Active: true}}

	a.executeCommand("/themes set crimson")

	if got := a.homeModel.Workspaces[0].ThemeID; got != "crimson" {
		t.Fatalf("workspace theme = %q, want crimson", got)
	}
	if got := a.config.UI.Theme; got != defaultThemeID {
		t.Fatalf("config.UI.Theme = %q, want %q", got, defaultThemeID)
	}
	if got := a.home.Status(); got != "workspace theme set: crimson" {
		t.Fatalf("status = %q, want %q", got, "workspace theme set: crimson")
	}
}

func TestHandleThemeModalActionPreviewApplyCancelWithWorkspaceTheme(t *testing.T) {
	handler := http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodPost || r.URL.Path != "/v1/workspace/theme" {
			w.WriteHeader(http.StatusNotFound)
			_ = json.NewEncoder(w).Encode(map[string]any{"ok": false, "error": "not found"})
			return
		}
		var req struct {
			Path    string `json:"path"`
			ThemeID string `json:"theme_id"`
		}
		if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
			t.Fatalf("decode workspace theme request: %v", err)
		}
		if req.ThemeID != "crimson" {
			t.Fatalf("workspace theme id = %q, want crimson", req.ThemeID)
		}
		_ = json.NewEncoder(w).Encode(map[string]any{
			"ok": true,
			"workspace": map[string]any{
				"resolved_path":  "/tmp/ws",
				"workspace_path": "/tmp/ws",
				"workspace_name": "ws",
				"theme_id":       "crimson",
			},
		})
	})
	server := httptest.NewServer(handler)
	defer server.Close()

	a := newCommandTestApp()
	a.api = client.New(server.URL)
	a.activePath = "/tmp/ws"
	a.workspacePath = "/tmp/ws"
	a.homeModel.Workspaces = []model.Workspace{{Name: "ws", Path: "/tmp/ws", ThemeID: "nord", Active: true}}

	a.handleThemeModalAction(ui.ThemeModalAction{Kind: ui.ThemeModalActionPreview, ThemeID: "crimson"})
	if got := a.themePreviewID; got != "crimson" {
		t.Fatalf("themePreviewID = %q, want crimson", got)
	}
	if got := a.home.Status(); got != "" {
		t.Fatalf("status after preview = %q, want empty", got)
	}

	a.handleThemeModalAction(ui.ThemeModalAction{Kind: ui.ThemeModalActionCancel, ThemeID: "nord"})
	if got := a.themePreviewID; got != "" {
		t.Fatalf("themePreviewID after cancel = %q, want empty", got)
	}
	if got := a.home.Status(); got != "theme unchanged: nord" {
		t.Fatalf("status after cancel = %q, want %q", got, "theme unchanged: nord")
	}

	a.handleThemeModalAction(ui.ThemeModalAction{Kind: ui.ThemeModalActionPreview, ThemeID: "crimson"})
	a.handleThemeModalAction(ui.ThemeModalAction{Kind: ui.ThemeModalActionApply, ThemeID: "crimson"})
	if got := a.themePreviewID; got != "" {
		t.Fatalf("themePreviewID after apply = %q, want empty", got)
	}
	if got := a.homeModel.Workspaces[0].ThemeID; got != "crimson" {
		t.Fatalf("workspace theme after apply = %q, want crimson", got)
	}
	if got := a.home.Status(); got != "workspace theme set: crimson" {
		t.Fatalf("status after apply = %q, want %q", got, "workspace theme set: crimson")
	}
}

func TestExecuteCommand_HomeFromChatRoutesHome(t *testing.T) {
	a := newCommandTestApp()
	a.chat = ui.NewChatPage(ui.ChatPageOptions{
		SessionID:      "session-1",
		SessionTitle:   "Session",
		ShowHeader:     true,
		AuthConfigured: true,
	})
	a.route = "chat"

	a.executeCommand("/home")

	if a.route != "home" {
		t.Fatalf("route = %q, want home", a.route)
	}
	if a.chat != nil {
		t.Fatalf("chat != nil, want nil")
	}
	if got := a.home.Status(); got != "home" {
		t.Fatalf("status = %q, want %q", got, "home")
	}
}

func TestExecuteCommand_ThemesOpenFromChatKeepsChatRoute(t *testing.T) {
	a := newCommandTestApp()
	a.chat = ui.NewChatPage(ui.ChatPageOptions{
		SessionID:      "session-1",
		SessionTitle:   "Session",
		ShowHeader:     true,
		AuthConfigured: true,
	})
	a.route = "chat"

	a.executeCommand("/themes")

	if a.route != "chat" {
		t.Fatalf("route = %q, want chat", a.route)
	}
	if a.chat == nil {
		t.Fatalf("chat == nil, want active chat")
	}
	if !a.home.ThemeModalVisible() {
		t.Fatalf("ThemeModalVisible() = false, want true")
	}
	if got := a.home.Status(); got != "theme modal" {
		t.Fatalf("status = %q, want %q", got, "theme modal")
	}
}

func TestExecuteCommand_ChatModalCommandsKeepChatRoute(t *testing.T) {
	cases := []struct {
		name    string
		command string
		visible func(*ui.HomePage) bool
	}{
		{name: "auth", command: "/auth", visible: func(home *ui.HomePage) bool { return home.AuthModalVisible() }},
		{name: "workspace", command: "/workspace", visible: func(home *ui.HomePage) bool { return home.WorkspaceModalVisible() }},
		{name: "add-dir", command: "/add-dir", visible: func(home *ui.HomePage) bool { return home.WorkspaceModalVisible() }},
		{name: "worktrees", command: "/worktrees", visible: func(home *ui.HomePage) bool { return home.WorktreesModalVisible() }},
		{name: "wt", command: "/wt", visible: func(home *ui.HomePage) bool { return home.WorktreesModalVisible() }},
		{name: "agents", command: "/agents", visible: func(home *ui.HomePage) bool { return home.AgentsModalVisible() }},
		{name: "voice", command: "/voice", visible: func(home *ui.HomePage) bool { return home.VoiceModalVisible() }},
		{name: "keybinds", command: "/keybinds", visible: func(home *ui.HomePage) bool { return home.KeybindsModalVisible() }},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			a := newCommandTestApp()
			a.api = client.New("http://127.0.0.1:7782")
			a.chat = ui.NewChatPage(ui.ChatPageOptions{
				SessionID:      "session-1",
				SessionTitle:   "Session",
				ShowHeader:     true,
				AuthConfigured: true,
			})
			a.route = "chat"

			a.executeCommand(tc.command)

			if a.route != "chat" {
				t.Fatalf("route = %q, want chat", a.route)
			}
			if a.chat == nil {
				t.Fatalf("chat == nil, want active chat")
			}
			if !tc.visible(a.home) {
				t.Fatalf("expected modal visible after %s", tc.command)
			}
		})
	}
}

func TestExecuteCommand_MCPDeferredFromChatKeepsChatRoute(t *testing.T) {
	a := newCommandTestApp()
	a.api = client.New("http://127.0.0.1:7782")
	a.chat = ui.NewChatPage(ui.ChatPageOptions{
		SessionID:      "session-1",
		SessionTitle:   "Session",
		ShowHeader:     true,
		AuthConfigured: true,
	})
	a.route = "chat"

	a.executeCommand("/mcp")

	if a.route != "chat" {
		t.Fatalf("route = %q, want chat", a.route)
	}
	if a.home.MCPModalVisible() {
		t.Fatalf("MCPModalVisible() = true, want deferred status only")
	}
	got := a.home.Status()
	if !strings.Contains(got, "MCP management is deferred") || !strings.Contains(got, "free Exa MCP") {
		t.Fatalf("status = %q, want deferred free Exa MCP message", got)
	}
}

func TestExecuteCommand_AgentsOpenFromChatKeepsChatRoute(t *testing.T) {
	a := newCommandTestApp()
	a.chat = ui.NewChatPage(ui.ChatPageOptions{
		SessionID:      "session-1",
		SessionTitle:   "Session",
		ShowHeader:     true,
		AuthConfigured: true,
	})
	a.route = "chat"

	a.executeCommand("/agents")

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

func TestExecuteCommand_HomeOutsideChatWarns(t *testing.T) {
	a := newCommandTestApp()

	a.executeCommand("/home")

	if got := a.home.Status(); got != "/home is available in chat only" {
		t.Fatalf("status = %q, want %q", got, "/home is available in chat only")
	}
}

func TestExecuteCommand_QuitRequestsQuit(t *testing.T) {
	a := newCommandTestApp()
	a.executeCommand("/quit")

	if !a.quitRequested {
		t.Fatalf("quitRequested = false, want true")
	}
	if got := a.home.Status(); got != "exiting swarmtui" {
		t.Fatalf("status = %q, want %q", got, "exiting swarmtui")
	}
}

func TestExecuteCommand_ExitAliasRequestsQuit(t *testing.T) {
	a := newCommandTestApp()
	a.executeCommand("/exit")

	if !a.quitRequested {
		t.Fatalf("quitRequested = false, want true")
	}
	if got := a.home.Status(); got != "exiting swarmtui" {
		t.Fatalf("status = %q, want %q", got, "exiting swarmtui")
	}
}

func TestApplyUpdateRequestsReleaseHandoff(t *testing.T) {
	a := newCommandTestApp()
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch r.URL.Path {
		case "/v1/update/status":
			_ = json.NewEncoder(w).Encode(client.UpdateStatus{LatestVersion: "v1.2.3", UpdateAvailable: true})
		case "/v1/deploy/remote/session":
			_ = json.NewEncoder(w).Encode(map[string]any{"ok": true, "sessions": []client.RemoteDeploySession{}})
		case "/v1/update/local-containers":
			_ = json.NewEncoder(w).Encode(client.LocalContainerUpdatePlan{Summary: client.LocalContainerUpdateSummary{Total: 0}})
		default:
			w.WriteHeader(http.StatusNotFound)
		}
	}))
	defer server.Close()
	a.api = client.New(server.URL)

	a.applyUpdate()

	if !a.quitRequested {
		t.Fatalf("quitRequested = false, want true")
	}
	if !a.releaseUpdateRequested {
		t.Fatalf("releaseUpdateRequested = false, want true")
	}
}

func TestConsumeReloadResultPreservesDirectoryModeCWD(t *testing.T) {
	var (
		worktreeCWD string
		contextCWD  string
		sessionsCWD string
	)
	handler := http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch {
		case r.Method == http.MethodGet && r.URL.Path == "/healthz":
			_ = json.NewEncoder(w).Encode(map[string]any{"status": "ok", "mode": "single"})
		case r.Method == http.MethodGet && r.URL.Path == "/v1/worktrees":
			worktreeCWD = strings.TrimSpace(r.URL.Query().Get("workspace_path"))
			_ = json.NewEncoder(w).Encode(map[string]any{"ok": true, "worktrees": map[string]any{"enabled": false}})
		case r.Method == http.MethodGet && r.URL.Path == "/v1/workspace/overview":
			_ = json.NewEncoder(w).Encode(map[string]any{
				"ok": true,
				"current_workspace": map[string]any{
					"workspace_path": "/tmp/start",
					"resolved_path":  "/tmp/start",
					"workspace_name": "start",
				},
				"workspaces": []map[string]any{{
					"path":           "/tmp/start",
					"workspace_name": "start",
					"active":         true,
				}},
				"directories": []map[string]any{{
					"name": "random",
					"path": "/tmp/random",
				}},
			})
		case r.Method == http.MethodGet && r.URL.Path == "/v1/providers":
			_ = json.NewEncoder(w).Encode(map[string]any{"providers": []map[string]any{{"id": "openai", "runnable": true}}})
		case r.Method == http.MethodGet && r.URL.Path == "/v1/model":
			_ = json.NewEncoder(w).Encode(map[string]any{"provider": "openai", "model": "gpt-5", "thinking": "medium"})
		case r.Method == http.MethodGet && r.URL.Path == "/v2/agents":
			_ = json.NewEncoder(w).Encode(map[string]any{"ok": true, "state": map[string]any{"profiles": []map[string]any{{"name": "swarm", "mode": "primary", "enabled": true}}, "active_primary": "swarm"}})
		case r.Method == http.MethodGet && r.URL.Path == "/v1/context/sources":
			contextCWD = strings.TrimSpace(r.URL.Query().Get("cwd"))
			_ = json.NewEncoder(w).Encode(map[string]any{"ok": true, "report": map[string]any{"rules": []any{}, "skills": []any{}}})
		case r.Method == http.MethodGet && r.URL.Path == "/v1/sessions":
			sessionsCWD = strings.TrimSpace(r.URL.Query().Get("cwd"))
			_ = json.NewEncoder(w).Encode(map[string]any{"sessions": []any{}})
		default:
			w.WriteHeader(http.StatusNotFound)
			_ = json.NewEncoder(w).Encode(map[string]any{"ok": false, "error": "not found"})
		}
	})
	server := httptest.NewServer(handler)
	defer server.Close()

	a := &App{
		home:   ui.NewHomePage(model.EmptyHome()),
		route:  "home",
		config: defaultAppConfig(),
		api:    client.New(server.URL),
		homeModel: model.HomeModel{
			CWD:         "/tmp/random",
			Workspaces:  []model.Workspace{{Name: "start", Path: "/tmp/start", Active: false}},
			Directories: []model.DirectoryItem{{Name: "random", Path: "/tmp/random", ResolvedPath: "/tmp/random", IsWorkspace: false}},
		},
		startupCWD:    "/tmp/start",
		activePath:    "/tmp/random",
		workspacePath: "/tmp/start",
		reloadCh:      make(chan homeReloadResult, 1),
	}
	a.home.SetStatus("keep me")

	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
	defer cancel()
	next, err := a.refreshHomeModel(ctx)
	if err != nil {
		t.Fatalf("refreshHomeModel() error = %v", err)
	}
	a.reloadCh <- homeReloadResult{model: next, silent: true}

	a.consumeReloadResult()

	if got := a.homeModel.CWD; got != "/tmp/random" {
		t.Fatalf("homeModel.CWD = %q, want /tmp/random", got)
	}
	if got := a.activePath; got != "/tmp/random" {
		t.Fatalf("activePath = %q, want /tmp/random", got)
	}
	if got := a.workspacePath; got != "" {
		t.Fatalf("workspacePath = %q, want empty in directory mode", got)
	}
	if got := a.home.ActiveWorkspaceName(); got != "" {
		t.Fatalf("active workspace name = %q, want empty", got)
	}
	if got := a.home.ActiveDirectory(); got.ResolvedPath != "/tmp/random" {
		t.Fatalf("active directory = %q, want /tmp/random", got.ResolvedPath)
	}
	if worktreeCWD != "/tmp/random" {
		t.Fatalf("worktree workspace_path = %q, want /tmp/random", worktreeCWD)
	}
	if contextCWD != "/tmp/random" {
		t.Fatalf("context cwd = %q, want /tmp/random", contextCWD)
	}
	if sessionsCWD != "/tmp/random" {
		t.Fatalf("sessions cwd = %q, want /tmp/random", sessionsCWD)
	}
	if got := a.home.Status(); got != "keep me" {
		t.Fatalf("status = %q, want keep me", got)
	}
}

func TestSyncKnownWorkspaceSelectionForPathKeepsDirectoryModeWhenCWDIsNotWorkspaceRoot(t *testing.T) {
	a := &App{
		home:   ui.NewHomePage(model.EmptyHome()),
		route:  "home",
		config: defaultAppConfig(),
		homeModel: model.HomeModel{
			CWD:        "/tmp/random",
			Workspaces: []model.Workspace{{Name: "start", Path: "/tmp/start", Active: true}},
			Directories: []model.DirectoryItem{
				{Name: "random", Path: "/tmp/random", ResolvedPath: "/tmp/random", IsWorkspace: false},
				{Name: "start", Path: "/tmp/start", ResolvedPath: "/tmp/start", IsWorkspace: true},
			},
		},
		startupCWD:    "/tmp/start",
		activePath:    "/tmp/random",
		workspacePath: "/tmp/start",
	}

	a.syncKnownWorkspaceSelectionForPath("/tmp/random")

	if got := a.activePath; got != "/tmp/random" {
		t.Fatalf("activePath = %q, want /tmp/random", got)
	}
	if got := a.homeModel.CWD; got != "/tmp/random" {
		t.Fatalf("homeModel.CWD = %q, want /tmp/random", got)
	}
	if got := a.workspacePath; got != "" {
		t.Fatalf("workspacePath = %q, want empty", got)
	}
	if got := a.home.ActiveWorkspaceName(); got != "" {
		t.Fatalf("active workspace name = %q, want empty", got)
	}
	if got := a.home.ActiveDirectory(); got.ResolvedPath != "/tmp/random" {
		t.Fatalf("active directory = %q, want /tmp/random", got.ResolvedPath)
	}
	if !a.homeModel.Directories[1].IsWorkspace {
		t.Fatalf("saved workspace directory should remain marked as workspace")
	}
}

func TestCommitExecutionContextUsesCurrentWorktreePath(t *testing.T) {
	a := &App{}
	summary := model.SessionSummary{
		WorkspacePath:      "/cache/swarmd/workspaces/repo-abc123/worktrees/ws_123",
		WorktreeEnabled:    true,
		WorktreeRootPath:   "/repo",
		WorktreeBranch:     "agent/123",
		WorktreeBaseBranch: "dev",
	}

	ctx := a.commitExecutionContext(summary)
	if ctx == nil {
		t.Fatalf("commitExecutionContext() = nil")
	}
	if got := strings.TrimSpace(ctx.WorkspacePath); got != "/cache/swarmd/workspaces/repo-abc123/worktrees/ws_123" {
		t.Fatalf("WorkspacePath = %q, want worktree path", got)
	}
	if got := strings.TrimSpace(ctx.CWD); got != "/cache/swarmd/workspaces/repo-abc123/worktrees/ws_123" {
		t.Fatalf("CWD = %q, want worktree path", got)
	}
	if got := strings.TrimSpace(ctx.WorktreeRootPath); got != "/repo" {
		t.Fatalf("WorktreeRootPath = %q, want /repo", got)
	}
	if got := strings.TrimSpace(ctx.WorktreeMode); got != "off" {
		t.Fatalf("WorktreeMode = %q, want off so commit stays in current worktree", got)
	}
}
