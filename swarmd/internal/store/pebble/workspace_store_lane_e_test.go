package pebblestore

import (
	"path/filepath"
	"strings"
	"testing"
)

// TestLaneE_E2E012PrincipalLessLegacyMutationsFailClosed covers
// E2E-012/REQ-ACC-002 and records both the returned denial and the absence of
// account-scoped or legacy persistence side effects.
func TestLaneE_E2E012PrincipalLessLegacyMutationsFailClosed(t *testing.T) {
	store, err := Open(filepath.Join(t.TempDir(), "lane-e-workspaces.pebble"))
	if err != nil {
		t.Fatalf("open store: %v", err)
	}
	defer store.Close()
	workspaces := NewWorkspaceStore(store)
	path := filepath.Join(t.TempDir(), "workspace")

	checks := []struct {
		name string
		run  func() error
	}{
		{name: "set current", run: func() error { _, err := workspaces.SetCurrent(path, "name"); return err }},
		{name: "add", run: func() error { _, err := workspaces.Add(path, "name"); return err }},
		{name: "save", run: func() error { _, err := workspaces.Save(path, "name", "", true); return err }},
		{name: "rename", run: func() error { _, err := workspaces.Rename(path, "renamed"); return err }},
		{name: "theme", run: func() error { _, err := workspaces.SetThemeID(path, "dark"); return err }},
		{name: "add directory", run: func() error { _, err := workspaces.AddDirectory(path, t.TempDir()); return err }},
		{name: "remove directory", run: func() error { _, err := workspaces.RemoveDirectory(path, t.TempDir()); return err }},
	}
	for _, check := range checks {
		t.Run(check.name, func(t *testing.T) {
			if err := check.run(); err == nil || !strings.Contains(err.Error(), "account scope is required") {
				t.Fatalf("legacy mutation error = %v", err)
			}
		})
	}
	legacy, err := workspaces.ListLegacy(100)
	if err != nil {
		t.Fatalf("list legacy entries: %v", err)
	}
	if len(legacy) != 0 {
		t.Fatalf("principal-less mutation persisted legacy state: %+v", legacy)
	}
	account, err := workspaces.ListForAccount("account-1", 100)
	if err != nil {
		t.Fatalf("list account entries: %v", err)
	}
	if len(account) != 0 {
		t.Fatalf("principal-less mutation persisted account state: %+v", account)
	}
}

// TestLaneE_REQ_LNK_003LinkedAuthorityCannotBeReintroduced covers the persistent
// linked-authority retirement boundary. This is requirement-level evidence, not
// E2E-035: that scenario requires destructive-operation classification/gating.
func TestLaneE_REQ_LNK_003LinkedAuthorityCannotBeReintroduced(t *testing.T) {
	store, err := Open(filepath.Join(t.TempDir(), "lane-e-linked.pebble"))
	if err != nil {
		t.Fatalf("open store: %v", err)
	}
	defer store.Close()
	workspaces := NewWorkspaceStore(store)
	primary := filepath.Join(t.TempDir(), "primary")
	linked := filepath.Join(t.TempDir(), "linked")
	created, err := workspaces.AddForAccount("account-1", primary, "primary")
	if err != nil {
		t.Fatalf("create flat workspace: %v", err)
	}

	if _, err := workspaces.AddDirectoryForAccount("account-1", primary, linked); err == nil || !strings.Contains(err.Error(), "linked workspace directories are retired") {
		t.Fatalf("linked add error = %v", err)
	}
	if _, err := workspaces.RemoveDirectoryForAccount("account-1", primary, linked); err == nil || !strings.Contains(err.Error(), "linked workspace directories are retired") {
		t.Fatalf("linked remove error = %v", err)
	}
	persisted, ok, err := workspaces.GetForAccount("account-1", primary)
	if err != nil || !ok {
		t.Fatalf("get flat workspace ok=%t err=%v", ok, err)
	}
	if persisted.WorkspaceID != created.WorkspaceID || persisted.WorkspaceGeneration != created.WorkspaceGeneration || len(persisted.Directories) != 1 || persisted.Directories[0] != primary {
		t.Fatalf("linked bypass mutated flat workspace: before=%+v after=%+v", created, persisted)
	}
	entries, err := workspaces.ListForAccount("account-1", 100)
	if err != nil || len(entries) != 1 {
		t.Fatalf("linked bypass created independent state: entries=%+v err=%v", entries, err)
	}
}
