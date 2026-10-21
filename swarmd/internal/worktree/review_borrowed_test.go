package worktree

import (
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"

	"swarm/packages/swarmd/internal/sandbox"
)

// Purpose: on a machine whose owner turned the sandbox off (no review
// sandbox registers anything), a reviewer reading the job's own worktree in
// place is still bound to that worktree, its owner's lane on its branch, at
// exactly the reviewed head: it is admitted there and refused once the
// worktree moved or for its own session as owner (the owning session itself
// is checked by the store's borrowed-lane binding).
// Threat: reviewers that cannot run at all where agents run on the machine
// (the headless default), or a sandbox-off reviewer admitted on a worktree
// that is not the reviewed head.
// Owners: ValidateBorrowedReviewExecutionIdentity with sandbox.OwnerTurnedOff
// (no default sandbox manager) over real Git.
func TestBorrowedReviewWithTheSandboxOff(t *testing.T) {
	if !sandbox.OwnerTurnedOff() {
		t.Skip("a default sandbox manager is set in this test binary")
	}
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
	git(repo, "init", "-b", "dev")
	git(repo, "commit", "--allow-empty", "-m", "base")
	s := &Service{}
	const owner = "task-session-coder"
	coder, err := s.allocateSessionWorkspaceWithBranchMode(repo, true, "", "agent/task-coder", owner, true)
	if err != nil {
		t.Fatal(err)
	}
	probe := filepath.Join(coder.WorkspacePath, "probe.md")
	if err := os.WriteFile(probe, []byte("Reviewed in place.\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	git(coder.WorkspacePath, "add", "probe.md")
	git(coder.WorkspacePath, "commit", "-m", "probe")
	head := git(coder.WorkspacePath, "rev-parse", "HEAD")

	if err := ValidateBorrowedReviewExecutionIdentity(repo, coder.WorkspacePath, owner, "worker-execution-review", coder.BranchName, head); err != nil {
		t.Fatalf("reviewer at the reviewed head refused with the sandbox off: %v", err)
	}
	for name, try := range map[string]func() error{
		"itself as owner": func() error {
			return ValidateBorrowedReviewExecutionIdentity(repo, coder.WorkspacePath, owner, owner, coder.BranchName, head)
		},
	} {
		if try() == nil {
			t.Fatalf("%s admitted", name)
		}
	}
	if err := os.WriteFile(probe, []byte("Reviewed in place.\nSecond round.\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	git(coder.WorkspacePath, "commit", "-qam", "second round")
	if err := ValidateBorrowedReviewExecutionIdentity(repo, coder.WorkspacePath, owner, "worker-execution-review", coder.BranchName, head); err == nil || !strings.Contains(err.Error(), "moved") {
		t.Fatalf("moved worktree = %v, want refused", err)
	}
}
