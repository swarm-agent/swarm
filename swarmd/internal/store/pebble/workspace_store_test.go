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

func TestWorkspaceStoreSaveForAccountWithResultReportsCreation(t *testing.T) {
	workspaces := newTestWorkspaceStore(t)

	first, created, err := workspaces.SaveForAccountWithResult("account-a", "/tmp/ws-created", "Workspace", "", false)
	if err != nil || !created {
		t.Fatalf("first save created=%v err=%v", created, err)
	}
	second, created, err := workspaces.SaveForAccountWithResult("account-a", "/tmp/ws-created", "Workspace", "", false)
	if err != nil || created {
		t.Fatalf("second save created=%v err=%v", created, err)
	}
	if first.WorkspaceID != second.WorkspaceID {
		t.Fatalf("duplicate save changed workspace id: first=%q second=%q", first.WorkspaceID, second.WorkspaceID)
	}
}

func TestWorkspaceStoreStableWorkspaceIDAndGeneration(t *testing.T) {
	workspaces := newTestWorkspaceStore(t)

	first, err := workspaces.AddForAccount("account-a", "/tmp/ws-a", "Workspace A")
	if err != nil {
		t.Fatalf("add workspace: %v", err)
	}
	if first.WorkspaceID == "" {
		t.Fatalf("workspace id is empty: %+v", first)
	}
	if first.WorkspaceGeneration != 1 {
		t.Fatalf("workspace generation = %d, want 1", first.WorkspaceGeneration)
	}
	if first.State != "active" {
		t.Fatalf("workspace state = %q, want active", first.State)
	}

	again, err := workspaces.AddForAccount("account-a", "/tmp/ws-a", "Workspace A renamed")
	if err != nil {
		t.Fatalf("duplicate add workspace: %v", err)
	}
	if again.WorkspaceID != first.WorkspaceID {
		t.Fatalf("duplicate add changed workspace id: got %q want %q", again.WorkspaceID, first.WorkspaceID)
	}
	if again.WorkspaceGeneration != first.WorkspaceGeneration {
		t.Fatalf("duplicate add changed generation: got %d want %d", again.WorkspaceGeneration, first.WorkspaceGeneration)
	}

	renamed, err := workspaces.RenameForAccount("account-a", "user-a", "/tmp/ws-a", "Renamed")
	if err != nil {
		t.Fatalf("rename workspace: %v", err)
	}
	if renamed.WorkspaceID != first.WorkspaceID {
		t.Fatalf("rename changed workspace id: got %q want %q", renamed.WorkspaceID, first.WorkspaceID)
	}
	if renamed.WorkspaceGeneration != first.WorkspaceGeneration {
		t.Fatalf("rename changed generation: got %d want %d", renamed.WorkspaceGeneration, first.WorkspaceGeneration)
	}

	themed, err := workspaces.SetThemeIDForAccount("account-a", "/tmp/ws-a", "dark-mode")
	if err != nil {
		t.Fatalf("set theme: %v", err)
	}
	if themed.WorkspaceID != first.WorkspaceID || themed.WorkspaceGeneration != first.WorkspaceGeneration {
		t.Fatalf("theme changed identity: %+v want id=%q generation=%d", themed, first.WorkspaceID, first.WorkspaceGeneration)
	}

	withDirectory, err := workspaces.AddDirectoryForAccount("account-a", "/tmp/ws-a", "/tmp/ws-a-extra")
	if err != nil {
		t.Fatalf("add directory: %v", err)
	}
	if withDirectory.WorkspaceID != first.WorkspaceID || withDirectory.WorkspaceGeneration != first.WorkspaceGeneration {
		t.Fatalf("add directory changed identity: %+v want id=%q generation=%d", withDirectory, first.WorkspaceID, first.WorkspaceGeneration)
	}

	moved, err := workspaces.MoveForAccount("account-a", "/tmp/ws-a", 0)
	if err != nil {
		t.Fatalf("move workspace: %v", err)
	}
	if moved.WorkspaceID != first.WorkspaceID || moved.WorkspaceGeneration != first.WorkspaceGeneration {
		t.Fatalf("move changed identity: %+v want id=%q generation=%d", moved, first.WorkspaceID, first.WorkspaceGeneration)
	}

	byID, ok, err := workspaces.GetByWorkspaceIDForAccount("account-a", first.WorkspaceID)
	if err != nil || !ok {
		t.Fatalf("get by workspace id ok=%v err=%v", ok, err)
	}
	if byID.Path != renamed.Path || byID.Name != renamed.Name {
		t.Fatalf("get by id = %+v, want path/name from renamed %+v", byID, renamed)
	}
}

func TestWorkspaceStoreDuplicateNamesHaveDifferentWorkspaceIDs(t *testing.T) {
	workspaces := newTestWorkspaceStore(t)

	left, err := workspaces.AddForAccount("account-a", "/tmp/ws-left", "Duplicate")
	if err != nil {
		t.Fatalf("add left: %v", err)
	}
	right, err := workspaces.AddForAccount("account-a", "/tmp/ws-right", "Duplicate")
	if err != nil {
		t.Fatalf("add right: %v", err)
	}
	if left.WorkspaceID == "" || right.WorkspaceID == "" {
		t.Fatalf("workspace ids are required: left=%+v right=%+v", left, right)
	}
	if left.WorkspaceID == right.WorkspaceID {
		t.Fatalf("duplicate names reused workspace id %q", left.WorkspaceID)
	}
}

func TestWorkspaceStorePathUpdatePreservesWorkspaceIDAndIncrementsGeneration(t *testing.T) {
	workspaces := newTestWorkspaceStore(t)

	created, err := workspaces.AddForAccount("account-a", "/tmp/source", "Source")
	if err != nil {
		t.Fatalf("add source: %v", err)
	}
	moved, err := workspaces.UpdatePathForWorkspaceIDForAccount("account-a", created.WorkspaceID, "/tmp/destination")
	if err != nil {
		t.Fatalf("update path: %v", err)
	}
	if moved.WorkspaceID != created.WorkspaceID {
		t.Fatalf("path update changed workspace id: got %q want %q", moved.WorkspaceID, created.WorkspaceID)
	}
	if moved.WorkspaceGeneration != created.WorkspaceGeneration+1 {
		t.Fatalf("path update generation = %d, want %d", moved.WorkspaceGeneration, created.WorkspaceGeneration+1)
	}
	if _, ok, err := workspaces.GetForAccount("account-a", "/tmp/source"); err != nil || ok {
		t.Fatalf("old path lookup ok=%v err=%v", ok, err)
	}
	byID, ok, err := workspaces.GetByWorkspaceIDForAccount("account-a", created.WorkspaceID)
	if err != nil || !ok {
		t.Fatalf("get moved by id ok=%v err=%v", ok, err)
	}
	if byID.Path != "/tmp/destination" {
		t.Fatalf("moved path = %q", byID.Path)
	}
}

func TestWorkspaceStoreLegacyEntryGetsDeterministicWorkspaceIDOnReadAndMutation(t *testing.T) {
	workspaces := newTestWorkspaceStore(t)
	legacy := WorkspaceEntry{AccountScopeID: "account-a", Path: "/tmp/legacy", Name: "Legacy"}
	if err := workspaces.store.PutJSON(KeyWorkspaceEntryForAccount("account-a", legacy.Path), legacy); err != nil {
		t.Fatalf("write legacy entry: %v", err)
	}

	read, ok, err := workspaces.GetForAccount("account-a", legacy.Path)
	if err != nil || !ok {
		t.Fatalf("read legacy ok=%v err=%v", ok, err)
	}
	if read.WorkspaceID == "" {
		t.Fatalf("legacy read did not materialize workspace id: %+v", read)
	}
	if read.WorkspaceGeneration != 1 {
		t.Fatalf("legacy read generation = %d, want 1", read.WorkspaceGeneration)
	}
	expectedID := read.WorkspaceID

	updated, err := workspaces.RenameForAccount("account-a", "", legacy.Path, "Updated Legacy")
	if err != nil {
		t.Fatalf("rename legacy: %v", err)
	}
	if updated.WorkspaceID != expectedID {
		t.Fatalf("legacy mutation changed deterministic id: got %q want %q", updated.WorkspaceID, expectedID)
	}

	byID, ok, err := workspaces.GetByWorkspaceIDForAccount("account-a", expectedID)
	if err != nil || !ok {
		t.Fatalf("legacy mutation did not write id index ok=%v err=%v", ok, err)
	}
	if byID.Name != "Updated Legacy" {
		t.Fatalf("indexed legacy entry name = %q", byID.Name)
	}
}

func TestWorkspaceStoreGetByWorkspaceIDBackfillsMissingIndex(t *testing.T) {
	workspaces := newTestWorkspaceStore(t)
	entry := WorkspaceEntry{
		AccountScopeID:      "account-a",
		WorkspaceID:         "ws_existing_without_index",
		WorkspaceGeneration: 3,
		State:               "active",
		Path:                "/tmp/indexless",
		Name:                "Indexless",
	}
	if err := workspaces.store.PutJSON(KeyWorkspaceEntryForAccount("account-a", entry.Path), entry); err != nil {
		t.Fatalf("write workspace entry without id index: %v", err)
	}

	byID, ok, err := workspaces.GetByWorkspaceIDForAccount("account-a", entry.WorkspaceID)
	if err != nil || !ok {
		t.Fatalf("get by workspace id ok=%v err=%v", ok, err)
	}
	if byID.WorkspaceID != entry.WorkspaceID || byID.Path != entry.Path || byID.WorkspaceGeneration != entry.WorkspaceGeneration {
		t.Fatalf("get by id = %+v, want %+v", byID, entry)
	}

	var indexed WorkspaceEntry
	if ok, err := workspaces.store.GetJSON(KeyWorkspaceEntryByIDForAccount("account-a", entry.WorkspaceID), &indexed); err != nil || !ok {
		t.Fatalf("id index backfill ok=%v err=%v", ok, err)
	}
	if indexed.Path != entry.Path {
		t.Fatalf("indexed path = %q, want %q", indexed.Path, entry.Path)
	}
}

func TestWorkspaceStorePathUpdateRejectsExistingDifferentWorkspacePath(t *testing.T) {
	workspaces := newTestWorkspaceStore(t)
	left, err := workspaces.AddForAccount("account-a", "/tmp/left", "Left")
	if err != nil {
		t.Fatalf("add left: %v", err)
	}
	if _, err := workspaces.AddForAccount("account-a", "/tmp/right", "Right"); err != nil {
		t.Fatalf("add right: %v", err)
	}

	if _, err := workspaces.UpdatePathForWorkspaceIDForAccount("account-a", left.WorkspaceID, "/tmp/right"); err == nil {
		t.Fatalf("path update to another workspace unexpectedly succeeded")
	}
}
