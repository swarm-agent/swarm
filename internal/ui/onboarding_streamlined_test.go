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
	if !strings.Contains(controls[0].label, "?") {
		t.Fatalf("expected first option to contain '?', got %+v", controls[0])
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

func TestStreamlinedStep4WithWorkspaceGuidanceDoesNotShowLegacyControls(t *testing.T) {
	page := NewHomePage(model.HomeModel{OnboardingRequired: true})
	// Simulate daemon startup guidance which sets ProjectParent to HomePath
	page.SetOnboardingWorkspaceGuidance("developer", "/home/developer")
	if page.onboarding.ProjectParent == "" {
		t.Fatal("expected ProjectParent to be populated by SetOnboardingWorkspaceGuidance")
	}

	page.ShowOnboardingProject("Name project")
	page.onboarding.ProjectName = "quantum-core"
	page.HandleKey(tcell.NewEventKey(tcell.KeyEnter, 0, 0))

	if !page.OnboardingWorkspaceActive() {
		t.Fatal("expected workspace phase to be active")
	}

	// Must have EXACTLY 2 controls
	controls := page.repositoryControls()
	if len(controls) != 2 {
		t.Fatalf("expected exactly 2 initial controls in Step 4 even with guidance, got %d: %+v", len(controls), controls)
	}
	if controls[0].action != "open_workspace_menu" || !strings.Contains(controls[0].label, "quantum-core") {
		t.Fatalf("expected open_workspace_menu with quantum-core, got %+v", controls[0])
	}
	if controls[1].action != "skip_to_swarm" || !strings.Contains(controls[1].label, "Skip to Talk to Swarm") {
		t.Fatalf("expected skip_to_swarm, got %+v", controls[1])
	}

	// Must NOT contain any legacy controls
	for _, c := range controls {
		switch c.action {
		case "new", "home", "discover", "consent", "inspect":
			t.Fatalf("unexpected legacy control %q in Step 4: %+v", c.action, c)
		}
	}

	// Test rendered output via simulation screen
	screen := tcell.NewSimulationScreen("UTF-8")
	if err := screen.Init(); err != nil {
		t.Fatal(err)
	}
	defer screen.Fini()
	screen.SetSize(80, 24)
	page.Draw(screen)
	rendered := dumpHomeTestScreen(screen, 80, 24)

	if !strings.Contains(rendered, "Project: quantum-core") {
		t.Fatalf("expected 'Project: quantum-core' in rendered output:\n%s", rendered)
	}
	if !strings.Contains(rendered, "Add workspaces into quantum-core?") {
		t.Fatalf("expected 'Add workspaces into quantum-core?' in rendered output:\n%s", rendered)
	}
	if !strings.Contains(rendered, "Skip to Talk to Swarm") {
		t.Fatalf("expected 'Skip to Talk to Swarm' in rendered output:\n%s", rendered)
	}
	if strings.Contains(rendered, "Runtime account:") {
		t.Fatalf("rendered output must not contain 'Runtime account:':\n%s", rendered)
	}
	if strings.Contains(rendered, "Create a new project folder") {
		t.Fatalf("rendered output must not contain legacy 'Create a new project folder':\n%s", rendered)
	}
	if strings.Contains(rendered, "Use home folder") {
		t.Fatalf("rendered output must not contain legacy 'Use home folder':\n%s", rendered)
	}
}

func TestStreamlinedStep4PersonalizingRendersCoolAnimation(t *testing.T) {
	page := NewHomePage(model.HomeModel{OnboardingRequired: true})
	page.ShowOnboardingWorkspace("Set up project")
	page.onboarding.ProjectName = "nebula-gateway"
	page.onboarding.Personalizing = true
	page.onboarding.Tick = 8

	screen := tcell.NewSimulationScreen("UTF-8")
	if err := screen.Init(); err != nil {
		t.Fatal(err)
	}
	defer screen.Fini()
	screen.SetSize(80, 24)
	page.Draw(screen)
	rendered := dumpHomeTestScreen(screen, 80, 24)

	if !strings.Contains(rendered, "PERSONALIZING NEBULA-GATEWAY") {
		t.Fatalf("expected 'PERSONALIZING NEBULA-GATEWAY' in rendered output:\n%s", rendered)
	}
	if !strings.Contains(rendered, "%") {
		t.Fatalf("expected percentage progress in rendered output:\n%s", rendered)
	}
	if !strings.Contains(rendered, "Linked workspaces indexed & mapped") {
		t.Fatalf("expected workspace indexing step in rendered output:\n%s", rendered)
	}
	if !strings.Contains(rendered, "AGENTS.md guidelines and rules extracted") {
		t.Fatalf("expected AGENTS.md extraction step in rendered output:\n%s", rendered)
	}
	if !strings.Contains(rendered, "Synthesizing PROJECT.md with AI Router") {
		t.Fatalf("expected PROJECT.md synthesis step in rendered output:\n%s", rendered)
	}
	if !strings.Contains(rendered, "Priming autonomous agent orchestrator") {
		t.Fatalf("expected agent priming step in rendered output:\n%s", rendered)
	}
}

func TestStreamlinedStep4PreFinishScreenRendersAndLaunches(t *testing.T) {
	page := NewHomePage(model.HomeModel{OnboardingRequired: true})
	page.ShowOnboardingPreFinish("proj_nebula", "nebula-gateway", []string{"/home/developer/code/repo"})

	if !page.OnboardingPreFinishActive() {
		t.Fatal("expected PreFinish to be active")
	}

	controls := page.repositoryControls()
	if len(controls) != 1 || controls[0].action != "finish_prefinish" {
		t.Fatalf("expected finish_prefinish control, got %+v", controls)
	}

	screen := tcell.NewSimulationScreen("UTF-8")
	if err := screen.Init(); err != nil {
		t.Fatal(err)
	}
	defer screen.Fini()
	screen.SetSize(80, 24)
	page.Draw(screen)
	rendered := dumpHomeTestScreen(screen, 80, 24)

	if !strings.Contains(rendered, "ALL SYSTEMS ONLINE · PROJECT READY") {
		t.Fatalf("expected header 'ALL SYSTEMS ONLINE · PROJECT READY' in rendered output:\n%s", rendered)
	}
	if !strings.Contains(rendered, "Project    :") || !strings.Contains(rendered, "nebula-gateway") {
		t.Fatalf("expected project name 'nebula-gateway' in rendered output:\n%s", rendered)
	}
	if !strings.Contains(rendered, "Workspaces :") || !strings.Contains(rendered, "repo") {
		t.Fatalf("expected workspace 'repo' in rendered output:\n%s", rendered)
	}
	if !strings.Contains(rendered, "Guidelines :") || !strings.Contains(rendered, "PROJECT.md configured & rules primed") {
		t.Fatalf("expected guidelines status in rendered output:\n%s", rendered)
	}
	if !strings.Contains(rendered, "Agent      :") || !strings.Contains(rendered, "Swarm orchestrator ready for commands") {
		t.Fatalf("expected agent orchestrator status in rendered output:\n%s", rendered)
	}
	if !strings.Contains(rendered, "Launch nebula-gateway & Talk to Swarm →") {
		t.Fatalf("expected launch button in rendered output:\n%s", rendered)
	}

	// Pressing Enter must trigger HomeActionFinishOnboardingProject
	page.HandleKey(tcell.NewEventKey(tcell.KeyEnter, 0, 0))
	action, ok := page.PopHomeAction()
	if !ok || action.Kind != HomeActionFinishOnboardingProject {
		t.Fatalf("expected HomeActionFinishOnboardingProject action, got %+v", action)
	}
	if action.ProjectID != "proj_nebula" {
		t.Fatalf("expected project ID 'proj_nebula', got %q", action.ProjectID)
	}
	if action.ProjectName != "nebula-gateway" {
		t.Fatalf("expected project name 'nebula-gateway', got %q", action.ProjectName)
	}
}

func TestStreamlinedStep4PreFinishAutoAdvancesOnTicks(t *testing.T) {
	page := NewHomePage(model.HomeModel{OnboardingRequired: true})
	page.ShowOnboardingPreFinish("proj_auto", "auto-project", nil)

	screen := tcell.NewSimulationScreen("UTF-8")
	if err := screen.Init(); err != nil {
		t.Fatal(err)
	}
	defer screen.Fini()
	screen.SetSize(80, 24)
	page.Draw(screen)
	rendered := dumpHomeTestScreen(screen, 80, 24)

	if !strings.Contains(rendered, "Standalone (No workspaces attached)") {
		t.Fatalf("expected standalone text for nil workspaces in rendered output:\n%s", rendered)
	}

	// Tick 19 times: must not yet auto-advance
	for i := 0; i < 19; i++ {
		page.HandleTick()
	}
	if _, ok := page.PopHomeAction(); ok {
		t.Fatal("must not auto-advance before 20 ticks")
	}

	// 20th tick: must auto-advance and emit HomeActionFinishOnboardingProject
	page.HandleTick()
	action, ok := page.PopHomeAction()
	if !ok || action.Kind != HomeActionFinishOnboardingProject {
		t.Fatalf("expected auto-advance on 20th tick, got %+v", action)
	}
	if action.ProjectID != "proj_auto" {
		t.Fatalf("expected project ID 'proj_auto', got %q", action.ProjectID)
	}
	if action.ProjectName != "auto-project" {
		t.Fatalf("expected project name 'auto-project', got %q", action.ProjectName)
	}
}
