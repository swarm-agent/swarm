package pebblestore

import (
	"errors"
	"path/filepath"
	"sync"
	"testing"
)

// Requirement: admission is atomic with its idempotency receipt and pinned
// revision. Threat: concurrent retries or a racing stop create duplicate work,
// or a restart loses the receipt. WorkerStore/commitWorkerRealtime is the
// narrowest durable boundary; assertions include rejected cross-account and
// mismatched-input attempts without any new run.
func TestWorkerAdmissionIdempotencyStopAndReopen(t *testing.T) {
	dir := filepath.Join(t.TempDir(), "workers.pebble")
	db, err := Open(dir)
	if err != nil {
		t.Fatal(err)
	}
	ws := NewWorkerStore(db)
	w, err := ws.CreateWorker("account", "owner", CreateWorkerRequest{Name: "review", Instructions: "Review", WorkspaceRequirements: []WorkerWorkspaceRequirement{{Role: "primary", Required: true}}}, nil)
	if err != nil {
		t.Fatal(err)
	}
	if _, err = ws.AdmitWorkerRun("account", WorkerRunAdmission{WorkerID: w.ID, UserID: "owner", RequestSource: "direct", IdempotencyKey: "pre", Input: map[string]any{"prompt": "review"}}); !errors.Is(err, ErrWorkerConflict) {
		t.Fatalf("idle admission accepted: %v", err)
	}
	w, err = ws.ActivateWorker("account", "owner", w.ID, w.Revision, map[string]string{"primary": "workspace-1"})
	if err != nil {
		t.Fatal(err)
	}
	req := WorkerRunAdmission{WorkerID: w.ID, UserID: "owner", RequestSource: "direct", IdempotencyKey: "same", Input: map[string]any{"prompt": "review"}}
	const n = 16
	results := make(chan WorkerRunRecord, n)
	errs := make(chan error, n)
	var group sync.WaitGroup
	for i := 0; i < n; i++ {
		group.Add(1)
		go func() { defer group.Done(); r, e := ws.AdmitWorkerRun("account", req); results <- r; errs <- e }()
	}
	group.Wait()
	close(results)
	close(errs)
	var first WorkerRunRecord
	for e := range errs {
		if e != nil {
			t.Fatal(e)
		}
	}
	for r := range results {
		if first.ID == "" {
			first = r
		}
		if r.ID != first.ID || r.WorkerRevision != w.Revision || r.SessionID != "worker-execution-"+r.ID {
			t.Fatalf("duplicate/drifted receipt: %+v", r)
		}
	}
	wrong := req
	wrong.Input = map[string]any{"prompt": "different"}
	if _, err = ws.AdmitWorkerRun("account", wrong); !errors.Is(err, ErrWorkerConflict) {
		t.Fatalf("key reused for other input: %v", err)
	}
	if _, err = ws.AdmitWorkerRun("other", req); !errors.Is(err, ErrWorkerNotFound) {
		t.Fatalf("foreign account admitted: %v", err)
	}
	w, err = ws.SetWorkerLifecycle("account", "owner", w.ID, w.Revision, WorkerLifecycleStateStopping)
	if err != nil {
		t.Fatal(err)
	}
	competing := req
	competing.IdempotencyKey = "late"
	competing.Input = map[string]any{"prompt": "later"}
	if _, err = ws.AdmitWorkerRun("account", competing); !errors.Is(err, ErrWorkerConflict) {
		t.Fatalf("stop did not fence admission: %v", err)
	}
	if _, err = ws.SetWorkerLifecycle("account", "owner", w.ID, w.Revision, WorkerLifecycleStatePaused); !errors.Is(err, ErrWorkerConflict) {
		t.Fatalf("unacknowledged run allowed pause: %v", err)
	}
	first.Status = "cancelled"
	first.CompletedAt = first.CreatedAt + 1
	if _, err = ws.RecordWorkerRun("account", first); err != nil {
		t.Fatal(err)
	}
	w, err = ws.SetWorkerLifecycle("account", "owner", w.ID, w.Revision, WorkerLifecycleStatePaused)
	if err != nil {
		t.Fatal(err)
	}
	if err = db.Close(); err != nil {
		t.Fatal(err)
	}
	reopened, err := Open(dir)
	if err != nil {
		t.Fatal(err)
	}
	defer reopened.Close()
	again, err := NewWorkerStore(reopened).AdmitWorkerRun("account", req)
	if err != nil || again.ID != first.ID || again.Status != "cancelled" {
		t.Fatalf("idempotency receipt not durable: %+v %v", again, err)
	}
	after, found, err := NewWorkerStore(reopened).GetWorker("account", w.ID)
	if err != nil || !found || after.LifecycleState != WorkerLifecycleStatePaused {
		t.Fatalf("stop barrier not durable: %+v %v", after, err)
	}
	runs, err := NewWorkerStore(reopened).UnfinishedWorkerRuns("account", w.ID, "")
	if err != nil || len(runs) != 0 {
		t.Fatalf("unexpected work after restart: %+v %v", runs, err)
	}
}

// Requirement: disabling one automation closes only its own admission and
// retains other eligible jobs. Threat: a shared worker stop cancels unrelated
// runs or lets a stale scheduled occurrence through. Pebble's worker mutex and
// revision history are the narrowest proof before live scheduling.
func TestWorkerAutomationDisableScopesAdmission(t *testing.T) {
	_, ws := openTestStore(t)
	auto := func(name string) WorkerAutomationDefinition {
		return WorkerAutomationDefinition{Name: name, Enabled: true, ActivationMode: "interval", Schedule: &AutomationV2Schedule{Kind: "interval", IntervalSeconds: 60}, PlanDocument: testPlanDoc(name)}
	}
	w, err := ws.CreateWorker("account", "owner", CreateWorkerRequest{Name: "worker", WorkspaceRequirements: []WorkerWorkspaceRequirement{{Role: "primary", Required: true}}, Automations: []WorkerAutomationDefinition{auto("one"), auto("two")}}, nil)
	if err != nil {
		t.Fatal(err)
	}
	w, err = ws.ActivateWorker("account", "owner", w.ID, w.Revision, map[string]string{"primary": "workspace-1"})
	if err != nil {
		t.Fatal(err)
	}
	admit := func(index int, occ string) WorkerRunAdmission {
		return WorkerRunAdmission{WorkerID: w.ID, AutomationID: w.Automations[index].ID, UserID: "owner", RequestSource: "schedule", OccurrenceID: occ, Input: map[string]any{"slot": occ}}
	}
	first, err := ws.AdmitWorkerRun("account", admit(0, "occ-first"))
	if err != nil {
		t.Fatal(err)
	}
	other, err := ws.AdmitWorkerRun("account", admit(1, "occ-other"))
	if err != nil {
		t.Fatal(err)
	}
	w, pending, err := ws.DisableWorkerAutomation("account", "owner", w.ID, w.Automations[0].ID, w.Revision)
	if err != nil || len(pending) != 1 || pending[0].ID != first.ID {
		t.Fatalf("disable touched wrong runs: %+v %v", pending, err)
	}
	if _, err = ws.AdmitWorkerRun("account", admit(0, "occ-late")); !errors.Is(err, ErrWorkerConflict) {
		t.Fatalf("disabled automation admitted: %v", err)
	}
	if _, err = ws.AdmitWorkerRun("account", admit(1, "occ-later")); err != nil {
		t.Fatalf("unrelated automation blocked: %v", err)
	}
	persisted, found, err := ws.GetWorkerRun("account", w.ID, other.ID)
	if err != nil || !found || persisted.Status != "admitted" {
		t.Fatalf("unrelated receipt altered: %+v %v", persisted, err)
	}
}

// Requirement: only the durable admission transaction allocates execution
// session identity and pins the accepted input. Threat: a forged session ID or
// an unapproved binding lets a caller attach a receipt to unrelated execution.
// WorkerStore.AdmitWorkerRun is the narrowest atomic boundary; rejected calls
// leave the account's run history and worker revision unchanged.
func TestWorkerAdmissionRejectsForgedSessionAndUnapprovedBinding(t *testing.T) {
	_, ws := openTestStore(t)
	w, err := ws.CreateWorker("account", "owner", CreateWorkerRequest{Name: "guarded", WorkspaceRequirements: []WorkerWorkspaceRequirement{{Role: "primary", Required: true}}}, nil)
	if err != nil {
		t.Fatal(err)
	}
	request := WorkerRunAdmission{WorkerID: w.ID, UserID: "owner", RequestSource: "direct", IdempotencyKey: "task", Input: map[string]any{"prompt": "inspect"}}
	w, err = ws.ActivateWorker("account", "owner", w.ID, w.Revision, map[string]string{"primary": "workspace-1"})
	if err != nil {
		t.Fatal(err)
	}
	forged := request
	forged.SessionID = "worker-execution-forged"
	if _, err = ws.AdmitWorkerRun("account", forged); !errors.Is(err, ErrWorkerConflict) {
		t.Fatalf("caller-owned session accepted: %v", err)
	}
	runs, _, err := ws.ListWorkerRuns("account", w.ID, 10, "")
	if err != nil || len(runs) != 0 {
		t.Fatalf("rejected request changed run history: %+v %v", runs, err)
	}
	admitted, err := ws.AdmitWorkerRun("account", request)
	if err != nil || admitted.SessionID != "worker-execution-"+admitted.ID || admitted.WorkerRevision != w.Revision || admitted.Input["prompt"] != "inspect" {
		t.Fatalf("incorrect durable execution linkage: %+v %v", admitted, err)
	}
	other := request
	other.IdempotencyKey = "other"
	other.Input = map[string]any{"prompt": "other"}
	if _, err = ws.AdmitWorkerRun("foreign", other); !errors.Is(err, ErrWorkerNotFound) {
		t.Fatalf("foreign account admitted: %v", err)
	}
	runs, _, err = ws.ListWorkerRuns("account", w.ID, 10, "")
	if err != nil || len(runs) != 1 || runs[0].ID != admitted.ID {
		t.Fatalf("rejected requests changed receipt: %+v %v", runs, err)
	}
}
