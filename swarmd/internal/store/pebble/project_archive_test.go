package pebblestore

import (
	"strings"
	"testing"
)

// Purpose: archival is metadata-only even for historical incoherent contracts;
// strict PutProjectTask validation remains the creation authority. Threats are
// a failed archive or a mutation of retained execution data, stale revision,
// foreign scope, and accidental acceptance of linked active work. The Pebble
// store is the narrowest layer proving durable record fidelity and guards.
func TestArchiveHistoricalProjectTask(t *testing.T) {
	db, err := Open(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	s := NewSessionStore(db)
	project := &ProjectRecord{ID: "project-a", Name: "Project"}
	if err := s.PutProject("account-a", project); err != nil {
		t.Fatal(err)
	}
	legacy := &ProjectTaskRecord{ID: "legacy", ProjectID: project.ID, Title: "Old task", Agent: "coder", OutcomeType: "media_bundle", Status: "blocked", Revision: 1, FullPlanMarkdown: "retained plan", Deliverables: []ProjectTaskDeliverable{{Kind: "image"}}}
	if err := s.PutProjectTask("account-a", legacy); err == nil {
		t.Fatal("creation accepted incoherent contract")
	}
	// Historical record is injected through the store's durable mutation boundary,
	// never via a production creation or a local database edit.
	legacy.AccountID = "account-a"
	db.projectsMu.Lock()
	mut, err := s.persistProjectTaskLocked("account-a", legacy, false)
	db.projectsMu.Unlock()
	if err != nil {
		t.Fatal(err)
	}
	db.publishProjectRealtime(mut)
	if _, err := s.ArchiveProjectTaskIfRevision("account-b", project.ID, legacy.ID, 1); err == nil {
		t.Fatal("foreign account archived task")
	}
	if _, err := s.ArchiveProjectTaskIfRevision("account-a", "project-b", legacy.ID, 1); err == nil {
		t.Fatal("foreign project archived task")
	}
	if _, err := s.ArchiveProjectTaskIfRevision("account-a", project.ID, legacy.ID, 2); err == nil {
		t.Fatal("stale revision archived task")
	}
	before, ok, err := s.GetProjectTask("account-a", project.ID, legacy.ID)
	if err != nil || !ok || before.Archived || before.Revision != 1 {
		t.Fatalf("rejected archive changed record: %+v %v", before, err)
	}
	archived, err := s.ArchiveProjectTaskIfRevision("account-a", project.ID, legacy.ID, 1)
	if err != nil {
		t.Fatal(err)
	}
	if !archived.Archived || archived.Revision != 2 {
		t.Fatalf("archive metadata: %+v", archived)
	}
	retained, ok, err := s.GetProjectTask("account-a", project.ID, legacy.ID)
	if err != nil || !ok || !retained.Archived || retained.Revision != 2 || retained.Agent != "coder" || retained.OutcomeType != "media_bundle" || retained.FullPlanMarkdown != "retained plan" || len(retained.Deliverables) != 1 || retained.Status != "blocked" {
		t.Fatalf("archive lost historical record: %+v %v", retained, err)
	}
	if _, err := s.ArchiveProjectTaskIfRevision("account-a", project.ID, legacy.ID, 2); err == nil {
		t.Fatal("duplicate archival succeeded")
	}
	if err := s.CreateSession(SessionSnapshot{
		ID:             "session-running",
		AccountScopeID: "account-a",
		Lifecycle:      &SessionLifecycleSnapshot{Active: true, Phase: "running"},
	}); err != nil {
		t.Fatal(err)
	}
	active := &ProjectTaskRecord{ID: "active", ProjectID: project.ID, Title: "Active", Agent: "coder", Status: "in_progress", SessionID: "session-running", Revision: 1}
	if err := s.PutProjectTask("account-a", active); err != nil {
		t.Fatal(err)
	}
	if _, err := s.ArchiveProjectTaskIfRevision("account-a", project.ID, active.ID, 1); err == nil || !strings.Contains(err.Error(), "lifecycle") {
		t.Fatalf("linked active task not protected: %v", err)
	}
	got, _, err := s.GetProjectTask("account-a", project.ID, active.ID)
	if err != nil || got.Archived || got.Revision != 1 {
		t.Fatalf("active task mutated: %+v %v", got, err)
	}
	stale := &ProjectTaskRecord{ID: "stale", ProjectID: project.ID, Title: "Stale", Agent: "coder", Status: "in_progress", Revision: 1}
	if err := s.PutProjectTask("account-a", stale); err != nil {
		t.Fatal(err)
	}
	got, err = s.ArchiveProjectTaskIfRevision("account-a", project.ID, stale.ID, 1)
	if err != nil || !got.Archived || got.Status != "in_progress" {
		t.Fatalf("orphaned historical task archive: %+v %v", got, err)
	}

	// Task with execution linkage (worktree branch) but NO session must be archivable:
	unlinked := &ProjectTaskRecord{ID: "unlinked", ProjectID: project.ID, Title: "Unlinked with branch", Agent: "coder", Status: "in_progress", WorktreeBranch: "agent/mission-test", Revision: 1}
	if err := s.PutProjectTask("account-a", unlinked); err != nil {
		t.Fatal(err)
	}
	gotUnlinked, err := s.ArchiveProjectTaskIfRevision("account-a", project.ID, unlinked.ID, 1)
	if err != nil || !gotUnlinked.Archived || gotUnlinked.Revision != 2 {
		t.Fatalf("task with execution linkage but no session must archive: %+v %v", gotUnlinked, err)
	}

	// Task with dead / nonexistent session must be archivable:
	deadSess := &ProjectTaskRecord{ID: "dead-sess", ProjectID: project.ID, Title: "Dead session", Agent: "coder", Status: "in_progress", SessionID: "session-does-not-exist", WorktreeBranch: "agent/dead", Revision: 1}
	if err := s.PutProjectTask("account-a", deadSess); err != nil {
		t.Fatal(err)
	}
	gotDead, err := s.ArchiveProjectTaskIfRevision("account-a", project.ID, deadSess.ID, 1)
	if err != nil || !gotDead.Archived || gotDead.Revision != 2 {
		t.Fatalf("task with nonexistent session must archive: %+v %v", gotDead, err)
	}

	// Task with completed/terminated session must be archivable:
	if err := s.CreateSession(SessionSnapshot{
		ID:             "session-completed",
		AccountScopeID: "account-a",
		Lifecycle:      &SessionLifecycleSnapshot{Active: false, Phase: "completed"},
	}); err != nil {
		t.Fatal(err)
	}
	completedTask := &ProjectTaskRecord{ID: "completed-task", ProjectID: project.ID, Title: "Completed session task", Agent: "coder", Status: "in_progress", SessionID: "session-completed", Revision: 1}
	if err := s.PutProjectTask("account-a", completedTask); err != nil {
		t.Fatal(err)
	}
	gotCompleted, err := s.ArchiveProjectTaskIfRevision("account-a", project.ID, completedTask.ID, 1)
	if err != nil || !gotCompleted.Archived || gotCompleted.Revision != 2 {
		t.Fatalf("task with terminated session must archive: %+v %v", gotCompleted, err)
	}

	// Queued task must be archivable:
	queuedTask := &ProjectTaskRecord{ID: "queued-task", ProjectID: project.ID, Title: "Queued task", Agent: "coder", Status: "queued", Revision: 1}
	if err := s.PutProjectTask("account-a", queuedTask); err != nil {
		t.Fatal(err)
	}
	gotQueued, err := s.ArchiveProjectTaskIfRevision("account-a", project.ID, queuedTask.ID, 1)
	if err != nil || !gotQueued.Archived || gotQueued.Revision != 2 {
		t.Fatalf("queued task must archive: %+v %v", gotQueued, err)
	}
}
