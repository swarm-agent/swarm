package app

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"

	"github.com/gdamore/tcell/v2"
	"swarm-refactor/swarmtui/internal/client"
	"swarm-refactor/swarmtui/internal/model"
	"swarm-refactor/swarmtui/internal/ui"
)

// Requirement: discoverOnboardingRepositories uses authenticated, bounded daemon
// discovery and does not mutate Git/catalog or release onboarding. Its async
// result must preserve errors rather than presenting them as an empty list.
// A fake HTTP boundary is the narrowest proof of app/client routing; it does not
// prove daemon filesystem discovery or a deployed terminal journey.
func TestOnboardingDiscoveryRouting(t *testing.T) {
	for _, fail := range []bool{false, true} {
		t.Run(map[bool]string{false: "repositories", true: "failure"}[fail], func(t *testing.T) {
			t.Setenv("SWARMD_LOCAL_TRANSPORT_SOCKET", "")
			server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				if r.Method != http.MethodGet || r.URL.Path != "/v1/workspace/discover" || r.URL.Query().Get("limit") != "100" || r.Header.Get("Authorization") != "Bearer fixture" {
					t.Error("wrong discovery authority or unbounded/mutating request")
					http.Error(w, "unexpected request", http.StatusBadRequest)
					return
				}
				if fail {
					http.Error(w, "discovery unavailable", http.StatusServiceUnavailable)
					return
				}
				_ = json.NewEncoder(w).Encode(map[string]any{"ok": true, "directories": []client.WorkspaceDiscoverEntry{{Path: "/projects/repo", IsGitRepo: true}}})
			}))
			defer server.Close()
			api := client.New(server.URL)
			api.SetToken("fixture")
			home := ui.NewHomePage(model.HomeModel{CWD: "/projects", OnboardingRequired: true})
			home.ShowOnboardingWorkspace("")
			a := &App{api: api, home: home, onboardingWorkspaceCh: make(chan onboardingWorkspaceResult, 1)}
			a.discoverOnboardingRepositories()
			var result onboardingWorkspaceResult
			select {
			case result = <-a.onboardingWorkspaceCh:
			case <-time.After(3 * time.Second):
				t.Fatal("discovery did not finish")
			}
			if (result.err != nil) != fail || !result.discovered {
				t.Fatalf("wrong result: %+v", result)
			}
			a.onboardingWorkspaceCh <- result
			a.consumeOnboardingWorkspaceResult()
			if !home.OnboardingVisible() {
				t.Fatal("discovery released onboarding")
			}
			if !fail {
				home.HandleKey(tcell.NewEventKey(tcell.KeyEnter, 0, 0))
				action, ok := home.PopHomeAction()
				if !ok || action.Kind != ui.HomeActionInspectOnboardingRepository || action.WorkspacePath != "/projects/repo" {
					t.Fatalf("picker failed to route verification: %+v", action)
				}
			}
		})
	}
}
