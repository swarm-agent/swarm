package pebblestore

import (
	"path/filepath"
	"testing"
)

// Purpose: repair the historical failed/canceled-executor receipt with erased
// follow-up fields using retained session lineage, across an actual reopen.
// Direct single-turn jobs must not acquire invented follow-up history.
func TestRecoverClearedWorkerFollowups(t *testing.T) {
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
	r, err := ws.RecordWorkerRun("account", WorkerRunRecord{WorkerID: w.ID, SessionID: "session", Status: "failed", Error: "executor context canceled"})
	if err != nil {
		t.Fatal(err)
	}
	ss := NewSessionStore(db)
	_, err = ss.ApplyV3SessionMutation(V3SessionMutationInput{SessionID: r.SessionID, UserID: "user", AccountScopeID: "account", Kind: V3SessionMutationCreateSession, IdempotencyKey: "create", PayloadHash: "create", Session: &SessionSnapshot{ID: r.SessionID, Metadata: map[string]any{"worker_id": w.ID, "worker_execution_run_id": r.ID}}})
	if err != nil {
		t.Fatal(err)
	}
	for n, id := range []string{"initial", "followup"} {
		for _, status := range []string{V3RunIntentPendingExecutor, V3RunIntentRunning, V3RunIntentCompleted} {
			reason := ""
			if n == 1 && status == V3RunIntentCompleted {
				status = V3RunIntentInterrupted
				reason = "executor context canceled"
			}
			_, err = ss.ApplyV3SessionMutation(V3SessionMutationInput{SessionID: r.SessionID, UserID: "user", AccountScopeID: "account", Kind: V3SessionMutationRecordRunIntent, IdempotencyKey: id + status, PayloadHash: id + status, RunIntent: &V3SessionRunIntent{RunID: id, Status: status, CreatedAt: int64(n + 1), BlockedReason: reason}})
			if err != nil {
				t.Fatal(err)
			}
		}
		if n == 0 {
			if err := ws.RecoverClearedWorkerFollowups(); err != nil {
				t.Fatal(err)
			}
			got, _, _ := ws.GetWorkerRun("account", w.ID, r.ID)
			if got.FollowupStatus != "" {
				t.Fatal(got)
			}
		}
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
	for i := 0; i < 2; i++ {
		if err := ws.RecoverClearedWorkerFollowups(); err != nil {
			t.Fatal(err)
		}
	}
	got, _, err := ws.GetWorkerRun("account", w.ID, r.ID)
	if err != nil || got.FollowupStatus != V3RunIntentInterrupted || got.FollowupError != "executor context canceled" || got.FollowupRunID != "followup" || got.Status != "failed" {
		t.Fatal(got, err)
	}
}
