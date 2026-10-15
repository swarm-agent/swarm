package startupconfig

import (
	"strings"
	"testing"
)

// Purpose: install configuration must preserve explicit sandbox resource limits
// and reject invalid/duplicate admission settings; Format and parse own the
// persisted contract, so a round-trip unit test is the narrowest proof.
func TestSandboxResourceConfig(t *testing.T) {
	cfg := Default("")
	cfg.SandboxMemory = "6g"
	cfg.WorkerMaxConcurrentRuns = 4
	parsed, _, err := parseEntries(Format(cfg), Default(""))
	if err != nil || parsed.SandboxMemory != "6g" || parsed.WorkerMaxConcurrentRuns != 4 {
		t.Fatalf("roundtrip: %+v %v", parsed, err)
	}
	for _, s := range []string{"worker_max_concurrent_runs = -1", "worker_max_concurrent_runs = 2\nworker_max_concurrent_runs = 3"} {
		if _, _, err := parseEntries(s, Default("")); err == nil {
			t.Fatalf("accepted %s", s)
		}
	}
	cfg.SandboxMemory = ""
	if _, _, err := parseEntries(Format(cfg), Default("")); err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(Format(cfg), "sandbox_memory = \n") {
		t.Fatal("automatic memory not persisted")
	}
}
