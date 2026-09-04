package app

import (
	"path/filepath"
	"testing"

	"github.com/gdamore/tcell/v2"

	"swarm-refactor/swarmtui/internal/model"
	"swarm-refactor/swarmtui/internal/ui"
)

// Requirement: entering the workspace step after provider setup must re-read the
// launch repository from disk before enforcing managed-worktree readiness.
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
	app := &App{startupCWD: repo, home: home, homeModel: homeModel}

	app.refreshOnboardingWorkspaceGitReadiness()
	home.ShowOnboardingWorkspace("Confirm workspace")
	home.HandleKey(tcell.NewEventKey(tcell.KeyEnter, 0, tcell.ModNone))

	if app.homeModel.WorkspaceSetupGitReadiness != model.GitReadinessReady || !app.homeModel.WorkspaceSetupHasGit {
		t.Fatalf("refreshed app readiness = %q, hasGit=%v", app.homeModel.WorkspaceSetupGitReadiness, app.homeModel.WorkspaceSetupHasGit)
	}
	action, ok := home.PopHomeAction()
	if !ok || action.Kind != ui.HomeActionCreateOnboardingWorkspace || action.WorkspacePath != repo {
		t.Fatalf("workspace action = %+v, ok=%v", action, ok)
	}
}

// Requirement: the final workspace confirmation must revalidate the launch
// repository at the moment Enter is handled, because a queued home reload can
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
	app.home.HandleKey(event)

	if app.homeModel.WorkspaceSetupGitReadiness != model.GitReadinessReady || !app.homeModel.WorkspaceSetupHasGit {
		t.Fatalf("submit-time readiness = %q, hasGit=%v", app.homeModel.WorkspaceSetupGitReadiness, app.homeModel.WorkspaceSetupHasGit)
	}
	action, ok := home.PopHomeAction()
	if !ok || action.Kind != ui.HomeActionCreateOnboardingWorkspace || action.WorkspacePath != repo {
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
