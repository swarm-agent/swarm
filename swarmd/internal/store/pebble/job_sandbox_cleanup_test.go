package pebblestore

import "testing"

// Purpose: successful removal must stop future retries while new follow-ups
// reopen cleanup. WorkerStore is the durable marker authority; engine failures
// must stay pending rather than being mistaken for completion.
func TestSandboxCleanupCompletionMarker(t *testing.T) {
	_, ws := openTestStore(t)
	w, err := ws.CreateWorker("a", "u", CreateWorkerRequest{Name: "worker"}, nil)
	if err != nil {
		t.Fatal(err)
	}
	r, err := ws.RecordWorkerRun("a", WorkerRunRecord{WorkerID: w.ID, Status: "succeeded", SessionID: "s"})
	if err != nil {
		t.Fatal(err)
	}
	if err := ws.SetSandboxCleanupError(r, "engine unavailable"); err != nil {
		t.Fatal(err)
	}
	pending, err := ws.WorkerSandboxRuns()
	if err != nil || len(pending) != 1 {
		t.Fatal(pending, err)
	}
	if err := ws.SetSandboxCleanupError(r, ""); err != nil {
		t.Fatal(err)
	}
	pending, err = ws.WorkerSandboxRuns()
	if err != nil || len(pending) != 0 {
		t.Fatal(pending, err)
	}
	if ok, err := ws.ReserveWorkerFollowup("a", w.ID, r.ID, "next", 1); err != nil || !ok {
		t.Fatal(ok, err)
	}
	pending, err = ws.WorkerSandboxRuns()
	if err != nil || len(pending) != 1 {
		t.Fatal(pending, err)
	}
}
