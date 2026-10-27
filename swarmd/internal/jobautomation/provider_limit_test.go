package jobautomation

import (
	"strings"
	"testing"
	"time"
)

// Requirement: an agent step whose job ended on a provider's session limit
// (the review step of the socials daily check: "claude code turn failed
// (success): You've hit your session limit · resets 4pm (UTC)", recorded by
// the reviewer as block / review unreadable) is not a failure and not a BLOCK:
// the step waits ("waiting for the provider's limit to reset (until …)") with
// no verdict, the later steps are neither run nor skipped, nothing reruns
// before the reset, and after it a new job (a new idempotency key) runs and
// the delivery carries on; the wait uses none of the step's retries.
// Threat: the delivery ended failed, merge and fan-out were skipped, and
// nothing ran after the reset, so the next day's plan stayed stuck.
// Owners: Runner.runAgent (providerLimitWait), Runner.runStep,
// Runner.runDelivery and nextAttemptWait over the real store and signal feed;
// only the agent job is scripted, so this is the narrowest layer that proves
// the delivery's postconditions.
func TestAgentStepWaitsForAProviderLimitResetAndReruns(t *testing.T) {
	h := newHarness(t)
	folder := t.TempDir()
	clock := time.Date(2026, 10, 27, 12, 58, 58, 0, time.UTC)
	h.runner.now = func() time.Time { return clock }
	limit := "claude code turn failed (success): You've hit your session limit · resets 4pm (UTC)"
	h.agents.outcomes = []AgentOutcome{
		{Status: StatusFailed, Error: limit, Verdict: VerdictBlock, ReviewUnreadable: true},
		{Status: StatusSucceeded, Verdict: VerdictOK},
		{Status: StatusSucceeded},
	}
	h.process()
	h.automation(Automation{Name: "content plan", Scope: Scope{Folder: folder}, Enabled: true, Steps: []Step{
		{ID: "review", Type: StepAgent, Worker: "reviewer", Brief: "Review the plan"},
		{ID: "merge", Type: StepAgent, Worker: "finder", Brief: "Merge it", If: []Condition{{Step: "review", Verdict: VerdictOK}}},
	}})
	h.results.results["run-1"] = workerResult(folder, "run-1")
	h.jobEnd("run-1", "", "succeeded", "direct")
	h.process()

	d := h.deliveries("worker/coder-1/run-1")[0]
	review := d.Steps[0]
	wantUntil := time.Date(2026, 10, 27, 16, 1, 0, 0, time.UTC)
	if Terminal(d.State) || review.State != StateWaiting || review.NextAttemptAt != wantUntil.UnixMilli() || review.LimitWaits != 1 ||
		!strings.HasPrefix(review.Reason, "waiting for the provider's limit to reset (until 2026-10-27T16:01:00Z)") {
		t.Fatalf("a provider limit did not leave the step waiting for the reset: delivery %s, step %+v", d.State, review)
	}
	if _, ok := review.Outcome["verdict"]; ok {
		t.Fatalf("a provider limit was recorded as a verdict: %+v", review.Outcome)
	}
	if _, ok := review.Outcome["review_unreadable"]; ok {
		t.Fatalf("a provider limit was recorded as an unreadable review: %+v", review.Outcome)
	}
	if d.Steps[1].State != StatePending || len(h.agents.started) != 1 {
		t.Fatalf("the step after a provider limit ran or was skipped: %+v, started %d", d.Steps[1], len(h.agents.started))
	}
	if n := len(h.signalsOf("automation.step.failed")); n != 0 {
		t.Fatalf("a provider limit signalled %d step failures", n)
	}
	if sigs := h.signalsOf("automation.step.waiting"); len(sigs) != 1 || sigs[0].Attrs["provider_limit"] != "true" {
		t.Fatalf("waiting signals = %+v", sigs)
	}

	// Before the reset nothing runs again.
	clock = time.Date(2026, 10, 27, 15, 59, 0, 0, time.UTC)
	h.process()
	if len(h.agents.started) != 1 || h.deliveries("worker/coder-1/run-1")[0].Steps[0].State != StateWaiting {
		t.Fatalf("the step ran again before the provider's reset: started %d", len(h.agents.started))
	}

	// After the reset a new job runs and the delivery carries on.
	clock = time.Date(2026, 10, 27, 16, 2, 0, 0, time.UTC)
	h.process()
	d = h.deliveries("worker/coder-1/run-1")[0]
	if len(h.agents.started) != 3 || h.agents.started[1].IdempotencyKey == h.agents.started[0].IdempotencyKey ||
		h.agents.started[1].IdempotencyKey != h.agents.started[0].IdempotencyKey+".limit1" {
		t.Fatalf("the rerun after the reset did not start a new job: %+v", h.agents.started)
	}
	if d.State != StateSucceeded || d.Steps[0].State != StateSucceeded || d.Steps[0].Outcome["verdict"] != VerdictOK || d.Steps[1].State != StateSucceeded {
		t.Fatalf("the delivery did not carry on after the reset: %s %+v", d.State, d.Steps)
	}
}

// Requirement: a provider limit with no readable reset waits a bounded
// backoff (5 minutes for the first) rather than failing.
// Threat: a limit without a stated reset recorded as a failure, or retried
// at once in a loop against the provider.
// Owners: Runner.providerLimitWait and providerlimit.Backoff.
func TestAgentStepProviderLimitWithoutResetBacksOff(t *testing.T) {
	h := newHarness(t)
	folder := t.TempDir()
	clock := time.Date(2026, 10, 27, 12, 0, 0, 0, time.UTC)
	h.runner.now = func() time.Time { return clock }
	h.agents.outcomes = []AgentOutcome{{Status: StatusFailed, Error: "429 Too Many Requests: rate limit exceeded"}}
	h.process()
	h.automation(Automation{Name: "rate", Scope: Scope{Folder: folder}, Enabled: true, Steps: []Step{{ID: "work", Type: StepAgent, Worker: "finder", Brief: "Do it"}}})
	h.results.results["run-1"] = workerResult(folder, "run-1")
	h.jobEnd("run-1", "", "succeeded", "direct")
	h.process()
	step := h.deliveries("worker/coder-1/run-1")[0].Steps[0]
	if step.State != StateWaiting || step.NextAttemptAt != clock.Add(5*time.Minute).UnixMilli() || !strings.Contains(step.Reason, "the provider stated no reset time") {
		t.Fatalf("step = %+v", step)
	}
}
