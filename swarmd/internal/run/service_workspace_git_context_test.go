package run

import (
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
)

// Requirement: every agent run receives explicit machine-readable Git readiness
// while ordinary non-Git work remains authorized. Threat: without this context an
// agent may attempt Git blindly or tell the user the entire workspace is broken.
// The runtime prompt helper is the narrowest authority that reaches every run.
func TestWorkspaceGitContextClassifiesOptionalGitStates(t *testing.T) {
	plain := t.TempDir()
	assertGitContextContains(t, plain, "workspace_git_state: not_repository", "normal usable workspace")

	unborn := t.TempDir()
	runWorkspaceGit(t, unborn, "init")
	assertGitContextContains(t, unborn, "workspace_git_state: needs_initial_commit", "Ordinary workspace work remains available")

	if err := os.WriteFile(filepath.Join(unborn, "README.md"), []byte("ready\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	runWorkspaceGit(t, unborn, "add", "README.md")
	runWorkspaceGit(t, unborn, "-c", "user.name=Test User", "-c", "user.email=test@example.com", "commit", "-m", "init")
	assertGitContextContains(t, unborn, "workspace_git_state: ready")
}

// Requirement: a missing Git executable must produce safe recovery instructions
// in system context rather than a startup failure. Threat: an agent could claim
// success, mutate packages without permission, or strand the user's requested
// Git operation. PATH isolation provides a hermetic missing-executable proof.
func TestWorkspaceGitContextExplainsSafeMissingGitRecovery(t *testing.T) {
	t.Setenv("PATH", t.TempDir())
	assertGitContextContains(t, t.TempDir(),
		"workspace_git_state: unavailable",
		"Ordinary workspace reads and edits remain available",
		"request the required permission",
		"verify `git --version`",
		"Never claim the Git operation completed",
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
