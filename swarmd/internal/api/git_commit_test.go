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
	if err := os.WriteFile(filepath.Join(repo, "untracked.txt"), []byte("new\n"), 0o644); err != nil {
		t.Fatalf("write untracked file: %v", err)
	}

	result, err := runWorkspaceGitCommit(context.Background(), repo, "feat: exact manual commit", true)
	if err != nil {
		t.Fatalf("runWorkspaceGitCommit error: %v output=%s", err, result.Output)
	}
	if !result.OK || result.ExitCode != 0 {
		t.Fatalf("result = %+v, want ok exit 0", result)
	}
	if !strings.Contains(result.Output, "[test-secret-check] PASS") {
		t.Fatalf("commit output missing secret-check evidence: %q", result.Output)
	}

	got := strings.TrimSpace(runGitCommitTestCommand(t, repo, "log", "-1", "--pretty=%s"))
	if got != "feat: exact manual commit" {
		t.Fatalf("commit subject = %q, want exact message", got)
	}
	committed := runGitCommitTestCommand(t, repo, "show", "--pretty=", "--name-only", "HEAD")
	if !strings.Contains(committed, "note.txt") || !strings.Contains(committed, "untracked.txt") {
		t.Fatalf("committed files = %q, want modified and untracked files", committed)
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

func TestRunWorkspaceGitCommitBlocksWhenSecretCheckFails(t *testing.T) {
	repo := initGitCommitTestRepo(t)
	privateTailnetURL := "https://device." + "tail" + "123456" + ".ts.net\n"
	if err := os.WriteFile(filepath.Join(repo, "leak.txt"), []byte(privateTailnetURL), 0o644); err != nil {
		t.Fatalf("write leak file: %v", err)
	}

	before := strings.TrimSpace(runGitCommitTestCommand(t, repo, "rev-parse", "HEAD"))
	result, err := runWorkspaceGitCommit(context.Background(), repo, "bad: leak", true)
	if err == nil {
		t.Fatal("expected secret-check failure")
	}
	if result.OK {
		t.Fatalf("secret-check failure result OK = true")
	}
	if result.Argv[0] != "scripts/check-precommit.sh" {
		t.Fatalf("failure argv = %#v, want scripts/check-precommit.sh", result.Argv)
	}
	if !strings.Contains(result.Output, "possible private Tailscale tailnet URL found") {
		t.Fatalf("failure output missing private tailnet diagnostic: %q", result.Output)
	}
	after := strings.TrimSpace(runGitCommitTestCommand(t, repo, "rev-parse", "HEAD"))
	if after != before {
		t.Fatalf("commit advanced despite secret-check failure: before=%s after=%s", before, after)
	}
}

func initGitCommitTestRepo(t *testing.T) string {
	t.Helper()
	repo := t.TempDir()
	runGitCommitTestCommand(t, repo, "init")
	runGitCommitTestCommand(t, repo, "config", "user.name", "Swarm Test")
	runGitCommitTestCommand(t, repo, "config", "user.email", "swarm-test@example.invalid")
	writeGitCommitTestSecretCheck(t, repo)
	if err := os.WriteFile(filepath.Join(repo, "note.txt"), []byte("initial\n"), 0o644); err != nil {
		t.Fatalf("write initial file: %v", err)
	}
	runGitCommitTestCommand(t, repo, "add", "note.txt", "scripts/check-precommit.sh")
	runGitCommitTestCommand(t, repo, "commit", "-m", "chore: initial")
	return repo
}

func writeGitCommitTestSecretCheck(t *testing.T, repo string) {
	t.Helper()
	scriptDir := filepath.Join(repo, "scripts")
	if err := os.MkdirAll(scriptDir, 0o755); err != nil {
		t.Fatalf("create scripts dir: %v", err)
	}
	script := `#!/usr/bin/env bash
set -euo pipefail
hits="$(grep -R -n -E --exclude-dir=.git --exclude='check-precommit.sh' 'tail[0-9a-z]{6,}\.ts\.net' . || true)"
if [ -n "$hits" ]; then
  echo '[secret-check] FAIL: possible private Tailscale tailnet URL found:'
  echo "$hits"
  exit 1
fi
echo '[test-secret-check] PASS'
`
	if err := os.WriteFile(filepath.Join(scriptDir, "check-precommit.sh"), []byte(script), 0o755); err != nil {
		t.Fatalf("write secret check script: %v", err)
	}
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
