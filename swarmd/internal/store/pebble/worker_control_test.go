package pebblestore

import (
	"errors"
	"path/filepath"
	"sync"
	"testing"
)

// Requirement: placement is durable intent, never readiness, and approval pins
// worker/context/target revisions. Threat: stale approval or a foreign account
// changes execution authority. WorkerStore transactions are the narrowest layer
// proving rejection without writes, including reopen and concurrent admission.
func TestWorkerControlDurabilityAndAdmission(t *testing.T) {
	dir := filepath.Join(t.TempDir(), "store")
	db, err := Open(dir)
	if err != nil {
		t.Fatal(err)
	}
	ws := NewWorkerStore(db)
	w, err := ws.CreateWorker("account", "owner", CreateWorkerRequest{Name: "review", WorkspaceRequirements: []WorkerWorkspaceRequirement{{Role: "primary", Required: true}}}, nil)
	if err != nil {
		t.Fatal(err)
	}
	w, err = ws.ActivateWorker("account", "owner", w.ID, w.Revision, map[string]string{"primary": "workspace-1"})
	if err != nil {
		t.Fatal(err)
	}
	ctx, err := ws.PutWorkerContext("account", "owner", w.ID, WorkerContextUpdate{ExpectedRevision: 0, Text: "retained knowledge", Provenance: "explicit user update"})
	if err != nil {
		t.Fatal(err)
	}
	target := WorkerTargetReference{Kind: "ssh", WorkspaceID: "workspace-1", ReferenceID: "connection-1", ReferenceDigest: "verified-reference-digest", Capacity: 2}
	d, err := ws.ProposeWorkerDeployment("account", "owner", w.ID, WorkerDeploymentRequest{WorkerRevision: w.Revision, ContextRevision: ctx.Revision, Target: target, Lifecycle: "on_demand", IdempotencyKey: "deployment-1"})
	if err != nil {
		t.Fatal(err)
	}
	if d.ObservedState != "unavailable" || d.ApprovalState != "pending" || d.CleanupScope != "owned_runtime" {
		t.Fatalf("false readiness/ownership: %+v", d)
	}
	if _, err = ws.ApproveWorkerDeployment("other", "owner", w.ID, d.ID, d.Revision, d.ApprovalDigest); !errors.Is(err, ErrWorkerNotFound) {
		t.Fatal(err)
	}
	if _, err = ws.ApproveWorkerDeployment("account", "owner", w.ID, d.ID, d.Revision+1, d.ApprovalDigest); !errors.Is(err, ErrWorkerConflict) {
		t.Fatal(err)
	}
	d, err = ws.ApproveWorkerDeployment("account", "owner", w.ID, d.ID, d.Revision, d.ApprovalDigest)
	if err != nil {
		t.Fatal(err)
	}
	req := WorkerRunAdmission{WorkerID: w.ID, UserID: "owner", RequestSource: "direct", IdempotencyKey: "job-1", Input: map[string]any{"prompt": "review"}, ExpectedWorkerRevision: w.Revision, ResolvedModelProfile: controlTestModel(), Placement: &WorkerPlacementAdmission{DeploymentID: d.ID, DeploymentRevision: d.Revision, ContextRevision: ctx.Revision}}
	var wg sync.WaitGroup
	results := make(chan WorkerRunRecord, 8)
	errs := make(chan error, 8)
	for i := 0; i < 8; i++ {
		wg.Add(1)
		go func() { defer wg.Done(); r, e := ws.AdmitWorkerRun("account", req); results <- r; errs <- e }()
	}
	wg.Wait()
	close(results)
	close(errs)
	for e := range errs {
		if e != nil {
			t.Fatal(e)
		}
	}
	var first WorkerRunRecord
	for r := range results {
		if first.ID == "" {
			first = r
		}
		if r.ID != first.ID || r.Placement == nil || r.Placement.ContextRevision != ctx.Revision || r.Placement.AttemptID == "" {
			t.Fatalf("duplicate/unpinned job: %+v", r)
		}
	}
	competing := req
	competing.IdempotencyKey = "job-2"
	if _, err = ws.AdmitWorkerRun("account", competing); !errors.Is(err, ErrWorkerConflict) {
		t.Fatalf("overlap: %v", err)
	}
	conflicting := req
	conflicting.Placement = &WorkerPlacementAdmission{DeploymentID: d.ID, DeploymentRevision: d.Revision + 1, ContextRevision: ctx.Revision}
	if _, err = ws.AdmitWorkerRun("account", conflicting); !errors.Is(err, ErrWorkerConflict) {
		t.Fatalf("conflicting replay: %v", err)
	}
	if _, err = ws.PutWorkerContext("account", "owner", w.ID, WorkerContextUpdate{ExpectedRevision: ctx.Revision, Text: "race", Provenance: "user"}); !errors.Is(err, ErrWorkerConflict) {
		t.Fatalf("writer race: %v", err)
	}
	stale := first
	stale.Status = "succeeded"
	if _, err = ws.RecordWorkerRun("account", stale); !errors.Is(err, ErrWorkerConflict) {
		t.Fatalf("unfenced outcome: %v", err)
	}
	cmd, err := ws.QueueWorkerCommand("account", "owner", w.ID, d.ID, WorkerCommandRequest{ExpectedRevision: d.Revision, Generation: d.Generation, Kind: "stop", IdempotencyKey: "stop-1"})
	if err != nil {
		t.Fatal(err)
	}
	if cmd.Status != "pending" {
		t.Fatal("fabricated acknowledgement")
	}
	replayedCommand, err := ws.QueueWorkerCommand("account", "owner", w.ID, d.ID, WorkerCommandRequest{ExpectedRevision: d.Revision, Generation: d.Generation, Kind: "stop", IdempotencyKey: "stop-1"})
	if err != nil || replayedCommand.ID != cmd.ID {
		t.Fatalf("command replay: %+v %v", replayedCommand, err)
	}
	if _, err = ws.QueueWorkerCommand("account", "owner", w.ID, d.ID, WorkerCommandRequest{ExpectedRevision: d.Revision + 1, Generation: d.Generation, Kind: "stop", IdempotencyKey: "stop-1"}); !errors.Is(err, ErrWorkerConflict) {
		t.Fatalf("command conflict: %v", err)
	}
	pending, err := ws.ProposeWorkerDeployment("account", "owner", w.ID, WorkerDeploymentRequest{WorkerRevision: w.Revision, ContextRevision: ctx.Revision, Target: target, Lifecycle: "persistent", IdempotencyKey: "pending-approval"})
	if err != nil {
		t.Fatal(err)
	}
	beforeReplay, err := NewSessionStore(db).ListV3RealtimeOutboxForAuthScopeAfter("account", "owner", 0, 100)
	if err != nil || len(beforeReplay) == 0 {
		t.Fatalf("missing durable replay: %v", err)
	}
	if err = db.Close(); err != nil {
		t.Fatal(err)
	}
	db, err = Open(dir)
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	ws = NewWorkerStore(db)
	pendingAgain, err := ws.GetWorkerDeployment("account", w.ID, pending.ID)
	if err != nil || pendingAgain.ApprovalState != "pending" || pendingAgain.ApprovalDigest != pending.ApprovalDigest {
		t.Fatalf("lost approval: %+v %v", pendingAgain, err)
	}
	afterReplay, err := NewSessionStore(db).ListV3RealtimeOutboxForAuthScopeAfter("account", "owner", 0, 100)
	if err != nil || len(afterReplay) != len(beforeReplay) {
		t.Fatalf("lost replay: %v", err)
	}
	for i := range beforeReplay {
		if beforeReplay[i].EndpointCursor != afterReplay[i].EndpointCursor {
			t.Fatal("replay cursor changed")
		}
	}
	foreignReplay, err := NewSessionStore(db).ListV3RealtimeOutboxForAuthScopeAfter("other", "owner", 0, 100)
	if err != nil || len(foreignReplay) != 0 {
		t.Fatalf("cross-account replay leak: %v", err)
	}
	again, err := ws.AdmitWorkerRun("account", req)
	if err != nil || again.ID != first.ID {
		t.Fatalf("reopen replay: %+v %v", again, err)
	}
	commands, err := ws.ListWorkerCommands("account", w.ID, d.ID)
	if err != nil || len(commands) != 1 || commands[0].ID != cmd.ID || commands[0].Status != "pending" {
		t.Fatalf("lost command: %+v %v", commands, err)
	}
	saved, err := ws.GetWorkerContext("account", w.ID, ctx.Revision)
	if err != nil || saved.Text != ctx.Text {
		t.Fatalf("lost context: %+v %v", saved, err)
	}
	runs, _, err := ws.ListWorkerRuns("account", w.ID, 100, "")
	if err != nil || len(runs) != 1 || runs[0].Status != "admitted" {
		t.Fatalf("partial/unauthorized mutation: %+v %v", runs, err)
	}
}

func controlTestModel() *SessionModelProfileSnapshot {
	return &SessionModelProfileSnapshot{Source: SessionModelProfileSourceTemporary, Action: ModelProfileSelection{Provider: "test", Model: "fixture"}, Plan: &ModelProfileSelection{Provider: "test", Model: "fixture"}}
}

// Requirement: provider does not choose lifecycle and cleanup never owns an
// existing SSH machine. Threat: cleanup escalation or stale context approval.
// Store validation proves invalid requests leave no placement behind.
func TestWorkerControlValidation(t *testing.T) {
	_, ws := openTestStore(t)
	w, err := ws.CreateWorker("account", "owner", CreateWorkerRequest{Name: "worker"}, nil)
	if err != nil {
		t.Fatal(err)
	}
	base := WorkerDeploymentRequest{WorkerRevision: w.Revision, Target: WorkerTargetReference{Kind: "ssh", WorkspaceID: "workspace-1", ReferenceID: "connection-1", ReferenceDigest: "digest", Capacity: 1}, Lifecycle: "persistent", IdempotencyKey: "dep"}
	bad := base
	bad.Lifecycle = "delete_host"
	if _, err = ws.ProposeWorkerDeployment("account", "owner", w.ID, bad); err == nil {
		t.Fatal("invalid lifecycle accepted")
	}
	d, err := ws.ProposeWorkerDeployment("account", "owner", w.ID, base)
	if err != nil {
		t.Fatal(err)
	}
	if d.CleanupScope != "owned_runtime" {
		t.Fatal("host cleanup granted")
	}
	if _, err = ws.PutWorkerContext("account", "owner", w.ID, WorkerContextUpdate{Text: "new", Provenance: "user"}); err != nil {
		t.Fatal(err)
	}
	if _, err = ws.ApproveWorkerDeployment("account", "owner", w.ID, d.ID, d.Revision, d.ApprovalDigest); !errors.Is(err, ErrWorkerConflict) {
		t.Fatalf("stale context approval: %v", err)
	}
	saved, err := ws.GetWorkerDeployment("account", w.ID, d.ID)
	if err != nil || saved.ApprovalState != "pending" || saved.Revision != d.Revision {
		t.Fatalf("rejected approval mutated: %+v %v", saved, err)
	}
	base.ContextRevision = 1
	base.IdempotencyKey = "gcp"
	base.Target.Kind = "gcp"
	base.Target.WorkspaceID = ""
	base.Target.ReferenceID = "runtime-1"
	gcp, err := ws.ProposeWorkerDeployment("account", "owner", w.ID, base)
	if err != nil {
		t.Fatal(err)
	}
	if gcp.Lifecycle != "persistent" || gcp.CleanupScope != "owned_runtime" || gcp.ObservedState != "unavailable" {
		t.Fatalf("unsafe inferred ownership: %+v", gcp)
	}
}

// Requirement: admission shares occurrence keys and capacity reservations across
// deployments, including worker-context serialization. Threat: a second worker
// overbooks a target, schedules duplicate jobs, or cancellation releases another
// attempt's reservations. Real Pebble transactions prove the narrow postconditions.
func TestWorkerControlScheduleCapacityAndFencing(t *testing.T) {
	_, ws := openTestStore(t)
	makeWorker := func(name string) WorkerRecord {
		t.Helper()
		w, e := ws.CreateWorker("account", "owner", CreateWorkerRequest{Name: name, WorkspaceRequirements: []WorkerWorkspaceRequirement{{Role: "primary", Required: true}}, Automations: []WorkerAutomationDefinition{{Name: "scheduled", ActivationMode: "interval", Enabled: true, Schedule: &AutomationV2Schedule{Kind: "interval", IntervalSeconds: 60}, PlanDocument: testPlanDoc("scheduled")}}}, nil)
		if e != nil {
			t.Fatal(e)
		}
		w, e = ws.ActivateWorker("account", "owner", w.ID, w.Revision, map[string]string{"primary": "workspace-1"})
		if e != nil {
			t.Fatal(e)
		}
		return w
	}
	target := WorkerTargetReference{Kind: "ssh", WorkspaceID: "workspace-1", ReferenceID: "connection-1", ReferenceDigest: "digest", Capacity: 1}
	deploy := func(w WorkerRecord, key string) WorkerDeploymentRecord {
		t.Helper()
		d, e := ws.ProposeWorkerDeployment("account", "owner", w.ID, WorkerDeploymentRequest{WorkerRevision: w.Revision, Target: target, Lifecycle: "persistent", IdempotencyKey: key})
		if e != nil {
			t.Fatal(e)
		}
		d, e = ws.ApproveWorkerDeployment("account", "owner", w.ID, d.ID, d.Revision, d.ApprovalDigest)
		if e != nil {
			t.Fatal(e)
		}
		return d
	}
	one, two := makeWorker("one"), makeWorker("two")
	d1, d2 := deploy(one, "one"), deploy(two, "two")
	other := deploy(one, "other")
	request := func(w WorkerRecord, d WorkerDeploymentRecord, occ string) WorkerRunAdmission {
		return WorkerRunAdmission{WorkerID: w.ID, AutomationID: w.Automations[0].ID, ExpectedWorkerRevision: w.Revision, ResolvedModelProfile: controlTestModel(), UserID: "owner", RequestSource: "schedule", OccurrenceID: occ, Placement: &WorkerPlacementAdmission{DeploymentID: d.ID, DeploymentRevision: d.Revision}}
	}
	req := request(one, d1, "occ-one")
	first, err := ws.AdmitWorkerRun("account", req)
	if err != nil {
		t.Fatal(err)
	}
	replay, err := ws.AdmitWorkerRun("account", req)
	if err != nil || replay.ID != first.ID {
		t.Fatalf("occurrence duplicated: %+v %v", replay, err)
	}
	if _, err = ws.AdmitWorkerRun("account", request(two, d2, "occ-two")); !errors.Is(err, ErrWorkerConflict) {
		t.Fatalf("overbooked target: %v", err)
	}
	if _, err = ws.AdmitWorkerRun("account", request(one, other, "occ-other")); !errors.Is(err, ErrWorkerConflict) {
		t.Fatalf("concurrent context writer: %v", err)
	}
	if _, err = ws.CancelPendingWorkerJob("account", "owner", one.ID, first.ID, first.Placement.Generation+1); !errors.Is(err, ErrWorkerConflict) {
		t.Fatalf("stale fence cancel: %v", err)
	}
	if _, err = ws.AdmitWorkerRun("account", request(two, d2, "occ-two")); !errors.Is(err, ErrWorkerConflict) {
		t.Fatalf("stale cancellation released capacity: %v", err)
	}
	if _, err = ws.CancelPendingWorkerJob("account", "owner", one.ID, first.ID, first.Placement.Generation); err != nil {
		t.Fatal(err)
	}
	if _, err = ws.AdmitWorkerRun("account", request(two, d2, "occ-two")); err != nil {
		t.Fatal(err)
	}
	// No second outcome/context publication can be smuggled through the local writer.
	if _, err = ws.RecordWorkerRun("account", first); !errors.Is(err, ErrWorkerConflict) {
		t.Fatalf("stale attempt accepted: %v", err)
	}
	saved, _, err := ws.GetWorkerRun("account", one.ID, first.ID)
	if err != nil || saved.Status != "cancelled" {
		t.Fatalf("outcome overwritten: %+v %v", saved, err)
	}
}

// Requirement: preparation failure must not persist partial reservations.
// Threat: invalid serialized input consumes a deployment/context slot. Admission
// before the atomic worker batch is the narrowest failure injection boundary.
func TestWorkerControlPartialFailureNoReservation(t *testing.T) {
	_, ws := openTestStore(t)
	w, err := ws.CreateWorker("account", "owner", CreateWorkerRequest{Name: "worker", WorkspaceRequirements: []WorkerWorkspaceRequirement{{Role: "primary", Required: true}}}, nil)
	if err != nil {
		t.Fatal(err)
	}
	w, err = ws.ActivateWorker("account", "owner", w.ID, w.Revision, map[string]string{"primary": "workspace-1"})
	if err != nil {
		t.Fatal(err)
	}
	d, err := ws.ProposeWorkerDeployment("account", "owner", w.ID, WorkerDeploymentRequest{WorkerRevision: w.Revision, Target: WorkerTargetReference{Kind: "ssh", WorkspaceID: "workspace-1", ReferenceID: "connection-1", ReferenceDigest: "digest", Capacity: 1}, Lifecycle: "on_demand", IdempotencyKey: "dep"})
	if err != nil {
		t.Fatal(err)
	}
	d, err = ws.ApproveWorkerDeployment("account", "owner", w.ID, d.ID, d.Revision, d.ApprovalDigest)
	if err != nil {
		t.Fatal(err)
	}
	req := WorkerRunAdmission{WorkerID: w.ID, UserID: "owner", RequestSource: "direct", IdempotencyKey: "job", Input: map[string]any{"prompt": "work", "invalid": make(chan int)}, ExpectedWorkerRevision: w.Revision, ResolvedModelProfile: controlTestModel(), Placement: &WorkerPlacementAdmission{DeploymentID: d.ID, DeploymentRevision: d.Revision}}
	if _, err = ws.AdmitWorkerRun("account", req); err == nil {
		t.Fatal("unsupported input accepted")
	}
	saved, err := ws.GetWorkerDeployment("account", w.ID, d.ID)
	if err != nil || saved.ActiveJobID != "" {
		t.Fatalf("partial reservation: %+v %v", saved, err)
	}
	req.Input = map[string]any{"prompt": "work"}
	if _, err = ws.AdmitWorkerRun("account", req); err != nil {
		t.Fatal(err)
	}
}
