package run

import (
	"path/filepath"
	"testing"

	sessions "swarm/packages/swarmd/internal/session"
	store "swarm/packages/swarmd/internal/store/pebble"
)

// Purpose: observeRun must persist a current execution's declared blocker as
// failure before RecordWorkerRun notifies the terminal signal observer. This
// prevents success workflows from running on blocked work, without allowing a
// stale declaration to poison a later execution. Real session mutations and a
// temporary Pebble store are the narrowest layer exercising this boundary;
// the observer assertion covers the receipt supplied to workerRunSignal.
func TestWorkerExecutionDeclaredOutcome(t *testing.T) {
	for _, tc := range []struct{ name, signal, runID, reason, status, wantError string }{
		{"blocked", "blocked", "execution", "required input missing", "failed", "blocked: required input missing"},
		{"normal", "needs_review", "execution", "", "succeeded", ""},
		{"stale blocker", "blocked", "previous", "old blocker", "succeeded", ""},
		{"unbound blocker", "blocked", "", "old blocker", "succeeded", ""},
		{"empty blocker", "blocked", "execution", " ", "succeeded", ""},
	} {
		t.Run(tc.name, func(t *testing.T) {
			db, err := store.Open(filepath.Join(t.TempDir(), "worker.pebble"))
			if err != nil {
				t.Fatal(err)
			}
			t.Cleanup(func() { _ = db.Close() })
			events, err := store.NewEventLog(db)
			if err != nil {
				t.Fatal(err)
			}
			svc := sessions.NewService(store.NewSessionStore(db), events)
			ws := svc.Store().WorkerStore()
			worker, err := ws.CreateWorker("account-a", "user-a", store.CreateWorkerRequest{Role: store.WorkerRoleCoder, Name: "Coder", IdempotencyKey: "coder"}, nil)
			if err != nil {
				t.Fatal(err)
			}
			pref := store.ModelPreference{Provider: "fixture-provider", Model: "fixture-model", Thinking: "high"}
			sess, _, err := svc.CreateSessionWithOptions(sessions.CreateSessionOptions{SessionID: "worker-session", WorkspacePath: t.TempDir(), UserID: "user-a", AccountScopeID: "account-a", Preference: &pref, Metadata: map[string]any{"worker_id": worker.ID, "lifecycle_signal": tc.signal, "lifecycle_signal_run_id": tc.runID, "blocker_reason": tc.reason, "worker_execution_run_id": "execution"}})
			if err != nil {
				t.Fatal(err)
			}
			r, err := ws.RecordWorkerRun("account-a", store.WorkerRunRecord{ID: "execution", WorkerID: worker.ID, UserID: "user-a", Role: store.WorkerRoleCoder, SessionID: sess.ID, Status: "running", RequestSource: "direct", CreatedAt: 1, StartedAt: 1})
			if err != nil {
				t.Fatal(err)
			}
			for _, status := range []string{sessions.RunIntentPendingExecutor, sessions.RunIntentCompleted} {
				key := "intent-" + status
				_, err = svc.ApplySessionMutation(sessions.SessionMutationInput{SessionID: sess.ID, UserID: "user-a", AccountScopeID: "account-a", Kind: sessions.SessionMutationRecordRunIntent, ClientRequestID: key, IdempotencyKey: key, PayloadHash: key, RequestHash: key, EventType: "session.run_intent.recorded", RunIntent: &store.V3SessionRunIntent{SessionID: sess.ID, UserID: "user-a", AccountScopeID: "account-a", RunID: "execution", Status: status, RunSessionID: sess.ID, ParentSessionID: sess.ID}, NowUnixMs: 10})
				if err != nil {
					t.Fatal(err)
				}
			}
			var notifications []store.WorkerRunRecord
			db.SetWorkerRunObserver(func(_ store.WorkerRecord, run store.WorkerRunRecord) { notifications = append(notifications, run) })
			execution := &WorkerExecutionService{host: &AutomationV2ExecutionHost{runs: &Service{sessions: svc}}}
			if err := execution.observeRun(r); err != nil {
				t.Fatal(err)
			}
			got, found, err := ws.GetWorkerRun("account-a", worker.ID, r.ID)
			if err != nil || !found {
				t.Fatalf("read run: found=%v err=%v", found, err)
			}
			if got.Status != tc.status || got.Error != tc.wantError || got.CompletedAt == 0 {
				t.Fatalf("terminal receipt: %+v", got)
			}
			if len(notifications) != 1 || notifications[0].Status != tc.status || notifications[0].Error != tc.wantError {
				t.Fatalf("signal observer receipts: %+v", notifications)
			}
			if err := execution.observeRun(r); err != nil {
				t.Fatal(err)
			}
			if len(notifications) != 1 {
				t.Fatalf("duplicate terminal notification: %+v", notifications)
			}
		})
	}
}
