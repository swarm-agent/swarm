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
// whether a commit in its range is already on dev. The review checkout is
// cloned from the job's own clone, whose branches stop at the job's creation;
// its origin is the folder, so its refs/remotes/origin/<branch> must be read
// from the folder.
// Threat (forge run): every reviewer of a rebased job found
// refs/remotes/origin/dev at the job's creation base and could not confirm
// what dev contained, so it blocked on commits that had already landed.
// Owner: Service.allocateReviewWorkspaceContext over real local Git (folder,
// managed job clone, review checkout); the job clone and folder must not
// change. No provider or sandbox is involved.
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
	// A worker job's session gets an independent clone of the folder.
	job, err := s.allocateSessionWorkspaceWithBranchMode(repo, true, "", "agent/job", "worker-execution-target-job", true)
	if err != nil {
		t.Fatal(err)
	}
	defer removeManagedWorktree(repo, job.WorkspacePath)
	git(job.WorkspacePath, "commit", "--allow-empty", "-m", "job")
	head := git(job.WorkspacePath, "rev-parse", "HEAD")
	jobDev := git(job.WorkspacePath, "for-each-ref", "--format=%(objectname)", "refs/heads/dev")

	// dev moves on in the folder after the job was created.
	tip := git(repo, "commit-tree", git(repo, "rev-parse", "dev^{tree}"), "-p", "dev", "-m", "landed elsewhere")
	git(repo, "update-ref", "refs/heads/dev", tip)
	if jobDev == "" || jobDev == tip {
		t.Fatalf("fixture: the job clone's dev %q must be the creation base, not %s", jobDev, tip)
	}

	review, err := s.allocateReviewWorkspaceContext(context.Background(), repo, "target-review", head, time.Minute, created)
	if err != nil {
		t.Fatal(err)
	}
	defer s.RemoveReviewWorkspace(repo, review.WorkspacePath, "target-review", head)
	if got := git(review.WorkspacePath, "rev-parse", "refs/remotes/origin/dev"); got != tip {
		t.Fatalf("review checkout origin/dev = %s, want the folder's dev %s (job clone's dev %s)", got, tip, jobDev)
	}
	if git(review.WorkspacePath, "rev-parse", "HEAD") != head || git(review.WorkspacePath, "branch", "--show-current") != "" {
		t.Fatal("review not detached at the exact reviewed head")
	}
	if git(job.WorkspacePath, "rev-parse", "HEAD") != head || git(repo, "rev-parse", "refs/heads/dev") != tip ||
		git(job.WorkspacePath, "for-each-ref", "--format=%(objectname)", "refs/heads/dev") != jobDev {
		t.Fatal("the review changed the job clone or the folder")
	}
}
