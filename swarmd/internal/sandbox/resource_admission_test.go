package sandbox

import "testing"

// Purpose: ResourceDefaults owns worker admission. Exercise its arithmetic at
// the unit boundary to prevent CPU count from suppressing RAM-funded agents.
func TestResourceAdmissionRAMOnly(t *testing.T) {
	for _, cpus := range []int{1, 2, 64} {
		workers, budget := ResourceDefaults(cpus, 64*gib)
		if workers != 16 || budget != 3*gib {
			t.Fatalf("cpus=%d: workers=%d budget=%d", cpus, workers, budget)
		}
	}
}

// Purpose: resolveCPU owns agent quota defaults. Empty settings must leave idle
// host CPU available, while explicit overrides remain supported.
func TestCPUDefaultUnrestricted(t *testing.T) {
	cfg := Config{}
	if err := resolveCPU(&cfg); err != nil {
		t.Fatal(err)
	}
	if cfg.CPUs != "" {
		t.Fatalf("derived quota %q", cfg.CPUs)
	}
}
