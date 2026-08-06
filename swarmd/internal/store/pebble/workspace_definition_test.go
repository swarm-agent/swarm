package pebblestore

import (
	"path/filepath"
	"testing"
)

func TestWorkspaceDefinitionGenerationRejectsStaleWrites(t *testing.T) {
	store, err := Open(filepath.Join(t.TempDir(), "workspace-definition.pebble"))
	if err != nil {
		t.Fatalf("open store: %v", err)
	}
	defer store.Close()
	workspaces := NewWorkspaceStore(store)
	entry, err := workspaces.AddForAccount("account", "/workspace", "workspace")
	if err != nil {
		t.Fatalf("add workspace: %v", err)
	}
	first, err := workspaces.MarkDefinitionPendingForAccount("account", entry.Path)
	if err != nil {
		t.Fatalf("mark first pending: %v", err)
	}
	second, err := workspaces.MarkDefinitionPendingForAccount("account", entry.Path)
	if err != nil {
		t.Fatalf("mark second pending: %v", err)
	}
	if second.DefinitionGeneration != first.DefinitionGeneration+1 || second.DefinitionStatus != WorkspaceDefinitionStatusPending {
		t.Fatalf("unexpected second generation: %+v", second)
	}
	if _, current, err := workspaces.CompleteDefinitionForAccount("account", entry.Path, first.DefinitionGeneration, "stale", 1); err != nil || current {
		t.Fatalf("stale completion current=%v err=%v", current, err)
	}
	completed, current, err := workspaces.CompleteDefinitionForAccount("account", entry.Path, second.DefinitionGeneration, "current definition", 2)
	if err != nil || !current {
		t.Fatalf("complete current generation current=%v err=%v", current, err)
	}
	if completed.Definition != "current definition" || completed.DefinitionAttemptCount != 2 || completed.DefinitionStatus != WorkspaceDefinitionStatusCompleted {
		t.Fatalf("unexpected completed state: %+v", completed)
	}
}

func TestWorkspaceDefinitionFailureIsDurable(t *testing.T) {
	store, err := Open(filepath.Join(t.TempDir(), "workspace-definition-failure.pebble"))
	if err != nil {
		t.Fatalf("open store: %v", err)
	}
	defer store.Close()
	workspaces := NewWorkspaceStore(store)
	entry, err := workspaces.AddForAccount("account", "/workspace", "workspace")
	if err != nil {
		t.Fatalf("add workspace: %v", err)
	}
	pending, err := workspaces.MarkDefinitionPendingForAccount("account", entry.Path)
	if err != nil {
		t.Fatalf("mark pending: %v", err)
	}
	failed, current, err := workspaces.FailDefinitionForAccount("account", entry.Path, pending.DefinitionGeneration, "provider failed", "change Router model", 3)
	if err != nil || !current {
		t.Fatalf("fail generation current=%v err=%v", current, err)
	}
	if failed.DefinitionStatus != WorkspaceDefinitionStatusFailed || failed.DefinitionAttemptCount != 3 || failed.DefinitionError != "provider failed" || failed.DefinitionModelSuggestion == "" || failed.DefinitionFailedAt == 0 {
		t.Fatalf("unexpected failed state: %+v", failed)
	}
}
