package workspace

import (
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
)

// Purpose: a first commit Swarm makes in a folder that had no Git repository
// leaves the index matching HEAD, so Git in the folder shows only the files
// left out of the commit as untracked, never the committed files as a
// staged deletion plus an untracked copy.
// Threat: the Orchestrator refusing to launch a Coder in a folder the
// person just readied, because Git reports it as dirty.
func TestBaselineOfANewRepositoryLeavesTheIndexAtHead(t *testing.T) {
	if _, err := exec.LookPath("git"); err != nil {
		t.Skip("git is not installed")
	}
	t.Setenv("GIT_CONFIG_NOSYSTEM", "1")
	t.Setenv("GIT_CONFIG_GLOBAL", os.DevNull)
	store, cleanup := newTestWorkspaceStore(t)
	defer cleanup()
	svc := NewService(store)
	principal := testPrincipal()
	folder := filepath.Join(t.TempDir(), "notes")
	if err := os.Mkdir(folder, 0o755); err != nil {
		t.Fatal(err)
	}
	for name, content := range map[string]string{"notes.txt": "first line\n", ".env": "KEY=1\n"} {
		if err := os.WriteFile(filepath.Join(folder, name), []byte(content), 0o644); err != nil {
			t.Fatal(err)
		}
	}
	review, err := svc.ReviewRepositoryForPrincipal(principal, folder)
	if err != nil {
		t.Fatalf("review: %v", err)
	}
	state, err := svc.PrepareRepositoryBaselineForPrincipal(principal, RepositoryBaselineRequest{
		Path: folder, ExpectedResolvedPath: folder, ReviewDigest: review.Digest,
		SelectedPaths: []string{"notes.txt"}, ConfirmBaseline: true, ConfirmOmissions: true,
	})
	if err != nil {
		t.Fatalf("baseline: %v", err)
	}
	if state.State != RepositoryStateReady || state.HeadCommit == "" {
		t.Fatalf("state after baseline: %+v", state)
	}
	status, err := runRepositoryGit(folder, "status", "--porcelain", "--untracked-files=all")
	if err != nil {
		t.Fatal(err)
	}
	if strings.TrimSpace(status) != "?? .env" {
		t.Fatalf("status after baseline:\n%s", status)
	}
}
