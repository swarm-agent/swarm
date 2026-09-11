package app

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"path/filepath"
	"swarm-refactor/swarmtui/internal/client"
	"testing"

	"github.com/gdamore/tcell/v2"

	"swarm-refactor/swarmtui/internal/model"
	"swarm-refactor/swarmtui/internal/ui"
)

// Requirement: entering the workspace step after provider setup must re-read the
// launch repository through authenticated daemon inspection, not local disk.
// Threat: identity bootstrap initially builds a model before local auth exists, so
// stale unknown readiness can leave a committed launch repository permanently
// blocked in the locked TUI onboarding flow. This app/UI boundary is the narrowest
// layer that proves the current launch path can queue workspace admission without
// weakening the committed-repository check.
func TestRefreshOnboardingWorkspaceGitReadinessUsesCurrentLaunchRepository(t *testing.T) {
	repo := initGitRepo(t)
	writeFile(t, filepath.Join(repo, "tracked.txt"), "ready\n")
	runGit(t, repo, "add", "tracked.txt")
	runGit(t, repo, "commit", "-m", "init")

	homeModel := model.HomeModel{
		OnboardingRequired:         true,
		CWD:                        repo,
		WorkspaceSetupPath:         repo,
		WorkspaceSetupGitReadiness: model.GitReadinessUnknown,
	}
	home := ui.NewHomePage(homeModel)
	home.ShowOnboardingProvider("Provider ready")
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/v1/workspace/repository" || r.URL.Query().Get("path") != repo {
			t.Error("wrong inspection")
		}
		json.NewEncoder(w).Encode(map[string]any{"ok": true, "repository": client.OnboardingRepository{Path: repo, State: "ready", HeadCommit: "verified", ContentReady: true}})
	}))
	defer server.Close()
	t.Setenv("SWARMD_LOCAL_TRANSPORT_SOCKET", "")
	api := client.New(server.URL)
	api.SetToken("fixture")
	app := &App{startupCWD: repo, home: home, homeModel: homeModel, api: api}

	app.refreshOnboardingWorkspaceGitReadiness()
	home.ShowOnboardingWorkspace("Confirm workspace")
	home.HandleKey(tcell.NewEventKey(tcell.KeyEnter, 0, tcell.ModNone))

	// A stale terminal-local model is not rewritten into daemon authority.
	if app.homeModel.WorkspaceSetupGitReadiness != model.GitReadinessUnknown {
		t.Fatal("local readiness promoted")
	}
	action, ok := home.PopHomeAction()
	if !ok || action.Kind != ui.HomeActionCreateOnboardingWorkspace || action.WorkspacePath != repo {
		t.Fatalf("verified repository must advance to canonical admission: %+v, ok=%v", action, ok)
	}
}

// Requirement: the final workspace confirmation must revalidate the launch
// repository through daemon inspection rather than a local Enter recheck; a reload can
// replace the provider-transition refresh with a stale pre-auth model.
// Threat: a valid committed repository remains blocked despite a correct earlier
// refresh. The app key-dispatch boundary is the narrowest layer that proves stale
// UI state cannot win the race while non-ready repositories remain rejected.
func TestOnboardingWorkspaceSubmitRevalidatesStaleReadiness(t *testing.T) {
	repo := initGitRepo(t)
	writeFile(t, filepath.Join(repo, "tracked.txt"), "ready\n")
	runGit(t, repo, "add", "tracked.txt")
	runGit(t, repo, "commit", "-m", "init")

	homeModel := model.HomeModel{
		OnboardingRequired:         true,
		CWD:                        repo,
		WorkspaceSetupPath:         repo,
		WorkspaceSetupGitReadiness: model.GitReadinessUnknown,
	}
	home := ui.NewHomePage(homeModel)
	home.ShowOnboardingWorkspace("Confirm workspace")
	app := &App{
		startupCWD: repo,
		home:       home,
		homeModel:  homeModel,
		keybinds:   ui.NewDefaultKeyBindings(),
	}

	event := tcell.NewEventKey(tcell.KeyEnter, 0, tcell.ModNone)
	app.refreshOnboardingWorkspaceGitReadinessBeforeSubmit(event)
	// Reproduce a final stale reload after the synchronous local recheck. An
	// inconclusive client status must still reach the daemon's canonical
	// repository-admission boundary.
	staleModel := app.homeModel
	staleModel.WorkspaceSetupGitReadiness = model.GitReadinessUnknown
	staleModel.WorkspaceSetupHasGit = false
	app.home.SetModel(staleModel)
	app.home.HandleKey(event)

	if app.homeModel.WorkspaceSetupGitReadiness != model.GitReadinessUnknown {
		t.Fatal("local readiness promoted")
	}
	action, ok := home.PopHomeAction()
	if !ok || action.Kind != ui.HomeActionInspectOnboardingRepository || action.WorkspacePath != repo {
		t.Fatalf("workspace action = %+v, ok=%v", action, ok)
	}
}

// Requirement: onboarding releases only for an active workspace backed by a
// committed Git repository, because every normal agent session uses managed
// worktree isolation. The threat is admitting a plain or unborn directory that
// cannot satisfy the runtime contract. This helper is the narrowest post-refresh
// gate used by onboarding.
func TestHomeModelHasActiveWorkspaceRequiresCommittedRepository(t *testing.T) {
	for _, readiness := range []model.GitReadiness{
		model.GitReadinessUnavailable,
		model.GitReadinessNotRepository,
		model.GitReadinessNeedsCommit,
		model.GitReadinessReady,
	} {
		home := model.HomeModel{
			Workspaces: []model.Workspace{
				{Name: "Other", Path: "/other", Active: false},
				{Name: "Launch", Path: "/repo/project", Active: true},
			},
			Directories: []model.DirectoryItem{{ResolvedPath: "/repo/project", HasGit: readiness == model.GitReadinessReady || readiness == model.GitReadinessNeedsCommit, GitReadiness: readiness, IsWorkspace: true}},
		}
		gotReady := homeModelHasActiveWorkspace(home, "/repo/project")
		if wantReady := readiness == model.GitReadinessReady; gotReady != wantReady {
			t.Errorf("active workspace readiness %q = %v, want %v", readiness, gotReady, wantReady)
		}
		if homeModelHasActiveWorkspace(home, "/other") {
			t.Fatal("inactive workspace must not release onboarding")
		}
		if homeModelHasActiveWorkspace(home, "/missing") {
			t.Fatal("missing workspace must not release onboarding")
		}
		if got := homeModelWorkspaceGitReadiness(home, "/repo/project"); got != readiness {
			t.Fatalf("workspace git readiness = %q, want %q", got, readiness)
		}
	}
}
