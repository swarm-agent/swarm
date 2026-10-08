package pebblestore

import (
	"reflect"
	"testing"
)

// Purpose: ReserveTaskFollowupWithRecovery atomically pins continuation evidence
// and delta base before allocation. The store boundary is the narrowest layer
// proving rejected foreign/concurrent requests cannot mutate a reservation and
// retries after DB reopen cannot replace its backend-captured source.
func TestTaskContinuationReservationPinsSource(t *testing.T) {
	path := t.TempDir()
	db, err := Open(path)
	if err != nil {
		t.Fatal(err)
	}
	defer func() { db.Close() }()
	s := NewSessionStore(db)
	task := &ProjectTaskRecord{ID: "task", ProjectID: "project", Title: "Task", SessionID: "origin", Agent: "swarm", Status: "needs_review", Revision: 1, WorkspacePath: "owned", WorktreeBranch: "agent/origin", BaseBranch: "dev", BaseCommit: "base"}
	if err := s.PutProjectTask("account", task); err != nil {
		t.Fatal(err)
	}
	source := &ProjectTaskRecoverySource{Kind: "retained_continuation", SessionID: "origin", WorkspacePath: "owned", Branch: "agent/origin", BaseCommit: "base", HeadCommit: "feature", TargetBranch: "dev", TargetHead: "target"}
	reserved, err := s.ReserveTaskFollowupWithRecovery("account", "project", "task", "user", "request", "continue", 1, 10, source)
	if err != nil {
		t.Fatal(err)
	}
	if reserved.BaseCommit != "base" || reserved.BaseBranch != "dev" || reserved.ActiveAttempt().AllocationHead != "feature" || !reflect.DeepEqual(reserved.ActiveAttempt().Recovery, source) {
		t.Fatalf("reservation lost source/destination: %+v", reserved)
	}
	for _, account := range []string{"account", "foreign"} {
		if _, err := s.ReserveTaskFollowupWithRecovery(account, "project", "task", "user", "concurrent", "continue", 1, 11, source); err == nil {
			t.Fatal("concurrent or foreign reservation accepted")
		}
	}
	after, _, err := s.GetProjectTask("account", "project", "task")
	if err != nil || !reflect.DeepEqual(reserved, after) {
		t.Fatal("rejected reservation mutated task")
	}
	if err := db.Close(); err != nil {
		t.Fatal(err)
	}
	db, err = Open(path)
	if err != nil {
		t.Fatal(err)
	}
	s = NewSessionStore(db)
	changed := *source
	changed.HeadCommit = "later-source"
	retried, err := s.ReserveTaskFollowupWithRecovery("account", "project", "task", "user", "request", "continue", 1, 12, &changed)
	if err != nil {
		t.Fatal(err)
	}
	if retried.SessionID != reserved.SessionID || retried.ActiveAttempt().AllocationHead != "feature" || !reflect.DeepEqual(retried.ActiveAttempt().Recovery, source) {
		t.Fatal("retry recaptured changed source")
	}
}
