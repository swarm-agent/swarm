package api

import (
	"context"
	"os/exec"
	"strings"
	"testing"
)

// Purpose: a worker job commits in its worktree of the folder, and the
// folder's publish command pushes from the folder, so the job's head must
// already be in the folder's repository; nothing is fetched from elsewhere.
// A head the folder lacks is refused by name instead of reaching the command.
// Real Git repositories are the boundary. Owner: jobHeadInFolder.
func TestJobHeadMustBeInTheFolder(t *testing.T) {
	t.Setenv("GIT_CONFIG_GLOBAL", "/dev/null")
	t.Setenv("GIT_CONFIG_NOSYSTEM", "1")
	git := func(dir string, args ...string) string {
		t.Helper()
		out, err := exec.Command("git", append([]string{"-C", dir, "-c", "user.name=t", "-c", "user.email=t@example.invalid"}, args...)...).CombinedOutput()
		if err != nil {
			t.Fatalf("git %v: %v %s", args, err, out)
		}
		return strings.TrimSpace(string(out))
	}
	folder, worktree, elsewhere := t.TempDir(), t.TempDir()+"/job", t.TempDir()
	git(folder, "init", "-q", "-b", "dev")
	git(folder, "commit", "-q", "--allow-empty", "-m", "base")
	git(folder, "worktree", "add", "-q", "-b", "agent/job", worktree)
	git(worktree, "commit", "-q", "--allow-empty", "-m", "the job's change")
	head := git(worktree, "rev-parse", "HEAD")
	if err := jobHeadInFolder(context.Background(), folder, head); err != nil {
		t.Fatalf("a commit made in the folder's worktree = %v", err)
	}
	git(elsewhere, "clone", "-q", "--no-local", folder, ".")
	git(elsewhere, "commit", "-q", "--allow-empty", "-m", "made elsewhere")
	foreign := git(elsewhere, "rev-parse", "HEAD")
	if err := jobHeadInFolder(context.Background(), folder, foreign); err == nil || !strings.Contains(err.Error(), "is not in") {
		t.Fatalf("a commit only in another repository = %v, want a clear refusal", err)
	}
	if err := exec.Command("git", "-C", folder, "cat-file", "-e", foreign+"^{commit}").Run(); err == nil {
		t.Fatal("the check brought the foreign commit into the folder")
	}
}
