package pebblestore

import "testing"

// Purpose: ReserveWorkerCapacity must queue across account boundaries before
// checkout creation, count preparation, and release capacity only on terminal
// receipts. Real temporary Pebble is the narrowest durable admission boundary.
func TestWorkerBoxCapacityQueue(t *testing.T) {
	_, ws := openTestStore(t)
	makeRun := func(account string) WorkerRunRecord {
		w, err := ws.CreateWorker(account, "user", CreateWorkerRequest{Name: "Worker", Instructions: "Do task"}, nil)
		if err != nil {
			t.Fatal(err)
		}
		r, err := ws.RecordWorkerRun(account, WorkerRunRecord{WorkerID: w.ID, Status: "admitted"})
		if err != nil {
			t.Fatal(err)
		}
		return r
	}
	a, b := makeRun("account-a"), makeRun("account-b")
	if ok, err := ws.ReserveWorkerCapacity(a, 1); err != nil || !ok {
		t.Fatalf("first: %v %v", ok, err)
	}
	if ok, err := ws.ReserveWorkerCapacity(b, 1); err != nil || ok {
		t.Fatalf("overflow: %v %v", ok, err)
	}
	queued, _, err := ws.GetWorkerRun(b.AccountScopeID, b.WorkerID, b.ID)
	if err != nil || queued.Status != "queued" || queued.QueueReason == "" || queued.SessionID != "" {
		t.Fatalf("queue: %+v %v", queued, err)
	}
	a, _, _ = ws.GetWorkerRun(a.AccountScopeID, a.WorkerID, a.ID)
	a.Status = "failed"
	if _, err := ws.RecordWorkerRun(a.AccountScopeID, a); err != nil {
		t.Fatal(err)
	}
	if ok, err := ws.ReserveWorkerCapacity(queued, 1); err != nil || !ok {
		t.Fatalf("drain: %v %v", ok, err)
	}
	if ok, err := ws.ReserveWorkerCapacity(a, 1); err == nil || ok {
		t.Fatal("terminal receipt reopened")
	}
}
