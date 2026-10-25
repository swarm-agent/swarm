package runtime

import (
	"path/filepath"
	"testing"

	"swarm/packages/swarmd/internal/signals"
	pebblestore "swarm/packages/swarmd/internal/store/pebble"
)

// Purpose: workerRunSignal must publish the latest follow-up outcome rather
// than the original failed receipt, without changing the identity used by
// Runner.consume/followupOf for workflow rounds and duplicate-head protection.
// A real temporary signal store is the narrowest layer that observes the emitted
// contract; failure cases ensure blocked first runs and failed retries cannot
// masquerade as success. Workflow delivery and Git head selection remain owned
// by the runner, not this signal boundary. Persisted exact-run lifecycle metadata
// proves failed-run recovery cannot hide a second block or reuse a stale blocker,
// while completed follow-ups after success retain their prior success contract.
func TestWorkerRunSignalFollowupOutcome(t *testing.T) {
	for _, tc := range []struct {
		name, original, followup, want, blockerRun string
	}{
		{"completed_after_failed", "failed", "completed", "succeeded", ""},
		{"failed_again", "failed", "failed", "failed", ""},
		{"interrupted_again", "failed", "interrupted", "failed", ""},
		{"failed_after_success", "succeeded", "failed", "failed", ""},
		{"workflow_reopen_completed", "succeeded", "completed", "succeeded", ""},
		{"completed_after_success_with_blocker", "succeeded", "completed", "succeeded", "followup-test"},
		{"completed_after_success_missing_session", "succeeded", "completed", "succeeded", "missing"},
		{"completed_but_blocked_again", "failed", "completed", "failed", "followup-test"},
		{"wrong_account", "failed", "completed", "failed", "wrong-account"},
		{"wrong_user", "failed", "completed", "failed", "wrong-user"},
		{"missing_session", "failed", "completed", "failed", "missing"},
		{"stale_blocker", "failed", "completed", "succeeded", "previous-run"},
		{"blocked_first_run", "failed", "", "failed", ""},
	} {
		t.Run(tc.name, func(t *testing.T) {
			db, err := pebblestore.Open(filepath.Join(t.TempDir(), "signals.pebble"))
			if err != nil {
				t.Fatal(err)
			}
			t.Cleanup(func() { _ = db.Close() })
			feed := pebblestore.NewSignalStore(db)
			run := pebblestore.WorkerRunRecord{
				ID: "run-test", AccountScopeID: "account-test", SessionID: "session-test", Role: pebblestore.WorkerRoleCoder,
				Status: tc.original, Error: "blocked: scope approval required",
				FollowupStatus: tc.followup, RequestSource: "manual",
			}
			if tc.followup != "" {
				run.FollowupRunID = "followup-test"
			}
			sessions := pebblestore.NewSessionStore(db)
			snapshot := pebblestore.SessionSnapshot{
				ID: run.SessionID, AccountScopeID: run.AccountScopeID, UserID: run.UserID,
				Metadata: map[string]interface{}{
					"lifecycle_signal": "blocked", "lifecycle_signal_run_id": tc.blockerRun,
					"blocker_reason": "additional approval required",
				},
			}
			if tc.blockerRun == "wrong-account" {
				snapshot.AccountScopeID = "other-account"
			}
			if tc.blockerRun == "wrong-user" {
				snapshot.UserID = "other-user"
			}
			if tc.blockerRun != "missing" {
				if err := sessions.CreateSession(snapshot); err != nil {
					t.Fatal(err)
				}
			}
			workerRunSignal(signals.NewEmitter(feed), sessions)(pebblestore.WorkerRecord{ID: "worker-test", Name: "Test worker"}, run)
			page, err := feed.ListAfter(0, 10, pebblestore.SignalFilter{Account: run.AccountScopeID})
			if err != nil {
				t.Fatal(err)
			}
			if len(page.Signals) != 1 {
				t.Fatalf("got %d signals, want exactly one: %+v", len(page.Signals), page.Signals)
			}
			sig := page.Signals[0]
			if sig.Kind != "worker.run."+tc.want {
				t.Fatalf("kind = %q, want worker.run.%s", sig.Kind, tc.want)
			}
			severity := pebblestore.SignalSeverityInfo
			if tc.want == "failed" {
				severity = pebblestore.SignalSeverityWarning
			}
			if sig.Severity != severity || sig.Account != run.AccountScopeID {
				t.Fatalf("incorrect severity/account: %+v", sig)
			}
			if sig.DedupKey != "worker.run:run-test:"+run.FollowupRunID || sig.Refs["worker_id"] != "worker-test" || sig.Refs["run_id"] != run.ID || sig.Refs["session_id"] != run.SessionID || sig.Attrs["source"] != run.RequestSource {
				t.Fatalf("workflow identity changed: %+v", sig)
			}
			if tc.blockerRun == "followup-test" && tc.want == "failed" && sig.Summary != "Worker Test worker run failed: blocked: additional approval required" {
				t.Fatalf("follow-up blocker summary = %q", sig.Summary)
			}
			if tc.want == "succeeded" && sig.Summary != "Worker Test worker run succeeded" {
				t.Fatalf("success summary = %q", sig.Summary)
			}
			if tc.followup == "" && sig.Summary != "Worker Test worker run failed: blocked: scope approval required" {
				t.Fatalf("first-run blocker summary changed: %q", sig.Summary)
			}
		})
	}
}

// Purpose: preserve successful plan-role reopen signals. PrepareFollowup admits
// plan-role follow-ups only after successful runs; workerRunSignal must not
// reinterpret that receipt using mutable plan state. A temporary session/signal
// store is the narrowest layer proving the emitted outcome and round identity.
func TestWorkerRunSignalPlanRoleReopen(t *testing.T) {
	for _, role := range []string{"", "swarm"} {
		for _, state := range []string{"missing", "blocked"} {
			t.Run(role+"/"+state, func(t *testing.T) {
				db, err := pebblestore.Open(filepath.Join(t.TempDir(), "signals.pebble"))
				if err != nil {
					t.Fatal(err)
				}
				t.Cleanup(func() { _ = db.Close() })
				sessions := pebblestore.NewSessionStore(db)
				run := pebblestore.WorkerRunRecord{ID: "run-test", SessionID: "session-test", AccountScopeID: "account-test", Role: role, Status: "succeeded", FollowupStatus: "completed", FollowupRunID: "followup-test"}
				if err := sessions.CreateSession(pebblestore.SessionSnapshot{ID: run.SessionID, AccountScopeID: run.AccountScopeID}); err != nil {
					t.Fatal(err)
				}
				if state != "missing" {
					id := run.ID
					checkpoints := []pebblestore.SessionPlanCheckpoint{{ID: "cp-1", Status: "completed"}, {ID: "cp-2", Status: state}}
					if err := sessions.PutPlan(pebblestore.SessionPlanSnapshot{ID: id, SessionID: run.SessionID, Document: &pebblestore.SessionPlanDocument{Checkpoints: checkpoints}}); err != nil {
						t.Fatal(err)
					}
					if err := sessions.SetActivePlan(run.SessionID, id, 1); err != nil {
						t.Fatal(err)
					}
				}
				feed := pebblestore.NewSignalStore(db)
				workerRunSignal(signals.NewEmitter(feed), sessions)(pebblestore.WorkerRecord{ID: "worker-test"}, run)
				page, err := feed.ListAfter(0, 10, pebblestore.SignalFilter{Account: run.AccountScopeID})
				if err != nil {
					t.Fatal(err)
				}
				if len(page.Signals) != 1 {
					t.Fatalf("signals = %+v", page.Signals)
				}
				want, severity := "worker.run.succeeded", pebblestore.SignalSeverityInfo
				sig := page.Signals[0]
				if sig.Kind != want || sig.Severity != severity || sig.DedupKey != "worker.run:run-test:followup-test" {
					t.Fatalf("unexpected signal: %+v", sig)
				}
			})
		}
	}
}
