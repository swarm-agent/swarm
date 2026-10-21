package sandbox

import (
	"context"
	"os"
	"path/filepath"
	"testing"
)

// Purpose: with the Docker sandbox on, a job's checkout runs in its project's
// one shared sandbox, not a container of its own, and ending the job never
// removes that shared sandbox.
func TestDockerJobUsesTheProjectSandbox(t *testing.T) {
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
	if err != nil || !ok || scope.JobCheckout != "" {
		t.Fatalf("job scope: %+v %v %v", scope, ok, err)
	}
	name := scopeContainerName(scope)
	if name != ContainerName(project) {
		t.Fatalf("job container %q, want the project's %q", name, ContainerName(project))
	}
	if err := m.EndJob(context.Background(), checkout, func() (bool, error) { return true, nil }); err != nil {
		t.Fatalf("end job: %v", err)
	}
	if runner.ran("rm -f " + name) {
		t.Fatal("ending a job removed the project's sandbox")
	}
}
