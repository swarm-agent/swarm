package pebblestore

import (
	"testing"
)

func TestProjectStoreCRUD(t *testing.T) {
	// Purpose:
	// - Invariant: Project records must persist durably in Pebble store under account-scoped keys,
	//   supporting Put, Get, List, Update, and Delete operations.
	// - Boundary/authority: SessionStore.PutProject/GetProject/ListProjects/UpdateProject/DeleteProject in project_store.go.
	// - Threat/regression: Stale records, missing account scoping, or cross-account leakage could corrupt project metadata.

	path := t.TempDir()
	db, err := Open(path)
	if err != nil {
		t.Fatalf("Open failed: %v", err)
	}
	defer db.Close()

	sessionStore := NewSessionStore(db)
	accountID := "acct_test_orchestrate"

	// 1. Put project
	proj1 := &ProjectRecord{
		Name:        "Swarm Platform",
		Description: "Core daemon, desktop client, and video production pipeline",
		Workspaces: []ProjectWorkspaceRef{
			{WorkspaceID: "ws_1", Path: "/path/to/swarm-go", Role: "primary_code", Label: "Core Engine"},
			{WorkspaceID: "ws_2", Path: "/path/to/swarm-social", Role: "auxiliary", Label: "Social Studio"},
		},
		ProjectContext: "# Swarm Platform\n\nCore platform architecture.",
	}

	if err := sessionStore.PutProject(accountID, proj1); err != nil {
		t.Fatalf("failed to put project: %v", err)
	}
	if proj1.ID == "" {
		t.Fatalf("expected project ID to be generated")
	}

	// 2. Put second project
	proj2 := &ProjectRecord{
		Name:        "Secondary App",
		Description: "Auxiliary application",
	}
	if err := sessionStore.PutProject(accountID, proj2); err != nil {
		t.Fatalf("failed to put second project: %v", err)
	}

	// 3. Get project
	fetched, found, err := sessionStore.GetProject(accountID, proj1.ID)
	if err != nil || !found {
		t.Fatalf("failed to get project: %v, found: %v", err, found)
	}
	if fetched.Name != proj1.Name {
		t.Fatalf("expected name %q, got %q", proj1.Name, fetched.Name)
	}
	if len(fetched.Workspaces) != 2 {
		t.Fatalf("expected 2 workspaces, got %d", len(fetched.Workspaces))
	}

	// Cross-account isolation check
	otherAccount := "acct_other"
	_, foundOther, err := sessionStore.GetProject(otherAccount, proj1.ID)
	if err != nil {
		t.Fatalf("unexpected error checking other account: %v", err)
	}
	if foundOther {
		t.Fatalf("expected project not to be found under different account")
	}

	// 4. List projects
	list, err := sessionStore.ListProjects(accountID, 10)
	if err != nil {
		t.Fatalf("failed to list projects: %v", err)
	}
	if len(list) != 2 {
		t.Fatalf("expected 2 projects, got %d", len(list))
	}

	// 5. Update project
	updated, err := sessionStore.UpdateProject(accountID, proj1.ID, func(p *ProjectRecord) error {
		p.Name = "Swarm Platform Unified"
		p.ActiveTaskIDs = []string{"task_1", "task_2"}
		return nil
	})
	if err != nil {
		t.Fatalf("failed to update project: %v", err)
	}
	if updated.Name != "Swarm Platform Unified" {
		t.Fatalf("expected updated name, got %q", updated.Name)
	}
	if len(updated.ActiveTaskIDs) != 2 {
		t.Fatalf("expected 2 active tasks, got %d", len(updated.ActiveTaskIDs))
	}

	// 6. Delete project
	if err := sessionStore.DeleteProject(accountID, proj2.ID); err != nil {
		t.Fatalf("failed to delete project: %v", err)
	}
	listAfterDelete, err := sessionStore.ListProjects(accountID, 10)
	if err != nil {
		t.Fatalf("failed to list projects after delete: %v", err)
	}
	if len(listAfterDelete) != 1 {
		t.Fatalf("expected 1 project after delete, got %d", len(listAfterDelete))
	}
}
