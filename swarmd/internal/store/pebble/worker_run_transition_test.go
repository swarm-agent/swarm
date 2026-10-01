package pebblestore

import (
	"errors"
	"reflect"
	"testing"
)

// Requirement: terminal worker runs cannot reopen or change outcome, and running
// work cannot return to admitted. Threat: delayed scheduler observations replay
// cancelled work or falsify history. Boundary: WorkerStore.RecordWorkerRun under
// workersMu; real temporary Pebble checks rejection and unchanged durable receipts.
func TestWorkerRunStatusCannotRegress(t *testing.T) {
	_, ws := openTestStore(t)
	w, err := ws.CreateWorker("account", "user", CreateWorkerRequest{Name: "Worker", Instructions: "Do the assigned task"}, nil)
	if err != nil {
		t.Fatal(err)
	}
	for _, from := range []string{"admitted", "running", "succeeded", "failed", "cancelled"} {
		for _, to := range []string{"admitted", "running", "succeeded", "failed", "cancelled"} {
			t.Run(from+"_to_"+to, func(t *testing.T) {
				run, err := ws.RecordWorkerRun("account", WorkerRunRecord{WorkerID: w.ID, Status: from, RequestSource: "direct"})
				if err != nil {
					t.Fatal(err)
				}
				// Compare durable before/after values, not the pre-serialization receipt
				// whose empty slices are omitted by the JSON storage contract.
				run, found, err := ws.GetWorkerRun("account", w.ID, run.ID)
				if err != nil || !found {
					t.Fatalf("read baseline: %v, %v", found, err)
				}
				update := run
				update.Status = to
				_, err = ws.RecordWorkerRun("account", update)
				reject := (AutomationV2Terminal(from) && from != to) || (from == "running" && to == "admitted")
				if reject && !errors.Is(err, ErrWorkerConflict) {
					t.Fatalf("expected conflict, got %v", err)
				}
				if !reject && err != nil {
					t.Fatal(err)
				}
				stored, found, err := ws.GetWorkerRun("account", w.ID, run.ID)
				if err != nil || !found {
					t.Fatalf("read receipt: %v, %v", found, err)
				}
				if reject && !reflect.DeepEqual(stored, run) {
					t.Fatal("rejected transition changed durable receipt")
				}
				if !reject && stored.Status != to {
					t.Fatalf("accepted transition not persisted: %s", stored.Status)
				}
			})
		}
	}
}
