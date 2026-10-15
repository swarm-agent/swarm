package run

import (
	"context"
	"os"
	"strings"
	"testing"
	"time"

	"swarm/packages/swarmd/internal/identity"
	sessions "swarm/packages/swarmd/internal/session"
	store "swarm/packages/swarmd/internal/store/pebble"
)

// Requirement: watchdog settlement must preserve the checkout, fail orphaned
// receipts after restart, and not mistake queued or progressing turns for lost
// execution. WorkerExecutionService is the authority; a temporary durable store
// and real Git lane exercise settlement without a provider dependency.
func TestWorkerWatchdog(t *testing.T) {
	for _, mode := range []string{"stalled", "restart", "progress", "queued"} {
		t.Run(mode, func(t *testing.T) {
			_, ss, execution, workspaceID := setupWorkerExecutionFixture(t, func(identity.Principal, store.V3SessionRunIntent) bool { return true })
			ws := ss.Store().WorkerStore()
			w, err := ws.CreateWorker("account", "owner", store.CreateWorkerRequest{Name: "watchdog", Instructions: "Inspect", WorkspaceRequirements: []store.WorkerWorkspaceRequirement{{Role: "primary", Required: true}}}, nil)
			if err != nil {
				t.Fatal(err)
			}
			w, err = execution.Activate("account", "owner", w.ID, w.Revision, map[string]string{"primary": workspaceID})
			if err != nil {
				t.Fatal(err)
			}
			r, err := execution.Dispatch(context.Background(), "account", "owner", store.WorkerRunAdmission{WorkerID: w.ID, RequestSource: "direct", Input: map[string]any{"prompt": "Inspect"}, IdempotencyKey: "watchdog"})
			if err != nil {
				t.Fatal(err)
			}
			snapshot, _, _ := ss.GetSession(r.SessionID)
			if mode != "queued" {
				intent, _, err := ss.GetSessionActiveRunIntent(r.SessionID)
				if err != nil {
					t.Fatal(err)
				}
				intent.Status = sessions.RunIntentRunning
				if _, err := ss.ApplySessionMutation(sessions.SessionMutationInput{SessionID: r.SessionID, UserID: r.UserID, AccountScopeID: r.AccountScopeID, Kind: sessions.SessionMutationRecordRunIntent, RunIntent: &intent, ClientRequestID: "running", IdempotencyKey: "running", PayloadHash: "running", RequestHash: "running"}); err != nil {
					t.Fatal(err)
				}
			}
			cancelled := false
			if mode == "stalled" || mode == "progress" {
				defer execution.trackExecution(r.SessionID, func() { cancelled = true })()
			}
			if mode == "restart" {
				// A fresh service has no inherited execution ownership.
				execution = &WorkerExecutionService{host: execution.host}
			}
			if mode == "stalled" || mode == "restart" {
				execution.SetStallTimeout(time.Nanosecond)
			}
			if err := execution.ReconcileWorker(context.Background(), "account", w.ID); err != nil {
				t.Fatal(err)
			}
			got, _, err := ws.GetWorkerRun("account", w.ID, r.ID)
			if err != nil {
				t.Fatal(err)
			}
			if mode == "stalled" || mode == "restart" {
				if got.Status != "failed" || !strings.HasPrefix(got.Error, "run stalled:") {
					t.Fatalf("not settled: %+v", got)
				}
				intent, found, err := ss.GetSessionActiveRunIntent(r.SessionID)
				if err != nil || found {
					t.Fatalf("active intent retained: %+v %v", intent, err)
				}
				if mode == "stalled" && !cancelled {
					t.Fatal("executor not cancelled")
				}
			} else if got.Status != "running" {
				t.Fatalf("healthy run failed: %+v", got)
			}
			if _, err := os.Stat(snapshot.WorktreeRootPath); err != nil {
				t.Fatalf("work lost: %v", err)
			}
		})
	}
}
