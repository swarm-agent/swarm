package ui

import (
	"github.com/gdamore/tcell/v2"
	"swarm-refactor/swarmtui/internal/model"
	"testing"
)

// Requirement: handleOnboardingWorkspaceKey must require explicit consent before
// repository setup, never queue admission instead, and permit cancellation/retry.
// The UI action boundary is the narrowest proof of user intent routing.
func TestOnboardingRepositorySetupConsent(t *testing.T) {
	for _, readiness := range []model.GitReadiness{model.GitReadinessNotRepository, model.GitReadinessNeedsCommit} {
		p := readyOnboardingPage()
		p.model.WorkspaceSetupGitReadiness = readiness
		p.ShowOnboardingWorkspace("")
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
