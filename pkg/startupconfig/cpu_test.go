package startupconfig

import "testing"

// Purpose: Format/parseEntries must preserve CPU admission controls and reject
// malformed or duplicate values rather than silently removing resource bounds.
func TestCPUConfigRoundTrip(t *testing.T) {
	cfg := Default("")
	cfg.SandboxCPUs = "2.0625"
	cfg.SandboxCgroupParent = "swarm-sandbox.slice"
	got, _, err := parseEntries(Format(cfg), Default(""))
	if err != nil || got.SandboxCPUs != cfg.SandboxCPUs || got.SandboxCgroupParent != cfg.SandboxCgroupParent {
		t.Fatalf("%+v %v", got, err)
	}
	for _, text := range []string{"sandbox_cpus = NaN", "sandbox_cpus = 0", "sandbox_cpus = 1\nsandbox_cpus = 2"} {
		if _, _, err := parseEntries(text, Default("")); err == nil {
			t.Fatal(text)
		}
	}
}
