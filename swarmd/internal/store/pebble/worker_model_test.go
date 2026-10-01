package pebblestore

import (
	"errors"
	"path/filepath"
	"reflect"
	"testing"
)

// Requirement: review acceptance pins an independent plan/action profile in the
// worker revision, surviving restart. Threat: stale/foreign review or aliasing
// changes the accepted choice. WorkerStore acceptance and revision batches are
// the narrowest layer that can prove durable nonmutation and revision pinning.
func TestWorkerModelAcceptancePersistenceAndGuards(t *testing.T) {
	dir := filepath.Join(t.TempDir(), "store")
	db, err := Open(dir)
	if err != nil {
		t.Fatal(err)
	}
	ws := NewWorkerStore(db)
	plan := ModelProfileSelection{Provider: "fixture", Model: "planning", Thinking: "low"}
	profile := &SessionModelProfileSnapshot{Source: SessionModelProfileSourceTemporary, Action: ModelProfileSelection{Provider: "fixture", Model: "action", Thinking: "high"}, Plan: &plan}
	w, err := ws.CreateWorker("account", "owner", CreateWorkerRequest{Name: "Review", Instructions: "Review", InitialLifecycleState: WorkerLifecycleStatePending, ProposedBindings: map[string]string{"primary": "workspace"}, WorkspaceRequirements: []WorkerWorkspaceRequirement{{Role: "primary", Required: true}}, ModelProfile: profile}, nil)
	if err != nil {
		t.Fatal(err)
	}
	original := CloneSessionModelProfileSnapshot(w.ModelProfile)
	profile.Action.Model = "mutated"
	for _, account := range []string{"foreign", "account"} {
		revision := w.Revision
		if account == "account" {
			revision++
		}
		if _, err = ws.AcceptWorker(account, "owner", w.ID, revision, w.ProposedBindings, original); err == nil {
			t.Fatal("unauthorized/stale acceptance succeeded")
		}
	}
	invalid := CloneSessionModelProfileSnapshot(original)
	invalid.Plan = nil
	if _, err = ws.AcceptWorker("account", "owner", w.ID, w.Revision, w.ProposedBindings, invalid); err == nil {
		t.Fatal("incomplete explicit profile accepted")
	}
	before, _, err := ws.GetWorker("account", w.ID)
	if err != nil || before.Revision != w.Revision || before.LifecycleState != WorkerLifecycleStatePending || !reflect.DeepEqual(before.ModelProfile, original) {
		t.Fatalf("rejection mutated worker: %+v %v", before, err)
	}
	accepted, err := ws.AcceptWorker("account", "owner", w.ID, w.Revision, w.ProposedBindings, original)
	if err != nil {
		t.Fatal(err)
	}
	if _, err = ws.InitializeWorkerModelProfile("account", "owner", w.ID, accepted.Revision, original); !errors.Is(err, ErrWorkerConflict) {
		t.Fatalf("model reinitialization: %v", err)
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
	loaded, found, err := ws.GetWorker("account", w.ID)
	if err != nil || !found || !reflect.DeepEqual(loaded.ModelProfile, original) {
		t.Fatalf("lost profile: %+v %v", loaded, err)
	}
	r, err := ws.AdmitWorkerRun("account", WorkerRunAdmission{WorkerID: w.ID, UserID: "owner", RequestSource: "direct", IdempotencyKey: "job", Input: map[string]any{"prompt": "review"}})
	if err != nil {
		t.Fatal(err)
	}
	history, found, err := ws.GetWorkerRevision("account", w.ID, r.WorkerRevision)
	if err != nil || !found || !reflect.DeepEqual(history.Worker.ModelProfile, original) {
		t.Fatalf("run did not pin profile: %+v %v", history, err)
	}
}

// Requirement: legacy profileless workers are initialized once by revision CAS;
// subsequent changes to the caller's default cannot replace that saved profile.
// WorkerStore is the narrowest atomic persistence boundary for this migration.
func TestWorkerModelLegacyInitializationOnce(t *testing.T) {
	db, err := Open(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	ws := NewWorkerStore(db)
	w, err := ws.CreateWorker("account", "owner", CreateWorkerRequest{Name: "Legacy", Instructions: "Review"}, nil)
	if err != nil {
		t.Fatal(err)
	}
	selection := ModelProfileSelection{Provider: "fixture", Model: "default"}
	profile := &SessionModelProfileSnapshot{Source: SessionModelProfileSourceSwarmSettings, Action: selection, Plan: &selection}
	if _, err = ws.InitializeWorkerModelProfile("foreign", "owner", w.ID, w.Revision, profile); !errors.Is(err, ErrWorkerNotFound) {
		t.Fatalf("foreign initialize: %v", err)
	}
	if _, err = ws.InitializeWorkerModelProfile("account", "owner", w.ID, w.Revision+1, profile); !errors.Is(err, ErrWorkerConflict) {
		t.Fatalf("stale initialize: %v", err)
	}
	before, _, _ := ws.GetWorker("account", w.ID)
	if before.ModelProfile != nil || before.Revision != w.Revision {
		t.Fatal("rejection initialized legacy worker")
	}
	initialized, err := ws.InitializeWorkerModelProfile("account", "owner", w.ID, w.Revision, profile)
	if err != nil {
		t.Fatal(err)
	}
	profile.Action.Model = "changed-default"
	if _, err = ws.InitializeWorkerModelProfile("account", "owner", w.ID, initialized.Revision, profile); !errors.Is(err, ErrWorkerConflict) {
		t.Fatalf("second initialize: %v", err)
	}
	after, _, _ := ws.GetWorker("account", w.ID)
	if after.Revision != initialized.Revision || after.ModelProfile.Action.Model != "default" {
		t.Fatal("saved model replaced by new default")
	}
}

// Requirement: inherited policy is accepted only by human revision review; each
// admission atomically stores resolved models. Threat: unresolved, stale or foreign
// admission creates a receipt, or replay replaces its snapshot after defaults change.
// WorkerStore is the narrowest durable admission/CAS/replay boundary.
func TestWorkerModelInheritedAdmissionSnapshotsAndGuards(t *testing.T) {
	db, err := Open(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	ws := NewWorkerStore(db)
	policy := &SessionModelProfileSnapshot{Source: SessionModelProfileSourceSwarmSettings, UseAccountDefault: true}
	w, err := ws.CreateWorker("account", "owner", CreateWorkerRequest{Name: "Defaults", Instructions: "Review", InitialLifecycleState: WorkerLifecycleStatePending, ProposedBindings: map[string]string{"primary": "workspace"}, WorkspaceRequirements: []WorkerWorkspaceRequirement{{Role: "primary", Required: true}}, ModelProfile: policy}, nil)
	if err != nil {
		t.Fatal(err)
	}
	w, err = ws.AcceptWorker("account", "owner", w.ID, w.Revision, w.ProposedBindings, policy)
	if err != nil {
		t.Fatal(err)
	}
	sel := ModelProfileSelection{Provider: "fixture", Model: "first"}
	resolved := &SessionModelProfileSnapshot{Source: SessionModelProfileSourceSwarmSettings, Action: sel, Plan: &sel}
	req := WorkerRunAdmission{WorkerID: w.ID, UserID: "owner", RequestSource: "direct", IdempotencyKey: "job", Input: map[string]any{"prompt": "Review"}}
	if _, err = ws.AdmitWorkerRun("account", req); err == nil {
		t.Fatal("unresolved admission succeeded")
	}
	req.ResolvedModelProfile = resolved
	req.ExpectedWorkerRevision = w.Revision + 1
	if _, err = ws.AdmitWorkerRun("account", req); !errors.Is(err, ErrWorkerConflict) {
		t.Fatalf("stale admission: %v", err)
	}
	req.ExpectedWorkerRevision = w.Revision
	if _, err = ws.AdmitWorkerRun("foreign", req); !errors.Is(err, ErrWorkerNotFound) {
		t.Fatalf("foreign admission: %v", err)
	}
	first, err := ws.AdmitWorkerRun("account", req)
	if err != nil {
		t.Fatal(err)
	}
	resolved.Action.Model = "second"
	*resolved.Plan = resolved.Action
	replay, err := ws.AdmitWorkerRun("account", req)
	if err != nil || replay.ID != first.ID || replay.ModelProfile.Action.Model != "first" {
		t.Fatalf("replay changed snapshot: %+v %v", replay, err)
	}
	req.IdempotencyKey = "next-job"
	next, err := ws.AdmitWorkerRun("account", req)
	if err != nil || next.ModelProfile.Action.Model != "second" {
		t.Fatalf("future job did not capture new defaults: %+v %v", next, err)
	}
	loaded, _, err := ws.GetWorker("account", w.ID)
	if err != nil || loaded.Revision != w.Revision || !loaded.ModelProfile.UseAccountDefault {
		t.Fatalf("admission changed worker policy: %+v %v", loaded, err)
	}
}

// Requirement: resetting an explicit worker to defaults remains a pending edit,
// not authority to change approved jobs. Threat: reset silently self-accepts.
// UpdateWorker/AcceptWorker are the narrowest durable human-review boundary.
func TestWorkerModelResetRequiresAcceptance(t *testing.T) {
	db, err := Open(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	ws := NewWorkerStore(db)
	sel := ModelProfileSelection{Provider: "fixture", Model: "override"}
	explicit := &SessionModelProfileSnapshot{Source: SessionModelProfileSourceTemporary, Action: sel, Plan: &sel}
	w, err := ws.CreateWorker("account", "owner", CreateWorkerRequest{Name: "Review", Instructions: "Review", InitialLifecycleState: WorkerLifecycleStatePending, ProposedBindings: map[string]string{"primary": "workspace"}, WorkspaceRequirements: []WorkerWorkspaceRequirement{{Role: "primary", Required: true}}, ModelProfile: explicit}, nil)
	if err != nil {
		t.Fatal(err)
	}
	w, err = ws.AcceptWorker("account", "owner", w.ID, w.Revision, w.ProposedBindings, explicit)
	if err != nil {
		t.Fatal(err)
	}
	reset := CloneSessionModelProfileSnapshot(explicit)
	reset.UseAccountDefault = true
	staged, err := ws.UpdateWorker("account", "owner", w.ID, w.Revision, UpdateWorkerRequest{ModelProfile: reset}, nil)
	if err != nil {
		t.Fatal(err)
	}
	if staged.ModelProfile.UseAccountDefault || staged.PendingReview == nil || !staged.PendingReview.ModelProfile.UseAccountDefault {
		t.Fatalf("reset bypassed review: %+v", staged)
	}
	if _, err = ws.AcceptWorker("account", "owner", w.ID, w.Revision, w.LocalBindings, reset); !errors.Is(err, ErrWorkerConflict) {
		t.Fatalf("stale reset accepted: %v", err)
	}
	approved, err := ws.AcceptWorker("account", "owner", w.ID, staged.Revision, w.LocalBindings, reset)
	if err != nil || !approved.ModelProfile.UseAccountDefault || approved.PendingReview != nil {
		t.Fatalf("reset not accepted: %+v %v", approved, err)
	}
}

// Requirement: update -> accept without a client override -> read -> future
// admission preserves the full explicit tuple. Threat: old approved/default
// models win, medium thinking is dropped, or already admitted runs are mutated.
// WorkerStore is the narrowest durable review and immutable receipt boundary.
func TestWorkerModelCandidateFutureAdmission(t *testing.T) {
	db, err := Open(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	ws := NewWorkerStore(db)
	old := ModelProfileSelection{Provider: "fixture", Model: "old", Thinking: "high"}
	profile := &SessionModelProfileSnapshot{Source: SessionModelProfileSourceTemporary, Action: old, Plan: &old}
	w, err := ws.CreateWorker("account", "owner", CreateWorkerRequest{Name: "Review", Instructions: "Review", InitialLifecycleState: WorkerLifecycleStatePending, ProposedBindings: map[string]string{"primary": "workspace"}, WorkspaceRequirements: []WorkerWorkspaceRequirement{{Role: "primary", Required: true}}, ModelProfile: profile}, nil)
	if err != nil {
		t.Fatal(err)
	}
	w, err = ws.AcceptWorker("account", "owner", w.ID, w.Revision, w.ProposedBindings)
	if err != nil {
		t.Fatal(err)
	}
	req := WorkerRunAdmission{WorkerID: w.ID, UserID: "owner", RequestSource: "direct", IdempotencyKey: "before", Input: map[string]any{"prompt": "Review"}, ResolvedModelProfile: profile, ExpectedWorkerRevision: w.Revision}
	first, err := ws.AdmitWorkerRun("account", req)
	if err != nil {
		t.Fatal(err)
	}
	candidate := CloneSessionModelProfileSnapshot(profile)
	candidate.Action = ModelProfileSelection{Provider: "other-fixture", Model: "new", Thinking: "medium", ServiceTier: "standard", ContextMode: "extended"}
	staged, err := ws.UpdateWorker("account", "owner", w.ID, w.Revision, UpdateWorkerRequest{ModelProfile: candidate}, nil)
	if err != nil {
		t.Fatal(err)
	}
	if staged.PendingReview == nil || !reflect.DeepEqual(staged.ModelProfile, profile) {
		t.Fatal("pending model applied early")
	}
	req.IdempotencyKey = "pending"
	req.ExpectedWorkerRevision = staged.Revision
	pending, err := ws.AdmitWorkerRun("account", req)
	if err != nil || !reflect.DeepEqual(pending.ModelProfile, first.ModelProfile) {
		t.Fatalf("pending proposal changed admission: %+v %v", pending, err)
	}
	approved, err := ws.AcceptWorker("account", "owner", w.ID, staged.Revision, w.LocalBindings)
	if err != nil {
		t.Fatal(err)
	}
	loaded, found, err := ws.GetWorker("account", w.ID)
	if err != nil || !found || loaded.PendingReview != nil || !reflect.DeepEqual(loaded.ModelProfile, candidate) || loaded.Revision != approved.Revision {
		t.Fatalf("accept/read lost tuple: %+v %v", loaded, err)
	}
	req.IdempotencyKey = "future"
	req.ExpectedWorkerRevision = approved.Revision
	req.ResolvedModelProfile = loaded.ModelProfile
	future, err := ws.AdmitWorkerRun("account", req)
	if err != nil || !reflect.DeepEqual(future.ModelProfile, candidate) {
		t.Fatalf("future admission lost tuple: %+v %v", future, err)
	}
	req.IdempotencyKey = "before"
	replay, err := ws.AdmitWorkerRun("account", req)
	if err != nil || replay.ID != first.ID || !reflect.DeepEqual(replay.ModelProfile, profile) {
		t.Fatalf("old receipt changed: %+v %v", replay, err)
	}
	if !reflect.DeepEqual(future.ModelProfile.Plan, profile.Plan) {
		t.Fatal("action edit changed plan policies")
	}
}
