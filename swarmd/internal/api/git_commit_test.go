package api

import (
	"context"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
)

func TestRunWorkspaceGitCommitCreatesCommitWithExactMessage(t *testing.T) {
	repo := initGitCommitTestRepo(t)
	if err := os.WriteFile(filepath.Join(repo, "note.txt"), []byte("changed\n"), 0o644); err != nil {
		t.Fatalf("write changed file: %v", err)
	}

	result, err := runWorkspaceGitCommit(context.Background(), repo, "feat: exact manual commit", true)
	if err != nil {
		t.Fatalf("runWorkspaceGitCommit error: %v output=%s", err, result.Output)
	}
	if !result.OK || result.ExitCode != 0 {
		t.Fatalf("result = %+v, want ok exit 0", result)
	}

	got := strings.TrimSpace(runGitCommitTestCommand(t, repo, "log", "-1", "--pretty=%s"))
	if got != "feat: exact manual commit" {
		t.Fatalf("commit subject = %q, want exact message", got)
	}
}

func TestRunWorkspaceGitCommitRequiresMessage(t *testing.T) {
	result, err := runWorkspaceGitCommit(context.Background(), t.TempDir(), "   ", true)
	if err == nil {
		t.Fatal("expected error for blank commit message")
	}
	if result.OK {
		t.Fatalf("blank message result OK = true")
	}
}

func initGitCommitTestRepo(t *testing.T) string {
	t.Helper()
	repo := t.TempDir()
	runGitCommitTestCommand(t, repo, "init")
	runGitCommitTestCommand(t, repo, "config", "user.name", "Swarm Test")
	runGitCommitTestCommand(t, repo, "config", "user.email", "swarm-test@example.invalid")
	if err := os.WriteFile(filepath.Join(repo, "note.txt"), []byte("initial\n"), 0o644); err != nil {
		t.Fatalf("write initial file: %v", err)
	}
	runGitCommitTestCommand(t, repo, "add", "note.txt")
	runGitCommitTestCommand(t, repo, "commit", "-m", "chore: initial")
	return repo
}

func runGitCommitTestCommand(t *testing.T, dir string, args ...string) string {
	t.Helper()
	cmd := exec.Command("git", args...)
	cmd.Dir = dir
	out, err := cmd.CombinedOutput()
	if err != nil {
		t.Fatalf("git %s failed: %v\n%s", strings.Join(args, " "), err, string(out))
	}
	return string(out)
}
