package app

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"runtime/debug"
	"strings"
	"testing"
	"time"

	"github.com/gdamore/tcell/v2"
	"swarm-refactor/swarmtui/internal/client"
	"swarm-refactor/swarmtui/internal/model"
	"swarm-refactor/swarmtui/internal/ui"
)

// Requirement: Onboarding must support a Project-First flow where:
// 1. Root user creation / identity is 100% preserved and executed first.
// 2. Provider is configured or skipped, transitioning to Project Creation.
// 3. Project Creation creates a genuine V3 project in Pebble store.
// 4. User can enter the project directly with zero workspaces/Git required.
// 5. If provider was configured, a system-orchestrator project session is opened.
// 6. Skipping steps marks FinishSetupNeeded on Home screen.
func TestOnboardingProjectFirstFlowWithoutWorkspaces(t *testing.T) {
	t.Setenv("SWARMD_LOCAL_TRANSPORT_SOCKET", "")

	var (
		createdProjectName string
		onboardingComplete bool
		sessionProjectID   string
		sessionAgentName   string
	)

	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch {
		case r.Method == http.MethodGet && r.URL.Path == "/v1/onboarding":
			_ = json.NewEncoder(w).Encode(client.OnboardingStatus{
				OK:              true,
				NeedsOnboarding: !onboardingComplete,
				Identity: client.OnboardingIdentity{
					Bootstrapped: true,
					Username:     "alice",
				},
				Config: client.OnboardingConfig{
					SwarmName: "AliceSwarm",
				},
			})
		case r.Method == http.MethodPost && r.URL.Path == "/v1/onboarding":
			var input client.SaveOnboardingInput
			_ = json.NewDecoder(r.Body).Decode(&input)
			if input.DesktopOnboardingComplete != nil && *input.DesktopOnboardingComplete {
				onboardingComplete = true
			}
			_ = json.NewEncoder(w).Encode(client.OnboardingStatus{
				OK:              true,
				NeedsOnboarding: !onboardingComplete,
				Identity: client.OnboardingIdentity{
					Bootstrapped: true,
					Username:     "alice",
				},
				Config: client.OnboardingConfig{
					SwarmName: "AliceSwarm",
				},
			})
		case r.Method == http.MethodPost && r.URL.Path == "/v3/projects":
			var input client.CreateProjectInput
			_ = json.NewDecoder(r.Body).Decode(&input)
			if strings.TrimSpace(input.ClientRequestID) == "" {
				w.WriteHeader(http.StatusBadRequest)
				_ = json.NewEncoder(w).Encode(map[string]any{"error": "client_request_id is required"})
				return
			}
			createdProjectName = input.Name
			w.WriteHeader(http.StatusCreated)
			_ = json.NewEncoder(w).Encode(client.ProjectRecord{
				ID:        "proj_test_123",
				Name:      input.Name,
				CreatedAt: 1000,
			})
		case r.Method == http.MethodGet && r.URL.Path == "/v3/projects":
			_ = json.NewEncoder(w).Encode(map[string]any{
				"projects": []client.ProjectRecord{
					{
						ID:   "proj_test_123",
						Name: createdProjectName,
					},
				},
			})
		case r.Method == http.MethodGet && r.URL.Path == "/v1/providers":
			_ = json.NewEncoder(w).Encode(map[string]any{
				"providers": []client.ProviderStatus{
					{
						ID:       "google",
						Ready:    true,
						Runnable: true,
					},
				},
			})
		case r.Method == http.MethodPost && r.URL.Path == "/v3/sessions":
			var payload map[string]any
			_ = json.NewDecoder(r.Body).Decode(&payload)
			if pid, ok := payload["project_id"].(string); ok {
				sessionProjectID = pid
			}
			if agent, ok := payload["agent_name"].(string); ok {
				sessionAgentName = agent
			}
			_ = json.NewEncoder(w).Encode(client.SessionV3Hydrated{
				Session: client.SessionSummary{
					ID:    "sess_project_orchestrator",
					Title: createdProjectName,
					Mode:  "auto",
				},
			})
		case r.Method == http.MethodGet && r.URL.Path == "/v3/sessions":
			_ = json.NewEncoder(w).Encode(map[string]any{"sessions": []any{}})
		default:
			w.WriteHeader(http.StatusOK)
			_, _ = w.Write([]byte(`{}`))
		}
	}))
	defer server.Close()

	api := client.New(server.URL)
	api.SetToken("test-token")

	home := ui.NewHomePage(model.HomeModel{
		OnboardingRequired: true,
		AuthConfigured:     true,
	})

	app := &App{
		api:                   api,
		home:                  home,
		onboardingWorkspaceCh: make(chan onboardingWorkspaceResult, 1),
	}

	// 1. Trigger project creation from onboarding (Direct to project, no workspace)
	app.handleCreateOnboardingProject("My Awesome Project", "Build the future", nil)

	// 2. Verify genuine V3 project was created
	if createdProjectName != "My Awesome Project" {
		t.Fatalf("expected project name 'My Awesome Project', got %q", createdProjectName)
	}

	// 3. Verify onboarding completion was acknowledged to daemon
	if !onboardingComplete {
		t.Fatal("expected desktop onboarding completion to be acknowledged")
	}

	// 4. Verify system-orchestrator project session was launched
	if sessionProjectID != "proj_test_123" {
		t.Fatalf("expected session project ID 'proj_test_123', got %q", sessionProjectID)
	}
	if sessionAgentName != "system-orchestrator" {
		t.Fatalf("expected session agent 'system-orchestrator', got %q", sessionAgentName)
	}

	// 5. Verify onboarding overlay is no longer required on Home
	if home.OnboardingVisible() {
		t.Fatal("expected onboarding to be dismissed")
	}
}

func TestOnboardingProjectFirstFlowSkipWorkspace(t *testing.T) {
	t.Setenv("SWARMD_LOCAL_TRANSPORT_SOCKET", "")

	var onboardingComplete bool

	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch {
		case r.Method == http.MethodGet && r.URL.Path == "/v1/onboarding":
			_ = json.NewEncoder(w).Encode(client.OnboardingStatus{
				OK:              true,
				NeedsOnboarding: !onboardingComplete,
			})
		case r.Method == http.MethodPost && r.URL.Path == "/v1/onboarding":
			var input client.SaveOnboardingInput
			_ = json.NewDecoder(r.Body).Decode(&input)
			if input.DesktopOnboardingComplete != nil && *input.DesktopOnboardingComplete {
				onboardingComplete = true
			}
			_ = json.NewEncoder(w).Encode(client.OnboardingStatus{
				OK:              true,
				NeedsOnboarding: !onboardingComplete,
			})
		case r.Method == http.MethodPost && r.URL.Path == "/v3/projects":
			var input client.CreateProjectInput
			_ = json.NewDecoder(r.Body).Decode(&input)
			if strings.TrimSpace(input.ClientRequestID) == "" {
				w.WriteHeader(http.StatusBadRequest)
				_ = json.NewEncoder(w).Encode(map[string]any{"error": "client_request_id is required"})
				return
			}
			w.WriteHeader(http.StatusCreated)
			_ = json.NewEncoder(w).Encode(client.ProjectRecord{
				ID:   "proj_default",
				Name: "default",
			})
		case r.Method == http.MethodGet && r.URL.Path == "/v3/projects":
			_ = json.NewEncoder(w).Encode(map[string]any{
				"projects": []client.ProjectRecord{
					{
						ID:   "proj_default",
						Name: "default",
					},
				},
			})
		default:
			w.WriteHeader(http.StatusOK)
			_, _ = w.Write([]byte(`{}`))
		}
	}))
	defer server.Close()

	api := client.New(server.URL)
	api.SetToken("test-token")

	home := ui.NewHomePage(model.HomeModel{
		OnboardingRequired: true,
		AuthConfigured:     false, // Provider was skipped
	})

	app := &App{
		api:                   api,
		home:                  home,
		onboardingWorkspaceCh: make(chan onboardingWorkspaceResult, 1),
	}

	// User skips workspace
	app.handleSkipOnboardingWorkspace()

	if !onboardingComplete {
		t.Fatal("expected onboarding to be complete after skipping workspace")
	}

	// Verify Home model flags FinishSetupNeeded because auth and workspace were skipped
	if !app.homeModel.FinishSetupNeeded {
		t.Fatal("expected FinishSetupNeeded to be true when setup was skipped")
	}
	if !app.homeModel.FinishSetupMissingProvider {
		t.Fatal("expected FinishSetupMissingProvider to be true")
	}
	if !app.homeModel.FinishSetupMissingWorkspace {
		t.Fatal("expected FinishSetupMissingWorkspace to be true")
	}
}

func TestOnboardingFreshDaemonStartsAtStepOneWithout401(t *testing.T) {
	t.Setenv("SWARMD_LOCAL_TRANSPORT_SOCKET", "")

	providersCalled := false
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch {
		case r.Method == http.MethodGet && r.URL.Path == "/v1/onboarding":
			_ = json.NewEncoder(w).Encode(client.OnboardingStatus{
				OK:              true,
				NeedsOnboarding: true,
				Identity: client.OnboardingIdentity{
					Bootstrapped: false,
					Username:     "testbench",
				},
				Config: client.OnboardingConfig{
					SwarmName: "testbench",
				},
			})
		case r.Method == http.MethodGet && r.URL.Path == "/v1/providers":
			providersCalled = true
			t.Logf("ListProviders called! Stack:\n%s", string(debug.Stack()))
			w.WriteHeader(http.StatusUnauthorized)
			_, _ = w.Write([]byte(`{"error":"product identity has not been bootstrapped"}`))
		case r.Method == http.MethodGet && r.URL.Path == "/v1/auth/credentials":
			w.WriteHeader(http.StatusUnauthorized)
			_, _ = w.Write([]byte(`{"error":"product identity has not been bootstrapped"}`))
		default:
			w.WriteHeader(http.StatusOK)
			_, _ = w.Write([]byte(`{}`))
		}
	}))
	defer server.Close()

	api := client.New(server.URL)
	home := ui.NewHomePage(model.EmptyHome())

	app := &App{
		api:                   api,
		home:                  home,
		onboardingWorkspaceCh: make(chan onboardingWorkspaceResult, 1),
	}

	t.Logf("calling refreshHomeV3Model...")
	next, err := app.refreshHomeV3Model(context.Background())
	t.Logf("refreshHomeV3Model returned: err=%v, providersCalled=%v", err, providersCalled)
	if err != nil {
		t.Fatalf("refreshHomeV3Model failed: %v", err)
	}

	if !next.OnboardingRequired {
		t.Fatal("expected OnboardingRequired to be true")
	}
	if next.OnboardingIdentityBootstrapped {
		t.Fatal("expected OnboardingIdentityBootstrapped to be false on fresh install")
	}

	t.Logf("before applyHomeModel: OnboardingVisible=%v, OnboardingProviderActive=%v",
		app.home.OnboardingVisible(), app.home.OnboardingProviderActive())
	app.applyHomeModel(next)
	t.Logf("after applyHomeModel: OnboardingVisible=%v, OnboardingProviderActive=%v",
		app.home.OnboardingVisible(), app.home.OnboardingProviderActive())

	if !app.home.OnboardingVisible() {
		t.Fatal("expected onboarding to be visible")
	}
	if app.home.OnboardingProviderActive() {
		t.Fatal("fresh install must NOT skip to provider phase before identity is bootstrapped")
	}
	if providersCalled {
		t.Fatal("providers API should not be called before identity bootstrap")
	}
}

func TestOnboardingProviderSaveTransitionsToProject(t *testing.T) {
	t.Setenv("SWARMD_LOCAL_TRANSPORT_SOCKET", "")

	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch {
		case r.Method == http.MethodGet && r.URL.Path == "/v1/onboarding":
			_ = json.NewEncoder(w).Encode(client.OnboardingStatus{
				OK:              true,
				NeedsOnboarding: true,
				Identity: client.OnboardingIdentity{
					Bootstrapped: true,
					Username:     "testbench",
				},
				Heuristics: client.OnboardingHeuristics{
					CredentialCount: 0,
					AgentCount:      0,
				},
			})
		case r.Method == http.MethodPost && (r.URL.Path == "/v1/onboarding/provider/credential" || r.URL.Path == "/v1/auth/credentials"):
			_ = json.NewEncoder(w).Encode(client.AuthCredential{
				Provider: "openrouter",
				ID:       "cred_1",
				Connection: &client.AuthConnectionStatus{
					Connected: true,
					Method:    "api_key",
				},
			})
		default:
			w.WriteHeader(http.StatusOK)
			_, _ = w.Write([]byte(`{}`))
		}
	}))
	defer server.Close()

	api := client.New(server.URL)
	api.SetToken("test-token")
	home := ui.NewHomePage(model.HomeModel{
		OnboardingRequired: true,
	})
	home.ShowOnboardingProvider("Select a provider")

	app := &App{
		api:  api,
		home: home,
	}

	if !app.home.OnboardingProviderActive() {
		t.Fatal("expected OnboardingProviderActive to be true")
	}

	app.handleAuthModalAction(ui.AuthModalAction{
		Kind: ui.AuthModalActionUpsert,
		Upsert: &ui.AuthModalUpsert{
			Provider: "openrouter",
			APIKey:   "sk-test",
		},
	})

	if !app.home.OnboardingProjectActive() {
		t.Fatalf("expected OnboardingProjectActive to be true after saving provider, but got phase=%v", app.home.OnboardingProjectActive())
	}
	if app.home.OnboardingWorkspaceActive() {
		t.Fatal("saving provider must NOT skip directly to OnboardingWorkspaceActive")
	}
}

func TestOnboardingProjectWithWorkspacesPersonalizesWithoutWorkspaceFiles(t *testing.T) {
	t.Setenv("SWARMD_LOCAL_TRANSPORT_SOCKET", "")

	tmpDir := t.TempDir()
	wsDir := filepath.Join(tmpDir, "my-code")
	_ = os.MkdirAll(wsDir, 0755)
	_ = os.WriteFile(filepath.Join(wsDir, "AGENTS.md"), []byte("# Agent Guidelines"), 0644)

	var projectCreated bool
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch {
		case r.Method == http.MethodPost && r.URL.Path == "/v1/workspace/add":
			_ = json.NewEncoder(w).Encode(map[string]any{
				"workspace": map[string]any{
					"workspace_id":   "ws-123",
					"resolved_path":  wsDir,
					"workspace_name": "my-code",
				},
			})
		case r.Method == http.MethodPost && r.URL.Path == "/v3/projects":
			var input client.CreateProjectInput
			_ = json.NewDecoder(r.Body).Decode(&input)
			if strings.TrimSpace(input.ClientRequestID) == "" {
				w.WriteHeader(http.StatusBadRequest)
				_ = json.NewEncoder(w).Encode(map[string]any{"error": "client_request_id is required"})
				return
			}
			projectCreated = true
			w.WriteHeader(http.StatusCreated)
			_ = json.NewEncoder(w).Encode(map[string]any{
				"project": client.ProjectRecord{
					ID:   "proj-abc",
					Name: "super-app",
					ContextGeneration: &client.ProjectContextGeneration{
						Status:  "running",
						Attempt: 1,
					},
				},
			})
		case r.Method == http.MethodGet && r.URL.Path == "/v3/projects/proj-abc":
			_ = json.NewEncoder(w).Encode(map[string]any{
				"project": client.ProjectRecord{
					ID:             "proj-abc",
					Name:           "super-app",
					ProjectContext: "# Generated PROJECT.md Guidelines\nFrom AGENTS.md",
					ContextGeneration: &client.ProjectContextGeneration{
						Status:  "ready",
						Attempt: 1,
					},
				},
			})
		case r.Method == http.MethodPost && r.URL.Path == "/v1/onboarding":
			_ = json.NewEncoder(w).Encode(client.OnboardingStatus{OK: true})
		case r.Method == http.MethodGet && r.URL.Path == "/v1/onboarding":
			_ = json.NewEncoder(w).Encode(client.OnboardingStatus{OK: true})
		case r.Method == http.MethodGet && r.URL.Path == "/v3/projects":
			_ = json.NewEncoder(w).Encode(map[string]any{
				"projects": []client.ProjectRecord{
					{ID: "proj-abc", Name: "super-app"},
				},
			})
		default:
			w.WriteHeader(http.StatusOK)
			_, _ = w.Write([]byte(`{}`))
		}
	}))
	defer server.Close()

	api := client.New(server.URL)
	api.SetToken("test-token")
	home := ui.NewHomePage(model.HomeModel{OnboardingRequired: true})
	home.ShowOnboardingWorkspace("Select workspaces")

	app := &App{
		api:                   api,
		home:                  home,
		onboardingWorkspaceCh: make(chan onboardingWorkspaceResult, 1),
	}

	app.handleCreateOnboardingProject("super-app", "", []string{wsDir})

	// Wait for completion from goroutine
	select {
	case res := <-app.onboardingWorkspaceCh:
		if res.err != nil {
			t.Fatalf("unexpected error: %v", res.err)
		}
		if !res.projectCreated || res.projectID != "proj-abc" {
			t.Fatalf("expected projectCreated with id 'proj-abc', got %+v", res)
		}
	case <-time.After(5 * time.Second):
		t.Fatal("timed out waiting for personalizing project creation")
	}

	if !projectCreated {
		t.Fatal("expected project to be created on daemon")
	}

	// Verify PROJECT.md was NOT created in wsDir (project context lives strictly in Pebble store)
	projMD := filepath.Join(wsDir, "PROJECT.md")
	if _, err := os.Stat(projMD); !os.IsNotExist(err) {
		t.Fatalf("PROJECT.md must NOT be created in the user workspace (it belongs strictly in Swarm Pebble store)")
	}
}

func TestOnboardingProjectWorkspaceRouterFailureDoesNotLockUserOut(t *testing.T) {
	t.Setenv("SWARMD_LOCAL_TRANSPORT_SOCKET", "")

	tmpDir := t.TempDir()
	wsDir := filepath.Join(tmpDir, "failed-router-code")
	_ = os.MkdirAll(wsDir, 0755)

	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch {
		case r.Method == http.MethodPost && r.URL.Path == "/v1/workspace/add":
			_ = json.NewEncoder(w).Encode(map[string]any{
				"workspace": map[string]any{
					"workspace_id":   "ws-failed",
					"resolved_path":  wsDir,
					"workspace_name": "failed-router-code",
				},
			})
		case r.Method == http.MethodPost && r.URL.Path == "/v3/projects":
			var input client.CreateProjectInput
			_ = json.NewDecoder(r.Body).Decode(&input)
			if strings.TrimSpace(input.ClientRequestID) == "" {
				w.WriteHeader(http.StatusBadRequest)
				_ = json.NewEncoder(w).Encode(map[string]any{"error": "client_request_id is required"})
				return
			}
			w.WriteHeader(http.StatusCreated)
			_ = json.NewEncoder(w).Encode(map[string]any{
				"project": client.ProjectRecord{
					ID:   "proj-fail",
					Name: "fail-safe-app",
					ContextGeneration: &client.ProjectContextGeneration{
						Status:  "running",
						Attempt: 1,
					},
				},
			})
		case r.Method == http.MethodGet && r.URL.Path == "/v3/projects/proj-fail":
			// Router fails!
			_ = json.NewEncoder(w).Encode(map[string]any{
				"project": client.ProjectRecord{
					ID:   "proj-fail",
					Name: "fail-safe-app",
					ContextGeneration: &client.ProjectContextGeneration{
						Status: "failed",
						Error:  "model provider quota exceeded",
					},
				},
			})
		case r.Method == http.MethodPost && r.URL.Path == "/v1/onboarding":
			_ = json.NewEncoder(w).Encode(client.OnboardingStatus{OK: true})
		case r.Method == http.MethodGet && r.URL.Path == "/v1/onboarding":
			_ = json.NewEncoder(w).Encode(client.OnboardingStatus{OK: true})
		case r.Method == http.MethodGet && r.URL.Path == "/v3/projects":
			_ = json.NewEncoder(w).Encode(map[string]any{
				"projects": []client.ProjectRecord{
					{ID: "proj-fail", Name: "fail-safe-app"},
				},
			})
		default:
			w.WriteHeader(http.StatusOK)
			_, _ = w.Write([]byte(`{}`))
		}
	}))
	defer server.Close()

	api := client.New(server.URL)
	api.SetToken("test-token")
	home := ui.NewHomePage(model.HomeModel{OnboardingRequired: true})

	app := &App{
		api:                   api,
		home:                  home,
		onboardingWorkspaceCh: make(chan onboardingWorkspaceResult, 1),
	}

	app.handleCreateOnboardingProject("fail-safe-app", "", []string{wsDir})

	// Must finish and NOT lock the user out!
	select {
	case res := <-app.onboardingWorkspaceCh:
		if res.err != nil {
			t.Fatalf("expected graceful completion, got error: %v", res.err)
		}
		if !res.projectCreated || res.projectID != "proj-fail" {
			t.Fatalf("expected projectCreated with id 'proj-fail', got %+v", res)
		}
	case <-time.After(5 * time.Second):
		t.Fatal("timed out waiting for graceful recovery after router failure")
	}
}

func TestOnboardingProjectPersonalizingTransitionsToPreFinishAndCompletes(t *testing.T) {
	t.Setenv("SWARMD_LOCAL_TRANSPORT_SOCKET", "")

	tmpDir := t.TempDir()
	wsDir := filepath.Join(tmpDir, "prefinish-code")
	_ = os.MkdirAll(wsDir, 0755)

	var (
		receivedClientRequestID string
		onboardingComplete      bool
		sessionLaunched         bool
	)

	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch {
		case r.Method == http.MethodPost && r.URL.Path == "/v1/workspace/add":
			_ = json.NewEncoder(w).Encode(map[string]any{
				"workspace": map[string]any{
					"workspace_id":   "ws-prefinish",
					"resolved_path":  wsDir,
					"workspace_name": "prefinish-code",
				},
			})
		case r.Method == http.MethodPost && r.URL.Path == "/v3/projects":
			var input client.CreateProjectInput
			_ = json.NewDecoder(r.Body).Decode(&input)
			receivedClientRequestID = input.ClientRequestID
			if strings.TrimSpace(input.ClientRequestID) == "" {
				w.WriteHeader(http.StatusBadRequest)
				_ = json.NewEncoder(w).Encode(map[string]any{"error": "client_request_id is required"})
				return
			}
			w.WriteHeader(http.StatusCreated)
			_ = json.NewEncoder(w).Encode(map[string]any{
				"project": client.ProjectRecord{
					ID:   "proj-prefinish-123",
					Name: input.Name,
					ContextGeneration: &client.ProjectContextGeneration{
						Status:  "ready",
						Attempt: 1,
					},
				},
			})
		case r.Method == http.MethodGet && r.URL.Path == "/v3/projects/proj-prefinish-123":
			_ = json.NewEncoder(w).Encode(map[string]any{
				"project": client.ProjectRecord{
					ID:             "proj-prefinish-123",
					Name:           "apex-app",
					ProjectContext: "# Apex Guidelines",
					ContextGeneration: &client.ProjectContextGeneration{
						Status:  "ready",
						Attempt: 1,
					},
				},
			})
		case r.Method == http.MethodPost && r.URL.Path == "/v1/onboarding":
			var input client.SaveOnboardingInput
			_ = json.NewDecoder(r.Body).Decode(&input)
			if input.DesktopOnboardingComplete != nil && *input.DesktopOnboardingComplete {
				onboardingComplete = true
			}
			_ = json.NewEncoder(w).Encode(client.OnboardingStatus{OK: true})
		case r.Method == http.MethodGet && r.URL.Path == "/v1/onboarding":
			_ = json.NewEncoder(w).Encode(client.OnboardingStatus{OK: true})
		case r.Method == http.MethodGet && r.URL.Path == "/v3/projects":
			_ = json.NewEncoder(w).Encode(map[string]any{
				"projects": []client.ProjectRecord{
					{ID: "proj-prefinish-123", Name: "apex-app"},
				},
			})
		case r.Method == http.MethodGet && r.URL.Path == "/v1/providers":
			_ = json.NewEncoder(w).Encode(map[string]any{
				"providers": []client.ProviderStatus{
					{
						ID:       "google",
						Ready:    true,
						Runnable: true,
					},
				},
			})
		case r.Method == http.MethodPost && r.URL.Path == "/v3/sessions":
			sessionLaunched = true
			_ = json.NewEncoder(w).Encode(map[string]any{
				"session": map[string]any{
					"id": "sess-prefinish-123",
				},
			})
		case r.Method == http.MethodGet && r.URL.Path == "/v3/sessions":
			_ = json.NewEncoder(w).Encode(map[string]any{"sessions": []any{}})
		default:
			w.WriteHeader(http.StatusOK)
			_, _ = w.Write([]byte(`{}`))
		}
	}))
	defer server.Close()

	api := client.New(server.URL)
	api.SetToken("test-token")
	home := ui.NewHomePage(model.HomeModel{
		OnboardingRequired: true,
		AuthConfigured:     true,
	})

	app := &App{
		api:                   api,
		home:                  home,
		onboardingWorkspaceCh: make(chan onboardingWorkspaceResult, 1),
	}

	// 1. Initiate project creation with workspaces (triggers personalization)
	app.handleCreateOnboardingProject("apex-app", "Build apex", []string{wsDir})

	// 2. Consume from channel and verify client_request_id was sent
	select {
	case res := <-app.onboardingWorkspaceCh:
		if res.err != nil {
			t.Fatalf("unexpected error: %v", res.err)
		}
		if !res.projectCreated || res.projectID != "proj-prefinish-123" {
			t.Fatalf("expected projectCreated with id 'proj-prefinish-123', got %+v", res)
		}
		if receivedClientRequestID == "" {
			t.Fatal("expected client_request_id to be populated in CreateProject request")
		}
		// Put back on channel to let consumeOnboardingWorkspaceResult process it
		app.onboardingWorkspaceCh <- res
	case <-time.After(5 * time.Second):
		t.Fatal("timed out waiting for project creation")
	}

	// 3. Process the result: must transition to PreFinish screen (not immediately complete!)
	app.consumeOnboardingWorkspaceResult()
	if !app.home.OnboardingPreFinishActive() {
		t.Fatal("expected Step 4 to transition to PreFinish screen post-personalization")
	}
	if onboardingComplete {
		t.Fatal("onboarding completion must NOT be called before user confirms PreFinish")
	}

	// 4. User confirms PreFinish via Enter
	app.home.HandleKey(tcell.NewEventKey(tcell.KeyEnter, 0, 0))
	action, ok := app.home.PopHomeAction()
	if !ok || action.Kind != ui.HomeActionFinishOnboardingProject {
		t.Fatalf("expected HomeActionFinishOnboardingProject, got %+v", action)
	}

	// 5. App processes finish action: complete onboarding & launch session
	app.handleHomeAction(action)
	if !onboardingComplete {
		t.Fatal("expected onboarding completion to be saved after PreFinish confirmation")
	}
	if !sessionLaunched {
		t.Fatal("expected session to be launched after completing onboarding")
	}
}

