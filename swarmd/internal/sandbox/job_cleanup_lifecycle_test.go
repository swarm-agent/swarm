package sandbox

import (
	"context"
	"errors"
	"testing"
)

// Purpose: startup inventory must remove terminal review containers, and a
// removal error must remain retryable. The engine boundary proves exact targets
// and preservation of live reviews without requiring Docker or real shutdown.
func TestTerminalReviewContainerReconcileRetry(t *testing.T) {
	runner := readyRunner()
	m := newTestManager(t, ModeRequired, runner, Config{})
	dead := ReviewContainerName(t.TempDir(), t.TempDir())
	live := ReviewContainerName(t.TempDir(), t.TempDir())
	runner.out["ps -a"] = dead + "\n" + live + "\n"
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
