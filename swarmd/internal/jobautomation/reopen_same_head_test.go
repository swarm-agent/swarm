package jobautomation

import "testing"

// Requirement: reopen results pin their new head, skip duplicate heads, and
// terminate blocked follow-ups without review, even for success-only workflows.
// Threat: reviewing stale work or hiding the worker's blocker behind a review.
// Owners: Runner.consume and Store.CreateDelivery. The real store/signal runner
// with bounded agent boundaries proves persisted rounds and dispatch counts.
func TestReopenRoundOutcome(t *testing.T) {
	for _, outcome := range []string{"committed", "same head", "blocked"} {
		t.Run(outcome, func(t *testing.T) {
			h := newHarness(t)
			h.withReopener(&sessionReopener{})
			folder := t.TempDir()
			h.agents.outcomes = []AgentOutcome{{Verdict: VerdictBlock, Findings: "fix the limit"}, {Verdict: VerdictOK}}
			h.process()
			a := reviewLoop(folder, 2)
			a.When = WhenSucceeded
			h.automation(a)
			result := workerResult(folder, "run-1")
			h.results.results["run-1"] = result
			h.jobEnd("run-1", "", "succeeded", "direct")
			h.process()
			first := h.deliveries(result.JobID)[0]
			if first.Steps[1].Outcome["followup"] != "followup-1" {
				t.Fatalf("no reopen: %+v", first)
			}
			status := "succeeded"
			switch outcome {
			case "committed":
				result.Git.Head = "cccccccccccccccccccccccccccccccccccccccc"
			case "blocked":
				status = "failed"
				result.Status, result.Error = StatusFailed, "blocked: approval required"
			}
			h.jobEnd("run-1", "followup-1", status, "direct")
			h.process()
			list := h.deliveries(result.JobID)
			if outcome == "same head" {
				if len(list) != 1 || len(h.agents.started) != 1 {
					t.Fatalf("duplicate reviewed: %+v", list)
				}
				return
			}
			if len(list) != 2 {
				t.Fatalf("deliveries = %+v", list)
			}
			var d Delivery
			for _, item := range list {
				if item.Followup == "followup-1" {
					d = item
				}
			}
			if d.Round != 1 || d.TriggerHead() != result.Git.Head {
				t.Fatalf("round head = %+v", d)
			}
			if outcome == "blocked" {
				if d.State != StateBlocked || d.BlockedReason != result.Error || len(h.agents.started) != 1 || d.Steps[0].Attempts != 0 {
					t.Fatalf("blocker lost or review started: %+v", d)
				}
			} else if len(h.agents.started) != 2 || h.agents.started[1].Head != result.Git.Head || d.Steps[0].State != StateSucceeded {
				t.Fatalf("new head not reviewed: %+v", d)
			}
		})
	}
}

// Requirement: missing Git is unknown, never a review target. Runner.runAgent
// must fail the step explicitly without calling the reviewer; the store/runner
// fixture is the narrowest test of both dispatch absence and durable failure.
func TestReviewWithoutHeadNeverStarts(t *testing.T) {
	h := newHarness(t)
	h.process()
	folder := t.TempDir()
	h.automation(reviewLoop(folder, 2))
	result := workerResult(folder, "run-1")
	result.Git = nil
	h.results.results["run-1"] = result
	h.jobEnd("run-1", "", "succeeded", "direct")
	h.process()
	d := h.deliveries(result.JobID)[0]
	if len(h.agents.started) != 0 || d.State != StateFailed || d.Steps[0].Reason != "no head to review" {
		t.Fatalf("headless review: %+v", d)
	}
}

// Requirement: same-head deduplication applies even to this workflow's reopen;
// unrelated workflow/job/step evidence must not bypass it either. Threat: an
// unnecessary review or unbounded reopen loop. Owner: Store.CreateDelivery;
// store fixtures prove rejection leaves the delivery set unchanged.
func TestSameHeadReopenProvenance(t *testing.T) {
	for _, source := range []string{"same automation", "other automation", "other job", "non-reopen step", "empty followup"} {
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
				t.Fatalf("deduplication bypassed: id %s created %v err %v", got.ID, created, err)
			}
		})
	}
}
