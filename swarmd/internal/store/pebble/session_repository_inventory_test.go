package pebblestore

import (
	"errors"
	"path/filepath"
	"testing"
)

// Purpose: program job-only transitions must invalidate inventory continuations,
// while replaying identical records must not. putTaskProgramHistory owns the
// atomic record/revision batch; this store test proves the postconditions without
// requiring Git, a scheduler, or a provider.
func TestRepositoryInventoryJobRevision(t *testing.T) {
	store := openV3SessionEventTestStore(t)
	sessions := NewSessionStore(store)
	historyCreate(t, sessions, "parent", "account", "user", "")
	historyReady(t, sessions)
	record := TaskProgramRecord{ParentSessionID: "parent", ProgramID: "program", State: TaskProgramStateRunning, Jobs: []TaskProgramJobRecord{{JobID: "job", State: TaskProgramJobDeclared}}}
	if err := sessions.putTaskProgramHistory(record); err != nil {
		t.Fatal(err)
	}
	q := RepositoryHistoryQuery{AccountScopeID: "account", UserID: "user", ParentSessionID: "parent", Limit: 1}
	_, token, err := sessions.RepositoryContinuation(q, "", []byte("anchor"))
	if err != nil {
		t.Fatal(err)
	}
	if err := sessions.putTaskProgramHistory(record); err != nil {
		t.Fatal(err)
	}
	if _, _, err := sessions.RepositoryContinuation(q, token, nil); err != nil {
		t.Fatalf("replay invalidated inventory: %v", err)
	}
	record.Jobs[0].ChildSessionID = "child"
	record.Jobs[0].WorkspacePath = filepath.Join(t.TempDir(), "worker")
	if err := sessions.putTaskProgramHistory(record); err != nil {
		t.Fatal(err)
	}
	if data, _, err := sessions.RepositoryContinuation(q, token, nil); !errors.Is(err, ErrRepositoryHistoryCursor) || len(data) != 0 {
		t.Fatalf("job-only change accepted stale cursor: %q %v", data, err)
	}
}
