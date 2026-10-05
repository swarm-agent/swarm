package ui

import (
	"testing"

	"github.com/gdamore/tcell/v2"
	"swarm-refactor/swarmtui/internal/client"
	"swarm-refactor/swarmtui/internal/model"
)

// Requirement: Streamlined Project-First Onboarding without Git/Workspace Requirement:
// 1. Phase 1 (Identity): Username input only; root user account creation preserved.
// 2. Phase 2 (Provider): Down arrow past last provider reaches [Skip for now]; Enter advances to Phase 3.
// 3. Phase 3 (Project): Only Project Name is prompted (no instructions box); Enter advances to Phase 4.
// 4. Phase 4 (Workspaces): Discovered folders displayed with multi-select [x] / [ ]; Space toggles;
//    Enter on [Add Selected Workspaces & Finish] emits project creation with selected workspaces;
//    [Continue without Workspaces (Skip)] completes onboarding with zero workspaces.
func TestStreamlinedProjectFirstOnboardingFlow(t *testing.T) {
	page := NewHomePage(model.HomeModel{OnboardingRequired: true})

	// Step 1: Phase 1 (Identity)
	page.model.OnboardingUsername = "developer"
	page.onboarding.Focus = onboardingFocusUsername
	// Press Enter on Username: focus moves to SwarmName
	page.HandleKey(tcell.NewEventKey(tcell.KeyEnter, 0, 0))
	if page.onboarding.Focus != onboardingFocusSwarmName {
		t.Fatalf("expected focus on swarm name, got %v", page.onboarding.Focus)
	}
	page.model.OnboardingSwarmName = "developer-swarm"
	// Press Enter to submit identity
	page.HandleKey(tcell.NewEventKey(tcell.KeyEnter, 0, 0))
	action, ok := page.PopHomeAction()
	if !ok || action.Kind != HomeActionSaveOnboarding {
		t.Fatalf("expected identity save action, got %+v", action)
	}
	if action.Username != "developer" {
		t.Fatalf("expected username 'developer', got %q", action.Username)
	}

	// Step 2: Phase 2 (Provider)
	page.SetOnboardingRequired(false, "developer", "developer-swarm")
	page.SetAuthModalData([]AuthModalProvider{{ID: "gemini"}, {ID: "anthropic"}}, nil)
	page.ShowOnboardingProvider("Identity saved")
	if !page.OnboardingProviderActive() {
		t.Fatal("expected provider phase to be active")
	}

	// Press Down on the last provider: should move to [Skip for now] (ActionIndex 1)
	page.authModal.SelectedProvider = 1 // on anthropic (last item)
	page.HandleKey(tcell.NewEventKey(tcell.KeyDown, 0, 0))
	if page.onboarding.ActionIndex != 1 {
		t.Fatalf("expected ActionIndex 1 (Skip for now), got %d", page.onboarding.ActionIndex)
	}
	// Press Enter on [Skip for now]: should advance to Phase 3 (Project)
	page.HandleKey(tcell.NewEventKey(tcell.KeyEnter, 0, 0))
	if !page.OnboardingProjectActive() {
		t.Fatal("expected project phase to be active after skipping provider")
	}

	// Step 3: Phase 3 (Project Name)
	page.onboarding.ProjectName = "my-awesome-app"
	// Press Enter on Project Name: advances directly to Phase 4 (Workspaces)
	page.HandleKey(tcell.NewEventKey(tcell.KeyEnter, 0, 0))
	if !page.OnboardingWorkspaceActive() {
		t.Fatal("expected workspace phase to be active after entering project name")
	}
	if page.onboarding.ProjectName != "my-awesome-app" {
		t.Fatalf("expected project name 'my-awesome-app', got %q", page.onboarding.ProjectName)
	}

	// Step 4: Phase 4 (Workspaces Multi-Select)
	page.SetOnboardingRepositories([]client.WorkspaceDiscoverEntry{
		{Path: "/home/developer/code/frontend", IsGitRepo: true},
		{Path: "/home/developer/code/backend", IsGitRepo: true},
	})
	// Initially, ChoosingRepository is true with discovered repos
	if len(page.onboarding.Repositories) != 2 {
		t.Fatalf("expected 2 repositories, got %d", len(page.onboarding.Repositories))
	}

	// Toggle selection on first repo with Space
	page.onboarding.ActionIndex = 0
	page.HandleKey(tcell.NewEventKey(tcell.KeyRune, ' ', 0))
	if !page.onboarding.Selected["/home/developer/code/frontend"] {
		t.Fatal("expected frontend repo to be selected after pressing Space")
	}

	// Navigate to [Add Selected Workspaces & Finish]
	controls := page.repositoryControls()
	saveIdx := -1
	for i, c := range controls {
		if c.action == "save" {
			saveIdx = i
			break
		}
	}
	if saveIdx == -1 {
		t.Fatal("expected save action control to exist")
	}
	page.onboarding.ActionIndex = saveIdx
	page.HandleKey(tcell.NewEventKey(tcell.KeyEnter, 0, 0))

	projAction, ok := page.PopHomeAction()
	if !ok || projAction.Kind != HomeActionCreateOnboardingProject {
		t.Fatalf("expected create onboarding project action, got %+v", projAction)
	}
	if projAction.ProjectName != "my-awesome-app" {
		t.Fatalf("expected project name 'my-awesome-app', got %q", projAction.ProjectName)
	}
	if len(projAction.WorkspacePaths) != 1 || projAction.WorkspacePaths[0] != "/home/developer/code/frontend" {
		t.Fatalf("expected selected workspace '/home/developer/code/frontend', got %+v", projAction.WorkspacePaths)
	}
}

func TestStreamlinedProjectFirstOnboardingSkipWorkspaces(t *testing.T) {
	page := NewHomePage(model.HomeModel{OnboardingRequired: true})
	page.ShowOnboardingProject("Name project")
	page.onboarding.ProjectName = "zero-workspace-project"
	page.HandleKey(tcell.NewEventKey(tcell.KeyEnter, 0, 0))

	// In Phase 4, navigate to skip control
	controls := page.repositoryControls()
	skipIdx := -1
	for i, c := range controls {
		if c.action == "skip" {
			skipIdx = i
			break
		}
	}
	if skipIdx == -1 {
		t.Fatal("expected skip control to exist")
	}
	page.onboarding.ActionIndex = skipIdx
	page.HandleKey(tcell.NewEventKey(tcell.KeyEnter, 0, 0))

	projAction, ok := page.PopHomeAction()
	if !ok || projAction.Kind != HomeActionCreateOnboardingProject {
		t.Fatalf("expected create onboarding project action, got %+v", projAction)
	}
	if len(projAction.WorkspacePaths) != 0 {
		t.Fatalf("expected zero workspaces on skip, got %+v", projAction.WorkspacePaths)
	}
}
