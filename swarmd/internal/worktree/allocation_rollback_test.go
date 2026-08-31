package worktree

import (
	"os"
	"path/filepath"
	"testing"
)

func TestRollbackAllocationRemovesOnlyRecordedAllocation(t *testing.T) {
	repo := initRollbackTestRepository(t)
	svc := &Service{}
	allocation, err := svc.allocateSessionWorkspaceWithBranchMode(repo, true, "", "agent/routed-start", "session-1", true)
	if err != nil {
		t.Fatalf("allocate worktree: %v", err)
	}
	wantBase, err := runGit(repo, "rev-parse", "--verify", "HEAD^{commit}")
	if err != nil {
		t.Fatalf("resolve fixture base: %v", err)
	}
	if allocation.BaseCommit != wantBase {
		t.Fatalf("allocation base commit = %q, want %q", allocation.BaseCommit, wantBase)
	}
	unrelated := filepath.Join(t.TempDir(), "unrelated")
	if _, err := runGit(repo, "worktree", "add", "-b", "agent/unrelated", unrelated, "HEAD"); err != nil {
		t.Fatalf("add unrelated worktree: %v", err)
	}

	if err := svc.RollbackAllocation(allocation); err != nil {
		t.Fatalf("RollbackAllocation: %v", err)
	}
	if _, err := os.Stat(allocation.WorkspacePath); !os.IsNotExist(err) {
		t.Fatalf("allocated path still exists or stat failed: %v", err)
	}
	if exists, err := localBranchExists(repo, allocation.BranchName); err != nil || exists {
		t.Fatalf("allocated branch exists=%t err=%v", exists, err)
	}
	if _, err := os.Stat(unrelated); err != nil {
		t.Fatalf("unrelated worktree was changed: %v", err)
	}
	if exists, err := localBranchExists(repo, "agent/unrelated"); err != nil || !exists {
		t.Fatalf("unrelated branch exists=%t err=%v", exists, err)
	}
}

func TestRollbackAllocationUnlocksAndRemovesRecordedAllocation(t *testing.T) {
	repo := initRollbackTestRepository(t)
	svc := &Service{}
	allocation, err := svc.allocateSessionWorkspaceWithBranchMode(repo, true, "", "agent/routed-locked", "session-locked", true)
	if err != nil {
		t.Fatalf("allocate worktree: %v", err)
	}
	if _, err := runGit(repo, "worktree", "lock", "--reason", "session reservation", allocation.WorkspacePath); err != nil {
		t.Fatalf("lock worktree: %v", err)
	}
	if err := svc.RollbackAllocation(allocation); err != nil {
		t.Fatalf("RollbackAllocation locked worktree: %v", err)
	}
	if _, err := os.Stat(allocation.WorkspacePath); !os.IsNotExist(err) {
		t.Fatalf("locked allocation path still exists or stat failed: %v", err)
	}
	if exists, err := localBranchExists(repo, allocation.BranchName); err != nil || exists {
		t.Fatalf("locked allocation branch exists=%t err=%v", exists, err)
	}
}

func TestRollbackAllocationPreservesChangedBranch(t *testing.T) {
	repo := initRollbackTestRepository(t)
	svc := &Service{}
	allocation, err := svc.allocateSessionWorkspaceWithBranchMode(repo, true, "", "agent/routed-change", "session-2", true)
	if err != nil {
		t.Fatalf("allocate worktree: %v", err)
	}
	if err := os.WriteFile(filepath.Join(allocation.WorkspacePath, "change.txt"), []byte("changed\n"), 0o644); err != nil {
		t.Fatalf("write branch change: %v", err)
	}
	if _, err := runGit(allocation.WorkspacePath, "add", "change.txt"); err != nil {
		t.Fatalf("stage branch change: %v", err)
	}
	if _, err := runGit(allocation.WorkspacePath, "commit", "-m", "change"); err != nil {
		t.Fatalf("commit branch change: %v", err)
	}
	if err := svc.RollbackAllocation(allocation); err == nil {
		t.Fatal("expected changed branch rollback to refuse branch deletion")
	}
	if exists, err := localBranchExists(repo, allocation.BranchName); err != nil || !exists {
		t.Fatalf("changed branch exists=%t err=%v", exists, err)
	}
}

func TestRollbackAllocationRefusesMismatchedPath(t *testing.T) {
	repo := initRollbackTestRepository(t)
	svc := &Service{}
	allocation, err := svc.allocateSessionWorkspaceWithBranchMode(repo, true, "", "agent/routed-start", "session-1", true)
	if err != nil {
		t.Fatalf("allocate worktree: %v", err)
	}
	originalPath := allocation.WorkspacePath
	allocation.WorkspacePath = filepath.Join(t.TempDir(), "unrelated")
	if err := svc.RollbackAllocation(allocation); err == nil {
		t.Fatal("expected mismatched path rollback to fail")
	}
	if _, err := os.Stat(originalPath); err != nil {
		t.Fatalf("recorded worktree was changed: %v", err)
	}
}

func initRollbackTestRepository(t *testing.T) string {
	t.Helper()
	repo := t.TempDir()
	if _, err := runGit(repo, "init", "-b", "dev"); err != nil {
		t.Fatalf("init repository: %v", err)
	}
	_, _ = runGit(repo, "config", "user.email", "test@example.invalid")
	_, _ = runGit(repo, "config", "user.name", "Test User")
	if err := os.WriteFile(filepath.Join(repo, "README.md"), []byte("base\n"), 0o644); err != nil {
		t.Fatalf("write fixture: %v", err)
	}
	if _, err := runGit(repo, "add", "README.md"); err != nil {
		t.Fatalf("stage fixture: %v", err)
	}
	if _, err := runGit(repo, "commit", "-m", "base"); err != nil {
		t.Fatalf("commit fixture: %v", err)
	}
	return repo
}
