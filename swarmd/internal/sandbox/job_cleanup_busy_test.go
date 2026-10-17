package sandbox

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"testing"
)

// Purpose: a finished job's container removal that meets a daemon command
// still starting in that container (reading the job's Git state right after a
// follow-up ends) must wait and retry, not fail: a recorded failure marks the
// job blocked and hides its commits from delivery. The engine boundary proves
// no removal runs while the command is starting and that the retry removes it.
func TestEndJobDefersWhileACommandIsStarting(t *testing.T) {
	for _, busy := range []string{"preparing", "changing"} {
		t.Run(busy, func(t *testing.T) {
			runner := readyRunner()
			m := newTestManager(t, ModeRequired, runner, Config{StateDir: t.TempDir()})
			project, worktrees := t.TempDir(), t.TempDir()
			checkout := filepath.Join(worktrees, "demo", "agent-worker-job")
			if err := os.MkdirAll(checkout, 0o700); err != nil {
				t.Fatal(err)
			}
			m.SetLayout(Layout{
				Projects:      func() ([]string, error) { return []string{project}, nil },
				WorktreesRoot: worktrees,
				Bucket:        func(string) (string, error) { return "demo", nil },
			})
			scope, ok, err := m.ScopeFor(checkout)
			if err != nil || !ok || scope.JobCheckout == "" {
				t.Fatalf("job scope: %+v %v %v", scope, ok, err)
			}
			name := scopeContainerName(scope)
			m.mu.Lock()
			b := &box{name: name, root: project, generation: 1}
			if busy == "preparing" {
				b.preparing = 1
			} else {
				b.changing = make(chan struct{})
			}
			m.boxes[name] = b
			m.mu.Unlock()
			terminal := func() (bool, error) { return true, nil }
			if err := m.EndJob(context.Background(), checkout, terminal); !errors.Is(err, ErrCleanupDeferred) {
				t.Fatalf("cleanup during a starting command = %v, want a deferral", err)
			}
			if runner.ran("rm -f " + name) {
				t.Fatal("removed the container under a starting command")
			}
			m.mu.Lock()
			b.preparing = 0
			if b.changing != nil {
				close(b.changing)
				b.changing = nil
			}
			m.mu.Unlock()
			if err := m.EndJob(context.Background(), checkout, terminal); err != nil {
				t.Fatalf("retry after the command: %v", err)
			}
			if !runner.ran("rm -f " + name) {
				t.Fatal("retry did not remove the container")
			}
		})
	}
}
