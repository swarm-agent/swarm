package app

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/gdamore/tcell/v2"

	"swarm-refactor/swarmtui/internal/client"
	"swarm-refactor/swarmtui/internal/model"
	"swarm-refactor/swarmtui/internal/ui"
)

func TestOrchestratorTUIFocusMode(t *testing.T) {
	t.Setenv("SWARMD_LOCAL_TRANSPORT_SOCKET", "")

	var openedSessionID string

	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		switch {
		case r.Method == http.MethodGet && r.URL.Path == "/v1/onboarding":
			_ = json.NewEncoder(w).Encode(client.OnboardingStatus{
				Identity: client.OnboardingIdentity{Bootstrapped: true, Username: "alice"},
				Config:   client.OnboardingConfig{SwarmName: "AliceSwarm"},
			})
		case r.Method == http.MethodGet && r.URL.Path == "/v1/vault":
			_ = json.NewEncoder(w).Encode(client.VaultStatus{Enabled: false})
		case r.Method == http.MethodGet && r.URL.Path == "/v3/projects":
			_ = json.NewEncoder(w).Encode(map[string]any{
				"projects": []client.ProjectRecord{
					{
						ID:               "proj-alpha",
						Name:             "Project Alpha",
						PrimarySessionID: "orch-sess-1",
						Workspaces:       []client.ProjectWorkspaceRef{{Path: "/work/alpha"}},
					},
					{
						ID:               "proj-beta",
						Name:             "Project Beta",
						PrimarySessionID: "orch-sess-2",
					},
				},
			})
		case r.Method == http.MethodGet && r.URL.Path == "/v3/projects/proj-alpha/tasks":
			_ = json.NewEncoder(w).Encode(map[string]any{
				"tasks": []client.ProjectTaskRecord{
					{ID: "task-1", ProjectID: "proj-alpha", Title: "Deploy backend", Status: "in_progress", Agent: "coder", SessionID: "sess-task-1"},
					{ID: "task-2", ProjectID: "proj-alpha", Title: "UI polish", Status: "queued", Agent: "designer"},
				},
				"count": 2,
			})
		case r.Method == http.MethodGet && r.URL.Path == "/v3/projects/proj-beta/tasks":
			_ = json.NewEncoder(w).Encode(map[string]any{
				"tasks": []client.ProjectTaskRecord{
					{ID: "task-beta-1", ProjectID: "proj-beta", Title: "Beta rollout", Status: "completed", Agent: "swarm"},
				},
				"count": 1,
			})
		case r.Method == http.MethodGet && r.URL.Path == "/v3/workspaces":
			_ = json.NewEncoder(w).Encode(map[string]any{
				"workspaces": []client.WorkspaceEntry{
					{WorkspaceName: "Alpha Workspace", Path: "/work/alpha", Active: true},
				},
			})
		case r.Method == http.MethodGet && r.URL.Path == "/v1/health":
			_ = json.NewEncoder(w).Encode(client.HealthStatus{Mode: "local"})
		case r.Method == http.MethodGet && r.URL.Path == "/v1/providers":
			_ = json.NewEncoder(w).Encode(map[string]any{"providers": []client.ProviderStatus{{ID: "codex", Runnable: true}}})
		case r.Method == http.MethodGet && r.URL.Path == "/v1/model":
			_ = json.NewEncoder(w).Encode(client.ModelResolved{Preference: client.ModelPreference{Provider: "codex", Model: "gpt-5.4"}})
		case r.Method == http.MethodGet && r.URL.Path == "/v1/model/profiles":
			_ = json.NewEncoder(w).Encode(map[string]any{"profiles": []client.ModelProfile{}})
		case r.Method == http.MethodGet && r.URL.Path == "/v3/agents":
			_ = json.NewEncoder(w).Encode(map[string]any{"agents": []any{}})
		case r.Method == http.MethodGet && r.URL.Path == "/v3/settings/agent-models":
			_ = json.NewEncoder(w).Encode(client.AgentModelSettings{})
		case r.Method == http.MethodGet && r.URL.Path == "/v1/update":
			_ = json.NewEncoder(w).Encode(client.UpdateStatus{CurrentVersion: "dev"})
		case r.Method == http.MethodGet && r.URL.Path == "/v3/tui/sessions/orch-sess-1":
			openedSessionID = "orch-sess-1"
			_ = json.NewEncoder(w).Encode(client.SessionV3Hydrated{
				Session: client.SessionSummary{ID: "orch-sess-1", Title: "Executive Orchestrator", SessionAPI: "v3"},
			})
		case r.Method == http.MethodGet && r.URL.Path == "/v3/tui/sessions/sess-task-1":
			openedSessionID = "sess-task-1"
			_ = json.NewEncoder(w).Encode(client.SessionV3Hydrated{
				Session: client.SessionSummary{ID: "sess-task-1", Title: "Deploy backend", SessionAPI: "v3"},
			})
		default:
			_ = json.NewEncoder(w).Encode(map[string]any{"ok": true})
		}
	}))
	defer server.Close()

	api := client.New(server.URL)
	api.SetToken("test-token")

	home := ui.NewHomePage(model.EmptyHome())
	app := &App{
		api:                 api,
		startupCWD:          "/work/alpha",
		workspacePath:       "/work/alpha",
		home:                home,
		homeModel:           model.EmptyHome(),
		keybinds:            ui.NewDefaultKeyBindings(),
		streamEvents:        make(chan client.StreamEventEnvelope, 1),
		tuiRealtimeFrames:   make(chan client.V3RealtimeFrame, 256),
		tuiRealtimeStatuses: make(chan tuiRealtimeStatus, 32),
		tuiRealtimeClientID: "tui:test",
	}
	ctx := context.Background()

	// 1. Initial Load: Home model should contain project and tasks
	m, err := app.refreshHomeV3Model(ctx)
	if err != nil {
		t.Fatalf("refreshHomeV3Model error: %v", err)
	}
	if m.ActiveProjectID != "proj-alpha" {
		t.Fatalf("expected active project 'proj-alpha', got %q", m.ActiveProjectID)
	}
	if m.ActiveProjectPrimarySessionID != "orch-sess-1" {
		t.Fatalf("expected primary session ID 'orch-sess-1', got %q", m.ActiveProjectPrimarySessionID)
	}
	if len(m.ProjectTasks) != 2 {
		t.Fatalf("expected 2 project tasks, got %d", len(m.ProjectTasks))
	}
	if m.ProjectTasks[0].Title != "Deploy backend" {
		t.Fatalf("expected first task 'Deploy backend', got %q", m.ProjectTasks[0].Title)
	}

	app.homeModel = m
	app.home.SetModel(m)

	// 2. Typing prompt and pressing Enter navigates to orchestrator chat
	app.home.SetPrompt("What is the current status of all tasks?")
	enterEv := tcell.NewEventKey(tcell.KeyEnter, 0, tcell.ModNone)
	if !app.handleHomeKey(enterEv) {
		t.Fatal("expected handleHomeKey to handle Enter with prompt")
	}
	if openedSessionID != "orch-sess-1" {
		t.Fatalf("expected Enter with prompt to open orchestrator session 'orch-sess-1', got %q", openedSessionID)
	}
	if app.route != "chat" && app.route != "v3chat" {
		t.Fatalf("expected route 'chat' or 'v3chat', got %q", app.route)
	}

	// 3. Ctrl+X from chat returns back to Home (task board)
	ctrlXEv := tcell.NewEventKey(tcell.KeyCtrlX, 0, tcell.ModNone)
	if !app.handleGlobalKey(ctrlXEv) {
		t.Fatal("expected Ctrl+X in chat to return to home")
	}
	if app.route != "home" {
		t.Fatalf("expected route 'home' after Ctrl+X from v3chat, got %q", app.route)
	}

	// 4. Ctrl+X from Home navigates back to orchestrator chat
	openedSessionID = ""
	if !app.handleGlobalKey(ctrlXEv) {
		t.Fatal("expected Ctrl+X on home to open orchestrator session")
	}
	if openedSessionID != "orch-sess-1" {
		t.Fatalf("expected Ctrl+X to open orchestrator session 'orch-sess-1', got %q", openedSessionID)
	}

	// Return to home again
	app.handleGlobalKey(ctrlXEv)

	// 5. Selecting task and pressing Enter when prompt is empty opens that task's session
	openedSessionID = ""
	app.home.ClearPrompt()
	app.home.SetSelectedTaskIndex(0) // task-1 has SessionID = "sess-task-1"
	if !app.handleHomeKey(enterEv) {
		t.Fatal("expected Enter on selected task to open task session")
	}
	if openedSessionID != "sess-task-1" {
		t.Fatalf("expected Enter on task-1 to open session 'sess-task-1', got %q", openedSessionID)
	}

	// 6. Project switching via workspace switcher:
	// Select "proj-beta" (path "project:proj-beta") via WorkspaceModalActionSelect
	app.home.ShowWorkspaceModal()
	app.handleWorkspaceModalAction(ui.WorkspaceModalAction{
		Kind: ui.WorkspaceModalActionSelect,
		Path: "project:proj-beta",
	})
	if app.activeProjectID != "proj-beta" {
		t.Fatalf("expected activeProjectID 'proj-beta', got %q", app.activeProjectID)
	}
	if app.homeModel.ActiveProjectID != "proj-beta" {
		t.Fatalf("expected homeModel.ActiveProjectID 'proj-beta', got %q", app.homeModel.ActiveProjectID)
	}
	if len(app.homeModel.ProjectTasks) != 1 || app.homeModel.ProjectTasks[0].ID != "task-beta-1" {
		t.Fatalf("expected beta tasks loaded after project switch, got %+v", app.homeModel.ProjectTasks)
	}
}
