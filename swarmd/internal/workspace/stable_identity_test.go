package workspace

import (
	"testing"
)

func TestAddForPrincipalReturnsStableWorkspaceIdentity(t *testing.T) {
	store, cleanup := newTestWorkspaceStore(t)
	defer cleanup()
	svc := NewService(store)
	workspacePath := t.TempDir()

	first, err := svc.AddForPrincipal(testPrincipal(), workspacePath, "Workspace", "", true)
	if err != nil {
		t.Fatalf("add workspace: %v", err)
	}
	if first.WorkspaceID == "" {
		t.Fatalf("workspace id is empty: %+v", first)
	}
	if first.WorkspaceGeneration != 1 {
		t.Fatalf("workspace generation = %d, want 1", first.WorkspaceGeneration)
	}
	if first.WorkspaceState != "active" {
		t.Fatalf("workspace state = %q, want active", first.WorkspaceState)
	}

	second, err := svc.AddForPrincipal(testPrincipal(), workspacePath, "Workspace Updated", "", true)
	if err != nil {
		t.Fatalf("duplicate add workspace: %v", err)
	}
	if second.WorkspaceID != first.WorkspaceID {
		t.Fatalf("duplicate add changed workspace id: got %q want %q", second.WorkspaceID, first.WorkspaceID)
	}
	if second.WorkspaceGeneration != first.WorkspaceGeneration {
		t.Fatalf("duplicate add changed generation: got %d want %d", second.WorkspaceGeneration, first.WorkspaceGeneration)
	}

	current, ok, err := svc.CurrentBindingForPrincipal(testPrincipal())
	if err != nil || !ok {
		t.Fatalf("current binding ok=%v err=%v", ok, err)
	}
	if current.WorkspaceID != first.WorkspaceID {
		t.Fatalf("current binding workspace id = %q, want %q", current.WorkspaceID, first.WorkspaceID)
	}
}
