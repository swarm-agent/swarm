package pebblestore

import (
	"encoding/json"
	"reflect"
	"testing"
)

// Purpose: UnarchiveProjectTaskIfRevision must restore visibility, not execution.
// The durable store layer proves legacy metadata fidelity, scope/CAS rejection
// without writes, and active/archive partition plus realtime replay after restart.
func TestUnarchiveProjectTaskPreservesHistory(t *testing.T) {
	path := t.TempDir()
	db, err := Open(path)
	if err != nil {
		t.Fatal(err)
	}
	s := NewSessionStore(db)
	if err := s.PutProject("account-a", &ProjectRecord{ID: "project", Name: "Project"}); err != nil {
		t.Fatal(err)
	}
	legacy := &ProjectTaskRecord{ID: "task", ProjectID: "project", AccountID: "account-a", Title: "Retained", Agent: "coder", OutcomeType: "media_bundle", Status: "needs_review", Archived: true, Revision: 7, WorkerName: "Worker", SessionID: "retained-session", WorktreeBranch: "retained-branch", TaskProgramID: "retained-program", PlanBinding: &ProjectTaskPlanBinding{}, GitStatus: "dirty", DirtyCount: 2, FullPlanMarkdown: "retained plan", FeedbackHistory: []string{"attempt feedback"}, Deliverables: []ProjectTaskDeliverable{{Kind: "image"}}}
	// Retained legacy contracts deliberately bypass creation-time validation.
	db.projectsMu.Lock()
	_, err = s.persistProjectTaskLocked("account-a", legacy, false)
	db.projectsMu.Unlock()
	if err != nil {
		t.Fatal(err)
	}
	if err := s.CreateSession(SessionSnapshot{ID: legacy.SessionID, AccountScopeID: "account-a", Lifecycle: &SessionLifecycleSnapshot{Active: false, Phase: "completed"}}); err != nil {
		t.Fatal(err)
	}
	sessionBefore, _, _ := s.GetSession(legacy.SessionID)
	before, _, _ := s.getProjectTask("account-a", "project", "task", false)
	events, err := s.ListV3RealtimeOutboxForAuthScopeAfter("account-a", "desktop", 0, 100)
	if err != nil {
		t.Fatal(err)
	}
	for _, scope := range []struct {
		account, project string
		revision         int
	}{{"account-b", "project", 7}, {"account-a", "other", 7}, {"account-a", "project", 6}, {"account-a", "project", 0}} {
		if _, err := s.UnarchiveProjectTaskIfRevision(scope.account, scope.project, "task", scope.revision); err == nil {
			t.Fatal("foreign/stale unarchive succeeded")
		}
		got, _, _ := s.getProjectTask("account-a", "project", "task", false)
		if !reflect.DeepEqual(got, before) {
			t.Fatal("rejected unarchive mutated history")
		}
	}
	after, _ := s.ListV3RealtimeOutboxForAuthScopeAfter("account-a", "desktop", 0, 100)
	if len(after) != len(events) {
		t.Fatal("rejection published event")
	}
	restored, err := s.UnarchiveProjectTaskIfRevision("account-a", "project", "task", 7)
	if err != nil {
		t.Fatal(err)
	}
	expected := *before
	expected.Archived = false
	expected.Revision = 8
	expected.UpdatedAt = restored.UpdatedAt
	if !reflect.DeepEqual(restored, &expected) {
		t.Fatalf("restoration changed execution: %+v", restored)
	}
	sessionAfter, _, _ := s.GetSession(legacy.SessionID)
	if !reflect.DeepEqual(sessionBefore, sessionAfter) {
		t.Fatal("restoration mutated linked session")
	}
	if _, err := s.UnarchiveProjectTaskIfRevision("account-a", "project", "task", 8); err == nil {
		t.Fatal("already-unarchived operation must explicitly reject")
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
	active, err := s.ListProjectTasksByArchive("account-a", "project", false)
	if err != nil || len(active) != 1 || !reflect.DeepEqual(&active[0], restored) {
		t.Fatalf("restored visibility/history lost: %+v %v", active, err)
	}
	archived, err := s.ListProjectTasksByArchive("account-a", "project", true)
	if err != nil || len(archived) != 0 {
		t.Fatalf("still archived: %+v %v", archived, err)
	}
	replayed, err := s.ListV3RealtimeOutboxForAuthScopeAfter("account-a", "desktop", 0, 100)
	if err != nil || len(replayed) != len(events)+1 {
		t.Fatalf("unexpected durable events: %d %v", len(replayed), err)
	}
	var payload struct {
		Archived bool `json:"archived"`
		Revision int  `json:"revision"`
	}
	if err := json.Unmarshal(replayed[len(replayed)-1].Event.Payload, &payload); err != nil || payload.Archived || payload.Revision != 8 {
		t.Fatalf("invalid restore replay: %+v %v", payload, err)
	}
}
