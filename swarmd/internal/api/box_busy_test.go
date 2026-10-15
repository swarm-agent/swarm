package api

import (
	pebblestore "swarm/packages/swarmd/internal/store/pebble"
	"testing"
)

// Purpose: the installer-facing boxBusy aggregation must count follow-ups and
// landing independently of completed original receipts. Real service/store
// fixtures prove this boundary without launching an executor or shutting down.
func TestBoxBusyFollowupAndLanding(t *testing.T) {
	s, _, _, cleanup := setupScopedAuthTestServer(t)
	defer cleanup()
	ws := s.sessions.Store().WorkerStore()
	w, err := ws.CreateWorker("account", "user", pebblestore.CreateWorkerRequest{Name: "worker"}, nil)
	if err != nil {
		t.Fatal(err)
	}
	r, err := ws.RecordWorkerRun("account", pebblestore.WorkerRunRecord{WorkerID: w.ID, Status: "succeeded"})
	if err != nil {
		t.Fatal(err)
	}
	if ok, err := ws.ReserveWorkerFollowup("account", w.ID, r.ID, "followup", 1); err != nil || !ok {
		t.Fatal(ok, err)
	}
	busy, err := s.boxBusy()
	if err != nil || !busy.Busy || busy.Followups != 1 {
		t.Fatal(busy, err)
	}
	if err := ws.ReleaseWorkerFollowup("account", w.ID, r.ID, "followup"); err != nil {
		t.Fatal(err)
	}
	if err := s.sessions.Store().Underlying().PutJSON("publish_queue/test", []map[string]any{{"delivery_state": "landing"}}); err != nil {
		t.Fatal(err)
	}
	busy, err = s.boxBusy()
	if err != nil || !busy.Busy || busy.Landing != 1 || busy.Followups != 0 {
		t.Fatal(busy, err)
	}
}
