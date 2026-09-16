package ui

import (
	"strings"
	"testing"

	"github.com/gdamore/tcell/v2"
	"swarm-refactor/swarmtui/internal/client"
)

// Requirement: first-workspace setup must progress without a provider or an
// existing repository, while filesystem/Git mutations require explicit consent.
// Threat: a verify-only loop strands new installs; stale selection stages user
// files. HomePage key dispatch proves exact intent and cancellation, not backend
// admission or installed-runtime success.
func TestOnboardingGuidedWorkspacePrimaryAction(t *testing.T) {
	for _, tc := range []struct {
		state client.OnboardingRepository
		want  string
	}{
		{client.OnboardingRepository{State: "directory_missing"}, "consent"},
		{client.OnboardingRepository{State: "not_repository", CanSetup: true}, "consent"},
		{client.OnboardingRepository{State: "needs_initial_commit", CanSetup: true, NeedsReview: true}, "review"},
		{client.OnboardingRepository{State: "needs_assisted_setup", NeedsReview: true}, "review"},
		{client.OnboardingRepository{State: "ready", NeedsReview: true}, "review"},
		{client.OnboardingRepository{State: "ready", ContentReady: true}, "save"},
		{client.OnboardingRepository{State: "access_denied"}, "inspect"},
	} {
		t.Run(tc.state.State+tc.want, func(t *testing.T) {
			p := readyOnboardingPage()
			p.ShowOnboardingWorkspace("")
			tc.state.Path = "/projects/selected"
			p.SetOnboardingRepository(tc.state)
			if got := p.repositoryControls()[0].action; got != tc.want {
				t.Fatalf("next step = %s, want %s", got, tc.want)
			}
			p.HandleKey(tcell.NewEventKey(tcell.KeyEnter, 0, 0))
			action, emitted := p.PopHomeAction()
			if tc.want == "consent" {
				if emitted || !p.onboarding.SetupConsent {
					t.Fatal("selection mutated before confirmation")
				}
				p.HandleKey(tcell.NewEventKey(tcell.KeyEscape, 0, 0))
				p.HandleKey(tcell.NewEventKey(tcell.KeyRune, 'y', 0))
				if _, ok := p.PopHomeAction(); ok {
					t.Fatal("cancelled setup dispatched")
				}
			} else if !emitted || action.WorkspacePath != tc.state.Path {
				t.Fatalf("next step did not target selected path: %+v", action)
			}
		})
	}
}

// Requirement: a named new project is a visible, provider-free path
// through one disclosed confirmation, and remains pending until acknowledgement.
func TestOnboardingGuidedNewWorkspace(t *testing.T) {
	p := readyOnboardingPage()
	p.ShowOnboardingWorkspace("")
	p.SetOnboardingWorkspaceGuidance("worker", "/projects")
	if p.OnboardingWorkspacePath() != "" {
		t.Fatal("launch CWD was implicitly selected")
	}
	if p.repositoryControls()[0].action != "new" {
		t.Fatal("fresh install does not lead with creation")
	}
	p.HandleKey(tcell.NewEventKey(tcell.KeyEnter, 0, 0))
	if !p.onboarding.NamingProject || p.onboarding.ProjectName != "" {
		t.Fatal("missing blank name field")
	}
	for _, r := range "new" {
		p.HandleKey(tcell.NewEventKey(tcell.KeyRune, r, 0))
	}
	p.HandleKey(tcell.NewEventKey(tcell.KeyEscape, 0, 0))
	p.HandleKey(tcell.NewEventKey(tcell.KeyEnter, 0, 0))
	if p.onboarding.ProjectName != "new" {
		t.Fatal("back discarded draft")
	}
	for range 3 {
		p.HandleKey(tcell.NewEventKey(tcell.KeyEnter, 0, 0))
	}
	if action, ok := p.PopHomeAction(); !ok || action.Kind != HomeActionInspectOnboardingRepository || action.WorkspacePath != "/projects/new" {
		t.Fatal("named destination was not inspected")
	}
	p.SetOnboardingRepository(client.OnboardingRepository{Path: "/projects/new", State: "directory_missing"})
	p.HandleKey(tcell.NewEventKey(tcell.KeyEnter, 0, 0))
	if _, ok := p.PopHomeAction(); ok || !p.onboarding.SetupConsent || !strings.Contains(p.onboarding.Status, "No existing files") {
		t.Fatal("missing non-staging consent")
	}
	p.HandleKey(tcell.NewEventKey(tcell.KeyEnter, 0, 0))
	action, ok := p.PopHomeAction()
	if !ok || action.Kind != HomeActionSetupOnboardingRepository || action.WorkspacePath != "/projects/new" || !p.onboarding.Pending {
		t.Fatalf("setup = %+v, %v", action, ok)
	}
	p.HandleKey(tcell.NewEventKey(tcell.KeyEnter, 0, 0))
	if _, ok := p.PopHomeAction(); ok || !p.OnboardingVisible() {
		t.Fatal("pending setup replayed or released onboarding")
	}
}

// Requirement: existing repositories must be selectable, never auto-admitted.
// The UI must distinguish an empty discovery from inability to create anything.
func TestOnboardingGuidedRepositoryPicker(t *testing.T) {
	p := readyOnboardingPage()
	p.ShowOnboardingWorkspace("")
	focusRepositoryControl(p, "discover")
	p.HandleKey(tcell.NewEventKey(tcell.KeyEnter, 0, 0))
	if action, ok := p.PopHomeAction(); !ok || action.Kind != HomeActionDiscoverOnboardingRepositories {
		t.Fatal("picker did not request daemon discovery")
	}
	p.SetOnboardingRepositories([]client.WorkspaceDiscoverEntry{
		{Path: "/projects/repo", IsGitRepo: true},
		{Path: "/projects/repo", IsGitRepo: true},
		{Path: "/projects/notes", HasSwarm: true},
	})
	if len(p.onboarding.Repositories) != 1 || !strings.Contains(p.repositoryControls()[0].label, "/projects/repo") {
		t.Fatal("picker must show unique Git repositories, not arbitrary folders")
	}
	p.HandleKey(tcell.NewEventKey(tcell.KeyEnter, 0, 0))
	if action, ok := p.PopHomeAction(); !ok || action.Kind != HomeActionInspectOnboardingRepository || action.WorkspacePath != "/projects/repo" {
		t.Fatal("repository selection bypassed inspection")
	}
	p.SetOnboardingRepositories(nil)
	if !strings.Contains(p.onboarding.Status, "No Git repositories found") || p.repositoryControls()[0].action != "edit" {
		t.Fatal("empty picker has no recovery path")
	}
	p.HandleKey(tcell.NewEventKey(tcell.KeyEscape, 0, 0))
	if p.onboarding.ChoosingRepository || !p.OnboardingWorkspaceActive() {
		t.Fatal("picker back did not return to workspace creation")
	}
}

// Requirement: creation and escape controls fit the standard 80x24 terminal.
// A simulation screen proves cell layout/legibility of the primary controls;
// dispatch tests above independently prove that the labels lead to actions.
func TestOnboardingGuidedWorkspaceFitsTerminal(t *testing.T) {
	screen := tcell.NewSimulationScreen("UTF-8")
	if err := screen.Init(); err != nil {
		t.Fatal(err)
	}
	defer screen.Fini()
	screen.SetSize(80, 24)
	p := readyOnboardingPage()
	p.ShowOnboardingWorkspace("")
	p.SetOnboardingWorkspaceGuidance("worker", "/projects/new")
	p.Draw(screen)
	text := dumpHomeTestScreen(screen, 80, 24)
	for _, label := range []string{"Create a new project folder (recommended)", "Use home folder", "Use an existing Git repository", "Cancel setup / Exit", "Enter activate"} {
		if !strings.Contains(text, label) {
			t.Fatalf("missing visible action %q:\n%s", label, text)
		}
	}
	p.HandleKey(tcell.NewEventKey(tcell.KeyEnter, 0, 0))
	p.Draw(screen)
	text = dumpHomeTestScreen(screen, 80, 24)
	for _, label := range []string{"Project name:", "Parent location:", "/projects/new", "Continue to setup confirmation", "Destination:"} {
		if !strings.Contains(text, label) {
			t.Fatalf("missing setup disclosure %q:\n%s", label, text)
		}
	}
}

// Requirement: choosing home emits only inspection until explicit empty-baseline
// consent; it must not lead to file selection or silently adopt the launch CWD.
func TestOnboardingGuidedHomeChoice(t *testing.T) {
	p := readyOnboardingPage()
	p.ShowOnboardingWorkspace("")
	p.SetOnboardingWorkspaceGuidance("worker", "/users/worker")
	focusRepositoryControl(p, "home")
	p.HandleKey(tcell.NewEventKey(tcell.KeyEnter, 0, 0))
	action, ok := p.PopHomeAction()
	if !ok || action.Kind != HomeActionInspectOnboardingRepository || action.WorkspacePath != "/users/worker" {
		t.Fatal("home did not request exact inspection")
	}
	p.SetOnboardingRepository(client.OnboardingRepository{Path: "/users/worker", State: "not_repository", CanSetup: true})
	p.HandleKey(tcell.NewEventKey(tcell.KeyEnter, 0, 0))
	if _, ok := p.PopHomeAction(); ok || !p.onboarding.SetupConsent || p.onboarding.Review != nil {
		t.Fatal("home setup bypassed consent or requested file review")
	}
	p.HandleKey(tcell.NewEventKey(tcell.KeyEnter, 0, 0))
	action, ok = p.PopHomeAction()
	if !ok || action.Kind != HomeActionSetupOnboardingRepository || action.WorkspacePath != "/users/worker" {
		t.Fatal("home confirmation failed")
	}
}
