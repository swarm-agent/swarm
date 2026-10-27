package pebblestore

import (
	"errors"
	"path/filepath"
	"reflect"
	"testing"
)

// Purpose: dispatch-time InitializeWorkerModelProfile must preserve approval of
// a legacy worker's pending model proposal, while explicit SetWorkerModelProfile
// must fence it. Exercise UpdateWorker, the shared model setter and AcceptWorker
// against a temporary store: this is the narrowest layer proving durable policy
// and rejection without invoking dispatch or a provider.
func TestWorkerModelInitializationPreservesPendingReview(t *testing.T) {
	for _, initialize := range []bool{true, false} {
		name := "explicit_edit"
		if initialize {
			name = "initialize"
		}
		t.Run(name, func(t *testing.T) {
			db, err := Open(filepath.Join(t.TempDir(), "workers.pebble"))
			if err != nil {
				t.Fatal(err)
			}
			t.Cleanup(func() { _ = db.Close() })
			ws := NewWorkerStore(db)
			w, err := ws.CreateWorker("account", "user", CreateWorkerRequest{Role: WorkerRoleCoder, Name: "Legacy worker", IdempotencyKey: "legacy"}, nil)
			if err != nil {
				t.Fatal(err)
			}
			if w.ModelProfile != nil {
				t.Fatal("fixture must have no model profile")
			}
			selection := ModelProfileSelection{Provider: "fixture-provider", Model: "chosen-model"}
			pin := &SessionModelProfileSnapshot{Source: SessionModelProfileSourceTemporary, Action: selection, Plan: &selection}
			staged, err := ws.UpdateWorker("account", "user", w.ID, w.Revision, UpdateWorkerRequest{ModelProfile: pin, ProposeModel: true}, nil)
			if err != nil {
				t.Fatal(err)
			}
			if staged.PendingReview == nil || staged.PendingReviewStale || staged.ModelProfile != nil {
				t.Fatal("expected fresh legacy model proposal")
			}
			inherited := &SessionModelProfileSnapshot{Source: SessionModelProfileSourceSwarmSettings, UseAccountDefault: true}
			mutate := ws.SetWorkerModelProfile
			if initialize {
				mutate = ws.InitializeWorkerModelProfile
			}
			changed, err := mutate("account", "user", w.ID, staged.Revision, inherited)
			if err != nil {
				t.Fatal(err)
			}
			saved, found, err := ws.GetWorker("account", w.ID)
			if err != nil || !found {
				t.Fatalf("read worker: %v", err)
			}
			if saved.PendingReviewStale == initialize || !reflect.DeepEqual(saved.PendingReview, staged.PendingReview) || !reflect.DeepEqual(saved.ModelProfile, inherited) {
				t.Fatalf("incorrect review or model after mutation: %+v", saved)
			}
			accepted, err := ws.AcceptWorker("account", "user", w.ID, changed.Revision, nil)
			if initialize {
				if err != nil {
					t.Fatalf("initialization invalidated approval: %v", err)
				}
				saved, found, err = ws.GetWorker("account", w.ID)
				if err != nil || !found || !reflect.DeepEqual(saved, accepted) || saved.PendingReview != nil || saved.PendingReviewStale || !reflect.DeepEqual(saved.ModelProfile, pin) {
					t.Fatalf("accept did not persist proposed pin: %+v, %v", saved, err)
				}
			} else {
				if !errors.Is(err, ErrWorkerConflict) {
					t.Fatalf("explicit edit should fence approval: %v", err)
				}
				after, found, err := ws.GetWorker("account", w.ID)
				if err != nil || !found || !reflect.DeepEqual(after, saved) {
					t.Fatal("rejected acceptance changed worker")
				}
			}
		})
	}
}
