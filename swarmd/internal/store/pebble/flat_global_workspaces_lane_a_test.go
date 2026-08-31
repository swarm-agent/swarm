package pebblestore

import (
	"encoding/json"
	"path/filepath"
	"reflect"
	"testing"
)

// Lane A: E2E-001, E2E-002, E2E-003, E2E-004, E2E-005, E2E-011, E2E-013.
// REQ-ACC-001, REQ-GEN-001, REQ-LNK-001.
func TestFlatGlobalWorkspacesLaneAIdentityAccountIsolationAndRestart(t *testing.T) {
	dbPath := filepath.Join(t.TempDir(), "lane-a-restart.pebble")
	store, err := Open(dbPath)
	if err != nil {
		t.Fatalf("open initial store: %v", err)
	}
	workspaces := NewWorkspaceStore(store)

	accountA, err := workspaces.AddForAccount("account-a", "/workspace/shared", "A")
	if err != nil {
		t.Fatalf("add account A workspace: %v", err)
	}
	accountB, err := workspaces.AddForAccount("account-b", "/workspace/shared", "B")
	if err != nil {
		t.Fatalf("add account B workspace: %v", err)
	}
	if accountA.WorkspaceID == accountB.WorkspaceID {
		t.Fatalf("cross-account workspace IDs collided: %q", accountA.WorkspaceID)
	}
	if accountA.WorkspaceGeneration != 1 || accountB.WorkspaceGeneration != 1 {
		t.Fatalf("initial generations: account-a=%d account-b=%d", accountA.WorkspaceGeneration, accountB.WorkspaceGeneration)
	}

	renamed, err := workspaces.RenameForAccount("account-a", "user-a", accountA.Path, "A renamed")
	if err != nil {
		t.Fatalf("rename account A workspace: %v", err)
	}
	themed, err := workspaces.SetThemeIDForAccount("account-a", accountA.Path, "Ocean Blue")
	if err != nil {
		t.Fatalf("theme account A workspace: %v", err)
	}
	if renamed.WorkspaceID != accountA.WorkspaceID || themed.WorkspaceID != accountA.WorkspaceID || themed.WorkspaceGeneration != accountA.WorkspaceGeneration {
		t.Fatalf("metadata update changed stable identity: initial=%+v renamed=%+v themed=%+v", accountA, renamed, themed)
	}
	if len(themed.Directories) != 1 || themed.Directories[0] != themed.Path {
		t.Fatalf("metadata update restored linked authority: %+v", themed.Directories)
	}
	if got, ok, err := workspaces.GetByWorkspaceIDForAccount("account-b", accountA.WorkspaceID); err != nil || ok {
		t.Fatalf("account B resolved account A identity: got=%+v ok=%v err=%v", got, ok, err)
	}
	if err := store.Close(); err != nil {
		t.Fatalf("close initial store: %v", err)
	}

	reopened, err := Open(dbPath)
	if err != nil {
		t.Fatalf("reopen store: %v", err)
	}
	defer reopened.Close()
	persisted := NewWorkspaceStore(reopened)
	gotA, ok, err := persisted.GetByWorkspaceIDForAccount("account-a", accountA.WorkspaceID)
	if err != nil || !ok {
		t.Fatalf("get account A after restart: ok=%v err=%v", ok, err)
	}
	if gotA.Name != "A renamed" || gotA.ThemeID != "ocean-blue" || gotA.Path != accountA.Path || gotA.WorkspaceGeneration != accountA.WorkspaceGeneration {
		t.Fatalf("account A changed across restart: %+v", gotA)
	}
	listB, err := persisted.ListForAccount("account-b", 10)
	if err != nil || len(listB) != 1 || listB[0].WorkspaceID != accountB.WorkspaceID || listB[0].Name != "B" {
		t.Fatalf("account B changed or leaked across restart: list=%+v err=%v", listB, err)
	}
}

// Lane A: E2E-006, E2E-007. REQ-LNK-002, REQ-ACC-001.
func TestFlatGlobalWorkspacesLaneAE2E002GlobalCatalogPaginationAndE2E004ExplicitDelete(t *testing.T) {
	workspaces := newTestWorkspaceStore(t)
	const account = "account-a"
	paths := []string{"/workspace/alpha", "/workspace/bravo", "/workspace/charlie", "/workspace/delta", "/workspace/echo"}
	entries := make(map[string]WorkspaceEntry, len(paths))
	for _, path := range paths {
		entry, err := workspaces.AddForAccount(account, path, filepath.Base(path))
		if err != nil {
			t.Fatalf("add workspace %q: %v", path, err)
		}
		entries[path] = entry
	}

	var walked []WorkspaceEntry
	for offset := 0; ; offset += 2 {
		all, err := workspaces.ListForAccount(account, 100)
		if err != nil {
			t.Fatalf("list catalog page at offset %d: %v", offset, err)
		}
		if offset >= len(all) {
			break
		}
		end := offset + 2
		if end > len(all) {
			end = len(all)
		}
		walked = append(walked, all[offset:end]...)
	}
	if len(walked) != len(paths) {
		t.Fatalf("walked catalog entries=%d, want %d: %+v", len(walked), len(paths), walked)
	}
	seenPaths, seenIDs := map[string]bool{}, map[string]bool{}
	for i, entry := range walked {
		if seenPaths[entry.Path] || seenIDs[entry.WorkspaceID] {
			t.Fatalf("catalog page walk duplicated entry at %d: %+v", i, entry)
		}
		seenPaths[entry.Path], seenIDs[entry.WorkspaceID] = true, true
		if entry.Path != paths[i] {
			t.Fatalf("catalog order[%d]=%q, want %q", i, entry.Path, paths[i])
		}
	}

	deleted := paths[2]
	if err := workspaces.DeleteForAccount(account, "user-a", deleted); err != nil {
		t.Fatalf("delete workspace %q: %v", deleted, err)
	}
	if _, ok, err := workspaces.GetForAccount(account, deleted); err != nil || ok {
		t.Fatalf("deleted workspace still resolves: ok=%t err=%v", ok, err)
	}
	if _, ok, err := workspaces.GetByWorkspaceIDForAccount(account, entries[deleted].WorkspaceID); err != nil || ok {
		t.Fatalf("deleted workspace id still resolves: ok=%t err=%v", ok, err)
	}
	remaining, err := workspaces.ListForAccount(account, 100)
	if err != nil {
		t.Fatalf("list catalog after delete: %v", err)
	}
	if len(remaining) != len(paths)-1 {
		t.Fatalf("remaining catalog entries=%d, want %d: %+v", len(remaining), len(paths)-1, remaining)
	}
	for _, entry := range remaining {
		if entry.Path == deleted || entry.WorkspaceID == entries[deleted].WorkspaceID {
			t.Fatalf("explicit delete left deleted identity in catalog: %+v", entry)
		}
		if want := entries[entry.Path]; want.WorkspaceID != entry.WorkspaceID || want.WorkspaceGeneration != entry.WorkspaceGeneration {
			t.Fatalf("explicit delete mutated unrelated workspace: got=%+v want=%+v", entry, want)
		}
	}
}

func TestFlatGlobalWorkspacesLaneAMigrationIsDeterministicAndExistingEntryWins(t *testing.T) {
	workspaces := newTestWorkspaceStore(t)
	const account = "account-a"
	existing, err := workspaces.AddForAccount(account, "/workspace/existing-child", "Existing child")
	if err != nil {
		t.Fatalf("add existing child: %v", err)
	}
	parent := WorkspaceEntry{
		AccountScopeID: account, WorkspaceID: "ws-parent", WorkspaceGeneration: 9,
		Path: "/workspace/parent", Name: "Parent", ThemeID: "dark",
		Directories: []string{
			"/workspace/parent",
			"/workspace/new-child",
			"/workspace/new-child",
			"/workspace/existing-child",
		},
	}
	if err := workspaces.store.PutJSON(KeyWorkspaceEntryForAccount(account, parent.Path), parent); err != nil {
		t.Fatalf("write historical linked entry: %v", err)
	}

	first, err := workspaces.ListForAccount(account, 20)
	if err != nil {
		t.Fatalf("first migration list: %v", err)
	}
	second, err := workspaces.ListForAccount(account, 20)
	if err != nil {
		t.Fatalf("second migration list: %v", err)
	}
	if len(first) != 3 || len(second) != 3 {
		t.Fatalf("migration cardinality first=%d second=%d: first=%+v second=%+v", len(first), len(second), first, second)
	}

	firstIDs := map[string]string{}
	for _, entry := range first {
		firstIDs[entry.Path] = entry.WorkspaceID
		if !reflect.DeepEqual(entry.Directories, []string{entry.Path}) {
			t.Fatalf("entry %q retained linked authority: %+v", entry.Path, entry.Directories)
		}
	}
	for _, entry := range second {
		if firstIDs[entry.Path] != entry.WorkspaceID {
			t.Fatalf("migration retry changed identity for %q: first=%q second=%q", entry.Path, firstIDs[entry.Path], entry.WorkspaceID)
		}
	}
	if firstIDs[existing.Path] != existing.WorkspaceID {
		t.Fatalf("historical parent replaced existing independent child: got=%q want=%q", firstIDs[existing.Path], existing.WorkspaceID)
	}
	if got := firstIDs["/workspace/new-child"]; got == "" || got != legacyWorkspaceID(account, "/workspace/new-child") {
		t.Fatalf("migrated child ID=%q, want deterministic %q", got, legacyWorkspaceID(account, "/workspace/new-child"))
	}
}

// Lane A: E2E-003, E2E-013. REQ-GEN-001.
func TestFlatGlobalWorkspacesLaneAPathGenerationConflictHasNoMutation(t *testing.T) {
	workspaces := newTestWorkspaceStore(t)
	left, err := workspaces.AddForAccount("account-a", "/workspace/left", "Left")
	if err != nil {
		t.Fatalf("add left: %v", err)
	}
	right, err := workspaces.AddForAccount("account-a", "/workspace/right", "Right")
	if err != nil {
		t.Fatalf("add right: %v", err)
	}
	if _, err := workspaces.UpdatePathForWorkspaceIDForAccount("account-a", left.WorkspaceID, right.Path); err == nil {
		t.Fatal("conflicting path update unexpectedly succeeded")
	}
	for _, want := range []WorkspaceEntry{left, right} {
		got, ok, err := workspaces.GetByWorkspaceIDForAccount("account-a", want.WorkspaceID)
		if err != nil || !ok {
			t.Fatalf("read workspace %q after conflict: ok=%v err=%v", want.WorkspaceID, ok, err)
		}
		if got.Path != want.Path || got.WorkspaceGeneration != want.WorkspaceGeneration {
			t.Fatalf("conflict mutated workspace %q: got=%+v want=%+v", want.WorkspaceID, got, want)
		}
	}
}

// Lane A: E2E-042, E2E-043, E2E-044, E2E-046.
// REQ-V3-002, REQ-V3-003, REQ-V3-004, REQ-V3-005.
func TestFlatGlobalWorkspacesLaneATypedProjectionIsDeterministicPathFreeAndHistorical(t *testing.T) {
	available, unavailable := true, false
	session := SessionSnapshot{WorkspaceGrants: []WorkspaceGrant{
		{Kind: WorkspaceGrantTemporary, Path: "/private/anonymous", Available: &available},
		{Kind: WorkspaceGrantWorktree, WorkspaceID: "ws-live", WorkspaceGeneration: 4, Path: "/private/worktree", Name: "Live", Available: &available},
		{Kind: WorkspaceGrantPrimary, WorkspaceID: "ws-old", WorkspaceGeneration: 7, Path: "/private/deleted", Name: "Deleted", Available: &unavailable},
		{Kind: WorkspaceGrantPrimary, WorkspaceID: "ws-old", WorkspaceGeneration: 7, Path: "/private/deleted", Name: "Deleted", Available: &unavailable},
	}}
	grants := NormalizeSessionWorkspaceGrants(session)
	if len(grants) != 3 {
		t.Fatalf("normalized grants=%#v, want one primary, worktree, and anonymous temporary grant", grants)
	}
	if grants[0].Kind != WorkspaceGrantPrimary || grants[0].WorkspaceID != "ws-old" || grants[0].Available == nil || *grants[0].Available {
		t.Fatalf("historical primary grant lost typed unavailable identity: %#v", grants[0])
	}
	if grants[1].Kind != WorkspaceGrantWorktree || grants[2].Kind != WorkspaceGrantTemporary {
		t.Fatalf("grant ordering is not deterministic: %#v", grants)
	}

	usage := WorkspaceUsageFromGrants(grants)
	if len(usage) != 2 || usage[0].WorkspaceID != "ws-old" || usage[1].WorkspaceID != "ws-live" {
		t.Fatalf("path-free usage projection=%#v", usage)
	}
	payload, err := json.Marshal(struct {
		WorkspaceGrants []WorkspaceGrant           `json:"workspace_grants"`
		WorkspaceUsage  []WorkspaceUsageProjection `json:"workspace_usage"`
	}{WorkspaceGrants: grants, WorkspaceUsage: usage})
	if err != nil {
		t.Fatalf("marshal realtime membership payload: %v", err)
	}
	var decoded struct {
		WorkspaceUsage []map[string]any `json:"workspace_usage"`
	}
	if err := json.Unmarshal(payload, &decoded); err != nil {
		t.Fatalf("decode realtime membership payload: %v", err)
	}
	for _, item := range decoded.WorkspaceUsage {
		if _, exposed := item["path"]; exposed {
			t.Fatalf("workspace usage exposed path: %s", payload)
		}
	}
}
