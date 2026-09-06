package worktree

import (
	"context"
	"os"
	"os/exec"
	"path/filepath"
	"reflect"
	"testing"
	"time"
)

// Purpose: AllocateTaskWorkspace must expose immutable committed prerequisites
// independently of mutation ownership. Real Git allocation and Go compilation
// reproduce omitted source dependencies at the narrowest useful boundary.
func TestAllocateTaskWorkspaceMaterializesCommittedDependencies(t *testing.T) {
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
	for _, relative := range []string{"large/unrelated.bin", "other/skip.txt", "dependency/value.go"} {
		if _, err := os.Stat(filepath.Join(allocation.WorkspacePath, relative)); err != nil {
			t.Fatalf("committed read dependency %q absent: %v", relative, err)
		}
	}
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()
	cmd := exec.CommandContext(ctx, "go", "test", "-p", "1", "./small")
	cmd.Dir = allocation.WorkspacePath
	cmd.Env = append(os.Environ(), "GOWORK=off", "GOPROXY=off", "GOSUMDB=off", "GOMAXPROCS=2")
	if out, err := cmd.CombinedOutput(); err != nil {
		t.Fatalf("compile committed out-of-scope dependency: %v: %s", err, out)
	}
	if head, err := runGit(allocation.WorkspacePath, "rev-parse", "HEAD"); err != nil || head != base {
		t.Fatalf("allocation changed base: %q %v", head, err)
	}
	if dirty, err := runGit(allocation.WorkspacePath, "status", "--porcelain"); err != nil || dirty != "" {
		t.Fatalf("allocated source is dirty: %q %v", dirty, err)
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
		"small/value.go":      "package small\nimport \"example.invalid/sparse/dependency\"\nvar Value = dependency.Value\n",
		"dependency/value.go": "package dependency\nconst Value = 42\n",
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
