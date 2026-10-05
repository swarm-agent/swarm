package ui

import (
	"swarm-refactor/swarmtui/internal/model"
	"testing"
)

// A persisted incomplete wizard must resume after identity rather than demand
// another bootstrap; completed state must stay dismissed on subsequent refresh.
func TestOnboardingResumesProviderAfterIdentity(t *testing.T) {
	m := model.HomeModel{OnboardingRequired: true, OnboardingIdentityBootstrapped: true, OnboardingUsername: "example", OnboardingSwarmName: "example"}
	p := NewHomePage(m)
	if !p.OnboardingProviderActive() {
		t.Fatal("incomplete identity did not resume provider setup")
	}
	p.CompleteOnboardingWorkspace()
	m.OnboardingRequired = false
	p.SetModel(m)
	if p.OnboardingVisible() {
		t.Fatal("completed onboarding reopened")
	}
}

func TestOnboardingUnbootstrappedIdentityStartsAtIdentityPhaseEvenWithPrefill(t *testing.T) {
	// Guidance prefills username and config may prefill swarm name, but identity is unbootstrapped.
	// The onboarding wizard MUST start on Step 1 (Identity), never jump to Provider phase.
	m := model.HomeModel{
		OnboardingRequired:             true,
		OnboardingIdentityBootstrapped: false,
		OnboardingUsername:             "testbench",
		OnboardingSwarmName:            "testbench",
	}
	p := NewHomePage(m)
	if p.OnboardingProviderActive() {
		t.Fatal("unbootstrapped identity jumped to provider phase")
	}
	if p.onboarding.Phase != onboardingPhaseIdentity {
		t.Fatalf("expected onboardingPhaseIdentity, got %v", p.onboarding.Phase)
	}
}
