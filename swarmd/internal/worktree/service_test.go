package worktree

import (
	"os"
	"path/filepath"
	"strings"
	"testing"

	"swarm/packages/swarmd/internal/appstorage"
	"swarm/packages/swarmd/internal/identity"
	pebblestore "swarm/packages/swarmd/internal/store/pebble"
	workspaceruntime "swarm/packages/swarmd/internal/workspace"
)

func TestResolveTaskBaseUsesExactHEADFromLinkedWorktree(t *testing.T) {
	repo := t.TempDir()
	if _, err := runGit(repo, "init", "-b", "dev"); err != nil {
		t.Fatalf("init repo: %v", err)
	}
	if _, err := runGit(repo, "config", "user.email", "test@example.invalid"); err != nil {
		t.Fatalf("configure email: %v", err)
	}
	if _, err := runGit(repo, "config", "user.name", "Test User"); err != nil {
		t.Fatalf("configure name: %v", err)
	}
	if err := os.WriteFile(filepath.Join(repo, "README.md"), []byte("base\n"), 0o644); err != nil {
		t.Fatalf("write fixture: %v", err)
	}
	if _, err := runGit(repo, "add", "README.md"); err != nil {
		t.Fatalf("add fixture: %v", err)
	}
	if _, err := runGit(repo, "commit", "-m", "base"); err != nil {
		t.Fatalf("commit fixture: %v", err)
	}
	linked := filepath.Join(t.TempDir(), "linked")
	if _, err := runGit(repo, "worktree", "add", "-b", "agent/refactor", linked, "HEAD"); err != nil {
		t.Fatalf("add linked worktree: %v", err)
	}
	base, err := (&Service{}).ResolveTaskBase(linked)
	if err != nil {
		t.Fatalf("ResolveTaskBase: %v", err)
	}
	head, err := runGit(linked, "rev-parse", "HEAD")
	if err != nil {
		t.Fatalf("resolve fixture HEAD: %v", err)
	}
	if base.RepoRoot != repo || base.ParentBranch != "agent/refactor" || base.BaseCommit != head {
		t.Fatalf("task base = %#v, want root=%q branch=agent/refactor commit=%q", base, repo, head)
	}
}

func TestDeterministicSessionWorktreePathUsesPrivateWorktreeDataDir(t *testing.T) {
	t.Setenv("XDG_CACHE_HOME", filepath.Join(t.TempDir(), "cache"))
	dataHome := filepath.Join(t.TempDir(), "data")
	t.Setenv("XDG_DATA_HOME", dataHome)
	t.Setenv("XDG_STATE_HOME", filepath.Join(t.TempDir(), "state"))

	repoRoot := filepath.Join(t.TempDir(), "repo")
	if err := os.MkdirAll(filepath.Join(repoRoot, ".git"), 0o755); err != nil {
		t.Fatalf("mkdir repo: %v", err)
	}

	got, err := deterministicSessionWorktreePath(repoRoot, "ws_abc123")
	if err != nil {
		t.Fatalf("deterministicSessionWorktreePath: %v", err)
	}
	wantRoot, err := appstorage.WorktreeDataDir(repoRoot)
	if err != nil {
		t.Fatalf("WorktreeDataDir: %v", err)
	}
	bucket, err := appstorage.WorktreeBucketName(repoRoot)
	if err != nil {
		t.Fatalf("WorktreeBucketName: %v", err)
	}
	if wantRoot != filepath.Join(dataHome, "swarm", appstorage.WorktreesDir, bucket) {
		t.Fatalf("worktree root = %q, want user-local bucket under XDG data home", wantRoot)
	}
	want := filepath.Join(wantRoot, "ws_abc123")
	if got != want {
		t.Fatalf("worktree path = %q, want %q", got, want)
	}
	if strings.Contains(got, filepath.Join(repoRoot, ".swarm", "worktrees")) {
		t.Fatalf("worktree path uses workspace-local .swarm path: %q", got)
	}
	info, err := os.Stat(wantRoot)
	if err != nil {
		t.Fatalf("stat worktree data root: %v", err)
	}
	if gotPerm := info.Mode().Perm(); gotPerm != appstorage.PrivateDirPerm {
		t.Fatalf("worktree data root permissions = %#o, want %#o", gotPerm, appstorage.PrivateDirPerm)
	}
}

func TestDeterministicSessionWorktreePathRejectsUnsafeWorkspaceID(t *testing.T) {
	t.Setenv("XDG_CACHE_HOME", filepath.Join(t.TempDir(), "cache"))
	t.Setenv("XDG_DATA_HOME", filepath.Join(t.TempDir(), "data"))
	repoRoot := filepath.Join(t.TempDir(), "repo")

	if _, err := deterministicSessionWorktreePath(repoRoot, "../escape"); err == nil {
		t.Fatal("expected unsafe workspace id to fail")
	}
	if _, err := deterministicSessionWorktreePath(repoRoot, "ws_escape/path"); err == nil {
		t.Fatal("expected workspace id with slash to fail")
	}
	if _, err := deterministicSessionWorktreePath(repoRoot, "ws_"); err == nil {
		t.Fatal("expected empty workspace slug to fail")
	}
}

func TestWorkspaceIdentityForRequestedBranchUsesLiteralRequestSlug(t *testing.T) {
	got, err := WorkspaceIdentityForRequestedBranch("agent/client-side-request")
	if err != nil {
		t.Fatalf("WorkspaceIdentityForRequestedBranch: %v", err)
	}
	if got != "agent-client-side-request" {
		t.Fatalf("workspace id = %q, want branch-derived slug", got)
	}
	if strings.Contains(got, "da56285170") || strings.Contains(got, "session") {
		t.Fatalf("workspace id fell back to session/random identity: %q", got)
	}

	got, err = WorkspaceIdentityForRequestedBranch(" Feature.Client Request ")
	if err != nil {
		t.Fatalf("WorkspaceIdentityForRequestedBranch mixed separators: %v", err)
	}
	if got != "feature-client-request" {
		t.Fatalf("workspace id = %q, want sanitized literal branch slug", got)
	}

	if _, err := WorkspaceIdentityForRequestedBranch("../..."); err == nil {
		t.Fatal("expected branch without filesystem-safe slug to fail")
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
	if got := normalizeGitWorktreeBranch(entries[1].Branch); got != "agent/abc123" {
		t.Fatalf("normalized branch = %q, want agent/abc123", got)
	}
	if !pathWithinRoot(root, entries[1].Path) {
		t.Fatalf("expected managed path under root: %q in %q", entries[1].Path, root)
	}
	if pathWithinRoot(root, entries[0].Path) {
		t.Fatalf("unexpected arbitrary repo path accepted as managed: %q", entries[0].Path)
	}
}

func TestEnsureWorktreeParentUsesPrivatePermissions(t *testing.T) {
	t.Setenv("XDG_CACHE_HOME", filepath.Join(t.TempDir(), "cache"))
	dataHome := filepath.Join(t.TempDir(), "data")
	t.Setenv("XDG_DATA_HOME", dataHome)
	repoRoot := filepath.Join(t.TempDir(), "repo")

	if err := ensureWorktreeParent(repoRoot); err != nil {
		t.Fatalf("ensureWorktreeParent: %v", err)
	}
	parent, err := worktreeCacheRoot(repoRoot)
	if err != nil {
		t.Fatalf("worktreeCacheRoot: %v", err)
	}
	if !strings.HasPrefix(parent, filepath.Join(dataHome, "swarm", appstorage.WorktreesDir)+string(filepath.Separator)) {
		t.Fatalf("worktree parent = %q, want under user-local swarm worktrees root", parent)
	}
	info, err := os.Stat(parent)
	if err != nil {
		t.Fatalf("stat worktree parent: %v", err)
	}
	if got := info.Mode().Perm(); got != appstorage.PrivateDirPerm {
		t.Fatalf("worktree parent permissions = %#o, want %#o", got, appstorage.PrivateDirPerm)
	}
}

func TestGetConfigForPrincipalAllowsUnmatchedDirectory(t *testing.T) {
	store, err := pebblestore.Open(t.TempDir())
	if err != nil {
		t.Fatalf("open store: %v", err)
	}
	t.Cleanup(func() { _ = store.Close() })

	workspaceSvc := workspaceruntime.NewService(pebblestore.NewWorkspaceStore(store))
	svc := NewService(pebblestore.NewWorktreeStore(store), workspaceSvc, nil)
	principal := identity.Principal{Type: identity.PrincipalTypeUser, UserID: "user-1", AccountScopeID: "account-1"}
	unmatched := t.TempDir()

	cfg, err := svc.GetConfigForPrincipal(principal, unmatched)
	if err != nil {
		t.Fatalf("GetConfigForPrincipal: %v", err)
	}
	if cfg.Enabled {
		t.Fatalf("Enabled = true, want false for unmatched directory")
	}
	if cfg.WorkspacePath != unmatched {
		t.Fatalf("WorkspacePath = %q, want %q", cfg.WorkspacePath, unmatched)
	}
}

func TestSetConfigForPrincipalRequiresAccountOwnedWorkspace(t *testing.T) {
	store, err := pebblestore.Open(t.TempDir())
	if err != nil {
		t.Fatalf("open store: %v", err)
	}
	t.Cleanup(func() { _ = store.Close() })

	workspaceSvc := workspaceruntime.NewService(pebblestore.NewWorkspaceStore(store))
	svc := NewService(pebblestore.NewWorktreeStore(store), workspaceSvc, nil)
	principal := identity.Principal{Type: identity.PrincipalTypeUser, UserID: "user-1", AccountScopeID: "account-1"}

	if _, _, err := svc.SetConfigForPrincipal(principal, t.TempDir(), true, true, "", ""); err == nil || !strings.Contains(err.Error(), errAccountOwnedWorkspaceRequired.Error()) {
		t.Fatalf("SetConfigForPrincipal error = %v, want %v", err, errAccountOwnedWorkspaceRequired)
	}
}
