package run

// Purpose. Requirement: a run that run.Service owns is cancelled only by its
// own stop (StopSessionRun, worker job control) or by the context its caller
// gave it, and the error it ends with says which one, while still matching
// context.Canceled so lifecycle classification and failure persistence keep
// treating it as a stop. Threat: a bare "context canceled" (seen as
// "executor context canceled" on a worker follow-up) cannot be attributed, and
// a stop aimed at one job's session must never cancel another job's run.
// Production owners: newRunContext and namedRunCancellation (used by
// runTurn), registerPendingAdmission, beginSessionLifecycle,
// attachLifecycleCancel, StopSessionRun and WorkerExecutionService.cancelRun.
// Layer: these registries and the run context are the whole cancellation
// boundary of runTurn; driving them with a real Pebble store and session
// service exercises the production stop paths without a model provider, which
// is the narrowest layer that proves both the named cause and the cross-job
// isolation.

import (
	"context"
	"errors"
	"path/filepath"
	"strings"
	"testing"
	"time"

	sessionruntime "swarm/packages/swarmd/internal/session"
	pebblestore "swarm/packages/swarmd/internal/store/pebble"
)

type runCancelFixture struct {
	svc      *Service
	sessions *sessionruntime.Service
}

func newRunCancelFixture(t *testing.T, sessionIDs ...string) runCancelFixture {
	t.Helper()
	db, err := pebblestore.Open(filepath.Join(t.TempDir(), "run-cancel.pebble"))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = db.Close() })
	events, err := pebblestore.NewEventLog(db)
	if err != nil {
		t.Fatal(err)
	}
	sessions := sessionruntime.NewService(pebblestore.NewSessionStore(db), events)
	pref := pebblestore.ModelPreference{Provider: "fixture-provider", Model: "fixture-model", Thinking: "low"}
	for _, id := range sessionIDs {
		if _, _, err := sessions.CreateSessionWithOptions(sessionruntime.CreateSessionOptions{SessionID: id, UserID: "user-a", AccountScopeID: "account-a", WorkspacePath: t.TempDir(), Preference: &pref}); err != nil {
			t.Fatal(err)
		}
	}
	return runCancelFixture{svc: &Service{sessions: sessions, activeRuns: make(map[string]*activeSessionRun)}, sessions: sessions}
}

// startRun claims a run exactly as runTurn does: a cause-carrying run context,
// a pending admission, the lifecycle claim and the attached cancel.
func (f runCancelFixture) startRun(t *testing.T, caller context.Context, sessionID, runID string) context.Context {
	t.Helper()
	runCtx, cancel := newRunContext(caller)
	t.Cleanup(func() { cancel(nil) })
	if err := f.svc.registerPendingAdmission(sessionID, runID, cancel); err != nil {
		t.Fatal(err)
	}
	if _, err := f.svc.beginSessionLifecycle(sessionID, runID, "http"); err != nil {
		t.Fatal(err)
	}
	f.svc.attachLifecycleCancel(sessionID, runID, cancel)
	f.svc.removePendingAdmission(sessionID, runID)
	return runCtx
}

func requireNamedCancellation(t *testing.T, caller, runCtx context.Context, by, reason string) {
	t.Helper()
	select {
	case <-runCtx.Done():
	case <-time.After(5 * time.Second):
		t.Fatal("run context was not cancelled")
	}
	err := namedRunCancellation(caller, runCtx, runCtx.Err())
	if !errors.Is(err, context.Canceled) {
		t.Fatalf("named cancellation %v no longer matches context.Canceled", err)
	}
	var named *RunCancelledError
	if !errors.As(err, &named) {
		t.Fatalf("error %q does not name its cause", err)
	}
	if named.By != by || named.Reason != reason {
		t.Fatalf("cause = (%q, %q), want (%q, %q)", named.By, named.Reason, by, reason)
	}
	if !strings.Contains(err.Error(), by) || !strings.Contains(err.Error(), reason) {
		t.Fatalf("error text %q omits its cause", err)
	}
}

func TestStopSessionRunNamesItsCauseAndSparesAnotherJobsRun(t *testing.T) {
	f := newRunCancelFixture(t, "worker-execution-coder", "worker-execution-reviewer")
	caller := context.Background()
	coderCtx := f.startRun(t, caller, "worker-execution-coder", "run-followup")
	reviewerCtx := f.startRun(t, caller, "worker-execution-reviewer", "run-review")

	const reason = "review loop ended the reviewer job"
	if err := f.svc.StopSessionRun("worker-execution-reviewer", "run-review", reason); err != nil {
		t.Fatal(err)
	}
	requireNamedCancellation(t, caller, reviewerCtx, RunCancelledByStopRequest, reason)
	if err := coderCtx.Err(); err != nil {
		t.Fatalf("stopping another job's run cancelled this follow-up: %v", context.Cause(coderCtx))
	}
	if err := namedRunCancellation(caller, coderCtx, nil); err != nil {
		t.Fatalf("a run that was not cancelled reported %v", err)
	}
	lifecycle, ok, err := f.sessions.GetLifecycle("worker-execution-coder")
	if err != nil || !ok || !lifecycle.Active || lifecycle.RunID != "run-followup" {
		t.Fatalf("the follow-up's lifecycle changed: %+v found=%v err=%v", lifecycle, ok, err)
	}
	stopped, ok, err := f.sessions.GetLifecycle("worker-execution-reviewer")
	if err != nil || !ok || stopped.Active || stopped.Phase != lifecyclePhaseCancelled || stopped.StopReason != reason {
		t.Fatalf("the stopped run's lifecycle = %+v found=%v err=%v", stopped, ok, err)
	}
}

func TestStopDuringPendingAdmissionNamesItsCause(t *testing.T) {
	f := newRunCancelFixture(t, "worker-execution-coder")
	caller := context.Background()
	runCtx, cancel := newRunContext(caller)
	defer cancel(nil)
	if err := f.svc.registerPendingAdmission("worker-execution-coder", "run-followup", cancel); err != nil {
		t.Fatal(err)
	}
	const reason = "person pressed stop"
	if err := f.svc.StopSessionRun("worker-execution-coder", "run-followup", reason); err != nil {
		t.Fatal(err)
	}
	requireNamedCancellation(t, caller, runCtx, RunCancelledByStopRequest, reason)
}

func TestWorkerJobControlNamesItsCause(t *testing.T) {
	f := newRunCancelFixture(t, "worker-execution-run-coder")
	ws := f.sessions.Store().WorkerStore()
	coder, err := ws.CreateWorker("account-a", "user-a", pebblestore.CreateWorkerRequest{Role: pebblestore.WorkerRoleCoder, Name: "Coder", IdempotencyKey: "coder"}, nil)
	if err != nil {
		t.Fatal(err)
	}
	r, err := ws.RecordWorkerRun("account-a", pebblestore.WorkerRunRecord{ID: "run-coder", WorkerID: coder.ID, UserID: "user-a", Role: pebblestore.WorkerRoleCoder, SessionID: "worker-execution-run-coder", Status: "running", RequestSource: "direct", CreatedAt: 1, StartedAt: 1})
	if err != nil {
		t.Fatal(err)
	}
	key := "run-cancel-test-intent"
	if _, err := f.sessions.ApplySessionMutation(sessionruntime.SessionMutationInput{SessionID: r.SessionID, UserID: "user-a", AccountScopeID: "account-a", Kind: sessionruntime.SessionMutationRecordRunIntent, ClientRequestID: key, IdempotencyKey: key, PayloadHash: key, RequestHash: key, EventType: "session.run_intent.recorded",
		RunIntent: &pebblestore.V3SessionRunIntent{SessionID: r.SessionID, UserID: "user-a", AccountScopeID: "account-a", RunID: "run-coder", Status: sessionruntime.RunIntentPendingExecutor, RunSessionID: r.SessionID, ParentSessionID: r.SessionID}, NowUnixMs: time.Now().UnixMilli()}); err != nil {
		t.Fatal(err)
	}
	caller := context.Background()
	runCtx := f.startRun(t, caller, r.SessionID, "run-coder")
	execution := &WorkerExecutionService{host: &AutomationV2ExecutionHost{runs: f.svc, apply: f.sessions.ApplySessionMutation}}
	if err := execution.cancelRun(r, "worker stopped"); err != nil {
		t.Fatal(err)
	}
	requireNamedCancellation(t, caller, runCtx, RunCancelledByWorkerControl, "worker stopped")
}

func TestCallerCancellationIsNamedAndOtherErrorsAreKept(t *testing.T) {
	f := newRunCancelFixture(t, "worker-execution-coder")
	caller, cancelCaller := context.WithCancelCause(context.Background())
	runCtx := f.startRun(t, caller, "worker-execution-coder", "run-followup")
	shutdown := errors.New("daemon shutting down")
	cancelCaller(shutdown)
	requireNamedCancellation(t, caller, runCtx, RunCancelledByCaller, shutdown.Error())

	unrelated := errors.New("provider returned 500")
	if got := namedRunCancellation(caller, runCtx, unrelated); got != unrelated {
		t.Fatalf("an unrelated error was rewritten to %v", got)
	}
	untouched, cancel := newRunContext(context.Background())
	defer cancel(nil)
	if got := namedRunCancellation(context.Background(), untouched, context.Canceled); got != context.Canceled {
		t.Fatalf("a cancellation the run context did not see was attributed: %v", got)
	}
}
