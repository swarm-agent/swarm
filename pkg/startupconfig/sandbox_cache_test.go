package startupconfig

import "testing"

// Purpose: Format/parseEntries own durable box settings. Round-trip and negative
// inputs prevent losing a cache limit or accepting an ambiguous/negative cap.
func TestSandboxCacheConfig(t *testing.T) {
	cfg := Default("")
	cfg.SandboxProjectCacheBytes = 123456
	got, _, err := parseEntries(Format(cfg), Default(""))
	if err != nil || got.SandboxProjectCacheBytes != cfg.SandboxProjectCacheBytes {
		t.Fatalf("roundtrip: %+v %v", got, err)
	}
	for _, input := range []string{"sandbox_project_cache_bytes = -1", "sandbox_project_cache_bytes = nope", "sandbox_project_cache_bytes = 1\nsandbox_project_cache_bytes = 2"} {
		if _, _, err := parseEntries(input, Default("")); err == nil {
			t.Fatalf("accepted %q", input)
		}
	}
}
