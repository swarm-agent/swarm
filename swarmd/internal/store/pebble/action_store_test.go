package pebblestore

import (
	"path/filepath"
	"strings"
	"testing"
)

func openWorkspaceActionTestStore(t *testing.T) *WorkspaceActionStore {
	t.Helper()
	store, err := Open(filepath.Join(t.TempDir(), "actions.pebble"))
	if err != nil {
		t.Fatalf("open store: %v", err)
	}
	t.Cleanup(func() {
		if err := store.Close(); err != nil {
			t.Errorf("close store: %v", err)
		}
	})
	return NewWorkspaceActionStore(store)
}

func workspaceActionTestRecord(id, name string) WorkspaceAction {
	return WorkspaceAction{
		ID:             id,
		AccountScopeID: "account-a",
		WorkspaceID:    "workspace-a",
		WorkspacePath:  "/workspace-a",
		Name:           name,
		Entrypoint:     "scripts/run.sh",
	}
}

func TestNormalizeWorkspaceActionRejectsUnsafeDefinitions(t *testing.T) {
	tests := []struct {
		name   string
		mutate func(*WorkspaceAction)
	}{
		{name: "absolute", mutate: func(action *WorkspaceAction) { action.Entrypoint = "/bin/sh" }},
		{name: "windows absolute", mutate: func(action *WorkspaceAction) { action.Entrypoint = `C:\\Windows\\system32\\cmd.exe` }},
		{name: "traversal", mutate: func(action *WorkspaceAction) { action.Entrypoint = "scripts/../run.sh" }},
		{name: "name control", mutate: func(action *WorkspaceAction) { action.Name = "unsafe\nname" }},
		{name: "argument null", mutate: func(action *WorkspaceAction) { action.Arguments = []string{"bad\x00arg"} }},
		{name: "input id", mutate: func(action *WorkspaceAction) {
			action.Inputs = []WorkspaceActionInput{{ID: "bad id", Label: "Bad", Kind: WorkspaceActionInputKindText}}
		}},
		{name: "duplicate input", mutate: func(action *WorkspaceAction) {
			action.Inputs = []WorkspaceActionInput{{ID: "value", Label: "First"}, {ID: "value", Label: "Second"}}
		}},
		{name: "secret default", mutate: func(action *WorkspaceAction) {
			action.Inputs = []WorkspaceActionInput{{ID: "token", Label: "Token", Kind: WorkspaceActionInputKindSecret, Default: "persisted-secret"}}
		}},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			action := workspaceActionTestRecord("action-a", "Action")
			test.mutate(&action)
			if _, err := NormalizeWorkspaceAction(action); err == nil {
				t.Fatal("NormalizeWorkspaceAction error = nil, want rejection")
			}
		})
	}
}

func TestWorkspaceActionStorePersistsPinnedAndOrder(t *testing.T) {
	actions := openWorkspaceActionTestStore(t)
	first, err := actions.Append(workspaceActionTestRecord("action-a", "First"))
	if err != nil {
		t.Fatalf("append first: %v", err)
	}
	secondRecord := workspaceActionTestRecord("action-b", "Second")
	secondRecord.Pinned = true
	second, err := actions.Append(secondRecord)
	if err != nil {
		t.Fatalf("append second: %v", err)
	}
	if first.SortIndex != 0 || second.SortIndex != 1 || !second.Pinned {
		t.Fatalf("appended actions = %+v, %+v", first, second)
	}

	ordered, err := actions.Reorder("account-a", "workspace-a", []string{second.ID, first.ID})
	if err != nil {
		t.Fatalf("reorder: %v", err)
	}
	if len(ordered) != 2 || ordered[0].ID != second.ID || ordered[0].SortIndex != 0 || !ordered[0].Pinned || ordered[1].SortIndex != 1 {
		t.Fatalf("ordered actions = %+v", ordered)
	}

	listed, err := actions.List("account-a", "workspace-a", 10)
	if err != nil {
		t.Fatalf("list: %v", err)
	}
	if len(listed) != 2 || listed[0].ID != second.ID || !listed[0].Pinned || listed[1].ID != first.ID {
		t.Fatalf("reloaded actions = %+v", listed)
	}

	if deleted, err := actions.Delete("account-a", "workspace-a", second.ID); err != nil || !deleted {
		t.Fatalf("delete: deleted=%t err=%v", deleted, err)
	}
	listed, err = actions.List("account-a", "workspace-a", 10)
	if err != nil {
		t.Fatalf("list after delete: %v", err)
	}
	if len(listed) != 1 || listed[0].ID != first.ID || listed[0].SortIndex != 0 {
		t.Fatalf("actions after delete = %+v", listed)
	}
}

func TestNormalizeWorkspaceActionBoundsArguments(t *testing.T) {
	action := workspaceActionTestRecord("action-a", "Action")
	action.Arguments = []string{strings.Repeat("x", maxWorkspaceActionArgumentBytes+1)}
	if _, err := NormalizeWorkspaceAction(action); err == nil {
		t.Fatal("oversized argument accepted")
	}
}
