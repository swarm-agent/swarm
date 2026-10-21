package pebblestore

import (
	"fmt"
	"path/filepath"
	"testing"
)

// Purpose: a box with no worker limit (0, the default) admits every run
// instead of queueing them all.
func TestReserveWorkerCapacityNoLimitAdmitsEveryRun(t *testing.T) {
	db, err := Open(filepath.Join(t.TempDir(), "capacity.pebble"))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = db.Close() })
	ws := NewWorkerStore(db)
	coder, err := ws.CreateWorker("acct", "user", CreateWorkerRequest{Role: WorkerRoleCoder, Name: "Coder", IdempotencyKey: "coder"}, nil)
	if err != nil {
		t.Fatal(err)
	}
	for i := 0; i < 12; i++ {
		run, err := ws.RecordWorkerRun("acct", WorkerRunRecord{ID: fmt.Sprintf("run-%d", i), WorkerID: coder.ID, UserID: "user", Role: WorkerRoleCoder, Status: "queued", RequestSource: "direct", CreatedAt: int64(i + 1)})
		if err != nil {
			t.Fatal(err)
		}
		admitted, err := ws.ReserveWorkerCapacity(run, 0)
		if err != nil || !admitted {
			t.Fatalf("run %d: admitted=%v err=%v", i, admitted, err)
		}
	}
}
