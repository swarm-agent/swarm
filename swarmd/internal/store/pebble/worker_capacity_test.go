package pebblestore

import (
	"encoding/json"
	"fmt"
	"github.com/cockroachdb/pebble"
	"path/filepath"
	"testing"
)

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

// Purpose: terminal history must never become an admission ceiling. The atomic
// index writer and real Pebble prove >10k receipts do not enter the reservation
// scan, while active reservations still queue across account boundaries.
func TestWorkerCapacityIgnoresTerminalHistory(t *testing.T) {
	db, ws := openTestStore(t)
	w, err := ws.CreateWorker("account", "user", CreateWorkerRequest{Name: "History"}, nil)
	if err != nil {
		t.Fatal(err)
	}
	batch := db.NewBatch()
	defer batch.Close()
	for i := 0; i < 10001; i++ {
		r := WorkerRunRecord{ID: fmt.Sprintf("history-%05d", i), AccountScopeID: "account", WorkerID: w.ID, Status: "succeeded"}
		data, err := json.Marshal(r)
		if err != nil {
			t.Fatal(err)
		}
		if err = batch.Set([]byte(KeyWorkerRun("account", w.ID, r.ID)), data, nil); err != nil {
			t.Fatal(err)
		}
		if err = indexWorkerRunInBatch(batch, "account", data); err != nil {
			t.Fatal(err)
		}
	}
	if err = batch.Commit(pebble.Sync); err != nil {
		t.Fatal(err)
	}
	r, err := ws.RecordWorkerRun("account", WorkerRunRecord{WorkerID: w.ID, Status: "admitted"})
	if err != nil {
		t.Fatal(err)
	}
	if ok, err := ws.ReserveWorkerCapacity(r, 1); err != nil || !ok {
		t.Fatalf("history prevented admission: %v %v", ok, err)
	}
}

// Purpose: follow-ups compete with normal jobs, keep the original outcome,
// and release only their exact reservation; delayed finishes cannot free a
// newer follow-up. Real durable index writes are the narrowest proof.
func TestWorkerFollowupCapacity(t *testing.T) {
	_, ws := openTestStore(t)
	w, err := ws.CreateWorker("account", "user", CreateWorkerRequest{Name: "Capacity"}, nil)
	if err != nil {
		t.Fatal(err)
	}
	first, err := ws.RecordWorkerRun("account", WorkerRunRecord{WorkerID: w.ID, Status: "admitted"})
	if err != nil {
		t.Fatal(err)
	}
	settled, err := ws.RecordWorkerRun("account", WorkerRunRecord{WorkerID: w.ID, Status: "succeeded"})
	if err != nil {
		t.Fatal(err)
	}
	if ok, err := ws.ReserveWorkerCapacity(first, 1); err != nil || !ok {
		t.Fatal(ok, err)
	}
	if ok, err := ws.ReserveWorkerFollowup("account", w.ID, settled.ID, "followup-1", 1); err != nil || ok {
		t.Fatal(ok, err)
	}
	queued, _, _ := ws.GetWorkerRun("account", w.ID, settled.ID)
	if queued.FollowupStatus != "queued" || queued.QueueReason == "" || queued.Status != "succeeded" {
		t.Fatalf("lost queue/outcome: %+v", queued)
	}
	first, _, _ = ws.GetWorkerRun("account", w.ID, first.ID)
	first.Status = "failed"
	if _, err = ws.RecordWorkerRun("account", first); err != nil {
		t.Fatal(err)
	}
	if ok, err := ws.ReserveWorkerFollowup("account", w.ID, settled.ID, "followup-1", 1); err != nil || !ok {
		t.Fatal(ok, err)
	}
	if err = ws.ReleaseWorkerFollowup("account", w.ID, settled.ID, "stale"); err != nil {
		t.Fatal(err)
	}
	another, err := ws.RecordWorkerRun("account", WorkerRunRecord{WorkerID: w.ID, Status: "admitted"})
	if err != nil {
		t.Fatal(err)
	}
	if ok, err := ws.ReserveWorkerCapacity(another, 1); err != nil || ok {
		t.Fatal("follow-up bypassed capacity", ok, err)
	}
	if err = ws.ReleaseWorkerFollowup("account", w.ID, settled.ID, "followup-1"); err != nil {
		t.Fatal(err)
	}
	if ok, err := ws.ReserveWorkerCapacity(another, 1); err != nil || !ok {
		t.Fatal("follow-up leaked capacity", ok, err)
	}
}

// Purpose: ApplyV3SessionMutation must release the exact follow-up reservation
// atomically with terminal intents, especially restart interruption. Reopening
// real Pebble proves startup cannot leave a slot held until a second restart.
func TestWorkerFollowupRestartRelease(t *testing.T) {
	for _, terminal := range []string{V3RunIntentInterrupted, V3RunIntentCompleted, V3RunIntentFailed, V3RunIntentCancelled, V3RunIntentExpired} {
		t.Run(terminal, func(t *testing.T) {
			path := filepath.Join(t.TempDir(), "store")
			db, err := Open(path)
			if err != nil {
				t.Fatal(err)
			}
			defer func() { _ = db.Close() }()
			ws := NewWorkerStore(db)
			w, err := ws.CreateWorker("account", "user", CreateWorkerRequest{Name: "worker"}, nil)
			if err != nil {
				t.Fatal(err)
			}
			r, err := ws.RecordWorkerRun("account", WorkerRunRecord{WorkerID: w.ID, Status: "succeeded", SessionID: "followup-session"})
			if err != nil {
				t.Fatal(err)
			}
			ss := NewSessionStore(db)
			_, err = ss.ApplyV3SessionMutation(V3SessionMutationInput{SessionID: r.SessionID, UserID: "user", AccountScopeID: "account", Kind: V3SessionMutationCreateSession, IdempotencyKey: "create", PayloadHash: "create", Session: &SessionSnapshot{ID: r.SessionID, Metadata: map[string]any{"worker_id": w.ID, "worker_execution_run_id": r.ID}}})
			if err != nil {
				t.Fatal(err)
			}
			transition := func(status string) {
				t.Helper()
				_, err := ss.ApplyV3SessionMutation(V3SessionMutationInput{SessionID: r.SessionID, UserID: "user", AccountScopeID: "account", Kind: V3SessionMutationRecordRunIntent, IdempotencyKey: status, PayloadHash: status, RunIntent: &V3SessionRunIntent{RunID: "followup", Status: status, BlockedReason: "daemon shutdown"}})
				if err != nil {
					t.Fatal(err)
				}
			}
			transition(V3RunIntentPendingExecutor)
			transition(V3RunIntentRunning)
			if ok, err := ws.ReserveWorkerFollowup("account", w.ID, r.ID, "followup", 1); err != nil || !ok {
				t.Fatal(ok, err)
			}
			if err := db.Close(); err != nil {
				t.Fatal(err)
			}
			db, err = Open(path)
			if err != nil {
				t.Fatal(err)
			}
			ws, ss = NewWorkerStore(db), NewSessionStore(db)
			if _, err := ws.ActiveWorkerSandboxSessions(); err != nil {
				t.Fatal(err)
			}
			observed := false
			db.SetWorkerRunObserver(func(_ WorkerRecord, got WorkerRunRecord) { observed = got.FollowupStatus == terminal })
			transition(terminal)
			if !observed {
				t.Fatal("missing follow-up terminal observer")
			}
			if err := db.Close(); err != nil {
				t.Fatal(err)
			}
			db, err = Open(path)
			if err != nil {
				t.Fatal(err)
			}
			ws, ss = NewWorkerStore(db), NewSessionStore(db)
			got, _, err := ws.GetWorkerRun("account", w.ID, r.ID)
			if err != nil || got.FollowupStatus != terminal || got.FollowupRunID != "followup" || got.FollowupError != "daemon shutdown" || got.Status != "succeeded" {
				t.Fatalf("reservation leaked or outcome changed: %+v %v", got, err)
			}
			next, err := ws.RecordWorkerRun("account", WorkerRunRecord{WorkerID: w.ID, Status: "admitted"})
			if err != nil {
				t.Fatal(err)
			}
			if ok, err := ws.ReserveWorkerCapacity(next, 1); err != nil || !ok {
				t.Fatalf("slot still held: %v %v", ok, err)
			}
		})
	}
}

// Purpose: upgrading the landed version-2 session index must backfill capacity
// reservations before admission. Real Pebble migration plus admission proves
// existing active runs cannot be overlooked by the combined index version.
func TestWorkerCapacityMigrationFromSessionIndex(t *testing.T) {
	db, ws := openTestStore(t)
	w, err := ws.CreateWorker("account", "user", CreateWorkerRequest{Name: "Worker"}, nil)
	if err != nil {
		t.Fatal(err)
	}
	active := WorkerRunRecord{ID: "existing", AccountScopeID: "account", WorkerID: w.ID, Status: "running"}
	if err := db.PutJSON(KeyWorkerRun("account", w.ID, active.ID), active); err != nil {
		t.Fatal(err)
	}
	if err := db.PutJSON(keyWorkerRunIndexVersion, map[string]int{"version": 2}); err != nil {
		t.Fatal(err)
	}
	for i := 0; i < 2; i++ {
		if _, err := RunWorkerRunIndexMigration(db); err != nil {
			t.Fatal(err)
		}
	}
	next, err := ws.RecordWorkerRun("account", WorkerRunRecord{WorkerID: w.ID, Status: "admitted"})
	if err != nil {
		t.Fatal(err)
	}
	if ok, err := ws.ReserveWorkerCapacity(next, 1); err != nil || ok {
		t.Fatalf("existing reservation ignored: %v %v", ok, err)
	}
}
