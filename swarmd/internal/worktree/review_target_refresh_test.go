package worktree

import (
	"context"
	"os"
	"os/exec"
	"strings"
	"testing"
	"time"
)

// Purpose: a reviewer's checkout must see the folder's target branch at its
// current tip when the review starts, so the reviewer can tell from evidence
// whether a commit in its range is already on dev. A worker job's checkout
// and its reviewer's are worktrees of the folder: the folder's branches are
// their own refs, and the job's commits are in the folder's repository the
// moment they are made.
// Threat (forge run): reviewers of a job checked out from a copy of the
// folder found dev at the job's creation base and blocked on commits that had
// already landed; a job's commits missing from the folder broke delivery.
// Owner: Service.allocateSessionWorkspaceWithBranchMode (worker job) and
// Service.allocateReviewWorkspaceContext over real local Git; the job's
// branch and the folder's HEAD must not change. No provider or sandbox.
func TestReviewCheckoutSeesTheTargetBranchAtItsCurrentTip(t *testing.T) {
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
	created := git(repo, "rev-parse", "HEAD")
	s := &Service{}
	job, err := s.allocateSessionWorkspaceWithBranchMode(repo, true, "", "agent/job", "worker-execution-target-job", true)
	if err != nil {
		t.Fatal(err)
	}
	defer removeManagedWorktree(repo, job.WorkspacePath)
	if !strings.Contains(git(repo, "worktree", "list", "--porcelain"), job.WorkspacePath) {
		t.Fatal("the worker job's checkout is not a worktree of the folder")
	}
	git(job.WorkspacePath, "commit", "--allow-empty", "-m", "job")
	head := git(job.WorkspacePath, "rev-parse", "HEAD")
	if git(repo, "rev-parse", "refs/heads/agent/job") != head {
		t.Fatal("the job's commit is not in the folder's repository")
	}

	// dev moves on in the folder after the job was created.
	tip := git(repo, "commit-tree", git(repo, "rev-parse", "dev^{tree}"), "-p", "dev", "-m", "landed elsewhere")
	git(repo, "update-ref", "refs/heads/dev", tip)

	review, err := s.allocateReviewWorkspaceContext(context.Background(), repo, "target-review", head, time.Minute, created)
	if err != nil {
		t.Fatal(err)
	}
	defer s.RemoveReviewWorkspace(repo, review.WorkspacePath, "target-review", head)
	if got := git(review.WorkspacePath, "rev-parse", "refs/heads/dev"); got != tip {
		t.Fatalf("review checkout dev = %s, want the folder's dev %s", got, tip)
	}
	if git(review.WorkspacePath, "rev-parse", "HEAD") != head || git(review.WorkspacePath, "branch", "--show-current") != "" {
		t.Fatal("review not detached at the exact reviewed head")
	}
	if git(job.WorkspacePath, "rev-parse", "HEAD") != head || git(repo, "rev-parse", "refs/heads/dev") != tip || git(repo, "symbolic-ref", "HEAD") != "refs/heads/dev" {
		t.Fatal("the review changed the job's branch or the folder")
	}
	// A head the folder's repository does not have is refused.
	if _, err := s.allocateReviewWorkspaceContext(context.Background(), repo, "missing-review", strings.Repeat("a", 40), time.Minute, created); err == nil {
		t.Fatal("a review of a commit the folder lacks was allocated")
	}
}
