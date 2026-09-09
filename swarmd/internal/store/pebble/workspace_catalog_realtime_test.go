package pebblestore

import (
	"path/filepath"
	"testing"
)

// Requirement: catalog changes and their account-scoped V3 signal commit together.
// Threat: missed updates after restart or signals for rejected/partial writes.
// Authority: putWorkspaceCatalogMutationAtomic -> ApplyV3SessionMutation; the
// temporary-store layer proves both persisted catalog and outbox postconditions.
func TestWorkspaceCatalogRealtimeAtomic(t *testing.T) {
	path := filepath.Join(t.TempDir(), "db")
	store, err := Open(path)
	if err != nil {
		t.Fatal(err)
	}
	ws := NewWorkspaceStore(store)
	entry, _, err := ws.CreateForAccountIfAbsent("account-a", "/workspace", "Before", "")
	if err != nil {
		t.Fatal(err)
	}
	sessions := NewSessionStore(store)
	check := func(want int) {
		t.Helper()
		rows, err := sessions.ListV3RealtimeOutboxAfter(0, 100)
		if err != nil || len(rows) != want {
			t.Fatalf("outbox: %d %v, want %d", len(rows), err, want)
		}
		for _, row := range rows {
			if row.AccountScopeID != "account-a" || row.Event.EventType != WorkspaceCatalogEventType || string(row.Event.Payload) != "{}" {
				t.Fatalf("unexpected signal: %+v", row)
			}
		}
	}
	check(1)
	name := "After"
	if _, err := ws.UpdateForWorkspaceIDForAccountGuarded("account-a", "user", entry.WorkspaceID, WorkspaceCatalogUpdate{ExpectedGeneration: entry.WorkspaceGeneration + 10, Name: &name}); err == nil {
		t.Fatal("accepted stale update")
	}
	if _, err := ws.DeleteForWorkspaceIDForAccountGuarded("account-b", "user", entry.WorkspaceID, entry.WorkspaceGeneration); err == nil {
		t.Fatal("accepted foreign deletion")
	}
	// Fail inside the shared batch after entry writes, before commit.
	if err := ws.putWorkspaceCatalogMutationAtomic("account-a", "", WorkspaceEntry{WorkspaceID: entry.WorkspaceID, Path: entry.Path, Name: "Partial"}, "", &WorkspaceBinding{Path: entry.Path}, false); err == nil {
		t.Fatal("accepted missing binding user")
	}
	got, ok, err := ws.GetByWorkspaceIDForAccount("account-a", entry.WorkspaceID)
	if err != nil || !ok || got.Name != "Before" {
		t.Fatalf("partial change: %+v %v", got, err)
	}
	check(1)
	entry, err = ws.UpdateForWorkspaceIDForAccountGuarded("account-a", "user", entry.WorkspaceID, WorkspaceCatalogUpdate{ExpectedGeneration: entry.WorkspaceGeneration, Name: &name})
	if err != nil {
		t.Fatal(err)
	}
	check(2)
	if _, err = ws.DeleteForWorkspaceIDForAccountGuarded("account-a", "user", entry.WorkspaceID, entry.WorkspaceGeneration); err != nil {
		t.Fatal(err)
	}
	check(3)
	if err := store.Close(); err != nil {
		t.Fatal(err)
	}
	store, err = Open(path)
	if err != nil {
		t.Fatal(err)
	}
	defer store.Close()
	sessions = NewSessionStore(store)
	check(3)
	if _, ok, err := NewWorkspaceStore(store).GetByWorkspaceIDForAccount("account-a", entry.WorkspaceID); err != nil || ok {
		t.Fatalf("deleted row resurrected: %v %v", ok, err)
	}
}
