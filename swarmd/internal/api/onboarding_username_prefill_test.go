package api

import (
	"swarm/packages/swarmd/internal/workspace"
	"testing"
)

// Requirement: trusted OS-account guidance only suggests the initial name; it
// never creates product identity or overwrites a configured owner. The payload
// helper is the narrowest deterministic layer for these suggestion semantics.
func TestOnboardingUsernamePrefill(t *testing.T) {
	for _, tc := range []struct {
		name    string
		initial onboardingIdentityPayload
		runtime string
		nonroot bool
		want    string
	}{
		{"new", onboardingIdentityPayload{}, "developer", true, "developer"},
		{"existing", onboardingIdentityPayload{Bootstrapped: true, Username: "owner"}, "developer", true, "owner"},
		{"typed", onboardingIdentityPayload{Username: "chosen"}, "developer", true, "chosen"},
		{"root", onboardingIdentityPayload{}, "root", false, ""},
		{"service", onboardingIdentityPayload{}, "swarm", true, ""},
	} {
		t.Run(tc.name, func(t *testing.T) {
			p := tc.initial
			prefillOnboardingUsername(&p, workspace.RuntimeWorkspaceGuidance{RuntimeUsername: tc.runtime, RuntimeNonRoot: tc.nonroot})
			if p.Username != tc.want || p.Bootstrapped != tc.initial.Bootstrapped || p.UserID != "" {
				t.Fatalf("unexpected identity: %+v", p)
			}
		})
	}
}
