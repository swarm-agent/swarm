package sandbox

import (
	"strings"
	"testing"
)

// Purpose: scopeRunArgs must preserve contention weights and confinement with
// no default agent quota, and honor explicit quotas only for agents. Inspect
// engine arguments at the narrowest container construction boundary.
func TestCPUIsolationArgs(t *testing.T) {
	for _, quota := range []string{"", "2"} {
		m := newTestManager(t, ModeRequired, readyRunner(), Config{CPUs: quota, CgroupParent: "swarm-sandbox.slice"})
		root := t.TempDir()
		image, _, err := m.sandboxImage(root)
		if err != nil {
			t.Fatal(err)
		}
		for _, daemon := range []bool{false, true} {
			name := scopeContainerName(Scope{Root: root, daemonGit: daemon})
			args := strings.Join(m.scopeRunArgs(name, root, "spec", []string{root}, SecretSetup{}, image, ""), " ")
			if strings.Contains(args, "--cpus") != (!daemon && quota != "") {
				t.Fatal(args)
			}
			shares := "256"
			if daemon {
				shares = "2048"
			}
			if !strings.Contains(args, "--cpu-shares "+shares) || !strings.Contains(args, "--cap-drop ALL") || !strings.Contains(args, "no-new-privileges") {
				t.Fatal(args)
			}
			if strings.Contains(args, "--cgroup-parent swarm-sandbox.slice") == daemon {
				t.Fatal(args)
			}
		}
	}
}

// Purpose: resolveCPU rejects invalid explicit quotas at the configuration
// boundary rather than silently disabling an operator's requested limit.
func TestCPUOverrideValidation(t *testing.T) {
	for _, value := range []string{"-1", "NaN", "Inf", "garbage", "0"} {
		if err := resolveCPU(&Config{CPUs: value}); err == nil {
			t.Fatal(value)
		}
	}
	cfg := Config{CPUs: "2.5"}
	if err := resolveCPU(&cfg); err != nil || cfg.CPUs != "2.5" {
		t.Fatalf("%+v %v", cfg, err)
	}
}
