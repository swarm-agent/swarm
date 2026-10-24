package jobautomation

import (
	"context"
	"strconv"
	"testing"
)

// Requirement: a recorded reopen's follow-up receives a fresh review even
// without a commit, exactly once, while unrelated same-head ends stay skipped
// and max_rounds still blocks. Threat: silent review loss or an unbounded loop.
// Owners: Store.CreateDelivery/ReopenCount and Runner.consume/runReopen.
// The real store and runner with stubbed host boundaries are the narrowest
// layer proving delivery creation, review execution, signals and round limits.
func TestReopenSameHeadRounds(t *testing.T) {
	h := newHarness(t)
	reopener := &sessionReopener{}
	h.withReopener(reopener)
	folder := t.TempDir()
	const maxRounds = 2
	for i := 0; i <= maxRounds; i++ {
		h.agents.outcomes = append(h.agents.outcomes, AgentOutcome{Verdict: VerdictBlock, Findings: "fix the unchecked limit"})
	}
	h.process()
	a := h.automation(reviewLoop(folder, maxRounds))
	h.results.results["run-1"] = workerResult(folder, "run-1")
	const jobID = "worker/coder-1/run-1"
	h.jobEnd("run-1", "", "succeeded", "direct")
	h.process()
	first := h.deliveries(jobID)[0]
	if first.Round != 0 || first.Steps[1].Outcome["followup"] != "followup-1" {
		t.Fatalf("initial reopen = %+v", first)
	}

	for round := 1; round <= maxRounds; round++ {
		followup := "followup-" + strconv.Itoa(round)
		report := "Retested without changes, round " + strconv.Itoa(round)
		h.results.results["run-1"].Report = report
		h.jobEnd("run-1", followup, "succeeded", "direct")
		h.process()
		list := h.deliveries(jobID)
		if len(list) != round+1 {
			t.Fatalf("round %d: deliveries = %d", round, len(list))
		}
		var current Delivery
		for _, d := range list {
			if d.Followup == followup {
				current = d
			}
		}
		if current.ID != DeliveryID(jobID, followup, a.ID, a.Revision) || current.Round != round || current.TriggerHead() != first.TriggerHead() {
			t.Fatalf("round %d: delivery = %+v", round, current)
		}
		if current.Steps[0].State != StateSucceeded || current.Steps[0].Attempts != 1 || len(h.agents.started) != round+1 {
			t.Fatalf("round %d: review did not run once: %+v", round, current.Steps[0])
		}
		if got := h.agents.started[round].Result; got.Report != report || got.Followup != followup {
			t.Fatalf("review did not receive new result: %+v", got)
		}
		if round == maxRounds {
			if current.State != StateBlocked || current.BlockedReason != ReasonRoundsExhausted || current.Steps[1].Reason != ReasonRoundsExhausted || len(reopener.sent) != maxRounds {
				t.Fatalf("round budget not enforced: %+v; sent %d", current, len(reopener.sent))
			}
		}

		// Replay at the consumer as well as the store boundary, without
		// relying on the signal feed's own deduplication to hide duplicates.
		for _, sig := range h.signalsOf("worker.run.succeeded") {
			if followupOf(sig) == followup {
				if err := h.runner.consume(context.Background(), testAccount, sig); err != nil {
					t.Fatal(err)
				}
			}
		}
		again, created, err := h.store.CreateDelivery(a, current.Result)
		if err != nil || created || again.ID != current.ID {
			t.Fatalf("repeated end: id %s created %v err %v", again.ID, created, err)
		}
		h.process()
		if len(h.deliveries(jobID)) != round+1 || len(h.agents.started) != round+1 {
			t.Fatal("repeated end created another delivery or review")
		}
	}

	h.jobEnd("run-1", "manual-followup", "succeeded", "direct")
	h.process()
	if len(h.deliveries(jobID)) != maxRounds+1 || len(h.agents.started) != maxRounds+1 || len(reopener.sent) != maxRounds {
		t.Fatal("unrelated same-head end changed workflow state")
	}
	skips := h.signalsOf("automation.delivery.skipped")
	if len(skips) != 1 || skips[0].Attrs["reason"] != ReasonDuplicateHead || skips[0].Attrs["followup"] != "manual-followup" {
		t.Fatalf("duplicate-head signals = %+v", skips)
	}
}

// Requirement: only this automation's reopen for this job grants a same-head
// exception. Threat: another automation/job or a non-reopen outcome bypasses
// deduplication. Owner: Store.CreateDelivery; store-level fixtures isolate the
// provenance decision and verify rejection leaves the delivery set unchanged.
func TestSameHeadReopenProvenance(t *testing.T) {
	for _, source := range []string{"other automation", "other job", "non-reopen step", "empty followup"} {
		t.Run(source, func(t *testing.T) {
			h := newHarness(t)
			a := h.automation(reviewLoop(t.TempDir(), 2))
			result := *workerResult(a.Scope.Folder, "run-1")
			first, _, err := h.store.CreateDelivery(a, result)
			if err != nil {
				t.Fatal(err)
			}
			evidence := first
			followup := "followup-1"
			switch source {
			case "other automation":
				other := a
				other.ID = "other-automation"
				evidence, _, err = h.store.CreateDelivery(other, result)
			case "other job":
				other := result
				other.JobID = "worker/coder-1/other-run"
				evidence, _, err = h.store.CreateDelivery(a, other)
			case "empty followup":
				followup = ""
			}
			if err != nil {
				t.Fatal(err)
			}
			evidence.Steps[1].Outcome = map[string]any{"followup": followup}
			if source == "non-reopen step" {
				evidence.Steps[1].Type = StepAgent
			}
			if err := h.store.SaveDelivery(evidence); err != nil {
				t.Fatal(err)
			}
			before := len(h.deliveries(result.JobID))
			result.Followup = followup
			got, created, err := h.store.CreateDelivery(a, result)
			if err != nil || created || got.ID != first.ID || len(h.deliveries(result.JobID)) != before {
				t.Fatalf("unrelated evidence bypassed deduplication: id %s created %v err %v", got.ID, created, err)
			}
		})
	}
}
