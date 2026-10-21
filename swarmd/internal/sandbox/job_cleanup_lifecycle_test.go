package sandbox

import (
	"context"
	"errors"
	"fmt"
	"path/filepath"
	"testing"
)

// Purpose: startup inventory must remove terminal review containers, and a
// removal error must remain retryable. The engine boundary proves exact targets
// and preservation of live reviews without requiring Docker or real shutdown.
func TestTerminalReviewContainerReconcileRetry(t *testing.T) {
	runner := readyRunner()
	m := newTestManager(t, ModeRequired, runner, Config{StateDir: t.TempDir()})
	// Legacy containers prove ownership through the daemon-owned bind mount.
	runner.out["inspect"] = fmt.Sprintf(`[{"Config":{"Labels":{"swarm.sandbox":"1"}},"Mounts":[{"Type":"bind","Source":%q,"Destination":"/etc/resolv.conf"}]}]`, filepath.Join(m.cfg.StateDir, "resolv.conf"))
	dead := ReviewContainerName(t.TempDir(), t.TempDir())
	live := ReviewContainerName(t.TempDir(), t.TempDir())
	runner.out["ps -a"] = dead + "\n" + live + "\n"
	for _, container := range []string{dead, live} {
		runner.out["inspect "+container] = runner.out["inspect"]
	}
	runner.fail["rm -f"] = errors.New("engine unavailable")
	active := func(name string) (bool, error) { return name == live, nil }
	if err := m.ReconcileContainers(context.Background(), active); err == nil {
		t.Fatal("removal failure hidden")
	}
	delete(runner.fail, "rm -f")
	if err := m.ReconcileContainers(context.Background(), active); err != nil {
		t.Fatal(err)
	}
	if !runner.ran("rm -f "+dead) || runner.ran("rm -f "+live) {
		t.Fatal("incorrect terminal inventory cleanup")
	}
}

// Purpose: ReconcileContainers must leave unverifiable containers untouched and
// continue reconciling owned inventory. The engine boundary tests empty, failed
// and malformed inspect without weakening terminal-removal failure handling.
func TestReconcileSkipsUnverifiableOwnership(t *testing.T) {
	for _, mode := range []string{"empty", "failed", "malformed"} {
		t.Run(mode, func(t *testing.T) {
			runner := readyRunner()
			m := newTestManager(t, ModeRequired, runner, Config{StateDir: t.TempDir()})
			unknown := ReviewContainerName(t.TempDir(), t.TempDir())
			owned := ReviewContainerName(t.TempDir(), t.TempDir())
			runner.out["ps -a"] = unknown + "\n" + owned
			runner.out["inspect "+owned] = fmt.Sprintf(`[{"Config":{"Labels":{"swarm.instance":%q}}}]`, instanceIdentity(m.cfg.StateDir))
			if mode == "failed" {
				runner.fail["inspect "+unknown] = errors.New("unavailable")
			}
			if mode == "malformed" {
				runner.out["inspect "+unknown] = "{"
			}
			if err := m.ReconcileContainers(context.Background(), func(name string) (bool, error) {
				if name == unknown {
					t.Fatal("unowned container reached cleanup decision")
				}
				return false, nil
			}); err != nil {
				t.Fatal(err)
			}
			if runner.ran("rm -f "+unknown) || runner.ran("stop --time 5 "+unknown) {
				t.Fatal("unverifiable container mutated")
			}
			if !runner.ran("rm -f " + owned) {
				t.Fatal("remaining inventory not reconciled")
			}
		})
	}
}
