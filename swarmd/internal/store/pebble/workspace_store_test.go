package pebblestore

import (
	"path/filepath"
	"testing"
)

func newTestWorkspaceStore(t *testing.T) *WorkspaceStore {
	t.Helper()
	store, err := Open(filepath.Join(t.TempDir(), "workspace.pebble"))
	if err != nil {
		t.Fatalf("open store: %v", err)
	}
	t.Cleanup(func() { _ = store.Close() })
	return NewWorkspaceStore(store)
}

func TestWorkspaceStoreAccountIsolation(t *testing.T) {
	workspaces := newTestWorkspaceStore(t)
	if _, err := workspaces.SaveForAccount("account-a", "/tmp/ws", "A", "", true); err != nil {
		t.Fatalf("save account A: %v", err)
	}
	if _, err := workspaces.SetCurrentForAccount("account-a", "user-a", "/tmp/ws", "A"); err != nil {
		t.Fatalf("set current account A: %v", err)
	}
	if _, err := workspaces.SaveForAccount("account-b", "/tmp/ws", "B", "", true); err != nil {
		t.Fatalf("save account B: %v", err)
	}
	if _, err := workspaces.SetCurrentForAccount("account-b", "user-b", "/tmp/ws", "B"); err != nil {
		t.Fatalf("set current account B: %v", err)
	}
	listA, err := workspaces.ListForAccount("account-a", 10)
	if err != nil || len(listA) != 1 || listA[0].Name != "A" || listA[0].AccountScopeID != "account-a" {
		t.Fatalf("list A = %+v err=%v", listA, err)
	}
	currentB, ok, err := workspaces.GetCurrentForAccount("account-b", "user-b")
	if err != nil || !ok || currentB.Name != "B" {
		t.Fatalf("current B = %+v ok=%v err=%v", currentB, ok, err)
	}
	if _, ok, err := workspaces.GetForAccount("account-b", "/tmp/missing"); err != nil || ok {
		t.Fatalf("missing B ok=%v err=%v", ok, err)
	}
	if _, _, err := workspaces.GetCurrentForAccount("", "user-a"); err == nil {
		t.Fatalf("empty account current unexpectedly succeeded")
	}
	if _, _, err := workspaces.GetCurrent(); err == nil {
		t.Fatalf("legacy current unexpectedly succeeded")
	}
}
