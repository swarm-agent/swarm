package tool

import (
	"context"
	"encoding/json"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
)

func TestExecuteGitCommitUsesConfiguredIdentityAndIgnoresEnvironmentOverrides(t *testing.T) {
	repo := t.TempDir()
	runGitTestCommand(t, repo, "init")
	runGitTestCommand(t, repo, "config", "user.name", "Test User")
	runGitTestCommand(t, repo, "config", "user.email", "test@example.invalid")
	if err := os.WriteFile(filepath.Join(repo, "note.txt"), []byte("changed\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	runGitTestCommand(t, repo, "add", "note.txt")
	t.Setenv("GIT_AUTHOR_NAME", "Injected Author")
	t.Setenv("GIT_AUTHOR_EMAIL", "injected-author@example.invalid")
	t.Setenv("GIT_COMMITTER_NAME", "Injected Committer")
	t.Setenv("GIT_COMMITTER_EMAIL", "injected-committer@example.invalid")

	output, err := executeGitCommit(context.Background(), WorkspaceScope{PrimaryPath: repo}, map[string]any{"message": "preserve identity"})
	if err != nil {
		t.Fatalf("executeGitCommit() error = %v output=%s", err, output)
	}
	got := strings.TrimSpace(runGitTestCommandOutput(t, repo, "log", "-1", "--format=%an|%ae|%cn|%ce"))
	if got != "Test User|test@example.invalid|Test User|test@example.invalid" {
		t.Fatalf("commit identity = %q, want repository-configured identity", got)
	}
}

func TestExecuteGitCommitDoesNotRunRepositoryWidePrecommitGate(t *testing.T) {
	repo := t.TempDir()
	runGitTestCommand(t, repo, "init")
	runGitTestCommand(t, repo, "config", "user.name", "Test User")
	runGitTestCommand(t, repo, "config", "user.email", "test@example.invalid")
	if err := os.WriteFile(filepath.Join(repo, "note.txt"), []byte("initial\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	runGitTestCommand(t, repo, "add", "note.txt")
	runGitTestCommand(t, repo, "commit", "-m", "initial")

	scripts := filepath.Join(repo, "scripts")
	if err := os.MkdirAll(scripts, 0o755); err != nil {
		t.Fatal(err)
	}
	gate := "#!/usr/bin/env bash\nset -euo pipefail\nprintf ran > .precommit-ran\necho unrelated repository-wide failure >&2\nexit 1\n"
	if err := os.WriteFile(filepath.Join(scripts, "check-precommit.sh"), []byte(gate), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(repo, "note.txt"), []byte("changed\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	runGitTestCommand(t, repo, "add", "note.txt")

	output, err := executeGitCommit(context.Background(), WorkspaceScope{PrimaryPath: repo}, map[string]any{"message": "update"})
	if err != nil {
		t.Fatalf("executeGitCommit() error = %v output=%s", err, output)
	}
	var payload map[string]any
	if err := json.Unmarshal([]byte(output), &payload); err != nil {
		t.Fatalf("decode output: %v output=%s", err, output)
	}
	if got := int(payload["exit_code"].(float64)); got != 0 {
		t.Fatalf("exit_code = %d output=%s", got, output)
	}
	if _, err := os.Stat(filepath.Join(repo, ".precommit-ran")); !os.IsNotExist(err) {
		t.Fatalf("repository-wide precommit gate ran during git_commit: err=%v", err)
	}
}

func runGitTestCommand(t *testing.T, dir string, args ...string) {
	t.Helper()
	_ = runGitTestCommandOutput(t, dir, args...)
}

func runGitTestCommandOutput(t *testing.T, dir string, args ...string) string {
	t.Helper()
	cmd := exec.Command("git", args...)
	cmd.Dir = dir
	output, err := cmd.CombinedOutput()
	if err != nil {
		t.Fatalf("git %v: %v\n%s", args, err, output)
	}
	return string(output)
}
