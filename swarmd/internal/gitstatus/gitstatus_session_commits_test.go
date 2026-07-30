package gitstatus

import (
	"context"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
)

func TestListCommitsSinceReturnsOnlySessionCommits(t *testing.T) {
	repo := t.TempDir()
	runGitStatusTest(t, repo, "init")
	runGitStatusTest(t, repo, "config", "user.name", "Swarm Test")
	runGitStatusTest(t, repo, "config", "user.email", "swarm-test@example.invalid")
	writeGitStatusTestFile(t, repo, "note.txt", "base\n")
	runGitStatusTest(t, repo, "add", "note.txt")
	runGitStatusTest(t, repo, "commit", "-m", "base commit")
	base := strings.TrimSpace(runGitStatusTest(t, repo, "rev-parse", "HEAD"))

	writeGitStatusTestFile(t, repo, "note.txt", "first\n")
	runGitStatusTest(t, repo, "commit", "-am", "first session commit")
	writeGitStatusTestFile(t, repo, "note.txt", "second\n")
	runGitStatusTest(t, repo, "commit", "-am", "second session commit")

	commits := ListCommitsSince(context.Background(), repo, base, 12)
	if len(commits) != 2 {
		t.Fatalf("commits = %+v, want two session commits", commits)
	}
	if commits[0].Subject != "second session commit" || commits[1].Subject != "first session commit" {
		t.Fatalf("commit subjects = %+v", commits)
	}
}

func TestListCommitsSinceRejectsOptionLikeRef(t *testing.T) {
	if commits := ListCommitsSince(context.Background(), t.TempDir(), "--all", 12); len(commits) != 0 {
		t.Fatalf("commits = %+v, want no commits for option-like ref", commits)
	}
}

func writeGitStatusTestFile(t *testing.T, repo, name, content string) {
	t.Helper()
	if err := os.WriteFile(filepath.Join(repo, name), []byte(content), 0o644); err != nil {
		t.Fatalf("write %s: %v", name, err)
	}
}

func runGitStatusTest(t *testing.T, repo string, args ...string) string {
	t.Helper()
	cmd := exec.Command("git", args...)
	cmd.Dir = repo
	out, err := cmd.CombinedOutput()
	if err != nil {
		t.Fatalf("git %s: %v\n%s", strings.Join(args, " "), err, out)
	}
	return string(out)
}
