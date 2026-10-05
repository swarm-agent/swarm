package app

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/gdamore/tcell/v2"

	"swarm-refactor/swarmtui/internal/client"
	"swarm-refactor/swarmtui/internal/model"
	"swarm-refactor/swarmtui/internal/ui"
)

func TestOrchestratorTUIFocusMode(t *testing.T) {
	t.Setenv("SWARMD_LOCAL_TRANSPORT_SOCKET", "")

	var openedSessionID string
	sessionCount := 0

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
		case r.Method == http.MethodPost && r.URL.Path == "/v3/sessions":
			sessionCount++
			sessID := fmt.Sprintf("new-sess-%d", sessionCount)
			_ = json.NewEncoder(w).Encode(client.SessionV3Hydrated{
				Session: client.SessionSummary{ID: sessID, Title: "New Session", SessionAPI: "v3"},
			})
		case r.Method == http.MethodGet && strings.HasPrefix(r.URL.Path, "/v3/tui/sessions/new-sess-"):
			openedSessionID = strings.TrimPrefix(r.URL.Path, "/v3/tui/sessions/")
			_ = json.NewEncoder(w).Encode(client.SessionV3Hydrated{
				Session: client.SessionSummary{ID: openedSessionID, Title: "New Session", SessionAPI: "v3"},
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

	// 2. Typing prompt and pressing Enter creates a completely new project session
	app.home.SetPrompt("What is the current status of all tasks?")
	enterEv := tcell.NewEventKey(tcell.KeyEnter, 0, tcell.ModNone)
	if !app.handleHomeKey(enterEv) {
		t.Fatal("expected handleHomeKey to handle Enter with prompt")
	}
	if openedSessionID != "new-sess-1" {
		t.Fatalf("expected Enter with prompt to create new session 'new-sess-1', got %q", openedSessionID)
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
	if openedSessionID != "new-sess-1" {
		t.Fatalf("expected Ctrl+X to open orchestrator session 'new-sess-1', got %q", openedSessionID)
	}

	// Return to home again
	app.handleGlobalKey(ctrlXEv)

	// 5. Focus starts on prompt: pressing Enter when prompt is empty opens orchestrator, NOT the top task
	openedSessionID = ""
	app.home.ClearPrompt()
	app.home.SetSelectedTaskIndex(0) // task-1 has SessionID = "sess-task-1"
	if app.home.TaskBoxFocused() {
		t.Fatal("expected focus to start on the prompt, not task box")
	}
	if !app.handleHomeKey(enterEv) {
		t.Fatal("expected Enter on empty prompt to open orchestrator chat")
	}
	if openedSessionID != "new-sess-1" {
		t.Fatalf("expected Enter on prompt to open orchestrator 'new-sess-1', got %q", openedSessionID)
	}

	// Return to home again
	app.handleGlobalKey(ctrlXEv)

	// Press Ctrl+Up to navigate up into the task box
	ctrlUpEv := tcell.NewEventKey(tcell.KeyUp, 0, tcell.ModCtrl)
	app.home.HandleKey(ctrlUpEv)
	if !app.home.TaskBoxFocused() {
		t.Fatal("expected Ctrl+Up to focus the task box")
	}

	// Pressing Enter when task box is focused opens the selected task's session
	openedSessionID = ""
	if !app.handleHomeKey(enterEv) {
		t.Fatal("expected Enter on focused task box to open task session")
	}
	if openedSessionID != "sess-task-1" {
		t.Fatalf("expected Enter on task-1 to open session 'sess-task-1', got %q", openedSessionID)
	}

	// Return to home again
	app.handleGlobalKey(ctrlXEv)

	// Press Ctrl+Down to return focus to prompt box
	ctrlDownEv := tcell.NewEventKey(tcell.KeyDown, 0, tcell.ModCtrl)
	app.home.HandleKey(ctrlDownEv)
	if app.home.TaskBoxFocused() {
		t.Fatal("expected Ctrl+Down to return focus to prompt box")
	}

	// 5b. /new creates a canonical new session
	openedSessionID = ""
	app.home.SetPrompt("/new Start next release milestone")
	if !app.handleHomeKey(enterEv) {
		t.Fatal("expected Enter on /new command to execute")
	}
	if openedSessionID != "new-sess-2" {
		t.Fatalf("expected /new to create and open 'new-sess-2', got %q", openedSessionID)
	}

	// Return to home again
	app.handleGlobalKey(ctrlXEv)

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

func TestOrchestratorResolutionAndEmptyTaskEnter(t *testing.T) {
	t.Setenv("SWARMD_LOCAL_TRANSPORT_SOCKET", "")

	var openedSessionID string
	var createdSessionProjectID string

	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		switch {
		case r.Method == http.MethodGet && r.URL.Path == "/v1/onboarding":
			_ = json.NewEncoder(w).Encode(client.OnboardingStatus{
				Identity: client.OnboardingIdentity{Bootstrapped: true, Username: "bob"},
			})
		case r.Method == http.MethodGet && r.URL.Path == "/v1/vault":
			_ = json.NewEncoder(w).Encode(client.VaultStatus{Enabled: false})
		case r.Method == http.MethodGet && r.URL.Path == "/v3/projects":
			_ = json.NewEncoder(w).Encode(map[string]any{
				"projects": []client.ProjectRecord{
					{
						ID:               "proj-gamma",
						Name:             "Project Gamma",
						PrimarySessionID: "", // Empty! Needs resolution
					},
				},
			})
		case r.Method == http.MethodGet && r.URL.Path == "/v3/sessions" && r.URL.Query().Get("project_id") == "proj-gamma":
			_ = json.NewEncoder(w).Encode(map[string]any{
				"ok": true,
				"sessions": []map[string]any{
					{
						"session": map[string]any{
							"id":         "orch-sess-gamma",
							"title":      "Project Gamma Orchestrator",
							"session_api": "v3",
							"metadata": map[string]any{
								"agent_name": "system-orchestrator",
								"project_id": "proj-gamma",
							},
						},
						"projection": map[string]any{
							"session_id": "orch-sess-gamma",
						},
					},
				},
			})
		case r.Method == http.MethodPost && r.URL.Path == "/v3/sessions":
			var body map[string]any
			_ = json.NewDecoder(r.Body).Decode(&body)
			createdSessionProjectID, _ = body["project_id"].(string)
			_ = json.NewEncoder(w).Encode(client.SessionV3Hydrated{
				Session: client.SessionSummary{ID: "new-orch-sess", Title: "Orchestrator", SessionAPI: "v3"},
			})
		case r.Method == http.MethodGet && r.URL.Path == "/v3/projects/proj-gamma/tasks":
			_ = json.NewEncoder(w).Encode(map[string]any{
				"tasks": []client.ProjectTaskRecord{},
				"count": 0,
			})
		case r.Method == http.MethodGet && r.URL.Path == "/v3/tui/sessions/orch-sess-gamma":
			openedSessionID = "orch-sess-gamma"
			_ = json.NewEncoder(w).Encode(client.SessionV3Hydrated{
				Session: client.SessionSummary{ID: "orch-sess-gamma", Title: "Orchestrator", SessionAPI: "v3"},
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
		api:       api,
		home:      home,
		homeModel: model.EmptyHome(),
		keybinds:  ui.NewDefaultKeyBindings(),
	}

	// 1. Refresh model: active project has empty PrimarySessionID, should resolve from /v3/sessions?project_id=proj-gamma
	m, err := app.refreshHomeV3Model(context.Background())
	if err != nil {
		t.Fatalf("refresh error: %v", err)
	}
	if m.ActiveProjectID != "proj-gamma" {
		t.Fatalf("expected proj-gamma, got %q", m.ActiveProjectID)
	}
	if m.ActiveProjectPrimarySessionID != "orch-sess-gamma" {
		t.Fatalf("expected resolved primary session 'orch-sess-gamma', got %q", m.ActiveProjectPrimarySessionID)
	}

	app.homeModel = m
	app.home.SetModel(m)

	// 2. Press Enter with empty prompt and 0 tasks: should open the orchestrator session
	enterEv := tcell.NewEventKey(tcell.KeyEnter, 0, tcell.ModNone)
	if !app.handleHomeKey(enterEv) {
		t.Fatal("expected handleHomeKey to handle Enter on empty tasks")
	}
	if openedSessionID != "orch-sess-gamma" {
		t.Fatalf("expected Enter to open 'orch-sess-gamma', got %q", openedSessionID)
	}
	_ = createdSessionProjectID
}

func TestHomepageTypingPromptCreatesNewSessionNotPriorSession(t *testing.T) {
	t.Setenv("SWARMD_LOCAL_TRANSPORT_SOCKET", "")

	var openedSessionID string
	var createdSessionTitle string
	var createdSessionProjectID string

	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		switch {
		case r.Method == http.MethodGet && r.URL.Path == "/v1/onboarding":
			_ = json.NewEncoder(w).Encode(client.OnboardingStatus{
				Identity: client.OnboardingIdentity{Bootstrapped: true, Username: "alice"},
				Config:   client.OnboardingConfig{SwarmName: "AliceSwarm"},
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
		case r.Method == http.MethodPost && r.URL.Path == "/v3/sessions":
			var req map[string]any
			_ = json.NewDecoder(r.Body).Decode(&req)
			if t, ok := req["title"].(string); ok {
				createdSessionTitle = t
			}
			if p, ok := req["project_id"].(string); ok {
				createdSessionProjectID = p
			}
			_ = json.NewEncoder(w).Encode(client.SessionV3Hydrated{
				Session: client.SessionSummary{ID: "new-brand-sess-123", Title: createdSessionTitle, SessionAPI: "v3"},
			})
		case r.Method == http.MethodGet && r.URL.Path == "/v3/tui/sessions/new-brand-sess-123":
			openedSessionID = "new-brand-sess-123"
			_ = json.NewEncoder(w).Encode(client.SessionV3Hydrated{
				Session: client.SessionSummary{ID: "new-brand-sess-123", Title: createdSessionTitle, SessionAPI: "v3"},
			})
		case r.Method == http.MethodGet && r.URL.Path == "/v3/tui/sessions/prior-sess-1":
			openedSessionID = "prior-sess-1"
			_ = json.NewEncoder(w).Encode(client.SessionV3Hydrated{
				Session: client.SessionSummary{ID: "prior-sess-1", Title: "Prior Session", SessionAPI: "v3"},
			})
		case r.Method == http.MethodGet && r.URL.Path == "/v3/tui/sessions/task-sess-99":
			openedSessionID = "task-sess-99"
			_ = json.NewEncoder(w).Encode(client.SessionV3Hydrated{
				Session: client.SessionSummary{ID: "task-sess-99", Title: "Fix Bug 99", SessionAPI: "v3"},
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
		api:       api,
		home:      home,
		homeModel: model.EmptyHome(),
		keybinds:  ui.NewDefaultKeyBindings(),
	}

	app.homeModel.ActiveProjectID = "proj-x"
	app.homeModel.ActiveProjectName = "Project X"
	app.homeModel.ActiveProjectPrimarySessionID = "prior-sess-1"
	app.homeModel.ProjectTasks = []client.ProjectTaskRecord{
		{ID: "task-99", Title: "Fix Bug 99", SessionID: "task-sess-99", Status: "in_progress"},
	}
	app.home.SetModel(app.homeModel)

	// Focus starts on prompt box (NOT tasks box)
	if app.home.TaskBoxFocused() {
		t.Fatal("expected focus to start on prompt box")
	}

	// 1. User types "swarm" and presses Enter
	app.home.SetPrompt("swarm")
	enterEv := tcell.NewEventKey(tcell.KeyEnter, 0, tcell.ModNone)
	if !app.handleHomeKey(enterEv) {
		t.Fatal("expected handleHomeKey to handle Enter with prompt")
	}
	if app.home.Status() != "" {
		t.Logf("home status: %s", app.home.Status())
	}

	// MUST create and open the brand new session, NOT prior-sess-1!
	if openedSessionID != "new-brand-sess-123" {
		t.Fatalf("expected typing 'swarm' on homepage to create and open new session 'new-brand-sess-123', got %q", openedSessionID)
	}
	if createdSessionProjectID != "proj-x" {
		t.Fatalf("expected created session project ID 'proj-x', got %q", createdSessionProjectID)
	}
	if app.homeModel.ActiveProjectPrimarySessionID != "new-brand-sess-123" {
		t.Fatalf("expected active project primary session updated to 'new-brand-sess-123', got %q", app.homeModel.ActiveProjectPrimarySessionID)
	}

	// 2. Return to home via Ctrl+X
	ctrlXEv := tcell.NewEventKey(tcell.KeyCtrlX, 0, tcell.ModNone)
	if !app.handleGlobalKey(ctrlXEv) {
		t.Fatal("expected Ctrl+X to return to home")
	}
	if app.route != "home" {
		t.Fatalf("expected route 'home', got %q", app.route)
	}

	// 3. User navigates into tasks via Ctrl+Up
	ctrlUpEv := tcell.NewEventKey(tcell.KeyUp, 0, tcell.ModCtrl)
	app.home.HandleKey(ctrlUpEv)
	if !app.home.TaskBoxFocused() {
		t.Fatal("expected Ctrl+Up to focus tasks box")
	}

	// 4. Pressing Enter on the selected task opens that task's session
	openedSessionID = ""
	if !app.handleHomeKey(enterEv) {
		t.Fatal("expected handleHomeKey on focused task to open task session")
	}
	if openedSessionID != "task-sess-99" {
		t.Fatalf("expected Enter on task-99 to open 'task-sess-99', got %q", openedSessionID)
	}
}
