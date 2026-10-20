package sandbox

import "testing"

// Purpose: resolveResources owns default caps. Without owner settings a
// sandbox has no memory cap and the box admits every worker run; explicit
// settings still apply.
func TestResourceDefaultsUncapped(t *testing.T) {
	cfg := Config{}
	if err := resolveResources(&cfg); err != nil {
		t.Fatal(err)
	}
	if cfg.Memory != "" || cfg.MaxWorkerRuns != 0 || cfg.PidsLimit != 0 {
		t.Fatalf("derived caps memory=%q runs=%d pids=%d", cfg.Memory, cfg.MaxWorkerRuns, cfg.PidsLimit)
	}
	set := Config{Memory: "2g", MaxWorkerRuns: 3}
	if err := resolveResources(&set); err != nil || set.Memory != "2g" || set.MaxWorkerRuns != 3 {
		t.Fatalf("owner settings changed: %+v %v", set, err)
	}
	m := &Manager{cfg: Config{}}
	if args := m.limitArgs(); len(args) != 0 {
		t.Fatalf("default container caps %v", args)
	}
	m.cfg = Config{Memory: "2g", PidsLimit: 512}
	if got := m.limitArgs(); len(got) != 6 {
		t.Fatalf("owner caps not applied: %v", got)
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
