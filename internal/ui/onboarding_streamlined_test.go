package ui

import (
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/gdamore/tcell/v2"
	"swarm-refactor/swarmtui/internal/client"
	"swarm-refactor/swarmtui/internal/model"
)

// Requirement: Streamlined Project-First Onboarding without Git/Workspace Requirement:
// 1. Phase 1 (Identity): Username input only; root user account creation preserved.
// 2. Phase 2 (Provider): Down arrow past last provider reaches [Skip for now]; Enter advances to Phase 3.
// 3. Phase 3 (Project): Project Name is required (no "default" fallback or placeholder, empty input rejected).
// 4. Phase 4 (Workspaces): Retains project name; shows 2 options: "Add workspaces into <projectname>?" OR "Skip to Talk to Swarm".
// 5. If "Add workspaces" chosen: shows workspace menu with AGENTS.md workspaces, launch recommendation, multi-select, and new folder creation.
// 6. Submitting selected workspaces activates "Personalizing" state.
func TestStreamlinedProjectNameRequiresNonEmpty(t *testing.T) {
	page := NewHomePage(model.HomeModel{OnboardingRequired: true})
	page.ShowOnboardingProject("Name your project")

	// Empty project name: pressing Enter must show error and NOT advance
	page.onboarding.ProjectName = ""
	page.HandleKey(tcell.NewEventKey(tcell.KeyEnter, 0, 0))
	if !page.OnboardingProjectActive() {
		t.Fatal("expected project phase to remain active when name is empty")
	}
	if page.onboarding.Error == "" {
		t.Fatal("expected validation error for empty project name")
	}
	if page.onboarding.ProjectName == "default" {
		t.Fatal("project name must never default to 'default'")
	}

	// Entering spaces: must also reject
	page.onboarding.ProjectName = "   "
	page.HandleKey(tcell.NewEventKey(tcell.KeyEnter, 0, 0))
	if !page.OnboardingProjectActive() {
		t.Fatal("expected project phase to remain active when name is whitespace")
	}
	if page.onboarding.ProjectName == "default" {
		t.Fatal("project name must never default to 'default'")
	}

	// Valid project name: pressing Enter advances to Phase 4 (Workspaces)
	page.onboarding.ProjectName = "my-awesome-app"
	page.HandleKey(tcell.NewEventKey(tcell.KeyEnter, 0, 0))
	if !page.OnboardingWorkspaceActive() {
		t.Fatal("expected workspace phase to be active after entering valid project name")
	}
	if page.onboarding.ProjectName != "my-awesome-app" {
		t.Fatalf("expected project name 'my-awesome-app', got %q", page.onboarding.ProjectName)
	}
}

func TestStreamlinedStep4InitialTwoChoices(t *testing.T) {
	page := NewHomePage(model.HomeModel{OnboardingRequired: true})
	page.ShowOnboardingProject("Name project")
	page.onboarding.ProjectName = "rocket-ship"
	page.HandleKey(tcell.NewEventKey(tcell.KeyEnter, 0, 0))

	if !page.OnboardingWorkspaceActive() {
		t.Fatal("expected workspace phase to be active")
	}

	// Exactly 2 initial controls in Step 4
	controls := page.repositoryControls()
	if len(controls) != 2 {
		t.Fatalf("expected exactly 2 initial controls in Step 4, got %d: %+v", len(controls), controls)
	}
	if controls[0].action != "open_workspace_menu" || !strings.Contains(controls[0].label, "rocket-ship") {
		t.Fatalf("expected first option to be 'Add workspaces into rocket-ship', got %+v", controls[0])
	}
	if controls[1].action != "skip_to_swarm" || !strings.Contains(controls[1].label, "Skip to Talk to Swarm") {
		t.Fatalf("expected second option to be 'Skip to Talk to Swarm', got %+v", controls[1])
	}

	// Selecting "Skip to Talk to Swarm" (ActionIndex 1)
	page.onboarding.ActionIndex = 1
	page.HandleKey(tcell.NewEventKey(tcell.KeyEnter, 0, 0))

	action, ok := page.PopHomeAction()
	if !ok || action.Kind != HomeActionCreateOnboardingProject {
		t.Fatalf("expected create onboarding project action, got %+v", action)
	}
	if action.ProjectName != "rocket-ship" {
		t.Fatalf("expected project name 'rocket-ship', got %q", action.ProjectName)
	}
	if len(action.WorkspacePaths) != 0 {
		t.Fatalf("expected 0 workspaces on skip, got %+v", action.WorkspacePaths)
	}
	if page.onboarding.Personalizing {
		t.Fatal("skipping workspaces must not activate personalizing state")
	}
}

func TestStreamlinedStep4WorkspaceAdditionMenuAndMultiSelect(t *testing.T) {
	tmpDir := t.TempDir()
	agentsDir := filepath.Join(tmpDir, "agents-repo")
	_ = os.MkdirAll(agentsDir, 0755)
	_ = os.WriteFile(filepath.Join(agentsDir, "AGENTS.md"), []byte("# Agents"), 0644)

	launchDir := filepath.Join(tmpDir, "launch-repo")
	_ = os.MkdirAll(launchDir, 0755)

	otherDir := filepath.Join(tmpDir, "other-repo")
	_ = os.MkdirAll(otherDir, 0755)

	page := NewHomePage(model.HomeModel{
		OnboardingRequired: true,
		CWD:                launchDir,
	})
	page.ShowOnboardingProject("Name project")
	page.onboarding.ProjectName = "hyper-drive"
	page.HandleKey(tcell.NewEventKey(tcell.KeyEnter, 0, 0))

	// Step 4 initial view: activate [ Add workspaces into hyper-drive ]
	page.onboarding.ActionIndex = 0
	page.HandleKey(tcell.NewEventKey(tcell.KeyEnter, 0, 0))

	if !page.onboarding.AddingWorkspaces {
		t.Fatal("expected AddingWorkspaces to be true after selecting add workspaces")
	}
	action, ok := page.PopHomeAction()
	if !ok || action.Kind != HomeActionDiscoverOnboardingRepositories {
		t.Fatalf("expected discover action, got %+v", action)
	}

	// Feed discovered repositories
	page.SetOnboardingRepositories([]client.WorkspaceDiscoverEntry{
		{Path: otherDir, Name: "other-repo"},
		{Path: agentsDir, Name: "agents-repo", HasSwarm: true},
	})

	// AGENTS.md workspace must be prioritized first
	if len(page.onboarding.Repositories) < 2 {
		t.Fatalf("expected at least 2 repositories, got %d", len(page.onboarding.Repositories))
	}
	if page.onboarding.Repositories[0].Path != agentsDir {
		t.Fatalf("expected AGENTS.md workspace %q to be sorted first, got %q", agentsDir, page.onboarding.Repositories[0].Path)
	}

	controls := page.repositoryControls()
	// Controls should include repository items, [ + Create a new folder… ], [ Add Selected Workspaces & Finish ], [ Back ]
	if !strings.Contains(controls[0].label, "[AGENTS.md]") {
		t.Fatalf("expected first item to have [AGENTS.md] tag, got %q", controls[0].label)
	}

	// Toggle selection on the first repository with Space
	page.onboarding.ActionIndex = 0
	page.HandleKey(tcell.NewEventKey(tcell.KeyRune, ' ', 0))
	if !page.onboarding.Selected[agentsDir] {
		t.Fatalf("expected %q to be selected after pressing Space", agentsDir)
	}

	// Find and activate [ Add Selected Workspaces & Finish ]
	finishIdx := -1
	for i, c := range controls {
		if c.action == "finish_workspaces" {
			finishIdx = i
			break
		}
	}
	if finishIdx == -1 {
		t.Fatal("expected finish_workspaces action control to exist")
	}
	page.onboarding.ActionIndex = finishIdx
	page.HandleKey(tcell.NewEventKey(tcell.KeyEnter, 0, 0))

	projAction, ok := page.PopHomeAction()
	if !ok || projAction.Kind != HomeActionCreateOnboardingProject {
		t.Fatalf("expected create onboarding project action, got %+v", projAction)
	}
	if projAction.ProjectName != "hyper-drive" {
		t.Fatalf("expected project name 'hyper-drive', got %q", projAction.ProjectName)
	}
	if len(projAction.WorkspacePaths) != 1 || projAction.WorkspacePaths[0] != agentsDir {
		t.Fatalf("expected selected workspace %q, got %+v", agentsDir, projAction.WorkspacePaths)
	}
	if !page.onboarding.Personalizing {
		t.Fatal("adding workspaces must activate personalizing state")
	}
}

func TestStreamlinedStep4CreateNewFolder(t *testing.T) {
	tmpDir := t.TempDir()
	newTarget := filepath.Join(tmpDir, "brand-new-folder")

	page := NewHomePage(model.HomeModel{OnboardingRequired: true})
	page.ShowOnboardingProject("Name project")
	page.onboarding.ProjectName = "new-folder-proj"
	page.HandleKey(tcell.NewEventKey(tcell.KeyEnter, 0, 0))

	// Open workspace addition menu
	page.onboarding.ActionIndex = 0
	page.HandleKey(tcell.NewEventKey(tcell.KeyEnter, 0, 0))

	controls := page.repositoryControls()
	createIdx := -1
	for i, c := range controls {
		if c.action == "create_folder" {
			createIdx = i
			break
		}
	}
	if createIdx == -1 {
		t.Fatal("expected create_folder control to exist")
	}

	// Activate [ + Create a new folder… ]
	page.onboarding.ActionIndex = createIdx
	page.HandleKey(tcell.NewEventKey(tcell.KeyEnter, 0, 0))

	if !page.onboarding.CreatingFolder {
		t.Fatal("expected CreatingFolder to be true")
	}

	// Type the new folder path
	for _, r := range newTarget {
		page.HandleKey(tcell.NewEventKey(tcell.KeyRune, r, 0))
	}
	if page.onboarding.NewFolderPath != newTarget {
		t.Fatalf("expected NewFolderPath %q, got %q", newTarget, page.onboarding.NewFolderPath)
	}

	// Press Enter to create folder
	page.HandleKey(tcell.NewEventKey(tcell.KeyEnter, 0, 0))

	if page.onboarding.CreatingFolder {
		t.Fatal("expected CreatingFolder to be false after submission")
	}

	// Verify directory was created on disk
	info, err := os.Stat(newTarget)
	if err != nil || !info.IsDir() {
		t.Fatalf("expected directory %q to be created on disk, err: %v", newTarget, err)
	}

	// Verify it was selected
	if !page.onboarding.Selected[newTarget] {
		t.Fatalf("expected %q to be selected in onboarding state", newTarget)
	}
}
