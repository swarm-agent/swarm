package run

import (
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
)

// Requirement: every agent run receives explicit machine-readable Git readiness
// and treats non-repository paths as invalid Swarm workspaces. Threat: without
// this context an agent may silently proceed without mandatory worktree isolation.
// The runtime prompt helper is the narrowest authority that reaches every run.
func TestWorkspaceGitContextClassifiesRequiredGitStates(t *testing.T) {
	plain := t.TempDir()
	assertGitContextContains(t, plain, "workspace_git_state: not_repository", "not a valid Swarm workspace", "requires the selected repository root and an initial commit")

	unborn := t.TempDir()
	runWorkspaceGit(t, unborn, "init")
	assertGitContextContains(t, unborn, "workspace_git_state: needs_initial_commit", "not a valid Swarm workspace until HEAD resolves")

	if err := os.WriteFile(filepath.Join(unborn, "README.md"), []byte("ready\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	runWorkspaceGit(t, unborn, "add", "README.md")
	runWorkspaceGit(t, unborn, "-c", "user.name=Test User", "-c", "user.email=test@example.com", "commit", "-m", "init")
	assertGitContextContains(t, unborn, "workspace_git_state: ready")
}

// Requirement: a missing Git executable marks the installation unusable and
// directs repair through the supported installer. Threat: an agent could claim
// success or install packages ad hoc from a workspace session. PATH isolation
// provides a hermetic missing-executable proof.
func TestWorkspaceGitContextExplainsMandatoryGitRepair(t *testing.T) {
	t.Setenv("PATH", t.TempDir())
	assertGitContextContains(t, t.TempDir(),
		"workspace_git_state: unavailable",
		"mandatory Swarm runtime prerequisite",
		"damaged or incomplete",
		"repair or reinstall Swarm",
		"verify `git --version`",
		"Never install Git ad hoc",
	)
}

func assertGitContextContains(t *testing.T, path string, wants ...string) {
	t.Helper()
	text := strings.Join(workspaceGitContext(path), "\n")
	for _, want := range wants {
		if !strings.Contains(text, want) {
			t.Fatalf("workspaceGitContext(%q) = %q, missing %q", path, text, want)
		}
	}
}

func runWorkspaceGit(t *testing.T, path string, args ...string) {
	t.Helper()
	argv := append([]string{"-C", path}, args...)
	if output, err := exec.Command("git", argv...).CombinedOutput(); err != nil {
		t.Fatalf("git %s: %v: %s", strings.Join(argv, " "), err, output)
	}
}
