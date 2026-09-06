package worktree

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// Purpose: exposing all committed source must not allow a scoped child's commit
// to cross the integration write boundary. Real Git proves both rejection and
// unchanged parent HEAD/files, including an out-of-scope path containing spaces.
func TestTaskIntegrationRejectsUnownedMaterializedSource(t *testing.T) {
	t.Setenv("XDG_DATA_HOME", t.TempDir())
	repo := initSparseTaskRepository(t)
	base, err := runGit(repo, "rev-parse", "HEAD")
	if err != nil {
		t.Fatal(err)
	}
	svc := &Service{}
	child, err := svc.AllocateTaskWorkspace(repo, TaskBase{RepoRoot: repo, ParentBranch: "dev", BaseCommit: base}, "scope-child", []string{"small/**"})
	if err != nil {
		t.Fatal(err)
	}
	path := filepath.Join(child.WorkspacePath, "other", "outside file.go")
	if err := os.WriteFile(path, []byte("unauthorized\n"), 0600); err != nil {
		t.Fatal(err)
	}
	if _, err := runGit(child.WorkspacePath, "add", "other/outside file.go"); err != nil {
		t.Fatal(err)
	}
	if _, err := runGit(child.WorkspacePath, "commit", "-m", "unowned fixture"); err != nil {
		t.Fatal(err)
	}
	head, err := runGit(child.WorkspacePath, "rev-parse", "HEAD")
	if err != nil {
		t.Fatal(err)
	}
	if _, err := svc.PrepareTaskIntegration(repo, "dev", base, []TaskIntegrationChild{{SessionID: "scope-child", BaseCommit: base, HeadCommit: head, OwnedScopes: []string{"small/**"}}}); err == nil || !strings.Contains(err.Error(), "outside owned scope") {
		t.Fatalf("unowned integration: %v", err)
	}
	if got, err := runGit(repo, "rev-parse", "HEAD"); err != nil || got != base {
		t.Fatalf("parent changed: %q %v", got, err)
	}
	if _, err := os.Stat(filepath.Join(repo, "other", "outside file.go")); !os.IsNotExist(err) {
		t.Fatalf("unowned file reached parent: %v", err)
	}
}
