package pebblestore

import (
	"path/filepath"
	"testing"
)

func TestSwarmDesktopTargetSelectionForAccountNoGlobalFallback(t *testing.T) {
	store, err := Open(filepath.Join(t.TempDir(), "desktop-target-no-fallback.pebble"))
	if err != nil {
		t.Fatalf("open store: %v", err)
	}
	defer func() { _ = store.Close() }()

	selection := NewSwarmDesktopTargetSelectionStore(store)
	if _, err := selection.Put("global-swarm"); err != nil {
		t.Fatalf("put global selection: %v", err)
	}
	if _, ok, err := selection.GetForAccount("account-a"); err != nil || ok {
		t.Fatalf("account selection fell back to global: ok=%v err=%v", ok, err)
	}
	if _, err := selection.PutForAccount("account-a", "user-a", "account-a-swarm"); err != nil {
		t.Fatalf("put account selection: %v", err)
	}
	recordA, ok, err := selection.GetForAccount("account-a")
	if err != nil || !ok {
		t.Fatalf("get account selection: ok=%v err=%v", ok, err)
	}
	if recordA.SwarmID != "account-a-swarm" || recordA.AccountScopeID != "account-a" || recordA.UserID != "user-a" {
		t.Fatalf("account record = %+v", recordA)
	}
	if _, ok, err := selection.GetForAccount("account-b"); err != nil || ok {
		t.Fatalf("account B selection read account A/global: ok=%v err=%v", ok, err)
	}
}
