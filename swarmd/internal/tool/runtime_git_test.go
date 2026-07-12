package tool

import (
	"context"
	"encoding/json"
	"os"
	"os/exec"
	"path/filepath"
	"testing"
)

func TestExecuteGitCommitRunsRepositoryPrecommitGate(t *testing.T) {
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
	gate := "#!/usr/bin/env bash\nset -euo pipefail\nprintf passed > .precommit-ran\n"
	if err := os.WriteFile(filepath.Join(scripts, "check-precommit.sh"), []byte(gate), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(repo, "note.txt"), []byte("changed\n"), 0o644); err != nil {
		t.Fatal(err)
	}

	output, err := executeGitCommit(context.Background(), WorkspaceScope{PrimaryPath: repo}, map[string]any{"message": "update", "all": true})
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
	if data, err := os.ReadFile(filepath.Join(repo, ".precommit-ran")); err != nil || string(data) != "passed" {
		t.Fatalf("precommit marker data=%q err=%v", data, err)
	}
}

func runGitTestCommand(t *testing.T, dir string, args ...string) {
	t.Helper()
	cmd := exec.Command("git", args...)
	cmd.Dir = dir
	if output, err := cmd.CombinedOutput(); err != nil {
		t.Fatalf("git %v: %v\n%s", args, err, output)
	}
}
