package worktree

import (
	"os"
	"path/filepath"
	"reflect"
	"testing"
)

func TestAllocateTaskWorkspaceMaterializesOnlyOwnedScopeAndContext(t *testing.T) {
	t.Setenv("XDG_DATA_HOME", t.TempDir())
	repo := initSparseTaskRepository(t)
	base, err := runGit(repo, "rev-parse", "HEAD")
	if err != nil {
		t.Fatal(err)
	}

	allocation, err := (&Service{}).AllocateTaskWorkspace(repo, TaskBase{RepoRoot: repo, ParentBranch: "dev", BaseCommit: base}, "sparse-child", []string{"small/**"})
	if err != nil {
		t.Fatalf("AllocateTaskWorkspace: %v", err)
	}
	for _, relative := range []string{"AGENTS.md", "README.md", "go.mod", "small/owned.txt"} {
		if _, err := os.Stat(filepath.Join(allocation.WorkspacePath, relative)); err != nil {
			t.Fatalf("expected sparse path %q: %v", relative, err)
		}
	}
	for _, relative := range []string{"large/unrelated.bin", "other/skip.txt"} {
		if _, err := os.Stat(filepath.Join(allocation.WorkspacePath, relative)); !os.IsNotExist(err) {
			t.Fatalf("unrelated path %q was materialized: %v", relative, err)
		}
	}
	sparseEnabled, err := runGit(allocation.WorkspacePath, "config", "--worktree", "--get", "core.sparseCheckout")
	if err != nil || sparseEnabled != "true" {
		t.Fatalf("core.sparseCheckout = %q, %v", sparseEnabled, err)
	}
}

func TestAllocateTaskWorkspaceWholeScopeMaterializesRepository(t *testing.T) {
	t.Setenv("XDG_DATA_HOME", t.TempDir())
	repo := initSparseTaskRepository(t)
	base, _ := runGit(repo, "rev-parse", "HEAD")
	allocation, err := (&Service{}).AllocateTaskWorkspace(repo, TaskBase{RepoRoot: repo, ParentBranch: "dev", BaseCommit: base}, "whole-child", []string{"."})
	if err != nil {
		t.Fatalf("AllocateTaskWorkspace: %v", err)
	}
	if _, err := os.Stat(filepath.Join(allocation.WorkspacePath, "large", "unrelated.bin")); err != nil {
		t.Fatalf("whole-worktree scope did not materialize repository: %v", err)
	}
}

func TestTaskWorktreeAddUsesNoCheckoutAndLongerTimeout(t *testing.T) {
	got := worktreeAddArgs("/worktree", "agent/child", "base", true)
	want := []string{"worktree", "add", "--no-checkout", "-b", "agent/child", "/worktree", "base"}
	if !reflect.DeepEqual(got, want) {
		t.Fatalf("worktree add args = %#v, want %#v", got, want)
	}
	if worktreeAllocationTimeout <= gitCommandTimeout {
		t.Fatalf("allocation timeout %s must exceed routine git timeout %s", worktreeAllocationTimeout, gitCommandTimeout)
	}
}

func initSparseTaskRepository(t *testing.T) string {
	t.Helper()
	repo := t.TempDir()
	if _, err := runGit(repo, "init", "-b", "dev"); err != nil {
		t.Fatal(err)
	}
	_, _ = runGit(repo, "config", "user.email", "test@example.invalid")
	_, _ = runGit(repo, "config", "user.name", "Test User")
	files := map[string]string{
		"AGENTS.md":           "root instructions\n",
		"README.md":           "context\n",
		"go.mod":              "module example.invalid/sparse\n",
		"small/owned.txt":     "owned\n",
		"large/unrelated.bin": "unrelated\n",
		"other/skip.txt":      "skip\n",
	}
	for relative, content := range files {
		path := filepath.Join(repo, relative)
		if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(path, []byte(content), 0o644); err != nil {
			t.Fatal(err)
		}
	}
	if _, err := runGit(repo, "add", "."); err != nil {
		t.Fatal(err)
	}
	if _, err := runGit(repo, "commit", "-m", "fixture"); err != nil {
		t.Fatal(err)
	}
	return repo
}
