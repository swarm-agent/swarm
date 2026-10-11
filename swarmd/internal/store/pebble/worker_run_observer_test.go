package pebblestore

import "testing"

// Purpose: monitors learn a worker run finished (worker.run.succeeded,
// .failed, .cancelled) from the run observer, so it must fire exactly once
// when a run first reaches a final status, never for admitted or running
// writes, never again when the same final record is written again, and never
// for a write the store refused. Owner: WorkerStore.RecordWorkerRun, the one
// write every run status goes through.
func TestWorkerRunObserverFiresOncePerFinish(t *testing.T) {
	s, ws := openTestStore(t)
	var seen []string
	s.SetWorkerRunObserver(func(worker WorkerRecord, run WorkerRunRecord) {
		seen = append(seen, worker.Name+":"+run.Status)
	})
	w, err := ws.CreateWorker("account", "user", CreateWorkerRequest{Name: "Poster", Instructions: "Post"}, nil)
	if err != nil {
		t.Fatal(err)
	}
	run, err := ws.RecordWorkerRun("account", WorkerRunRecord{WorkerID: w.ID, Status: "admitted", RequestSource: "schedule"})
	if err != nil {
		t.Fatal(err)
	}
	for _, status := range []string{"running", "failed", "failed"} {
		run.Status = status
		if run, err = ws.RecordWorkerRun("account", run); err != nil {
			t.Fatalf("%s: %v", status, err)
		}
	}
	run.Status = "succeeded"
	if _, err := ws.RecordWorkerRun("account", run); err == nil {
		t.Fatal("a failed run was rewritten as succeeded")
	}
	if len(seen) != 1 || seen[0] != "Poster:failed" {
		t.Fatalf("observer calls = %v, want [Poster:failed]", seen)
	}
	if _, err := ws.RecordWorkerRun("account", WorkerRunRecord{WorkerID: w.ID, Status: "cancelled", RequestSource: "direct"}); err != nil {
		t.Fatal(err)
	}
	if len(seen) != 2 || seen[1] != "Poster:cancelled" {
		t.Fatalf("observer calls = %v", seen)
	}
}
