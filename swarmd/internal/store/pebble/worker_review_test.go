package pebblestore

import (
	"errors"
	"path/filepath"
	"reflect"
	"testing"
)

// Requirement: stable workers stage job changes without replacing approved work;
// only exact-revision human acceptance opens admission for the new jobs. Threat:
// active proposals execute early, drop unrelated jobs, or revive disabled work.
// WorkerStore's atomic revision/outbox boundary is the narrowest durable layer;
// temporary-store reopen and rejected admission/CAS assert the postconditions.
func TestWorkerStableReviewPreservesApprovedJobs(t *testing.T) {
	dir := filepath.Join(t.TempDir(), "store")
	db, err := Open(dir)
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = db.Close() }()
	ws := NewWorkerStore(db)
	job := func(name string) WorkerAutomationDefinition {
		return WorkerAutomationDefinition{Name: name, ActivationMode: "interval", Schedule: &AutomationV2Schedule{Kind: "interval", IntervalSeconds: 300}, Enabled: true, PlanDocument: SessionPlanDocument{Title: name, Info: SessionPlanInfo{Goal: "Say hello"}, Checkpoints: []SessionPlanCheckpoint{{ID: "cp-1", Title: "Hello", Tasks: []string{"Say hello"}, AcceptanceCriteria: []string{"Hello returned"}}}}}
	}
	w, err := ws.CreateWorker("account", "owner", CreateWorkerRequest{Name: "Worker", Instructions: "Say hello", InitialLifecycleState: WorkerLifecycleStatePending, ProposedBindings: map[string]string{"primary": "workspace"}, WorkspaceRequirements: []WorkerWorkspaceRequirement{{Role: "primary", Required: true}}, Automations: []WorkerAutomationDefinition{job("prior")}}, nil)
	if err != nil {
		t.Fatal(err)
	}
	w, err = ws.AcceptWorker("account", "owner", w.ID, w.Revision, w.ProposedBindings)
	if err != nil {
		t.Fatal(err)
	}
	approved := w.Automations
	proposed, err := ws.UpdateWorker("account", "owner", w.ID, w.Revision, UpdateWorkerRequest{Automations: []WorkerAutomationDefinition{job("hello"), job("second")}}, nil)
	if err != nil {
		t.Fatal(err)
	}
	if proposed.ID != w.ID || proposed.LifecycleState != WorkerLifecycleStateActive || !reflect.DeepEqual(proposed.Automations, approved) || proposed.PendingReview == nil || len(proposed.PendingReview.Automations) != 3 {
		t.Fatalf("proposal replaced approved work: %+v", proposed)
	}
	pendingID := proposed.PendingReview.Automations[1].ID
	if _, err := ws.AdmitWorkerRun("account", WorkerRunAdmission{WorkerID: w.ID, UserID: "owner", AutomationID: pendingID, RequestSource: "schedule", IdempotencyKey: "pending"}); err == nil {
		t.Fatal("pending job admitted")
	}
	if _, err := ws.AcceptWorker("account", "owner", w.ID, w.Revision, w.ProposedBindings); !errors.Is(err, ErrWorkerConflict) {
		t.Fatalf("stale acceptance: %v", err)
	}
	if _, err := ws.UpdateWorker("foreign", "owner", w.ID, proposed.Revision, UpdateWorkerRequest{}, nil); !errors.Is(err, ErrWorkerNotFound) {
		t.Fatalf("foreign update: %v", err)
	}
	unchanged, _, err := ws.GetWorker("account", w.ID)
	if err != nil || !reflect.DeepEqual(unchanged, proposed) {
		t.Fatal("rejection changed worker")
	}
	if err := db.Close(); err != nil {
		t.Fatal(err)
	}
	db, err = Open(dir)
	if err != nil {
		t.Fatal(err)
	}
	ws = NewWorkerStore(db)
	loaded, _, err := ws.GetWorker("account", w.ID)
	if err != nil || !reflect.DeepEqual(loaded, proposed) {
		t.Fatal("review lost on reopen")
	}
	accepted, err := ws.AcceptWorker("account", "owner", w.ID, loaded.Revision, loaded.PendingReview.ProposedBindings)
	if err != nil {
		t.Fatal(err)
	}
	if accepted.PendingReview != nil || len(accepted.Automations) != 3 || !reflect.DeepEqual(accepted.Automations[0], approved[0]) {
		t.Fatal("acceptance dropped prior job")
	}
	runs, _, err := ws.ListWorkerRuns("account", w.ID, 20, "")
	if err != nil || len(runs) != 0 {
		t.Fatal("review or acceptance dispatched unsolicited work")
	}
	history, _, err := ws.ListWorkerRevisions("account", w.ID, 20, "")
	if err != nil || len(history) != 4 || history[2].Worker.PendingReview == nil || history[3].Worker.PendingReview != nil {
		t.Fatalf("history missing review linkage: %+v %v", history, err)
	}
	for i := 1; i < len(history); i++ {
		if history[i].Revision <= history[i-1].Revision {
			t.Fatal("history not chronological")
		}
	}
	staged, err := ws.AttachWorkerAutomation("account", "owner", w.ID, accepted.Revision, job("later"), nil)
	if err != nil || staged.PendingReview == nil {
		t.Fatalf("attach failed: %v", err)
	}
	disabled, _, err := ws.DisableWorkerAutomation("account", "owner", w.ID, approved[0].ID, staged.Revision)
	if err != nil || disabled.PendingReview != nil || disabled.Automations[0].Enabled {
		t.Fatalf("disable did not invalidate review: %v", err)
	}
	if _, err := ws.AcceptWorker("account", "owner", w.ID, staged.Revision, staged.PendingReview.ProposedBindings); !errors.Is(err, ErrWorkerConflict) {
		t.Fatal("stale review resurrected disabled work")
	}
}

// Requirement: successive proposals upsert reviewed jobs and acceptance never
// resumes a paused worker. Threat: re-proposing overwrites approved or pending
// sibling jobs, or consent unexpectedly resumes schedules. The atomic WorkerStore
// boundary is the narrowest layer proving candidates and rejection postconditions.
func TestWorkerSuccessiveReviewUpsertsAndPreservesPause(t *testing.T) {
	db, err := Open(filepath.Join(t.TempDir(), "store"))
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	ws := NewWorkerStore(db)
	job := WorkerAutomationDefinition{Name: "hello", ActivationMode: "interval", Schedule: &AutomationV2Schedule{Kind: "interval", IntervalSeconds: 300}, Enabled: true, PlanDocument: SessionPlanDocument{Title: "Hello", Info: SessionPlanInfo{Goal: "Hello"}, Checkpoints: []SessionPlanCheckpoint{{ID: "cp-1", Title: "Hello", Tasks: []string{"Hello"}, AcceptanceCriteria: []string{"Hello"}}}}}
	w, err := ws.CreateWorker("account", "owner", CreateWorkerRequest{Name: "Stable", Instructions: "Hello", InitialLifecycleState: WorkerLifecycleStatePending, ProposedBindings: map[string]string{"primary": "workspace"}, WorkspaceRequirements: []WorkerWorkspaceRequirement{{Role: "primary", Required: true}}, Automations: []WorkerAutomationDefinition{job}}, nil)
	if err != nil {
		t.Fatal(err)
	}
	w, err = ws.AcceptWorker("account", "owner", w.ID, w.Revision, w.ProposedBindings)
	if err != nil {
		t.Fatal(err)
	}
	w, err = ws.SetWorkerLifecycle("account", "owner", w.ID, w.Revision, WorkerLifecycleStateStopping)
	if err != nil {
		t.Fatal(err)
	}
	w, err = ws.SetWorkerLifecycle("account", "owner", w.ID, w.Revision, WorkerLifecycleStatePaused)
	if err != nil {
		t.Fatal(err)
	}
	original := w.Automations[0]
	revised := original
	revised.Schedule = &AutomationV2Schedule{Kind: "interval", IntervalSeconds: 600}
	candidate, err := ws.UpdateWorker("account", "owner", w.ID, w.Revision, UpdateWorkerRequest{Automations: []WorkerAutomationDefinition{revised}}, nil)
	if err != nil {
		t.Fatal(err)
	}
	second := job
	second.Name = "second"
	candidate, err = ws.UpdateWorker("account", "owner", w.ID, candidate.Revision, UpdateWorkerRequest{Automations: []WorkerAutomationDefinition{second}}, nil)
	if err != nil {
		t.Fatal(err)
	}
	if !reflect.DeepEqual(candidate.Automations[0], original) || candidate.LifecycleState != WorkerLifecycleStatePaused || len(candidate.PendingReview.Automations) != 2 || candidate.PendingReview.Automations[0].ID != original.ID || candidate.PendingReview.Automations[0].Revision != original.Revision+1 || candidate.PendingReview.Automations[0].Schedule.IntervalSeconds != 600 {
		t.Fatalf("successive review changed approved or pending state: %+v", candidate)
	}
	invalid := "invalid"
	if _, err := ws.UpdateWorker("account", "owner", w.ID, candidate.Revision, UpdateWorkerRequest{ExecutionMode: &invalid}, nil); err == nil {
		t.Fatal("invalid execution mode accepted")
	}
	unchanged, _, err := ws.GetWorker("account", w.ID)
	if err != nil || !reflect.DeepEqual(unchanged, candidate) {
		t.Fatal("invalid proposal mutated state")
	}
	accepted, err := ws.AcceptWorker("account", "owner", w.ID, candidate.Revision, candidate.PendingReview.ProposedBindings)
	if err != nil {
		t.Fatal(err)
	}
	if accepted.LifecycleState != WorkerLifecycleStatePaused || accepted.PendingReview != nil || len(accepted.Automations) != 2 || accepted.Automations[0].Schedule.IntervalSeconds != 600 {
		t.Fatalf("acceptance resumed worker or lost revisions: %+v", accepted)
	}
	if _, err := ws.AdmitWorkerRun("account", WorkerRunAdmission{WorkerID: w.ID, UserID: "owner", AutomationID: original.ID, RequestSource: "schedule", IdempotencyKey: "paused"}); err == nil {
		t.Fatal("paused accepted worker admitted scheduled run")
	}
	runs, _, err := ws.ListWorkerRuns("account", w.ID, 20, "")
	if err != nil || len(runs) != 0 {
		t.Fatal("paused admission rejection created receipt")
	}
}
