package artifact

import "testing"

// Requirement: reviewed native particles are bounded, and historical snapshots
// retain their original budgets. Threat: forged policy or silent budget upgrades.
// ResolveAnimationProfile/ValidateAnimationProfileSnapshot are the narrow authority.
func TestAnimationSnapshotAdmission(t *testing.T) {
	p, err := ResolveAnimationProfile(&AnimationProfileInput{Profile: "motion_ui"})
	if err != nil || p.Budgets.MaxParticles != 2000 {
		t.Fatalf("profile=%+v err=%v", p, err)
	}
	if err := ValidateAnimationProfileSnapshot(p); err != nil {
		t.Fatal(err)
	}
	p.RegistryVersion = "2026-08-16.v1"
	p.Budgets.MaxParticles = 0
	before := *p
	if err := ValidateAnimationProfileSnapshot(p); err != nil || *p != before {
		t.Fatalf("historical mutated/rejected: %v", err)
	}
	p.Budgets.MaxParticles = 2000
	if ValidateAnimationProfileSnapshot(p) == nil {
		t.Fatal("forged historical budget accepted")
	}
	p.Budgets.NetworkAllowed = true
	if ValidateAnimationProfileSnapshot(p) == nil {
		t.Fatal("network override accepted")
	}
}
