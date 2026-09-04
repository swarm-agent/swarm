package ui

import (
	"strings"
	"testing"

	"github.com/gdamore/tcell/v2"

	"swarm-refactor/swarmtui/internal/model"
)

func readyOnboardingPage() *HomePage {
	page := NewHomePage(model.HomeModel{
		OnboardingRequired:             true,
		OnboardingUsername:             "alice",
		OnboardingSwarmName:            "Local Swarm",
		CWD:                            "/repo/project",
		WorkspaceSetupHasGit:           true,
		WorkspaceSetupGitReadiness:     model.GitReadinessReady,
	})
	page.SetAuthModalData([]AuthModalProvider{{ID: "codex"}, {ID: "openai"}}, nil)
	return page
}

func TestOnboardingIdentityAdvancesOnlyAfterSaveCompletion(t *testing.T) {
	page := readyOnboardingPage()
	page.HandleKey(tcell.NewEventKey(tcell.KeyEnter, 0, tcell.ModNone))

	action, ok := page.PopHomeAction()
	if !ok || action.Kind != HomeActionSaveOnboarding {
		t.Fatalf("identity action = %+v, ok=%v", action, ok)
	}
	if !page.OnboardingVisible() || page.OnboardingProviderActive() {
		t.Fatal("identity submission should remain pending until the API completes")
	}

	page.SetOnboardingRequired(false, "alice", "Local Swarm")
	page.ShowOnboardingProvider("Identity saved")
	if !page.OnboardingProviderActive() {
		t.Fatal("provider phase did not open after identity completion")
	}
}

func TestOnboardingProviderSkipRequiresWorkspaceConfirmation(t *testing.T) {
	page := readyOnboardingPage()
	page.ShowOnboardingProvider("Identity saved")
	page.HandleKey(tcell.NewEventKey(tcell.KeyRune, 's', tcell.ModNone))

	if !page.OnboardingWorkspaceActive() {
		t.Fatal("provider skip did not advance to workspace confirmation")
	}
	page.HideOnboarding()
	if !page.OnboardingVisible() {
		t.Fatal("required onboarding escaped before workspace completion")
	}
}

// Requirement: TUI onboarding must not save an unborn repository because normal
// sessions require managed worktrees. The threat is bypassing the backend
// prerequisite from the beginner flow; this UI action gate is the narrowest proof.
func TestOnboardingWorkspaceRejectsRepositoryWithoutInitialCommit(t *testing.T) {
	page := readyOnboardingPage()
	page.model.WorkspaceSetupGitReadiness = model.GitReadinessNeedsCommit
	page.ShowOnboardingWorkspace("Confirm workspace")
	page.HandleKey(tcell.NewEventKey(tcell.KeyEnter, 0, tcell.ModNone))
	if _, ok := page.PopHomeAction(); ok {
		t.Fatal("unborn repository queued workspace creation")
	}
	if !strings.Contains(page.onboarding.Error, "no initial commit") || !strings.Contains(page.onboarding.Error, "explicit permission") {
		t.Fatalf("unborn repository guidance = %q", page.onboarding.Error)
	}
}

func TestOnboardingWorkspaceEnterQueuesLaunchCWDAndLocksPending(t *testing.T) {
	page := readyOnboardingPage()
	page.ShowOnboardingWorkspace("Confirm workspace")
	page.HandleKey(tcell.NewEventKey(tcell.KeyEnter, 0, tcell.ModNone))

	action, ok := page.PopHomeAction()
	if !ok || action.Kind != HomeActionCreateOnboardingWorkspace {
		t.Fatalf("workspace action = %+v, ok=%v", action, ok)
	}
	if action.WorkspacePath != "/repo/project" {
		t.Fatalf("workspace path = %q", action.WorkspacePath)
	}
	page.HandleKey(tcell.NewEventKey(tcell.KeyEsc, 0, tcell.ModNone))
	if !page.OnboardingVisible() || !page.OnboardingWorkspaceActive() {
		t.Fatal("pending workspace creation must keep onboarding locked")
	}
}

func TestOnboardingWorkspaceErrorAllowsRetryAndCompletionUnlocks(t *testing.T) {
	page := readyOnboardingPage()
	page.ShowOnboardingWorkspace("Confirm workspace")
	page.HandleKey(tcell.NewEventKey(tcell.KeyEnter, 0, tcell.ModNone))
	if _, ok := page.PopHomeAction(); !ok {
		t.Fatal("initial workspace action was not queued")
	}
	page.SetOnboardingError("workspace setup failed")
	page.HandleKey(tcell.NewEventKey(tcell.KeyEnter, 0, tcell.ModNone))
	if _, ok := page.PopHomeAction(); !ok {
		t.Fatal("workspace error did not allow Enter retry")
	}
	page.CompleteOnboardingWorkspace()
	if page.OnboardingVisible() {
		t.Fatal("completed workspace setup did not unlock the main TUI")
	}
}

func TestOnboardingProviderRendersTwoCardsPerRow(t *testing.T) {
	screen := tcell.NewSimulationScreen("")
	if err := screen.Init(); err != nil {
		t.Fatalf("screen init: %v", err)
	}
	defer screen.Fini()
	screen.SetSize(100, 30)

	page := readyOnboardingPage()
	page.ShowOnboardingProvider("Choose a provider")
	page.Draw(screen)
	lines := strings.Split(dumpHomeTestScreen(screen, 100, 30), "\n")
	for _, line := range lines {
		if strings.Contains(line, "codex") && strings.Contains(line, "openai") {
			return
		}
	}
	t.Fatalf("provider onboarding did not render two cards in one row:\n%s", strings.Join(lines, "\n"))
}

func TestOnboardingRendersCohesiveThreePhaseSurface(t *testing.T) {
	screen := tcell.NewSimulationScreen("")
	if err := screen.Init(); err != nil {
		t.Fatalf("screen init: %v", err)
	}
	defer screen.Fini()
	screen.SetSize(100, 30)

	page := readyOnboardingPage()
	page.ShowOnboardingWorkspace("Confirm workspace")
	page.Draw(screen)
	text := dumpHomeTestScreen(screen, 100, 30)
	for _, want := range []string{"STEP 3 OF 3", "Create your first workspace.", "managed worktrees", "Creating workspace in", "/repo/project", "Git repository ready"} {
		if !strings.Contains(text, want) {
			t.Fatalf("workspace onboarding missing %q:\n%s", want, text)
		}
	}
}
