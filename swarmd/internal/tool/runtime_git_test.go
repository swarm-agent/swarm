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

// Requirement: first-workspace onboarding initializes Git through a fixed,
// shell-free operation scoped to the selected directory. Threat: arbitrary
// shell access or caller-controlled init arguments could escape that authority.
// The tool runtime is the narrowest layer that proves the exact command effect.
func TestExecuteGitInitCreatesMainRepositoryInExactScope(t *testing.T) {
	repo := t.TempDir()
	if _, err := executeGitInit(context.Background(), WorkspaceScope{PrimaryPath: repo}, nil); err != nil {
		t.Fatalf("executeGitInit() error = %v", err)
	}
	if got := strings.TrimSpace(runGitTestCommandOutput(t, repo, "branch", "--show-current")); got != "main" {
		t.Fatalf("initial branch = %q, want main", got)
	}
	if got := strings.TrimSpace(runGitTestCommandOutput(t, repo, "rev-parse", "--show-toplevel")); got != repo {
		t.Fatalf("repository root = %q, want %q", got, repo)
	}
}

func TestGitAddRejectsPathOutsideCoderOwnedScope(t *testing.T) {
	repo := t.TempDir()
	runGitTestCommand(t, repo, "init")
	outside := filepath.Join(repo, "tests", "outside.txt")
	if err := os.MkdirAll(filepath.Dir(outside), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(outside, []byte("outside"), 0o644); err != nil {
		t.Fatal(err)
	}
	scope := WorkspaceScope{PrimaryPath: repo, MutationScopes: []string{"src/**"}}
	if _, err := executeGitAdd(context.Background(), scope, map[string]any{"pathspec": []any{"tests/outside.txt"}}); err == nil || !strings.Contains(err.Error(), "outside the Coder owned scope") {
		t.Fatalf("git_add error = %v, want owned-scope rejection", err)
	}
}

// Requirement: a fresh first-workspace user may have no Git identity yet. The
// onboarding assistant may apply only the user's explicitly supplied identity to
// that commit without persisting repository configuration. Threat: inventing or
// retaining an identity would silently change later Git behavior.
func TestExecuteGitCommitInitialUsesExplicitOneTimeIdentityWithoutPersistingIt(t *testing.T) {
	repo := t.TempDir()
	runGitTestCommand(t, repo, "init")
	if err := os.WriteFile(filepath.Join(repo, "note.txt"), []byte("first\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	runGitTestCommand(t, repo, "add", "note.txt")

	output, err := executeGitCommitInitial(context.Background(), WorkspaceScope{PrimaryPath: repo}, map[string]any{
		"message": "first commit", "author_name": "First User", "author_email": "first@example.invalid",
	})
	if err != nil {
		t.Fatalf("executeGitCommit() error = %v output=%s", err, output)
	}
	got := strings.TrimSpace(runGitTestCommandOutput(t, repo, "log", "-1", "--format=%an|%ae|%cn|%ce"))
	if got != "First User|first@example.invalid|First User|first@example.invalid" {
		t.Fatalf("commit identity = %q", got)
	}
	cmd := exec.Command("git", "config", "--local", "--get", "user.name")
	cmd.Dir = repo
	if configOutput, configErr := cmd.CombinedOutput(); configErr == nil || len(configOutput) != 0 {
		t.Fatalf("one-time identity persisted in repository config: output=%q err=%v", configOutput, configErr)
	}
}

func TestExecuteGitCommitInitialRejectsMissingExplicitIdentity(t *testing.T) {
	repo := t.TempDir()
	runGitTestCommand(t, repo, "init")
	if _, err := executeGitCommitInitial(context.Background(), WorkspaceScope{PrimaryPath: repo}, map[string]any{"message": "first", "author_name": "First User"}); err == nil || !strings.Contains(err.Error(), "requires message, author_name, and author_email") {
		t.Fatalf("missing identity error = %v", err)
	}
}

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

func TestExecuteGitCommitProvidesPrivateTempEnvironmentAndRecoveryEvidence(t *testing.T) {
	repo := t.TempDir()
	runGitTestCommand(t, repo, "init")
	runGitTestCommand(t, repo, "config", "user.name", "Test User")
	runGitTestCommand(t, repo, "config", "user.email", "test@example.invalid")
	if err := os.WriteFile(filepath.Join(repo, "note.txt"), []byte("changed\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	runGitTestCommand(t, repo, "add", "note.txt")
	hooks := filepath.Join(repo, ".git", "hooks")
	tempDirRecord := filepath.Join(t.TempDir(), "hook-tmpdir")
	hook := "#!/usr/bin/env bash\nset -euo pipefail\ntest -n \"$TMPDIR\"\ntest \"$TMPDIR\" = \"$TMP\"\ntest \"$TMPDIR\" = \"$TEMP\"\ntest -d \"$TMPDIR\"\nprintf '%s' \"$TMPDIR\" > \"" + tempDirRecord + "\"\n"
	if err := os.WriteFile(filepath.Join(hooks, "pre-commit"), []byte(hook), 0o755); err != nil {
		t.Fatal(err)
	}

	scope := WorkspaceScope{PrimaryPath: repo, WorktreeEnabled: true, WorktreeRootPath: repo, WorktreeBranch: "agent/test", WorktreeBaseBranch: "dev", WorktreeBaseCommit: strings.Repeat("a", 40)}
	output, err := executeGitCommit(context.Background(), scope, map[string]any{"message": "private temp"})
	if err != nil {
		t.Fatalf("executeGitCommit() error = %v output=%s", err, output)
	}
	var payload map[string]any
	if err := json.Unmarshal([]byte(output), &payload); err != nil {
		t.Fatalf("decode output: %v", err)
	}
	if payload["branch"] != "agent/test" || payload["base_commit"] != strings.Repeat("a", 40) || payload["head_oid"] == "" || payload["clean"] != true {
		t.Fatalf("managed worktree evidence = %s", output)
	}
	tempDirBytes, err := os.ReadFile(tempDirRecord)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := os.Stat(string(tempDirBytes)); !os.IsNotExist(err) {
		t.Fatalf("private Git temp directory was not removed: %q err=%v", tempDirBytes, err)
	}
}

func TestExecuteGitCommitFailurePreservesDirtyRecoveryEvidence(t *testing.T) {
	repo := t.TempDir()
	runGitTestCommand(t, repo, "init")
	runGitTestCommand(t, repo, "config", "user.name", "Test User")
	runGitTestCommand(t, repo, "config", "user.email", "test@example.invalid")
	if err := os.WriteFile(filepath.Join(repo, "note.txt"), []byte("changed\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	runGitTestCommand(t, repo, "add", "note.txt")
	hook := "#!/usr/bin/env bash\necho hook rejected commit >&2\nexit 1\n"
	if err := os.WriteFile(filepath.Join(repo, ".git", "hooks", "pre-commit"), []byte(hook), 0o755); err != nil {
		t.Fatal(err)
	}

	output, err := executeGitCommit(context.Background(), WorkspaceScope{PrimaryPath: repo, WorktreeEnabled: true, WorktreeRootPath: repo, WorktreeBranch: "agent/test"}, map[string]any{"message": "blocked"})
	if err != nil {
		t.Fatalf("ordinary Git exit should be represented in tool output: %v", err)
	}
	var payload struct {
		ExitCode        int  `json:"exit_code"`
		Clean           bool `json:"clean"`
		DirtyCount      int  `json:"dirty_count"`
		FailureEvidence struct {
			Recover bool   `json:"recover_existing_worktree"`
			Output  string `json:"output"`
		} `json:"failure_evidence"`
	}
	if err := json.Unmarshal([]byte(output), &payload); err != nil {
		t.Fatalf("decode output: %v", err)
	}
	if payload.ExitCode == 0 || payload.Clean || payload.DirtyCount != 1 || !payload.FailureEvidence.Recover || !strings.Contains(payload.FailureEvidence.Output, "hook rejected commit") {
		t.Fatalf("failure recovery evidence = %s", output)
	}
}

func TestExecuteGitAddTreatsLeadingDashPathspecAsAPath(t *testing.T) {
	repo := t.TempDir()
	runGitTestCommand(t, repo, "init")
	name := "--dry-run"
	if err := os.WriteFile(filepath.Join(repo, name), []byte("approved\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	if _, err := executeGitAdd(context.Background(), WorkspaceScope{PrimaryPath: repo}, map[string]any{"pathspec": []any{name}}); err != nil {
		t.Fatalf("executeGitAdd() error = %v", err)
	}
	if got := strings.TrimSpace(runGitTestCommandOutput(t, repo, "diff", "--cached", "--name-only")); got != name {
		t.Fatalf("staged path = %q, want %q", got, name)
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
