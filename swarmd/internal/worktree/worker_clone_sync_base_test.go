package worktree

import (
	"os"
	"os/exec"
	"strings"
	"testing"
)

// Requirement: in a folder that syncs with its forge, a job starts from the
// forge's base commit, and its clone's view of that branch (origin/<branch>
// and the local branch) must be that commit too, not the folder's lagging
// branch: a worker asked to rebase onto "current dev" otherwise moves its work
// onto an older commit and the delivery's recorded base is no longer its
// ancestor. Real Git through the allocator is the boundary.
func TestWorkerCloneBaseBranchIsTheSyncedCommit(t *testing.T) {
	t.Setenv("HOME", t.TempDir())
	t.Setenv("GIT_CONFIG_GLOBAL", os.DevNull)
	t.Setenv("GIT_CONFIG_NOSYSTEM", "1")
	repo := t.TempDir()
	git := func(path string, args ...string) string {
		t.Helper()
		out, err := exec.Command("git", append([]string{"-C", path, "-c", "user.name=Fixture", "-c", "user.email=fixture@example.invalid"}, args...)...).CombinedOutput()
		if err != nil {
			t.Fatalf("git %v: %v %s", args, err, out)
		}
		return strings.TrimSpace(string(out))
	}
	git(repo, "init", "-q", "-b", "dev")
	git(repo, "commit", "-q", "--allow-empty", "-m", "folder dev")
	stale := git(repo, "rev-parse", "HEAD")
	// The forge's dev moved on; the sync step fetched it beside the folder's dev.
	synced := git(repo, "commit-tree", "HEAD^{tree}", "-p", "HEAD", "-m", "merged on the forge")
	git(repo, "update-ref", "refs/swarm/publish/dev", synced)

	s := &Service{}
	a, err := s.allocateSessionWorkspaceWithBranchMode(repo, false, synced, "agent/job", "worker-execution-sync", true)
	if err != nil {
		t.Fatal(err)
	}
	if a.BaseCommit != synced || git(a.WorkspacePath, "rev-parse", "HEAD") != synced {
		t.Fatalf("job starts at %s, want the synced %s", git(a.WorkspacePath, "rev-parse", "HEAD"), synced)
	}
	if got := git(a.WorkspacePath, "rev-parse", "origin/dev"); got != stale {
		t.Fatalf("fixture: clone's origin/dev = %s, want the folder's %s before alignment", got, stale)
	}
	if err := s.AlignWorkerCloneBase(a.WorkspacePath, "dev", synced); err != nil {
		t.Fatal(err)
	}
	for _, ref := range []string{"origin/dev", "dev"} {
		if got := git(a.WorkspacePath, "rev-parse", ref); got != synced {
			t.Fatalf("clone's %s = %s, want the synced %s", ref, got, synced)
		}
	}
	if git(a.WorkspacePath, "branch", "--show-current") != "agent/job" || git(repo, "rev-parse", "dev") != stale {
		t.Fatal("alignment moved the job's branch or the folder's dev")
	}
	if err := s.AlignWorkerCloneBase(a.WorkspacePath, "dev", "HEAD"); err == nil {
		t.Fatal("aligned to a symbolic name")
	}
}
