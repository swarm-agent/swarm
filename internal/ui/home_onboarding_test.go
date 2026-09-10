package ui

import (
	"strings"
	"testing"

	"github.com/gdamore/tcell/v2"

	"swarm-refactor/swarmtui/internal/model"
)

func readyOnboardingPage() *HomePage {
	page := NewHomePage(model.HomeModel{
		OnboardingRequired:         true,
		OnboardingUsername:         "alice",
		OnboardingSwarmName:        "Local Swarm",
		CWD:                        "/repo/project",
		WorkspaceSetupHasGit:       true,
		WorkspaceSetupGitReadiness: model.GitReadinessReady,
	})
	page.SetAuthModalData([]AuthModalProvider{{ID: "codex"}, {ID: "openai"}}, nil)
	return page
}

func TestOnboardingIdentityAdvancesOnlyAfterSaveCompletion(t *testing.T) {
	page := NewHomePage(model.HomeModel{OnboardingRequired: true})
	// Unsaved typed names are distinct from persisted identity on resume.
	page.model.OnboardingUsername = "alice"
	page.model.OnboardingSwarmName = "Local Swarm"
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

// Requirement: TUI onboarding must not admit an unborn repository before explicit
// setup consent creates HEAD. The UI action gate proves Enter requests only
// daemon inspection and does not bypass canonical managed-worktree admission.
func TestOnboardingWorkspaceRejectsRepositoryWithoutInitialCommit(t *testing.T) {
	page := readyOnboardingPage()
	page.model.WorkspaceSetupGitReadiness = model.GitReadinessNeedsCommit
	page.ShowOnboardingWorkspace("Confirm workspace")
	page.HandleKey(tcell.NewEventKey(tcell.KeyEnter, 0, tcell.ModNone))
	if action, ok := page.PopHomeAction(); !ok || action.Kind != HomeActionInspectOnboardingRepository {
		t.Fatalf("must inspect before mutation: %+v", action)
	}
}

// Requirement: an inconclusive TUI-local Git check must reach the authenticated
// repository inspection gate before any workspace-add mutation.
// Threat: namespace or ownership constraints can make client-side Git return an
// indeterminate status for a repository the daemon can validate, permanently
// blocking first-run onboarding before the canonical authority is consulted.
func TestOnboardingWorkspaceIndeterminateReadinessQueuesCanonicalAdmission(t *testing.T) {
	for _, readiness := range []model.GitReadiness{model.GitReadinessUnknown, model.GitReadinessCheckFailed} {
		page := readyOnboardingPage()
		page.model.WorkspaceSetupGitReadiness = readiness
		page.ShowOnboardingWorkspace("Confirm workspace")
		page.HandleKey(tcell.NewEventKey(tcell.KeyEnter, 0, tcell.ModNone))
		action, ok := page.PopHomeAction()
		if !ok || action.Kind != HomeActionInspectOnboardingRepository || action.WorkspacePath != "/repo/project" {
			t.Fatalf("readiness %q action = %+v, ok=%v", readiness, action, ok)
		}
	}
}

func TestOnboardingWorkspaceEnterQueuesLaunchCWDAndLocksPending(t *testing.T) {
	page := readyOnboardingPage()
	page.ShowOnboardingWorkspace("Confirm workspace")
	page.HandleKey(tcell.NewEventKey(tcell.KeyEnter, 0, tcell.ModNone))

	action, ok := page.PopHomeAction()
	if !ok || action.Kind != HomeActionInspectOnboardingRepository {
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
	for _, want := range []string{"STEP 3 OF 3", "Create your first workspace.", "managed worktrees", "Select another location", "/repo/project", "Verify / Retry selected folder"} {
		if !strings.Contains(text, want) {
			t.Fatalf("workspace onboarding missing %q:\n%s", want, text)
		}
	}
}

// Requirement: daemon guidance must not replace a chosen project or authorize
// filesystem mutation. HomePage's real key handler is the narrow consent boundary;
// cancellation and repeated Enter must emit no setup action before explicit y.
func TestOnboardingDaemonSuggestionRequiresSelectionAndConsent(t *testing.T) {
	page := readyOnboardingPage()
	page.ShowOnboardingWorkspace("")
	original := page.OnboardingWorkspacePath()
	page.SetOnboardingWorkspaceGuidance("worker", "/projects/new-workspace")
	if page.OnboardingWorkspacePath() != original {
		t.Fatal("guidance replaced the selected project")
	}
	page.HandleKey(tcell.NewEventKey(tcell.KeyCtrlS, 0, tcell.ModNone))
	if page.OnboardingWorkspacePath() != "/projects/new-workspace" {
		t.Fatal("explicit suggestion selection failed")
	}
	if _, ok := page.PopHomeAction(); ok {
		t.Fatal("selection authorized mutation")
	}
	page.HandleKey(tcell.NewEventKey(tcell.KeyEscape, 0, tcell.ModNone))
	page.HandleKey(tcell.NewEventKey(tcell.KeyRune, 's', tcell.ModNone))
	page.HandleKey(tcell.NewEventKey(tcell.KeyRune, 'y', tcell.ModNone))
	if _, ok := page.PopHomeAction(); ok {
		t.Fatal("cancelled consent remained usable")
	}
	page.HandleKey(tcell.NewEventKey(tcell.KeyCtrlS, 0, tcell.ModNone))
	page.HandleKey(tcell.NewEventKey(tcell.KeyRune, 'y', tcell.ModNone))
	action, ok := page.PopHomeAction()
	if !ok || action.Kind != HomeActionSetupOnboardingRepository || action.WorkspacePath != "/projects/new-workspace" {
		t.Fatalf("consented setup = %+v, %v", action, ok)
	}
}
