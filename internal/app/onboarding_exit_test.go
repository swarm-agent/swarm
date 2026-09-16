package app

import (
	"github.com/gdamore/tcell/v2"
	"net/http"
	"net/http/httptest"
	"swarm-refactor/swarmtui/internal/client"
	"swarm-refactor/swarmtui/internal/model"
	"swarm-refactor/swarmtui/internal/ui"
	"testing"
	"time"
)

// Requirement: handleGlobalKey must deliver Ctrl+C to the normal quit lifecycle
// before onboarding/pending/editor guards, without marking setup complete.
// Dispatch-level tests isolate this regression without a daemon or credentials.
func TestOnboardingCtrlCExitsEveryStage(t *testing.T) {
	for _, stage := range []string{"identity", "identity-pending", "provider", "workspace", "workspace-pending", "error"} {
		t.Run(stage, func(t *testing.T) {
			home := ui.NewHomePage(model.HomeModel{OnboardingRequired: true, OnboardingUsername: "Example", OnboardingSwarmName: "Example", CWD: "/workspace", WorkspaceSetupGitReadiness: model.GitReadinessReady})
			switch stage {
			case "provider":
				home.ShowOnboardingProvider("")
			case "workspace", "workspace-pending", "error":
				home.ShowOnboardingWorkspace("")
			}
			if stage == "identity-pending" || stage == "workspace-pending" {
				home.HandleKey(tcell.NewEventKey(tcell.KeyEnter, 0, 0))
				home.PopHomeAction()
			}
			if stage == "error" {
				home.SetOnboardingError("setup failed")
			}
			a := &App{home: home, route: "home"}
			if !a.handleGlobalKey(tcell.NewEventKey(tcell.KeyCtrlC, 0, 0)) || !a.quitRequested {
				t.Fatal("quit was swallowed")
			}
			if !home.OnboardingVisible() {
				t.Fatal("exit falsely completed onboarding")
			}
		})
	}
}

// Requirement: a failed canonical setup must not attempt add or completion.
// App-to-client integration with a bounded fake server proves ordering and
// preserves locked UI state rather than trusting a successful action dispatch.
func TestOnboardingSetupFailureDoesNotAdmitOrComplete(t *testing.T) {
	t.Setenv("SWARMD_LOCAL_TRANSPORT_SOCKET", "")
	t.Setenv("DATA_DIR", "")
	requests := make(chan string, 4)
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		requests <- r.URL.Path
		w.WriteHeader(http.StatusConflict)
		w.Write([]byte(`{"error":"setup rejected"}`))
	}))
	defer server.Close()
	home := ui.NewHomePage(model.HomeModel{OnboardingRequired: true})
	home.ShowOnboardingWorkspace("")
	a := &App{home: home, api: client.New(server.URL), onboardingWorkspaceCh: make(chan onboardingWorkspaceResult, 1)}
	a.createOnboardingWorkspaceWithSetup(t.TempDir(), true)
	select {
	case result := <-a.onboardingWorkspaceCh:
		if result.err == nil {
			t.Fatal("failed setup reported success")
		}
		a.onboardingWorkspaceCh <- result
		a.consumeOnboardingWorkspaceResult()
	case <-time.After(3 * time.Second):
		t.Fatal("setup did not finish")
	}
	if !home.OnboardingVisible() || a.homeWorkspaceBootstrapped.Load() {
		t.Fatal("failed setup admitted workspace")
	}
	if got := <-requests; got != "/v1/workspace/repository/setup" {
		t.Fatalf("first request %s", got)
	}
	select {
	case got := <-requests:
		t.Fatalf("unauthorized follow-up %s", got)
	default:
	}
}

// Requirement: startup consumes authenticated daemon guidance without changing
// the selected path or writing state. App/client integration is the narrow layer
// proving decoding, account mismatch handling, and key-driven consent together.
func TestOnboardingGuidancePreservesSelectionUntilExplicitKey(t *testing.T) {
	t.Setenv("SWARMD_LOCAL_TRANSPORT_SOCKET", "")
	t.Setenv("DATA_DIR", "")
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodGet || r.URL.Path != "/v1/onboarding" {
			t.Errorf("unexpected mutation/request: %s %s", r.Method, r.URL.Path)
			w.WriteHeader(http.StatusBadRequest)
			return
		}
		w.Write([]byte(`{"workspace_guidance":{"runtime_username":"worker","runtime_uid":"different-user","runtime_non_root":true,"home_path":"/projects/new"}}`))
	}))
	defer server.Close()
	home := ui.NewHomePage(model.HomeModel{OnboardingRequired: true, CWD: "/selected"})
	home.ShowOnboardingWorkspace("")
	a := &App{home: home, api: client.New(server.URL)}
	a.refreshOnboardingWorkspaceGuidance()
	if !a.onboardingDifferentUser || home.OnboardingWorkspacePath() != "/selected" {
		t.Fatal("guidance ignored daemon identity or replaced selection")
	}
	home.HandleKey(tcell.NewEventKey(tcell.KeyCtrlS, 0, 0))
	if home.OnboardingWorkspacePath() != "/projects/new" {
		t.Fatal("suggestion was not consumed by key handler")
	}
	if _, ok := home.PopHomeAction(); ok {
		t.Fatal("guidance selection bypassed setup consent")
	}
}
