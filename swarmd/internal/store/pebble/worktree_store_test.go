package pebblestore

import (
	"path/filepath"
	"testing"
)

func newTestWorktreeStore(t *testing.T) *WorktreeStore {
	t.Helper()
	store, err := Open(filepath.Join(t.TempDir(), "worktree.pebble"))
	if err != nil {
		t.Fatalf("open store: %v", err)
	}
	t.Cleanup(func() { _ = store.Close() })
	return NewWorktreeStore(store)
}

func TestWorktreeStoreAccountConfigIsolation(t *testing.T) {
	worktrees := newTestWorktreeStore(t)
	if _, err := worktrees.SetConfigForAccount("account-a", "/tmp/ws", true, false, "main", "agent-a"); err != nil {
		t.Fatalf("set config A: %v", err)
	}
	if _, err := worktrees.SetConfigForAccount("account-b", "/tmp/ws", false, true, "", "agent-b"); err != nil {
		t.Fatalf("set config B: %v", err)
	}
	cfgA, ok, err := worktrees.GetConfigForAccount("account-a", "/tmp/ws")
	if err != nil || !ok || !cfgA.Enabled || cfgA.AccountScopeID != "account-a" || cfgA.BranchName != "agent-a" {
		t.Fatalf("config A = %+v ok=%v err=%v", cfgA, ok, err)
	}
	cfgB, ok, err := worktrees.GetConfigForAccount("account-b", "/tmp/ws")
	if err != nil || !ok || cfgB.Enabled || cfgB.AccountScopeID != "account-b" || cfgB.BranchName != "agent-b" {
		t.Fatalf("config B = %+v ok=%v err=%v", cfgB, ok, err)
	}
	cfgMissing, ok, err := worktrees.GetConfigForAccount("account-a", "/tmp/other")
	if err != nil || ok || cfgMissing.Enabled {
		t.Fatalf("missing config = %+v ok=%v err=%v", cfgMissing, ok, err)
	}
	if _, _, err := worktrees.GetConfig("/tmp/ws"); err == nil {
		t.Fatalf("legacy config unexpectedly succeeded")
	}
}
