package worktree

import (
	"context"
	"os"
	"os/exec"
	"strings"
	"testing"
	"time"
)

// Purpose: review admission must include immutable landing evidence even when
// only a private folder ref contains it, without changing the coder clone.
// Real local Git exercises the allocator/object boundary, not a provider.
func TestReviewPrivateBaseHydration(t *testing.T) {
	t.Setenv("HOME", t.TempDir())
	t.Setenv("GIT_CONFIG_GLOBAL", os.DevNull)
	t.Setenv("GIT_CONFIG_NOSYSTEM", "1")
	repo := t.TempDir()
	git := func(path string, args ...string) string {
		t.Helper()
		out, err := exec.Command("git", append([]string{"-C", path}, args...)...).CombinedOutput()
		if err != nil {
			t.Fatalf("git %v: %v %s", args, err, out)
		}
		return strings.TrimSpace(string(out))
	}
	git(repo, "init", "-b", "dev")
	git(repo, "-c", "user.name=Fixture", "-c", "user.email=fixture@example.invalid", "commit", "--allow-empty", "-m", "base")
	original := git(repo, "rev-parse", "HEAD")
	s := &Service{}
	job, err := s.allocateSessionWorkspaceWithBranchMode(repo, true, "", "agent/job", "base-job", true)
	if err != nil {
		t.Fatal(err)
	}
	defer removeManagedWorktree(repo, job.WorkspacePath)
	git(job.WorkspacePath, "-c", "user.name=Fixture", "-c", "user.email=fixture@example.invalid", "commit", "--allow-empty", "-m", "job")
	head := git(job.WorkspacePath, "rev-parse", "HEAD")
	base := git(repo, "-c", "user.name=Fixture", "-c", "user.email=fixture@example.invalid", "commit-tree", git(repo, "rev-parse", "HEAD^{tree}"), "-p", original, "-m", "landing")
	git(repo, "update-ref", "refs/swarm/publish/dev", base)
	// Explicit evidence must import this private commit from the folder.
	review, err := s.allocateReviewWorkspaceContext(context.Background(), repo, "base-review", head, time.Minute, base, original)
	if err != nil {
		t.Fatal(err)
	}
	defer s.RemoveReviewWorkspace(repo, review.WorkspacePath, "base-review", head)
	git(review.WorkspacePath, "cat-file", "-e", base+"^{commit}")
	if git(review.WorkspacePath, "rev-parse", "HEAD") != head || git(review.WorkspacePath, "branch", "--show-current") != "" {
		t.Fatal("review not detached at exact head")
	}
	for _, missing := range []string{"", "refs/swarm/publish/dev", strings.Repeat("f", 40)} {
		rejected, err := s.allocateReviewWorkspaceContext(context.Background(), repo, "missing-review", head, time.Minute, missing)
		if err == nil || rejected.WorkspacePath != "" {
			t.Fatalf("missing base admitted: %+v %v", rejected, err)
		}
		path, err := deterministicSessionWorktreePath(repo, sessionWorkspaceID("missing-review"))
		if err != nil {
			t.Fatal(err)
		}
		if _, err := os.Stat(path); !os.IsNotExist(err) {
			t.Fatal("failed admission left checkout", err)
		}
	}
	if git(job.WorkspacePath, "rev-parse", "HEAD") != head || git(repo, "rev-parse", "HEAD") != original {
		t.Fatal("source changed")
	}
}
