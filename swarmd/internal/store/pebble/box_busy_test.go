package pebblestore

import (
	"path/filepath"
	"testing"
)

// Purpose: BoxExecutionCounts must prevent restart for running follow-ups,
// reviews and queued landing, across accounts. Real Pebble reopen verifies the
// decision is durable rather than dependent on in-memory executors.
func TestBoxExecutionCounts(t *testing.T) {
	path := filepath.Join(t.TempDir(), "db")
	db, err := Open(path)
	if err != nil {
		t.Fatal(err)
	}
	ws := NewWorkerStore(db)
	w, err := ws.CreateWorker("account", "user", CreateWorkerRequest{Name: "worker"}, nil)
	if err != nil {
		t.Fatal(err)
	}
	r, err := ws.RecordWorkerRun("account", WorkerRunRecord{WorkerID: w.ID, Status: "succeeded"})
	if err != nil {
		t.Fatal(err)
	}
	if ok, err := ws.ReserveWorkerFollowup("account", w.ID, r.ID, "followup", 1); err != nil || !ok {
		t.Fatal(ok, err)
	}
	if err := db.PutJSON("publish_queue/test", []map[string]any{{"delivery_state": "landing"}}); err != nil {
		t.Fatal(err)
	}
	if err := ws.SetSandboxCleanupError(r, "engine unavailable"); err != nil {
		t.Fatal(err)
	}
	if err := db.Close(); err != nil {
		t.Fatal(err)
	}
	db, err = Open(path)
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	ws = NewWorkerStore(db)
	saved, found, err := ws.GetWorkerRun("account", w.ID, r.ID)
	if err != nil || !found || saved.SandboxCleanupError != "engine unavailable" {
		t.Fatal(saved, err)
	}
	if err := ws.SetSandboxCleanupError(saved, ""); err != nil {
		t.Fatal(err)
	}
	f, _, l, err := ws.BoxExecutionCounts()
	if err != nil || f != 1 || l != 1 {
		t.Fatal(f, l, err)
	}
	if err := ws.ReleaseWorkerFollowup("account", w.ID, r.ID, "followup"); err != nil {
		t.Fatal(err)
	}
	if err := db.PutJSON("publish_queue/test", []map[string]any{{"delivery_state": "landed"}}); err != nil {
		t.Fatal(err)
	}
	f, _, l, err = ws.BoxExecutionCounts()
	if err != nil || f != 0 || l != 0 {
		t.Fatal(f, l, err)
	}
}
