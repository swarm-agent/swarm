package ui

import (
	"swarm-refactor/swarmtui/internal/model"
	"testing"
)

// A persisted incomplete wizard must resume after identity rather than demand
// another bootstrap; completed state must stay dismissed on subsequent refresh.
func TestOnboardingResumesProviderAfterIdentity(t *testing.T) {
	m := model.HomeModel{OnboardingRequired: true, OnboardingUsername: "example", OnboardingSwarmName: "example"}
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
