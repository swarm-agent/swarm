package jobautomation

import (
	"testing"
	"time"
)

// Purpose:
//   - Requirement: an agent step waiting for a provider's limit whose stated
//     reset is hours away runs again now once the person fixed the provider:
//     Runner.RetryLimitWaitsNow makes the wait due now, and the runner's
//     usual due-step path starts one new job (the next .limit<n> key) and the
//     delivery carries on. A second call changes nothing (no second job).
//     Another account, or a job that does not match, is untouched.
//   - Threat/regression: after a limit the step waited for the old account's
//     reset and nothing resumed after the operator signed in to another
//     account; a retry that skipped the due-step path would start a duplicate
//     job or use up the step's retries.
//   - Boundary/authority: Runner.RetryLimitWaitsNow / Store.retryLimitWaitsNow
//     and Runner.runDelivery / runAgent over the real store and signal feed;
//     only the agent job is scripted.
//   - Narrowest layer: the delivery's step records and the started jobs are
//     the observable postconditions of the runner itself.
func TestRetryLimitWaitsNowRunsAWaitingAgentStepOnce(t *testing.T) {
	h := newHarness(t)
	folder := t.TempDir()
	clock := time.Date(2026, 10, 27, 12, 58, 58, 0, time.UTC)
	h.runner.now = func() time.Time { return clock }
	limit := "claude code turn failed (success): You've hit your session limit · resets 4pm (UTC)"
	h.agents.outcomes = []AgentOutcome{
		{Status: StatusFailed, Error: limit},
		{Status: StatusSucceeded},
	}
	h.process()
	h.automation(Automation{Name: "content plan", Scope: Scope{Folder: folder}, Enabled: true, Steps: []Step{
		{ID: "write", Type: StepAgent, Worker: "finder", Brief: "Write the plan"},
	}})
	h.results.results["run-1"] = workerResult(folder, "run-1")
	h.jobEnd("run-1", "", "succeeded", "direct")
	h.process()
	d := h.deliveries("worker/coder-1/run-1")[0]
	if d.Steps[0].State != StateWaiting || d.Steps[0].NextAttemptAt <= clock.UnixMilli() || len(h.agents.started) != 1 {
		t.Fatalf("precondition: the step is not waiting for a future reset: %+v started %d", d.Steps[0], len(h.agents.started))
	}
	limitedJob := limitStepJob(d.Steps[0])
	if limitedJob == "" {
		t.Fatalf("the waiting step names no job: %+v", d.Steps[0].Outcome)
	}

	// Another account and a job that does not match change nothing.
	if got, err := h.runner.RetryLimitWaitsNow("another-account", nil); err != nil || len(got) != 0 {
		t.Fatalf("another account's retry reached the step: %+v %v", got, err)
	}
	if got, err := h.runner.RetryLimitWaitsNow(d.Account, func(job string) bool { return job == "worker/other/run" }); err != nil || len(got) != 0 {
		t.Fatalf("a retry for another job reached the step: %+v %v", got, err)
	}
	h.process()
	if len(h.agents.started) != 1 || h.deliveries("worker/coder-1/run-1")[0].Steps[0].State != StateWaiting {
		t.Fatalf("an unmatched retry ran the step: started %d", len(h.agents.started))
	}

	// Retry now: the step's wait is due and a new job runs at once.
	retried, err := h.runner.RetryLimitWaitsNow(d.Account, func(job string) bool { return job == limitedJob })
	if err != nil || len(retried) != 1 || retried[0].StepID != "write" || retried[0].DeliveryID != d.ID || retried[0].JobID != limitedJob {
		t.Fatalf("retry now = %+v %v", retried, err)
	}
	h.process()
	d = h.deliveries("worker/coder-1/run-1")[0]
	if len(h.agents.started) != 2 || h.agents.started[1].IdempotencyKey != h.agents.started[0].IdempotencyKey+".limit1" {
		t.Fatalf("the retry did not start one new job: %+v", h.agents.started)
	}
	if d.State != StateSucceeded || d.Steps[0].State != StateSucceeded {
		t.Fatalf("the delivery did not carry on after the retry: %s %+v", d.State, d.Steps)
	}

	// A second call finds nothing waiting and starts nothing.
	if again, err := h.runner.RetryLimitWaitsNow(d.Account, nil); err != nil || len(again) != 0 {
		t.Fatalf("second retry = %+v %v", again, err)
	}
	h.process()
	if len(h.agents.started) != 2 {
		t.Fatalf("a second retry started another job: %d", len(h.agents.started))
	}
}
