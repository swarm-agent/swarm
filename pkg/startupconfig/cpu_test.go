package startupconfig

import "testing"

// Purpose: Format/parseEntries must preserve CPU admission controls and reject
// malformed or duplicate values rather than silently removing resource bounds.
func TestCPUConfigRoundTrip(t *testing.T) {
	cfg := Default("")
	cfg.SandboxCPUs = "2.0625"
	cfg.LandingMaxConcurrent = 8
	cfg.SandboxCgroupParent = "swarm-sandbox.slice"
	got, _, err := parseEntries(Format(cfg), Default(""))
	if err != nil || got.SandboxCPUs != cfg.SandboxCPUs || got.LandingMaxConcurrent != 8 || got.SandboxCgroupParent != cfg.SandboxCgroupParent {
		t.Fatalf("%+v %v", got, err)
	}
	for _, text := range []string{"sandbox_cpus = NaN", "sandbox_cpus = 0", "landing_max_concurrent = -1", "landing_max_concurrent = 2\nlanding_max_concurrent = 3"} {
		if _, _, err := parseEntries(text, Default("")); err == nil {
			t.Fatal(text)
		}
	}
}
