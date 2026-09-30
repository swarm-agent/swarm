package pebblestore

import (
	"fmt"
	"reflect"
	"strings"
	"sync"
	"testing"
)

func TestTaskFollowupDurableHistory(t *testing.T) {
	// Purpose: ReserveTaskFollowup/project realtime persistence must preserve requests
	// and original evidence across two days and restart. Store layer is narrowest
	// authority for atomic concurrency, payload guards, and rejected-write postconditions.
	path := t.TempDir()
	db, err := Open(path)
	if err != nil { t.Fatal(err) }
	s := NewSessionStore(db)
	original := &ProjectTaskRecord{ID: "task", ProjectID: "project", Title: "task", Agent: "swarm", Status: "needs_review", SessionID: "original", Revision: 1, CreatedAt: 100, FeedbackHistory: []string{"undated legacy feedback"}}
	if err := s.PutProjectTask("account", original); err != nil { t.Fatal(err) }
	var wg sync.WaitGroup
	errs := make(chan error, 8)
	for i := 0; i < 8; i++ {
		wg.Add(1)
		go func() { defer wg.Done(); _, err := s.ReserveTaskFollowup("account", "project", "task", "user", "first", "  full request\n", 1, 200); errs <- err }()
	}
	wg.Wait()
	close(errs)
	for err := range errs { if err != nil { t.Fatal(err) } }
	first, _, err := s.GetProjectTask("account", "project", "task")
	if err != nil { t.Fatal(err) }
	if len(first.Attempts) != 2 || first.Attempts[1].Request != "  full request\n" || first.SessionID == "original" || first.ExecutionRunID() == "" { t.Fatalf("lost lineage: %+v", first) }
	if first.Attempts[0].Request != "" || first.Attempts[0].CreatedAt != 100 || len(first.FeedbackHistory) != 1 { t.Fatal("invented legacy request or lost feedback") }
	// Changed retry, running new request, account/project mismatch, oversized input
	// and stale full-record writes must reject with unchanged durable state.
	before := *first
	for _, input := range []struct{ account, project, key, request string }{
		{"account", "project", "first", "changed"},
		{"account", "project", "second", "running request"},
		{"other", "project", "first", "full request"},
		{"account", "other", "first", "full request"},
		{"account", "project", "long", strings.Repeat("x", 32001)},
	} {
		if _, err := s.ReserveTaskFollowup(input.account, input.project, "task", "user", input.key, input.request, 1, 300); err == nil { t.Fatal("accepted forbidden request") }
	}
	if err := s.PutProjectTask("account", original); err == nil { t.Fatal("stale session replaced active attempt") }
	corrupt := *first
	corrupt.Attempts = append([]ProjectTaskAttempt(nil), first.Attempts...)
	corrupt.Attempts[1].Request = "rewritten"
	if err := s.PutProjectTask("account", &corrupt); err == nil { t.Fatal("history rewrite accepted") }
	after, _, _ := s.GetProjectTask("account", "project", "task")
	if !reflect.DeepEqual(before, *after) { t.Fatal("rejected request mutated task") }
	if _, err := s.UpdateProjectTask("account", "project", "task", func(task *ProjectTaskRecord) error { task.Status = "needs_review"; return nil }); err != nil { t.Fatal(err) }
	if err := db.Close(); err != nil { t.Fatal(err) }
	db, err = Open(path)
	if err != nil { t.Fatal(err) }
	defer db.Close()
	s = NewSessionStore(db)
	second, err := s.ReserveTaskFollowup("account", "project", "task", "user", "second", "next day", 2, 86400200)
	if err != nil { t.Fatal(err) }
	if len(second.Attempts) != 3 || second.Attempts[1].SessionID != first.SessionID || second.Attempts[2].CreatedAt != 86400200 || second.SessionID == first.SessionID { t.Fatal("restart lost chronological attempts") }
	page, next, err := second.TaskAttemptPage(0, 2)
	if err != nil || len(page) != 2 || next != 2 { t.Fatal("incorrect first history page") }
	page, next, err = second.TaskAttemptPage(next, 2)
	if err != nil || len(page) != 1 || next != 0 || page[0].Request != "next day" { t.Fatal("incorrect trailing page") }
}

func TestTaskFollowupReviewAndMigration(t *testing.T) {
	// Purpose: EnsureTaskAttempts must not invent legacy dates/requests, and
	// ReserveTaskFollowup must not bypass planning/rejected approval states.
	// Threat: migration duplication and review bypass; pure/store layers own these rules.
	task := &ProjectTaskRecord{SessionID: "old", Agent: "swarm", Status: "failed"}
	task.EnsureTaskAttempts()
	task.EnsureTaskAttempts()
	if len(task.Attempts) != 1 || task.Attempts[0].CreatedAt != 0 || task.Attempts[0].Request != "" { t.Fatal("migration invented history") }
	db, err := Open(t.TempDir())
	if err != nil { t.Fatal(err) }
	defer db.Close()
	s := NewSessionStore(db)
	for _, state := range []string{"planning", "pending_approval", "rejected", "in_progress"} {
		record := &ProjectTaskRecord{ID: state, ProjectID: "project", Title: state, Agent: "swarm", Status: state, Revision: 1}
		if err := s.PutProjectTask("account", record); err != nil { t.Fatal(err) }
		before, _, _ := s.GetProjectTask("account", "project", state)
		if _, err := s.ReserveTaskFollowup("account", "project", state, "user", "key", "request", 1, 200); err == nil { t.Fatalf("bypassed %s", state) }
		after, _, _ := s.GetProjectTask("account", "project", state)
		if !reflect.DeepEqual(before, after) { t.Fatalf("mutated %s", state) }
	}
}

func TestTaskFollowupReservationFailureLeavesNoAttempt(t *testing.T) {
	// Purpose: durable reservation failure must precede allocation and leave no
	// active coordinator or lost request. UpdateProjectTask's injected failure
	// hook is the narrow transaction boundary, not a simulated provider workload.
	db, err := Open(t.TempDir())
	if err != nil { t.Fatal(err) }
	defer db.Close()
	s := NewSessionStore(db)
	task := &ProjectTaskRecord{ID: "task", ProjectID: "project", Title: "Task", Agent: "swarm", Status: "needs_review", SessionID: "original", Revision: 1}
	if err := s.PutProjectTask("account", task); err != nil { t.Fatal(err) }
	before, _, _ := s.GetProjectTask("account", "project", "task")
	db.beforeProjectTaskUpdateHook = func(string) error { return fmt.Errorf("injected persistence failure") }
	if _, err := s.ReserveTaskFollowup("account", "project", "task", "user", "key", "request", 1, 200); err == nil { t.Fatal("failed reservation reported success") }
	db.beforeProjectTaskUpdateHook = nil
	after, _, _ := s.GetProjectTask("account", "project", "task")
	if !reflect.DeepEqual(before, after) { t.Fatal("partial attempt survived failed reservation") }
	if _, err := s.ReserveTaskFollowup("account", "project", "task", "user", "key", "request", 1, 200); err != nil { t.Fatal(err) }
}
