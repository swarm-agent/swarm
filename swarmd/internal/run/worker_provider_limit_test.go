package run

import (
	"context"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"swarm/packages/swarmd/internal/identity"
	"swarm/packages/swarmd/internal/providerlimit"
	sessions "swarm/packages/swarmd/internal/session"
	store "swarm/packages/swarmd/internal/store/pebble"
)

const workerSessionLimit = "claude code turn failed (success): You've hit your session limit · resets 4pm (UTC)"

// limitRunFixture is a real Pebble store, session service and worker store
// holding one running direct-role worker run (a coder, or a reviewer of a
// coder's job) in its own session. Only the model provider is replaced: the
// test records the run intents and assistant messages a provider turn would.
type limitRunFixture struct {
	t         *testing.T
	path      string
	db        *store.Store
	svc       *sessions.Service
	execution *WorkerExecutionService
	worker    store.WorkerRecord
	coder     string
	run       store.WorkerRunRecord
	woken     []string
}

func newLimitRunFixture(t *testing.T, role string) *limitRunFixture {
	t.Helper()
	f := &limitRunFixture{t: t, path: filepath.Join(t.TempDir(), "worker.pebble")}
	f.open()
	t.Cleanup(func() { _ = f.db.Close() })
	ws := f.svc.Store().WorkerStore()
	coder, err := ws.CreateWorker("account-a", "user-a", store.CreateWorkerRequest{Role: store.WorkerRoleCoder, Name: "Coder", IdempotencyKey: "coder"}, nil)
	if err != nil {
		t.Fatal(err)
	}
	f.coder, f.worker = coder.ID, coder
	metadata := map[string]any{"worker_id": coder.ID, "worker_execution_run_id": "execution"}
	input := map[string]any{"prompt": "Write the plan"}
	if role == store.WorkerRoleReviewer {
		if _, err := ws.RecordWorkerRun("account-a", store.WorkerRunRecord{ID: "run-coder", WorkerID: coder.ID, UserID: "user-a", Role: store.WorkerRoleCoder, Status: "succeeded", RequestSource: "direct", CreatedAt: 1}); err != nil {
			t.Fatal(err)
		}
		reviewer, err := ws.CreateWorker("account-a", "user-a", store.CreateWorkerRequest{Role: store.WorkerRoleReviewer, Name: "Reviewer", IdempotencyKey: "reviewer"}, nil)
		if err != nil {
			t.Fatal(err)
		}
		f.worker = reviewer
		job := "worker/" + coder.ID + "/run-coder"
		metadata = map[string]any{"worker_id": reviewer.ID, "worker_role": store.WorkerRoleReviewer, "worker_execution_run_id": "execution",
			"worker_review_target": map[string]any{"head": reviewRecordHead, "base": strings.Repeat("a", 40), "job": job}}
		input = map[string]any{"review_job": job}
	}
	pref := store.ModelPreference{Provider: "fixture-provider", Model: "fixture-model", Thinking: "high"}
	sess, _, err := f.svc.CreateSessionWithOptions(sessions.CreateSessionOptions{SessionID: "worker-execution-execution", WorkspacePath: t.TempDir(), UserID: "user-a", AccountScopeID: "account-a", Preference: &pref, Metadata: metadata})
	if err != nil {
		t.Fatal(err)
	}
	f.run, err = ws.RecordWorkerRun("account-a", store.WorkerRunRecord{ID: "execution", WorkerID: f.worker.ID, UserID: "user-a", Role: role, SessionID: sess.ID, Status: "running", RequestSource: "direct", Input: input, CreatedAt: 2, StartedAt: 2})
	if err != nil {
		t.Fatal(err)
	}
	f.intent("execution", sessions.RunIntentPendingExecutor, "")
	return f
}

// open (re)opens the store and builds the execution service over it, as a
// daemon start does.
func (f *limitRunFixture) open() {
	f.t.Helper()
	db, err := store.Open(f.path)
	if err != nil {
		f.t.Fatal(err)
	}
	events, err := store.NewEventLog(db)
	if err != nil {
		f.t.Fatal(err)
	}
	f.db, f.svc = db, sessions.NewService(store.NewSessionStore(db), events)
	f.execution = &WorkerExecutionService{host: &AutomationV2ExecutionHost{runs: &Service{sessions: f.svc}, apply: f.svc.ApplySessionMutation,
		enqueue: func(_ identity.Principal, intent store.V3SessionRunIntent) bool {
			f.woken = append(f.woken, intent.RunID)
			return true
		}}}
}

func (f *limitRunFixture) restart() {
	f.t.Helper()
	if err := f.db.Close(); err != nil {
		f.t.Fatal(err)
	}
	f.open()
}

// intent records a provider turn's run intent state, as the executor does.
func (f *limitRunFixture) intent(runID, status, reason string) {
	f.t.Helper()
	key := "intent-" + runID + "-" + status
	_, err := f.svc.ApplySessionMutation(sessions.SessionMutationInput{SessionID: f.run.SessionID, UserID: "user-a", AccountScopeID: "account-a", Kind: sessions.SessionMutationRecordRunIntent, ClientRequestID: key, IdempotencyKey: key, PayloadHash: key, RequestHash: key, EventType: "session.run_intent.recorded",
		RunIntent: &store.V3SessionRunIntent{SessionID: f.run.SessionID, UserID: "user-a", AccountScopeID: "account-a", RunID: runID, Status: status, BlockedReason: reason, RunSessionID: f.run.SessionID, ParentSessionID: f.run.SessionID}, NowUnixMs: time.Now().UnixMilli()})
	if err != nil {
		f.t.Fatal(err)
	}
}

// assistant records the rerun's final assistant message through the
// canonical V3 message mutation, as the executor does.
func (f *limitRunFixture) assistant(content string) {
	f.t.Helper()
	key := "assistant-" + f.run.SessionID
	message := store.MessageSnapshot{ID: "assistant-message", SessionID: f.run.SessionID, UserID: "user-a", AccountScopeID: "account-a", Role: "assistant", Content: content, CreatedAt: time.Now().UnixMilli()}
	if _, err := f.svc.ApplySessionMutation(sessions.SessionMutationInput{SessionID: f.run.SessionID, UserID: "user-a", AccountScopeID: "account-a", ClientRequestID: key, IdempotencyKey: key, PayloadHash: key, RequestHash: key, Kind: sessions.SessionMutationAppendMessage, Message: &message, NowUnixMs: time.Now().UnixMilli()}); err != nil {
		f.t.Fatal(err)
	}
}

func (f *limitRunFixture) current() store.WorkerRunRecord {
	f.t.Helper()
	r, found, err := f.svc.Store().WorkerStore().GetWorkerRun("account-a", f.worker.ID, f.run.ID)
	if err != nil || !found {
		f.t.Fatalf("read run: %v %v", found, err)
	}
	return r
}

func (f *limitRunFixture) observe() store.WorkerRunRecord {
	f.t.Helper()
	if err := f.execution.observeRun(f.run); err != nil {
		f.t.Fatal(err)
	}
	return f.current()
}

func (f *limitRunFixture) rerun(now time.Time) store.WorkerRunRecord {
	f.t.Helper()
	active := f.worker
	active.LifecycleState = store.WorkerLifecycleStateActive
	if err := f.execution.rerunAfterProviderLimit(context.Background(), f.svc.Store().WorkerStore(), active, f.current(), false, now); err != nil {
		f.t.Fatal(err)
	}
	return f.current()
}

// Requirement: a worker run whose session run ends on the provider's session
// limit is not failed: it is recorded "waiting" for the reset the provider
// states (no capacity held, still unfinished so the scheduler observes it),
// nothing runs before the reset, the wait survives a daemon restart, and at
// the reset the job runs again in its own session (one new message and run
// intent, woken once) and then settles on that run's outcome. A failure that
// is not a provider limit, Swarm's own quota among them, still fails.
// Threat: a provider limit ended a worker job as failed and nothing reran it
// after the reset (the socials daily check).
// Owners: WorkerExecutionService.observeRun / waitForProviderLimit,
// rerunAfterProviderLimit / startLimitRerun (called by reconcileWorkerRuns
// for each unfinished run), WorkerStore.RecordWorkerRun's run index over a
// real Pebble store; the model provider is the only boundary replaced.
func TestWorkerRunWaitsForProviderLimitAndRerunsAfterReset(t *testing.T) {
	f := newLimitRunFixture(t, store.WorkerRoleCoder)
	f.intent("execution", sessions.RunIntentFailed, workerSessionLimit)
	before := time.Now()
	waiting := f.observe()
	after := time.Now()
	low, _ := providerlimit.ResetAt(workerSessionLimit, before)
	high, _ := providerlimit.ResetAt(workerSessionLimit, after)
	if waiting.Status != store.WorkerRunStatusWaiting || waiting.Error != "" || waiting.CompletedAt != 0 || waiting.LimitWaits != 1 || waiting.LimitRunID != "execution" ||
		waiting.RetryAt < low.UnixMilli() || waiting.RetryAt > high.UnixMilli() || !strings.HasPrefix(waiting.QueueReason, "waiting for the provider's limit to reset (until ") {
		t.Fatalf("a provider limit did not leave the run waiting for the reset: %+v", waiting)
	}
	if store.AutomationV2Terminal(waiting.Status) {
		t.Fatal("a waiting run is terminal")
	}

	// The wait is durable: after a restart the run is still unfinished (the
	// scheduler's busy set) and nothing runs before the reset.
	f.restart()
	pending, err := f.svc.Store().WorkerStore().UnfinishedWorkerRuns("account-a", f.worker.ID, "")
	if err != nil || len(pending) != 1 || pending[0].Status != store.WorkerRunStatusWaiting {
		t.Fatalf("waiting run is not unfinished after a restart: %+v %v", pending, err)
	}
	if got := f.rerun(time.UnixMilli(waiting.RetryAt - 1)); got.Status != store.WorkerRunStatusWaiting || len(f.woken) != 0 {
		t.Fatalf("the run reran before the provider's reset: %+v woken %v", got, f.woken)
	}

	// At the reset the job runs again in its own session.
	running := f.rerun(time.UnixMilli(waiting.RetryAt))
	if running.Status != "running" || running.RetryAt != 0 || running.QueueReason != "" || len(f.woken) != 1 || f.woken[0] != "execution.limit1" {
		t.Fatalf("the run did not rerun at the reset: %+v woken %v", running, f.woken)
	}
	intent, found, err := f.svc.GetSessionRunIntent(f.run.SessionID, "execution.limit1")
	if err != nil || !found || intent.Status != sessions.RunIntentPendingExecutor {
		t.Fatalf("rerun intent = %+v %v %v", intent, found, err)
	}
	messages, err := f.svc.ListMessages(f.run.SessionID, 0, 100)
	if err != nil || len(messages) == 0 || messages[len(messages)-1].Content != workerLimitRerunMessage {
		t.Fatalf("rerun message missing: %+v %v", messages, err)
	}
	// A replayed rerun (the reconcile loop again) adds nothing.
	if err := f.execution.startLimitRerun(f.current()); err != nil {
		t.Fatal(err)
	}
	if again, _ := f.svc.ListMessages(f.run.SessionID, 0, 100); len(again) != len(messages) {
		t.Fatalf("replayed rerun appended a second message: %d -> %d", len(messages), len(again))
	}

	f.intent("execution.limit1", sessions.RunIntentCompleted, "")
	if done := f.observe(); done.Status != "succeeded" || done.Error != "" {
		t.Fatalf("the rerun's outcome was not recorded: %+v", done)
	}
}

// Requirement: a crash after the run turned running for its rerun but before
// the rerun's message was recorded resumes the rerun at once (the limit
// already reset), without counting another wait; a second limit on the rerun
// waits again under a new rerun; after the bounded number of waits the run
// fails with the exhausted reason (no further wait anywhere); a cancel while
// waiting cancels; a failure that is not a provider limit, Swarm's own
// artifact quota among them, fails as before.
// Threat: a lost rerun leaving the job stuck, a day's wait from misreading a
// past reset, an endless wait loop, or Swarm's own quota retried as a
// provider limit.
// Owners: waitForProviderLimit and rerunAfterProviderLimit over a real store.
func TestWorkerRunProviderLimitRecoveryAndBounds(t *testing.T) {
	f := newLimitRunFixture(t, store.WorkerRoleCoder)
	f.intent("execution", sessions.RunIntentFailed, workerSessionLimit)
	waiting := f.observe()
	crashed := waiting
	crashed.Status, crashed.QueueReason, crashed.RetryAt = "running", "", 0
	if _, err := f.svc.Store().WorkerStore().RecordWorkerRun("account-a", crashed); err != nil {
		t.Fatal(err)
	}
	now := time.Now()
	resumed := f.observe()
	if resumed.Status != store.WorkerRunStatusWaiting || resumed.LimitWaits != 1 || resumed.RetryAt > time.Now().UnixMilli() || resumed.RetryAt < now.UnixMilli() {
		t.Fatalf("an interrupted rerun did not resume at once: %+v", resumed)
	}
	f.rerun(time.Now())
	f.intent("execution.limit1", sessions.RunIntentFailed, "429 Too Many Requests: rate limit exceeded")
	if again := f.observe(); again.Status != store.WorkerRunStatusWaiting || again.LimitWaits != 2 || again.LimitRunID != "execution.limit1" {
		t.Fatalf("a second limit did not wait again: %+v", again)
	}

	// The run reached its last allowed rerun, and that rerun hit the limit too.
	exhausted := f.current()
	exhausted.Status, exhausted.LimitWaits, exhausted.LimitRunID, exhausted.RetryAt = "running", maxWorkerLimitWaits, "execution.limit11", 0
	if _, err := f.svc.Store().WorkerStore().RecordWorkerRun("account-a", exhausted); err != nil {
		t.Fatal(err)
	}
	f.intent(limitRerunID(exhausted), sessions.RunIntentPendingExecutor, "")
	f.intent(limitRerunID(exhausted), sessions.RunIntentFailed, workerSessionLimit)
	if failed := f.observe(); failed.Status != "failed" || !providerlimit.IsExhausted(failed.Error) || providerlimit.Is(failed.Error) {
		t.Fatalf("a run past the wait bound = %+v", failed)
	}

	cancelled := newLimitRunFixture(t, store.WorkerRoleCoder)
	cancelled.intent("execution", sessions.RunIntentFailed, workerSessionLimit)
	cancelled.observe()
	r := cancelled.current()
	r.CancelRequested = true
	if _, err := cancelled.svc.Store().WorkerStore().RecordWorkerRun("account-a", r); err != nil {
		t.Fatal(err)
	}
	if got := cancelled.rerun(time.Now()); got.Status != "cancelled" || len(cancelled.woken) != 0 {
		t.Fatalf("a cancelled waiting run = %+v woken %v", got, cancelled.woken)
	}

	for _, reason := range []string{"artifact v3 git: quota exceeded", "the checkout could not be prepared"} {
		other := newLimitRunFixture(t, store.WorkerRoleCoder)
		other.intent("execution", sessions.RunIntentFailed, reason)
		if got := other.observe(); got.Status != "failed" || got.Error != reason || got.LimitWaits != 0 {
			t.Fatalf("%q = %+v", reason, got)
		}
	}
}

// Requirement: a standalone reviewer worker job whose provider turn ends on
// a session limit records no review on the reviewed job (it stays awaiting
// review, never BLOCK), waits for the reset, reruns in its own session, and
// then records the verdict its rerun wrote, so the reviewed job does not stay
// awaiting review forever.
// Threat: a reviewer stopped by a provider limit recorded BLOCK ("missing or
// malformed review verdict"), or recorded nothing and was never rerun.
// Owners: observeRun / waitForProviderLimit, rerunAfterProviderLimit and
// recordReviewResult over WorkerStore.GetJobReview on a real store.
func TestStandaloneReviewerRerunsAfterProviderLimitAndRecordsItsVerdict(t *testing.T) {
	f := newLimitRunFixture(t, store.WorkerRoleReviewer)
	ws := f.svc.Store().WorkerStore()
	f.intent("execution", sessions.RunIntentFailed, workerSessionLimit)
	waiting := f.observe()
	if waiting.Status != store.WorkerRunStatusWaiting {
		t.Fatalf("reviewer = %+v", waiting)
	}
	if review, err := ws.GetJobReview("account-a", f.coder, "run-coder"); err != nil || review != nil {
		t.Fatalf("a provider limit recorded a review: %+v %v", review, err)
	}
	f.rerun(time.UnixMilli(waiting.RetryAt))
	if len(f.woken) != 1 {
		t.Fatalf("reviewer not rerun: %v", f.woken)
	}
	f.assistant(reviewRecordMessage("OK", "None"))
	f.intent("execution.limit1", sessions.RunIntentCompleted, "")
	if done := f.observe(); done.Status != "succeeded" {
		t.Fatalf("reviewer rerun = %+v", done)
	}
	review, err := ws.GetJobReview("account-a", f.coder, "run-coder")
	if err != nil || review == nil || review.Verdict != "ok" {
		t.Fatalf("the rerun's verdict was not recorded: %+v %v", review, err)
	}
}
