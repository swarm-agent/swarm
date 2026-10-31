package run

import (
	"context"
	"errors"
	"testing"
	"time"

	sessions "swarm/packages/swarmd/internal/session"
	store "swarm/packages/swarmd/internal/store/pebble"
)

// Purpose:
//   - Requirement: once the person fixed the provider (a new sign-in, key or
//     model), a worker run waiting for a provider's limit whose stated reset
//     is days away runs again now: RetryLimitWaitsNow moves its RetryAt to
//     now and hands it to the one rerun path (rerunAfterProviderLimit /
//     startLimitRerun), which records one rerun message and run intent and
//     wakes the executor once. A second call is a no-op (no second message,
//     intent or wake). A rerun that hits the limit again waits again, and
//     the machine-wide form with a provider filter reruns it once more. Other
//     accounts, other providers and an unknown run change nothing.
//   - Threat/regression: after a weekly limit the old account's reset was
//     days away and nothing resumed after the operator signed in to another
//     account; a retry that bypassed the rerun path would double-run the job
//     or lose LimitWaits accounting.
//   - Boundary/authority: WorkerExecutionService.RetryLimitWaitsNow over
//     rerunAfterProviderLimit / startLimitRerun, WorkerStore and the V3
//     session mutation boundary on a real Pebble store; only the model
//     provider is replaced (the fixture records the turns' run intents).
//   - Narrowest layer: the service owns the retry and the rerun; the store
//     and session records are its observable postconditions.
func TestRetryLimitWaitsNowRerunsAWaitingWorkerRunOnce(t *testing.T) {
	f := newLimitRunFixture(t, store.WorkerRoleCoder)
	ws := f.svc.Store().WorkerStore()
	active, err := ws.ConfigureWorkerBindings("account-a", "user-a", f.worker.ID, f.worker.Revision, nil, true)
	if err != nil || active.LifecycleState != store.WorkerLifecycleStateActive {
		t.Fatalf("activate worker: %+v %v", active, err)
	}
	f.worker = active
	f.intent("execution", sessions.RunIntentFailed, workerSessionLimit)
	waiting := f.observe()
	if waiting.Status != store.WorkerRunStatusWaiting {
		t.Fatalf("run did not wait on the limit: %+v", waiting)
	}
	// The old account's weekly reset is days away.
	future := time.Now().Add(72 * time.Hour).UnixMilli()
	waiting.RetryAt = future
	if _, err := ws.RecordWorkerRun("account-a", waiting); err != nil {
		t.Fatal(err)
	}
	if got := f.rerun(time.Now()); got.Status != store.WorkerRunStatusWaiting || len(f.woken) != 0 {
		t.Fatalf("precondition: the run reran before its reset: %+v woken %v", got, f.woken)
	}
	messagesBefore, err := f.svc.ListMessages(f.run.SessionID, 0, 100)
	if err != nil {
		t.Fatal(err)
	}

	// Nothing of another account, another provider or an unknown run.
	if got, err := f.execution.RetryLimitWaitsNow(context.Background(), "account-b", LimitRetryFilter{}); err != nil || len(got) != 0 {
		t.Fatalf("another account's retry reached this run: %+v %v", got, err)
	}
	if got, err := f.execution.RetryLimitWaitsNow(context.Background(), "account-a", LimitRetryFilter{Provider: "other-provider"}); err != nil || len(got) != 0 {
		t.Fatalf("a retry for another provider reached this run: %+v %v", got, err)
	}
	if _, err := f.execution.RetryLimitWaitsNow(context.Background(), "account-a", LimitRetryFilter{WorkerID: f.worker.ID, RunID: "no-such-run"}); !errors.Is(err, store.ErrWorkerNotFound) {
		t.Fatalf("unknown run = %v, want ErrWorkerNotFound", err)
	}
	if got := f.current(); got.Status != store.WorkerRunStatusWaiting || got.RetryAt != future || len(f.woken) != 0 {
		t.Fatalf("a refused or unmatched retry changed the run: %+v woken %v", got, f.woken)
	}

	// Retry this run now: it reruns at once through the rerun path.
	retried, err := f.execution.RetryLimitWaitsNow(context.Background(), "account-a", LimitRetryFilter{WorkerID: f.worker.ID, RunID: f.run.ID})
	if err != nil || len(retried) != 1 || retried[0].RunID != f.run.ID || retried[0].Status != "running" {
		t.Fatalf("retry now = %+v %v", retried, err)
	}
	running := f.current()
	if running.Status != "running" || running.RetryAt != 0 || running.LimitWaits != 1 || len(f.woken) != 1 || f.woken[0] != "execution.limit1" {
		t.Fatalf("the run did not rerun now: %+v woken %v", running, f.woken)
	}
	intent, found, err := f.svc.GetSessionRunIntent(f.run.SessionID, "execution.limit1")
	if err != nil || !found || intent.Status != sessions.RunIntentPendingExecutor {
		t.Fatalf("rerun intent = %+v %v %v", intent, found, err)
	}
	messages, err := f.svc.ListMessages(f.run.SessionID, 0, 100)
	if err != nil || len(messages) != len(messagesBefore)+1 || messages[len(messages)-1].Content != workerLimitRerunMessage {
		t.Fatalf("rerun message: %d -> %d %v", len(messagesBefore), len(messages), err)
	}

	// A second call is idempotent: nothing waits, nothing is recorded again.
	again, err := f.execution.RetryLimitWaitsNow(context.Background(), "account-a", LimitRetryFilter{WorkerID: f.worker.ID, RunID: f.run.ID})
	if err != nil || len(again) != 0 || len(f.woken) != 1 {
		t.Fatalf("second retry = %+v %v woken %v", again, err, f.woken)
	}
	if after, _ := f.svc.ListMessages(f.run.SessionID, 0, 100); len(after) != len(messages) {
		t.Fatalf("second retry appended a message: %d -> %d", len(messages), len(after))
	}

	// The rerun hits the limit again: it waits again (a new wait), and the
	// machine-wide retry for this provider reruns it once more.
	f.intent("execution.limit1", sessions.RunIntentFailed, workerSessionLimit)
	second := f.observe()
	if second.Status != store.WorkerRunStatusWaiting || second.LimitWaits != 2 || second.LimitRunID != "execution.limit1" {
		t.Fatalf("a rerun that hit the limit again did not wait again: %+v", second)
	}
	second.RetryAt = future
	if _, err := ws.RecordWorkerRun("account-a", second); err != nil {
		t.Fatal(err)
	}
	all, err := f.execution.RetryLimitWaitsNow(context.Background(), "account-a", LimitRetryFilter{Provider: "fixture-provider"})
	if err != nil || len(all) != 1 || all[0].Status != "running" || len(f.woken) != 2 || f.woken[1] != "execution.limit2" {
		t.Fatalf("machine-wide retry = %+v %v woken %v", all, err, f.woken)
	}
}
