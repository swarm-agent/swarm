package pebblestore

import "testing"

// Purpose: the shared Begin/Finish boundary must atomically publish durable task
// progress and completion, reject foreign/stale attempts, and retain conflicts.
// This temp-store layer proves persisted postconditions rather than UI optimism.
// Fixtures identify the required task agent to reach the integration boundary.
func TestProjectTaskIntegrationLifecycle(t *testing.T) {
	db, err := Open(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	s := NewSessionStore(db)
	var states []string
	db.SetProjectPublisher(func(_ V3RealtimeOutboxRecord) {
		task, found, err := s.GetProjectTask("account", "project", "task")
		if err == nil && found && task.Integration != nil {
			states = append(states, task.Integration.State)
		}
	})
	task := &ProjectTaskRecord{ID: "task", ProjectID: "project", Title: "Task", Agent: "coder", SessionID: "source", Status: "needs_review", Revision: 1, ActionNeeded: "Integrate"}
	if err := s.PutProjectTask("account", task); err != nil {
		t.Fatal(err)
	}
	task, _, _ = s.GetProjectTask("account", "project", "task")
	receipt := &ProjectTaskIntegration{SessionID: "source", SourceHead: "head", TargetBranch: "dev", PreviousTargetHead: "base"}
	if err := BeginProjectTaskIntegration(s, "foreign", task, receipt); err == nil {
		t.Fatal("foreign account accepted")
	}
	if err := BeginProjectTaskIntegration(s, "account", task, receipt); err != nil {
		t.Fatal(err)
	}
	pending, _, _ := s.GetProjectTask("account", "project", "task")
	if pending.IsIntegrated || pending.Status == "completed" || pending.Integration.OperationID == "" {
		t.Fatalf("optimistic completion: %+v", pending)
	}
	duplicate := &ProjectTaskIntegration{SessionID: "source"}
	if err := BeginProjectTaskIntegration(s, "account", pending, duplicate); err == nil {
		t.Fatal("duplicate accepted")
	}
	receipt.State = "conflict"
	receipt.Error = "resolve target conflict"
	failed, err := FinishProjectTaskIntegration(s, "account", task, receipt)
	if err != nil || failed.IsIntegrated || failed.Status == "completed" || failed.ActionNeeded != receipt.Error {
		t.Fatalf("lost failure: %+v %v", failed, err)
	}
	if err := BeginProjectTaskIntegration(s, "account", failed, receipt); err != nil {
		t.Fatal(err)
	}
	receipt.State = "integrated"
	receipt.ResultingTargetHead = "verified"
	completed, err := FinishProjectTaskIntegration(s, "account", failed, receipt)
	if err != nil || !completed.IsIntegrated || completed.Status != "completed" || completed.ActionNeeded != "" {
		t.Fatalf("lost completion: %+v %v", completed, err)
	}
	if len(states) != 4 || states[0] != "in_progress" || states[1] != "conflict" || states[2] != "in_progress" || states[3] != "integrated" {
		t.Fatalf("missing committed wakeups: %v", states)
	}
	// A same-session attempt change is rejected too, independently of session ID.
	sameSession := *completed
	sameSession.ActiveAttemptID = "new-attempt"
	if err := CheckProjectTaskIntegration(&sameSession, receipt); err == nil {
		t.Fatal("same-session stale attempt accepted")
	}
	_, err = s.UpdateProjectTask("account", "project", "task", func(t *ProjectTaskRecord) error {
		t.SessionID = "replacement"
		t.Status = "needs_review"
		t.IsIntegrated = false
		return nil
	})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := FinishProjectTaskIntegration(s, "account", task, receipt); err == nil {
		t.Fatal("old operation overwrote reopened attempt")
	}
	current, _, _ := s.GetProjectTask("account", "project", "task")
	if current.Status != "needs_review" || current.IsIntegrated {
		t.Fatal("rejected completion changed state")
	}
}

// Purpose: FinishProjectTaskIntegrationGuarded must reject a task revision
// changed during preparation BEFORE invoking Git, while preserving receipt and
// realtime authority. The store callback boundary is the narrowest proof of
// ordering; worktree tests separately prove actual Git effects.
func TestProjectTaskRecoveryRevisionGuard(t *testing.T) {
	db, err := Open(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	s := NewSessionStore(db)
	task := &ProjectTaskRecord{ID: "task", ProjectID: "project", Title: "Task", Agent: "coder", SessionID: "source", Status: "needs_review", Revision: 1}
	if err := s.PutProjectTask("account", task); err != nil {
		t.Fatal(err)
	}
	task, _, _ = s.GetProjectTask("account", "project", "task")
	r := &ProjectTaskIntegration{SessionID: "source", SourceHead: "original", RecoveryBase: "base", RecoveredHead: "recovered", TargetBranch: "dev"}
	if err := BeginProjectTaskIntegration(s, "account", task, r); err != nil {
		t.Fatal(err)
	}
	pending, _, _ := s.GetProjectTask("account", "project", "task")
	if _, err := s.UpdateProjectTask("account", "project", "task", func(row *ProjectTaskRecord) error { row.Revision++; return nil }); err != nil {
		t.Fatal(err)
	}
	called := false
	if _, err := FinishProjectTaskIntegrationGuarded(s, "account", task, r, pending.Revision, func() error { called = true; return nil }); err == nil || called {
		t.Fatal("stale task invoked promotion")
	}
	current, _, _ := s.GetProjectTask("account", "project", "task")
	if current.Integration.State != "in_progress" || current.IsIntegrated {
		t.Fatal("stale guard mutated receipt")
	}
	events := 0
	db.SetProjectPublisher(func(_ V3RealtimeOutboxRecord) { events++ })
	result, err := FinishProjectTaskIntegrationGuarded(s, "account", task, r, current.Revision, func() error { r.State, r.ResultingTargetHead = "recovered", "delivered"; return nil })
	if err != nil || result.IsIntegrated || result.Status != "completed" || result.Integration.SourceHead != "original" || result.Integration.RecoveredHead != "recovered" || events != 1 {
		t.Fatalf("untruthful or unpublished receipt: %+v %v events=%d", result, err, events)
	}
}
