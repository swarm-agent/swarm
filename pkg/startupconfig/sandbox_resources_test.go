package startupconfig

import (
	"os"
	"path/filepath"
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

// Purpose: Load/appendMissingKeys must migrate resource keys independently of
// diagnostics, preserving explicit limits and never writing duplicate keys that
// block the next startup. Real temporary config loads are the narrowest layer
// proving both persisted migration and byte-for-byte second-load idempotency.
func TestSandboxResourceMigration(t *testing.T) {
	t.Setenv(childStartupConfigEnv, "")
	for _, tc := range []struct {
		name, text, memory string
		workers            int
	}{
		{"both", "sandbox_memory = 6g\nworker_max_concurrent_runs = 4\n", "6g", 4},
		{"memory only", "sandbox_memory = 6g\n", "6g", 0},
		{"workers only", "worker_max_concurrent_runs = 4\n", "", 4},
		{"neither", "", "", 0},
		{"diagnostics present", "long_session_diagnostics = true\n", "", 0},
	} {
		t.Run(tc.name, func(t *testing.T) {
			path := filepath.Join(t.TempDir(), "swarm.conf")
			if err := os.WriteFile(path, []byte("peer_transport_port = 7379\n"+tc.text), 0600); err != nil {
				t.Fatal(err)
			}
			var first []byte
			for load := 0; load < 2; load++ {
				cfg, err := Load(path)
				if err != nil {
					t.Fatalf("load %d: %v", load+1, err)
				}
				if cfg.SandboxMemory != tc.memory || cfg.WorkerMaxConcurrentRuns != tc.workers {
					t.Fatalf("limits changed: memory=%q workers=%d", cfg.SandboxMemory, cfg.WorkerMaxConcurrentRuns)
				}
				data, err := os.ReadFile(path)
				if err != nil {
					t.Fatal(err)
				}
				for _, key := range []string{"sandbox_memory", "worker_max_concurrent_runs", "long_session_diagnostics"} {
					if n := strings.Count(string(data), key+" ="); n != 1 {
						t.Fatalf("%s occurs %d times after load %d", key, n, load+1)
					}
				}
				if load == 0 {
					first = data
				} else if string(data) != string(first) {
					t.Fatal("second load changed migrated file")
				}
			}
		})
	}
}
