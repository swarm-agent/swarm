package pebblestore

import (
	"reflect"
	"sync"
	"testing"
)

// Purpose: ReserveTaskFollowupWithRecovery must atomically preserve admitted
// catalog identities across concurrent retries and restart without retaining
// mutable assignments. The Pebble reservation is the narrowest transaction layer;
// API tests separately prove fresh catalog/readiness and scheduler admission.
func TestTaskFollowupSourceReservationDurability(t *testing.T) {
	path := t.TempDir()
	db, err := Open(path)
	if err != nil {
		t.Fatal(err)
	}
	s := NewSessionStore(db)
	primary := ProjectTaskSource{Path: t.TempDir(), WorkspaceID: "primary", WorkspaceGeneration: 1, Provenance: "explicit"}
	secondary := ProjectTaskSource{Path: t.TempDir(), WorkspaceID: "secondary", WorkspaceGeneration: 1, Provenance: "explicit"}
	owned := SessionSnapshot{ID: "original", AccountScopeID: "account", UserID: "user", WorkspacePath: primary.Path, Metadata: map[string]any{"project_id": "project", "task_id": "task", "swarm_v3_source_workspace_path": primary.Path, "swarm_v3_source_workspace_id": primary.WorkspaceID, "swarm_v3_source_workspace_generation": primary.WorkspaceGeneration}, WorkspaceGrants: []WorkspaceGrant{{Kind: WorkspaceGrantPrimary, Path: primary.Path, WorkspaceID: primary.WorkspaceID, WorkspaceGeneration: 1}, {Kind: WorkspaceGrantAdditional, Path: secondary.Path, WorkspaceID: secondary.WorkspaceID, WorkspaceGeneration: 1}}}
	if _, err := s.ApplyV3SessionMutation(V3SessionMutationInput{SessionID: owned.ID, AccountScopeID: owned.AccountScopeID, UserID: owned.UserID, Kind: V3SessionMutationCreateSession, Session: &owned, ClientRequestID: "create", PayloadHash: "create"}); err != nil {
		t.Fatal(err)
	}
	task := &ProjectTaskRecord{ID: "task", ProjectID: "project", Title: "Sources", Agent: "swarm", Status: "failed", SessionID: owned.ID, Revision: 1, SourceWorkspace: primary, ProgramSources: []ProjectTaskSource{primary, secondary}}
	if err := s.PutProjectTask("account", task); err != nil {
		t.Fatal(err)
	}
	var wg sync.WaitGroup
	errs := make(chan error, 4)
	for i := 0; i < 4; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			_, err := s.ReserveTaskFollowup("account", "project", "task", "user", "reopen", "continue", 1, 200)
			errs <- err
		}()
	}
	wg.Wait()
	close(errs)
	for err := range errs {
		if err != nil {
			t.Fatal(err)
		}
	}
	stored, _, err := s.GetProjectTask("account", "project", "task")
	if err != nil || len(stored.Attempts) != 2 || !reflect.DeepEqual(stored.ProgramSources, task.ProgramSources) || stored.SessionID == owned.ID || stored.PlanBinding != nil || stored.TaskProgramID != "" || len(stored.CoderAssignments) != 0 {
		t.Fatalf("reservation lost sources or replayed execution: %+v %v", stored, err)
	}
	if err := db.Close(); err != nil {
		t.Fatal(err)
	}
	db, err = Open(path)
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	s = NewSessionStore(db)
	after, _, err := s.GetProjectTask("account", "project", "task")
	if err != nil || !reflect.DeepEqual(after.ProgramSources, stored.ProgramSources) || after.SessionID != stored.SessionID || after.ExecutionRunID() != stored.ExecutionRunID() {
		t.Fatalf("restart changed authority or identity: %+v %v", after, err)
	}
	before := *after
	if _, err := s.ReserveTaskFollowup("account", "project", "task", "user", "reopen", "changed", 1, 300); err == nil {
		t.Fatal("changed retry accepted")
	}
	after, _, _ = s.GetProjectTask("account", "project", "task")
	if !reflect.DeepEqual(before, *after) {
		t.Fatal("rejected retry mutated reservation")
	}
}
