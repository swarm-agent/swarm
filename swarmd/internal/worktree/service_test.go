package worktree

import (
	"os"
	"path/filepath"
	"strings"
	"testing"

	"swarm/packages/swarmd/internal/appstorage"
)

func TestDeterministicSessionWorktreePathUsesPrivateWorkspaceCache(t *testing.T) {
	cacheRoot := filepath.Join(t.TempDir(), "cache")
	t.Setenv("STATE_DIRECTORY", filepath.Join(t.TempDir(), "state"))
	t.Setenv("CACHE_DIRECTORY", cacheRoot)
	t.Setenv("RUNTIME_DIRECTORY", filepath.Join(t.TempDir(), "run"))
	t.Setenv("LOGS_DIRECTORY", filepath.Join(t.TempDir(), "logs"))
	t.Setenv("CONFIGURATION_DIRECTORY", filepath.Join(t.TempDir(), "config"))

	repoRoot := filepath.Join(t.TempDir(), "repo")
	if err := os.MkdirAll(filepath.Join(repoRoot, ".git"), 0o755); err != nil {
		t.Fatalf("mkdir repo: %v", err)
	}

	got, err := deterministicSessionWorktreePath(repoRoot, "ws_abc123")
	if err != nil {
		t.Fatalf("deterministicSessionWorktreePath: %v", err)
	}
	wantRoot, err := appstorage.WorkspaceCacheDir(repoRoot, "worktrees")
	if err != nil {
		t.Fatalf("WorkspaceCacheDir: %v", err)
	}
	want := filepath.Join(wantRoot, "ws_abc123")
	if got != want {
		t.Fatalf("worktree path = %q, want %q", got, want)
	}
	if strings.Contains(got, filepath.Join(repoRoot, ".swarm", "worktrees")) {
		t.Fatalf("worktree path uses workspace-local .swarm path: %q", got)
	}
	assertPathUnderWorktreeTest(t, got, filepath.Join(cacheRoot, appstorage.WorkspacesDir))
	info, err := os.Stat(wantRoot)
	if err != nil {
		t.Fatalf("stat cache root: %v", err)
	}
	if gotPerm := info.Mode().Perm(); gotPerm != appstorage.PrivateDirPerm {
		t.Fatalf("cache root permissions = %#o, want %#o", gotPerm, appstorage.PrivateDirPerm)
	}
}

func TestDeterministicSessionWorktreePathRejectsUnsafeWorkspaceID(t *testing.T) {
	t.Setenv("STATE_DIRECTORY", filepath.Join(t.TempDir(), "state"))
	t.Setenv("CACHE_DIRECTORY", filepath.Join(t.TempDir(), "cache"))
	t.Setenv("RUNTIME_DIRECTORY", filepath.Join(t.TempDir(), "run"))
	t.Setenv("LOGS_DIRECTORY", filepath.Join(t.TempDir(), "logs"))
	t.Setenv("CONFIGURATION_DIRECTORY", filepath.Join(t.TempDir(), "config"))
	repoRoot := filepath.Join(t.TempDir(), "repo")

	if _, err := deterministicSessionWorktreePath(repoRoot, "../escape"); err == nil {
		t.Fatal("expected unsafe workspace id to fail")
	}
	if _, err := deterministicSessionWorktreePath(repoRoot, "ws_escape/path"); err == nil {
		t.Fatal("expected workspace id with slash to fail")
	}
}

func TestParseWorktreeListAndManagedPathFilter(t *testing.T) {
	root := filepath.Join(t.TempDir(), "managed")
	inside := filepath.Join(root, "ws_abc123")
	outside := filepath.Join(t.TempDir(), "repo")
	output := strings.Join([]string{
		"worktree " + outside,
		"HEAD 1111111",
		"branch refs/heads/dev",
		"",
		"worktree " + inside,
		"HEAD 2222222",
		"branch refs/heads/agent/abc123",
		"",
	}, "\n")

	entries := parseWorktreeList(output)
	if len(entries) != 2 {
		t.Fatalf("entry count = %d, want 2", len(entries))
	}
	if !pathWithinRoot(root, entries[1].Path) {
		t.Fatalf("expected managed path under root: %q in %q", entries[1].Path, root)
	}
	if pathWithinRoot(root, entries[0].Path) {
		t.Fatalf("unexpected arbitrary repo path accepted as managed: %q", entries[0].Path)
	}
}

func TestEnsureWorktreeParentUsesPrivatePermissions(t *testing.T) {
	cacheRoot := filepath.Join(t.TempDir(), "cache")
	t.Setenv("STATE_DIRECTORY", filepath.Join(t.TempDir(), "state"))
	t.Setenv("CACHE_DIRECTORY", cacheRoot)
	t.Setenv("RUNTIME_DIRECTORY", filepath.Join(t.TempDir(), "run"))
	t.Setenv("LOGS_DIRECTORY", filepath.Join(t.TempDir(), "logs"))
	t.Setenv("CONFIGURATION_DIRECTORY", filepath.Join(t.TempDir(), "config"))
	repoRoot := filepath.Join(t.TempDir(), "repo")

	if err := ensureWorktreeParent(repoRoot); err != nil {
		t.Fatalf("ensureWorktreeParent: %v", err)
	}
	parent, err := worktreeCacheRoot(repoRoot)
	if err != nil {
		t.Fatalf("worktreeCacheRoot: %v", err)
	}
	assertPathUnderWorktreeTest(t, parent, filepath.Join(cacheRoot, appstorage.WorkspacesDir))
	info, err := os.Stat(parent)
	if err != nil {
		t.Fatalf("stat worktree parent: %v", err)
	}
	if got := info.Mode().Perm(); got != appstorage.PrivateDirPerm {
		t.Fatalf("worktree parent permissions = %#o, want %#o", got, appstorage.PrivateDirPerm)
	}
}

func assertPathUnderWorktreeTest(t *testing.T, path, prefix string) {
	t.Helper()
	path = filepath.Clean(path)
	prefix = filepath.Clean(prefix)
	if path != prefix && !strings.HasPrefix(path, prefix+string(filepath.Separator)) {
		t.Fatalf("path %q is not under %q", path, prefix)
	}
}
