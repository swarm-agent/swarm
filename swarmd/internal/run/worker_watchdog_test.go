package run

import (
	"context"
	"fmt"
	"os"
	"strings"
	"sync"
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
	for _, mode := range []string{"stalled", "restart", "progress", "queued", "completion"} {
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
			if mode == "progress" {
				defer execution.TrackV3Execution(r.SessionID, func() { cancelled = true })()
			}
			if mode == "stalled" || mode == "restart" || mode == "completion" {
				// Simulate the reported torn durable ownership: receipt running,
				// but no active intent remains for executor recovery.
				if err := ss.Store().Underlying().Delete(store.KeyV3SessionRunIntentActive(r.SessionID)); err != nil {
					t.Fatal(err)
				}
			}
			if mode == "completion" {
				// Pause after watchdog inspection, then let completion win before
				// its conditional write. Channels make the race deterministic.
				inspected := make(chan struct{})
				done := make(chan error, 1)
				execution.watchdogBeforeSettleForTest = func() {
					close(inspected)
					if err := <-done; err != nil {
						t.Fatal(err)
					}
				}
				go func() {
					<-inspected
					completed := r
					completed.Status = "succeeded"
					_, err := ws.RecordWorkerRun("account", completed)
					done <- err
				}()
				settled, err := execution.watchdogRun(r, time.Now().Add(time.Hour))
				if err != nil || settled {
					t.Fatalf("completion overwritten: %v %v", settled, err)
				}
				got, _, _ := ws.GetWorkerRun("account", w.ID, r.ID)
				if got.Status != "succeeded" {
					t.Fatal(got)
				}
				return
			}
			if mode == "restart" {
				// A fresh service has no inherited execution ownership.
				execution = &WorkerExecutionService{host: execution.host}
			}
			if mode == "stalled" || mode == "restart" || mode == "queued" || mode == "progress" {
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
				if cancelled {
					t.Fatal("owned executor cancelled")
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

// Requirement: overflow compaction must not release the outer V3 turn's
// ownership before its continuation finishes. trackExecution/TrackV3Execution
// are the shared boundary used by RunTurn and the V3 executor. This unit test
// exercises that exact nesting with a channel-blocked continuation, without a
// provider dependency, and checks both release orders and duplicate cleanup.
func TestWorkerExecutionNestedCompactionOwnership(t *testing.T) {
	for _, outerFirst := range []bool{false, true} {
		t.Run(fmt.Sprintf("outer_first_%t", outerFirst), func(t *testing.T) {
			s := &WorkerExecutionService{}
			outer := s.TrackV3Execution("session", func() {})
			defer outer()
			compact := s.trackExecution("session", func() {})
			defer compact()
			if outerFirst {
				outer()
				outer()
			} else {
				compact()
				compact()
			}
			started, release, done := make(chan struct{}), make(chan struct{}), make(chan struct{})
			go func() { close(started); <-release; close(done) }()
			var releaseOnce sync.Once
			finish := func() { releaseOnce.Do(func() { close(release) }); <-done }
			defer finish()
			<-started
			if !s.RunActive("session") {
				t.Fatal("nested release lost ownership during continuation")
			}
			s.touchExecution("session")
			finish()
			if outerFirst {
				compact()
			} else {
				outer()
			}
			if s.RunActive("session") {
				t.Fatal("final release retained ownership")
			}
			// A stale release must not decrement a subsequent turn's ownership.
			next := s.TrackV3Execution("session", func() {})
			outer()
			compact()
			if !s.RunActive("session") {
				t.Fatal("stale release removed new owner")
			}
			next()
			if s.RunActive("session") {
				t.Fatal("new owner leaked")
			}
		})
	}
}
