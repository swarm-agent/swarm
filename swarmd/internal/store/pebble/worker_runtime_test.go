package pebblestore

import (
	"errors"
	"path/filepath"
	"reflect"
	"sync"
	"testing"
)

func runtimeFixture(t *testing.T, ws *WorkerStore, lifecycle string) (WorkerRecord, WorkerDeploymentRecord) {
	t.Helper()
	w, err := ws.CreateWorker("account", "owner", CreateWorkerRequest{Name: "runtime", WorkspaceRequirements: []WorkerWorkspaceRequirement{{Role: "primary", Required: true}}}, nil)
	if err != nil {
		t.Fatal(err)
	}
	w, err = ws.ActivateWorker("account", "owner", w.ID, w.Revision, map[string]string{"primary": "workspace"})
	if err != nil {
		t.Fatal(err)
	}
	d, err := ws.ProposeWorkerDeployment("account", "owner", w.ID, WorkerDeploymentRequest{WorkerRevision: w.Revision, Target: WorkerTargetReference{Kind: "ssh", WorkspaceID: "workspace", ReferenceID: "connection", ReferenceDigest: "digest", Capacity: 1}, Lifecycle: lifecycle, IdempotencyKey: "deployment"})
	if err != nil {
		t.Fatal(err)
	}
	d, err = ws.ApproveWorkerDeployment("account", "owner", w.ID, d.ID, d.Revision, d.ApprovalDigest)
	if err != nil {
		t.Fatal(err)
	}
	return w, d
}

// Requirement: commit launch ownership before external effects and recover an
// uncertain launch without allocating another identity. Threat: concurrent hub
// reconciliation or restart duplicates compute. WorkerStore's atomic journal is
// the narrowest layer proving identity, replay and no-write rejection; this is
// not an SSH/GCE execution test.
func TestWorkerRuntimeLaunchJournal(t *testing.T) {
	dir := filepath.Join(t.TempDir(), "db")
	db, err := Open(dir)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() {
		if db != nil {
			_ = db.Close()
		}
	})
	ws := NewWorkerStore(db)
	w, d := runtimeFixture(t, ws, "persistent")
	req := WorkerRuntimeClaim{ExpectedRevision: d.Revision, Generation: d.Generation, LaunchKey: "launch", BindingDigest: "aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa"}
	var wg sync.WaitGroup
	results := make(chan WorkerRuntimeRecord, 8)
	errs := make(chan error, 8)
	for i := 0; i < 8; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			r, e := ws.ClaimWorkerRuntime("account", w.ID, d.ID, req)
			results <- r
			errs <- e
		}()
	}
	wg.Wait()
	close(results)
	close(errs)
	for e := range errs {
		if e != nil {
			t.Fatal(e)
		}
	}
	var first WorkerRuntimeRecord
	for r := range results {
		if first.RuntimeID == "" {
			first = r
		}
		if !reflect.DeepEqual(first, r) {
			t.Fatal("duplicate launch identity")
		}
	}
	if first.State != "launching" || first.RuntimeID == "" || first.LastSequence != 0 {
		t.Fatalf("invalid launch: %+v", first)
	}
	bad := req
	bad.LaunchKey = "competing"
	if _, err = ws.ClaimWorkerRuntime("account", w.ID, d.ID, bad); !errors.Is(err, ErrWorkerConflict) {
		t.Fatalf("second launch: %v", err)
	}
	bad = req
	bad.BindingDigest = "bbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbb"
	if _, err = ws.ClaimWorkerRuntime("account", w.ID, d.ID, bad); !errors.Is(err, ErrWorkerConflict) {
		t.Fatalf("changed pin: %v", err)
	}
	if _, err = ws.ClaimWorkerRuntime("foreign", w.ID, d.ID, req); !errors.Is(err, ErrWorkerNotFound) {
		t.Fatalf("foreign claim: %v", err)
	}
	if err = db.Close(); err != nil {
		t.Fatal(err)
	}
	db = nil
	db, err = Open(dir)
	if err != nil {
		t.Fatal(err)
	}
	ws = NewWorkerStore(db)
	replay, err := ws.ClaimWorkerRuntime("account", w.ID, d.ID, req)
	if err != nil || !reflect.DeepEqual(first, replay) {
		t.Fatalf("restart allocated: %+v %v", replay, err)
	}
	saved, err := ws.GetWorkerDeployment("account", w.ID, d.ID)
	if err != nil || saved.ObservedState != "launching" || saved.CleanupState != "pending" {
		t.Fatalf("false ready/cleanup: %+v %v", saved, err)
	}
}

// Requirement: authenticated adapters report ordered, generation-bound facts;
// heartbeat/replay cannot resurrect stopped runtimes or authorize host deletion.
// Threat: delayed events, conflicting duplicate events and stale generations
// overwrite current ownership. The store is the narrow atomic rejection layer.
func TestWorkerRuntimeObservationFences(t *testing.T) {
	_, ws := openTestStore(t)
	w, d := runtimeFixture(t, ws, "persistent")
	claim := WorkerRuntimeClaim{ExpectedRevision: d.Revision, Generation: d.Generation, LaunchKey: "launch", BindingDigest: "aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa"}
	r, err := ws.ClaimWorkerRuntime("account", w.ID, d.ID, claim)
	if err != nil {
		t.Fatal(err)
	}
	ready := WorkerRuntimeObservation{RuntimeID: r.RuntimeID, Generation: r.Generation, Sequence: 1, BindingDigest: r.BindingDigest, State: "ready", EvidenceDigest: "bbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbb"}
	for _, mutate := range []func(*WorkerRuntimeObservation){
		func(x *WorkerRuntimeObservation) { x.Generation++ },
		func(x *WorkerRuntimeObservation) { x.Sequence++ },
		func(x *WorkerRuntimeObservation) { x.RuntimeID = "foreign" },
		func(x *WorkerRuntimeObservation) { x.BindingDigest = x.EvidenceDigest },
		func(x *WorkerRuntimeObservation) { x.EvidenceDigest = "" },
		func(x *WorkerRuntimeObservation) { x.State = "delete_host" },
	} {
		bad := ready
		mutate(&bad)
		if _, e := ws.ObserveWorkerRuntime("account", w.ID, d.ID, bad); e == nil {
			t.Fatal("invalid observation accepted")
		}
		got, e := ws.GetWorkerRuntime("account", w.ID, d.ID, d.Generation)
		if e != nil || !reflect.DeepEqual(got, r) {
			t.Fatalf("rejection mutated runtime: %v", e)
		}
	}
	if _, err = ws.ObserveWorkerRuntime("foreign", w.ID, d.ID, ready); !errors.Is(err, ErrWorkerNotFound) {
		t.Fatal(err)
	}
	accepted, err := ws.ObserveWorkerRuntime("account", w.ID, d.ID, ready)
	if err != nil {
		t.Fatal(err)
	}
	replay, err := ws.ObserveWorkerRuntime("account", w.ID, d.ID, ready)
	if err != nil || !reflect.DeepEqual(accepted, replay) {
		t.Fatal("observation replay changed state")
	}
	conflict := ready
	conflict.State = "disconnected"
	if _, err = ws.ObserveWorkerRuntime("account", w.ID, d.ID, conflict); !errors.Is(err, ErrWorkerConflict) {
		t.Fatal("conflicting duplicate accepted")
	}
	disconnected := ready
	disconnected.Sequence = 2
	disconnected.State = "disconnected"
	if _, err = ws.ObserveWorkerRuntime("account", w.ID, d.ID, disconnected); err != nil {
		t.Fatal(err)
	}
	ready.Sequence = 3
	if _, err = ws.ObserveWorkerRuntime("account", w.ID, d.ID, ready); err != nil {
		t.Fatal(err)
	}
	stopped := ready
	stopped.Sequence = 4
	stopped.State = "stopped"
	if _, err = ws.ObserveWorkerRuntime("account", w.ID, d.ID, stopped); !errors.Is(err, ErrWorkerConflict) {
		t.Fatal("cleanup without stop intent")
	}
	current, err := ws.GetWorkerDeployment("account", w.ID, d.ID)
	if err != nil {
		t.Fatal(err)
	}
	if _, err = ws.QueueWorkerCommand("account", "owner", w.ID, d.ID, WorkerCommandRequest{ExpectedRevision: current.Revision, Generation: current.Generation, Kind: "stop", IdempotencyKey: "stop"}); err != nil {
		t.Fatal(err)
	}
	if _, err = ws.ObserveWorkerRuntime("account", w.ID, d.ID, stopped); err != nil {
		t.Fatal(err)
	}
	ready.Sequence = 5
	if _, err = ws.ObserveWorkerRuntime("account", w.ID, d.ID, ready); !errors.Is(err, ErrWorkerConflict) {
		t.Fatal("resurrected stopped runtime")
	}
	commands, err := ws.ListWorkerCommands("account", w.ID, d.ID)
	if err != nil || len(commands) != 1 || commands[0].Status != "pending" {
		t.Fatal("observation fabricated command acknowledgement")
	}
}

// Requirement: an on-demand runtime is only claimed for a reserved job; cleanup
// must wait for job/result finalization. Threat: speculative launches and premature
// cleanup lose work. Store assertions prove rejected mutations retain the slot.
func TestWorkerRuntimeOnDemandRequiresJob(t *testing.T) {
	_, ws := openTestStore(t)
	w, d := runtimeFixture(t, ws, "on_demand")
	req := WorkerRuntimeClaim{ExpectedRevision: d.Revision, Generation: d.Generation, LaunchKey: "launch", BindingDigest: "aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa"}
	if _, err := ws.ClaimWorkerRuntime("account", w.ID, d.ID, req); !errors.Is(err, ErrWorkerConflict) {
		t.Fatal("idle on-demand launch accepted")
	}
	job, err := ws.AdmitWorkerRun("account", WorkerRunAdmission{WorkerID: w.ID, UserID: "owner", RequestSource: "direct", IdempotencyKey: "job", ExpectedWorkerRevision: w.Revision, Input: map[string]any{"prompt": "work"}, ResolvedModelProfile: controlTestModel(), Placement: &WorkerPlacementAdmission{DeploymentID: d.ID, DeploymentRevision: d.Revision}})
	if err != nil {
		t.Fatal(err)
	}
	r, err := ws.ClaimWorkerRuntime("account", w.ID, d.ID, req)
	if err != nil {
		t.Fatal(err)
	}
	if _, err = ws.CancelPendingWorkerJob("account", "owner", w.ID, job.ID, d.Generation); !errors.Is(err, ErrWorkerConflict) {
		t.Fatal("pre-dispatch cancellation released an uncertain launch")
	}
	obs := WorkerRuntimeObservation{RuntimeID: r.RuntimeID, Generation: r.Generation, Sequence: 1, BindingDigest: r.BindingDigest, State: "stopped", EvidenceDigest: r.BindingDigest}
	if _, err = ws.ObserveWorkerRuntime("account", w.ID, d.ID, obs); !errors.Is(err, ErrWorkerConflict) {
		t.Fatal("cleanup before finalization accepted")
	}
	saved, err := ws.GetWorkerDeployment("account", w.ID, d.ID)
	if err != nil || saved.ActiveJobID != job.ID || saved.CleanupScope != "owned_runtime" || saved.CleanupState != "pending" {
		t.Fatalf("lost reservation/ownership: %+v %v", saved, err)
	}
}

// Requirement: idle persistent runtimes consume the configured target capacity.
// Threat: capacity counted only for active jobs overbooks a shared SSH server;
// disconnect or duplicate cleanup releases another runtime's slot. Atomic store
// transitions prove exclusion and exactly-once release without cloud execution.
func TestWorkerRuntimeCapacity(t *testing.T) {
	_, ws := openTestStore(t)
	w1, d1 := runtimeFixture(t, ws, "persistent")
	w2, d2 := runtimeFixture(t, ws, "persistent")
	req := WorkerRuntimeClaim{ExpectedRevision: d1.Revision, Generation: d1.Generation, LaunchKey: "launch", BindingDigest: "aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa"}
	r, err := ws.ClaimWorkerRuntime("account", w1.ID, d1.ID, req)
	if err != nil {
		t.Fatal(err)
	}
	if _, err = ws.ClaimWorkerRuntime("account", w2.ID, d2.ID, req); !errors.Is(err, ErrWorkerConflict) {
		t.Fatal("persistent target overbooked")
	}
	obs := WorkerRuntimeObservation{RuntimeID: r.RuntimeID, Generation: r.Generation, Sequence: 1, BindingDigest: r.BindingDigest, EvidenceDigest: r.BindingDigest, State: "disconnected"}
	if _, err = ws.ObserveWorkerRuntime("account", w1.ID, d1.ID, obs); err != nil {
		t.Fatal(err)
	}
	if _, err = ws.ClaimWorkerRuntime("account", w2.ID, d2.ID, req); !errors.Is(err, ErrWorkerConflict) {
		t.Fatal("disconnect released ownership")
	}
	if _, err = ws.QueueWorkerCommand("account", "owner", w1.ID, d1.ID, WorkerCommandRequest{ExpectedRevision: d1.Revision, Generation: d1.Generation, Kind: "stop", IdempotencyKey: "stop"}); err != nil {
		t.Fatal(err)
	}
	obs.Sequence = 2
	obs.State = "stopped"
	for i := 0; i < 2; i++ {
		if _, err = ws.ObserveWorkerRuntime("account", w1.ID, d1.ID, obs); err != nil {
			t.Fatal(err)
		}
	}
	if _, err = ws.ClaimWorkerRuntime("account", w2.ID, d2.ID, req); err != nil {
		t.Fatal(err)
	}
	w3, d3 := runtimeFixture(t, ws, "persistent")
	if _, err = ws.ClaimWorkerRuntime("account", w3.ID, d3.ID, req); !errors.Is(err, ErrWorkerConflict) {
		t.Fatal("duplicate stop released extra capacity")
	}
}
