package ui

import (
	"github.com/gdamore/tcell/v2"
	"swarm-refactor/swarmtui/internal/client"
	"testing"
)

// Requirement: handleOnboardingWorkspaceKey must require explicit consent before
// repository setup, never queue admission instead, and permit cancellation/retry.
// The UI action boundary is the narrowest proof of user intent routing.
func TestOnboardingRepositorySetupConsent(t *testing.T) {
	for _, readiness := range []string{"not_repository", "needs_initial_commit"} {
		p := readyOnboardingPage()
		p.ShowOnboardingWorkspace("")
		p.SetOnboardingRepository(client.OnboardingRepository{Path: "/repo/project", State: readiness, CanSetup: true})
		focusRepositoryControl(p, "consent")
		p.HandleKey(tcell.NewEventKey(tcell.KeyEnter, 0, 0))
		if _, ok := p.PopHomeAction(); ok {
			t.Fatal("Enter mutated before consent")
		}
		p.HandleKey(tcell.NewEventKey(tcell.KeyRune, 'y', 0))
		action, ok := p.PopHomeAction()
		if !ok || action.Kind != HomeActionSetupOnboardingRepository {
			t.Fatalf("setup action: %+v", action)
		}
		p.SetOnboardingError("Git failed")
		p.HandleKey(tcell.NewEventKey(tcell.KeyRune, 'y', 0))
		if _, ok := p.PopHomeAction(); ok {
			t.Fatal("failure retained consent")
		}
		p.HandleKey(tcell.NewEventKey(tcell.KeyEsc, 0, 0))
		if !p.OnboardingProviderActive() {
			t.Fatal("back did not work")
		}
	}
}

// Requirement: changing location revokes Git consent, and cancelling an edit
// restores the selected path. Folder creation is a distinct action, never Git
// consent. Exercise the production key router rather than rendered strings.
func TestOnboardingLocationRevokesConsent(t *testing.T) {
	p := readyOnboardingPage()
	p.ShowOnboardingWorkspace("")
	p.onboarding.SetupConsent = true
	original := p.OnboardingWorkspacePath()
	p.HandleKey(tcell.NewEventKey(tcell.KeyCtrlL, 0, 0))
	p.HandleKey(tcell.NewEventKey(tcell.KeyCtrlU, 0, 0))
	for _, r := range "/new-project" {
		p.HandleKey(tcell.NewEventKey(tcell.KeyRune, r, 0))
	}
	p.HandleKey(tcell.NewEventKey(tcell.KeyEsc, 0, 0))
	if p.OnboardingWorkspacePath() != original {
		t.Fatal("cancel changed selection")
	}
	p.HandleKey(tcell.NewEventKey(tcell.KeyRune, 'y', 0))
	if _, ok := p.PopHomeAction(); ok {
		t.Fatal("location edit retained consent")
	}
	p.HandleKey(tcell.NewEventKey(tcell.KeyCtrlN, 0, 0))
	action, ok := p.PopHomeAction()
	if !ok || action.Kind != HomeActionCreateOnboardingFolder || action.WorkspacePath != original {
		t.Fatalf("folder action: %+v", action)
	}
}
