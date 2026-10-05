package app

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"runtime/debug"
	"testing"

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
