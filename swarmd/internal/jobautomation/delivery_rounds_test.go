package jobautomation

import (
	"strings"
	"testing"
)

// Requirement: a reopen loop's rounds count per (automation, job) across
// deliveries. A delivery for a later end of the same job continues the count
// of reopens the automation already sent into that job, whether or not the
// end is the follow-up a reopen step sent (a follow-up that failed and was
// followed by another turn breaks that link), and the loop ends blocked
// (rounds_exhausted) once the job has had max_rounds reopens in total.
// Threat (forge run, 2026-10-19): a follow-up turn failed on a provider limit,
// a later turn succeeded, and its delivery restarted at round 0, so a
// max_rounds 2 loop reopened the same job far more than twice in a day.
// Owners: Store.CreateDelivery (round assignment), Runner.runReopen /
// sendReopen and Store.PutReopen over the real store, runner and signal feed;
// only the reviewer job (model provider) and the job's session are stubbed.
// Layer: the runner over the real store is where job ends become deliveries
// and reopens are counted.
func TestReopenRoundsContinueAcrossDeliveriesOfTheSameJob(t *testing.T) {
	h := newHarness(t)
	reopener := &sessionReopener{}
	h.withReopener(reopener)
	folder := t.TempDir()
	writeScript(t, folder, "push.sh", "echo pushed\n")
	for i := 0; i < 6; i++ {
		h.agents.outcomes = append(h.agents.outcomes, AgentOutcome{Verdict: VerdictBlock, Findings: "1. high a.go:1 the limit is not checked"})
	}
	h.process()
	h.automation(reviewLoop(folder, 2))
	result := workerResult(folder, "run-1")
	h.results.results["run-1"] = result
	h.jobEnd("run-1", "", "succeeded", "direct")
	h.process() // round 0: reopen 1 (followup-1)
	if len(reopener.sent) != 1 {
		t.Fatalf("follow-ups after the first end = %d", len(reopener.sent))
	}

	// The reopen's follow-up fails (provider session limit): when succeeded,
	// so no delivery. A later turn of the same job succeeds on a new head.
	result.Status = StatusFailed
	h.jobEnd("run-1", "followup-1", "failed", "direct")
	h.process()
	result.Status = StatusSucceeded
	result.Git.Head = strings.Repeat("c", 40)
	h.jobEnd("run-1", "followup-retry", "succeeded", "direct")
	h.process() // continues at round 1: reopen 2 (followup-2)

	var retry Delivery
	for _, d := range h.deliveries("worker/coder-1/run-1") {
		if d.Followup == "followup-retry" {
			retry = d
		}
	}
	if retry.ID == "" || retry.Round != 1 {
		t.Fatalf("the later turn's delivery = %+v, want round 1", retry)
	}
	if len(reopener.sent) != 2 {
		t.Fatalf("follow-ups after the retry = %d, want 2", len(reopener.sent))
	}

	// The second reopen's follow-up ends on a new head: round 2 of 2, so the
	// loop ends blocked and sends nothing more.
	result.Git.Head = strings.Repeat("d", 40)
	h.jobEnd("run-1", "followup-2", "succeeded", "direct")
	h.process()
	var last Delivery
	for _, d := range h.deliveries("worker/coder-1/run-1") {
		if d.Followup == "followup-2" {
			last = d
		}
	}
	if last.Round != 2 || last.State != StateBlocked || last.BlockedReason != ReasonRoundsExhausted {
		t.Fatalf("last delivery = round %d state %s reason %q", last.Round, last.State, last.BlockedReason)
	}
	if len(reopener.sent) != 2 {
		t.Fatalf("reopens in total = %d, want max_rounds 2", len(reopener.sent))
	}
}

// Requirement: at most one delivery per (automation, job, head). A job end
// whose head already has an active or finished delivery for the automation
// (a stopped follow-up, a turn that committed nothing) creates no delivery,
// and an automation.delivery.skipped signal records why and which delivery
// already covers that head.
// Threat (forge run, 2026-10-19): stopping a running follow-up counted as a
// job end and created a second delivery for a head whose first delivery was
// still running its review.
// Owners: Store.CreateDelivery and Runner.consume over the real store, runner
// and signal feed; the reviewer job (model provider) is stubbed.
// Layer: the consumer over the real store is where job ends become
// deliveries.
func TestJobEndForAnAlreadyDeliveredHeadCreatesNoSecondDelivery(t *testing.T) {
	h := newHarness(t)
	h.withReopener(&sessionReopener{})
	folder := t.TempDir()
	writeScript(t, folder, "push.sh", "echo pushed\n")
	h.agents.outcomes = []AgentOutcome{{Verdict: VerdictOK}, {Verdict: VerdictOK}, {Verdict: VerdictOK}}
	h.process()
	a := reviewLoop(folder, 2)
	a.When = WhenAlways
	h.automation(a)
	result := workerResult(folder, "run-1")
	h.results.results["run-1"] = result
	// The job's turn ends; before its delivery has finished, the person stops
	// a follow-up of the same job on the same head.
	h.jobEnd("run-1", "followup-a", "succeeded", "direct")
	h.jobEnd("run-1", "followup-b", "cancelled", "direct")
	h.process()
	list := h.deliveries("worker/coder-1/run-1")
	if len(list) != 1 {
		t.Fatalf("deliveries for one head = %d, want 1", len(list))
	}
	first := list[0]
	if first.Followup != "followup-a" || first.State != StateSucceeded {
		t.Fatalf("delivery = followup %q state %s", first.Followup, first.State)
	}
	skipped := h.signalsOf("automation.delivery.skipped")
	if len(skipped) != 1 || skipped[0].Attrs["reason"] != "duplicate_head" || skipped[0].Refs["delivery_id"] != first.ID ||
		skipped[0].Attrs["head"] != result.Git.Head || skipped[0].Attrs["followup"] != "followup-b" {
		t.Fatalf("skipped signals = %+v", skipped)
	}
	if len(h.agents.started) != 1 {
		t.Fatalf("reviews started = %d, want 1", len(h.agents.started))
	}

	// Once finished, another end on the same head still delivers nothing; an
	// end on a new head does.
	h.jobEnd("run-1", "followup-c", "succeeded", "direct")
	h.process()
	if list := h.deliveries("worker/coder-1/run-1"); len(list) != 1 {
		t.Fatalf("deliveries after a finished head ended again = %d", len(list))
	}
	result.Git.Head = strings.Repeat("e", 40)
	h.jobEnd("run-1", "followup-d", "succeeded", "direct")
	h.process()
	if list := h.deliveries("worker/coder-1/run-1"); len(list) != 2 {
		t.Fatalf("deliveries after a new head = %d, want 2", len(list))
	}
}
