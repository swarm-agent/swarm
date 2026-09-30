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
	invalid.UseAccountDefault = true
	if _, err = ws.AcceptWorker("account", "owner", w.ID, w.Revision, w.ProposedBindings, invalid); err == nil {
		t.Fatal("mutable default accepted")
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
