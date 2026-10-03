package pebblestore

import (
	"encoding/json"
	"testing"
)

// Purpose: ArchiveProjectTaskIfRevision/persistProjectTaskLocked must durably
// pair the archive revision with the project-scoped event. The temporary Pebble
// boundary proves replay after restart and no event/domain mutation on stale or
// foreign requests, without relying on transport timing or in-memory publication.
func TestProjectArchiveReceiptDurableReplay(t *testing.T) {
	path := t.TempDir()
	db, err := Open(path)
	if err != nil {
		t.Fatal(err)
	}
	s := NewSessionStore(db)
	p := &ProjectRecord{ID: "project-receipt", Name: "Receipt"}
	if err := s.PutProject("account-a", p); err != nil {
		t.Fatal(err)
	}
	task := &ProjectTaskRecord{ID: "task-receipt", ProjectID: p.ID, Title: "Task", Agent: "coder", Status: "queued"}
	if err := s.PutProjectTask("account-a", task); err != nil {
		t.Fatal(err)
	}
	before, err := s.ListV3RealtimeOutboxForAuthScopeAfter("account-a", "desktop", 0, 100)
	if err != nil {
		t.Fatal(err)
	}
	for _, scope := range []struct {
		account, project string
		revision         int
	}{
		{"account-b", p.ID, task.Revision},
		{"account-a", "foreign-project", task.Revision},
		{"account-a", p.ID, task.Revision + 1},
	} {
		if _, err := s.ArchiveProjectTaskIfRevision(scope.account, scope.project, task.ID, scope.revision); err == nil {
			t.Fatal("unauthorized/stale archive succeeded")
		}
	}
	after, err := s.ListV3RealtimeOutboxForAuthScopeAfter("account-a", "desktop", 0, 100)
	if err != nil || len(after) != len(before) {
		t.Fatalf("rejected operations published: %d -> %d: %v", len(before), len(after), err)
	}
	unchanged, ok, err := s.GetProjectTask("account-a", p.ID, task.ID)
	if err != nil || !ok || unchanged.Archived || unchanged.Revision != task.Revision {
		t.Fatalf("rejection mutated task: %+v %v", unchanged, err)
	}
	receipt, err := s.ArchiveProjectTaskIfRevision("account-a", p.ID, task.ID, task.Revision)
	if err != nil {
		t.Fatal(err)
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
	replayed, err := s.ListV3RealtimeOutboxForAuthScopeAfter("account-a", "desktop", 0, 100)
	if err != nil || len(replayed) != len(before)+1 {
		t.Fatalf("archive replay missing: %d: %v", len(replayed), err)
	}
	var payload struct {
		Project  string `json:"project_id"`
		Task     string `json:"task_id"`
		Action   string `json:"action"`
		Archived bool   `json:"archived"`
		Revision int    `json:"revision"`
	}
	last := replayed[len(replayed)-1]
	if err := json.Unmarshal(last.Event.Payload, &payload); err != nil {
		t.Fatal(err)
	}
	if last.AccountScopeID != "account-a" || payload.Project != p.ID || payload.Task != task.ID || payload.Action != "task_updated" || !payload.Archived || payload.Revision != receipt.Revision {
		t.Fatalf("invalid archive receipt: %+v", payload)
	}
	retained, ok, err := s.GetProjectTask("account-a", p.ID, task.ID)
	if err != nil || !ok || !retained.Archived || retained.Revision != payload.Revision || retained.Title != task.Title {
		t.Fatalf("archive domain/replay mismatch: %+v %v", retained, err)
	}
	foreign, err := s.ListV3RealtimeOutboxForAuthScopeAfter("account-b", "desktop", 0, 100)
	if err != nil || len(foreign) != 0 {
		t.Fatalf("foreign replay leaked receipt: %v %v", foreign, err)
	}
}
