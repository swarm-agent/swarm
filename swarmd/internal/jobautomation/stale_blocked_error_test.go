package jobautomation

import (
	"path/filepath"
	"strings"
	"testing"
)

// Requirement: a reopen's follow-up closes its workflow round as blocked only
// when that follow-up's own run ended blocked (Result.EndedBlocked: status
// failed with a "blocked: " error). A follow-up that succeeded opens the next
// round and runs its steps, even when the result still carries an earlier
// run's "blocked: " text.
// Threat (socials machine, 2026-10-30): a job blocked once, later follow-ups
// succeeded, and its workflow's round-1 delivery was created already blocked
// with the morning's blocker, so the fixed work was never re-reviewed,
// merged or drafted.
// Owners: Runner.consume and Store.CreateDelivery over the real store, runner
// and signal feed; only the reviewer job (model provider) and the job's
// session are stubbed. Layer: the runner is where job ends become deliveries.
func TestStaleBlockedErrorDoesNotBlockReopenRound(t *testing.T) {
	const stale = "blocked: Narration can't be generated"
	for _, outcome := range []string{"succeeded", "blocked"} {
		t.Run(outcome, func(t *testing.T) {
			h := newHarness(t)
			reopener := &sessionReopener{}
			h.withReopener(reopener)
			folder := t.TempDir()
			writeScript(t, folder, "push.sh", "echo \"$SWARM_HEAD\" >> \"$PWD/pushed\"\n")
			h.agents.outcomes = []AgentOutcome{{Verdict: VerdictBlock, Findings: "1. high a.go:1 narration missing"}, {Verdict: VerdictOK}}
			h.process()
			a := reviewLoop(folder, 2)
			a.When = WhenSucceeded
			h.automation(a)

			// The job blocked earlier; a person's follow-up then succeeded,
			// and the job's result still carries the earlier blocker.
			result := workerResult(folder, "run-1")
			result.Error = stale
			h.results.results["run-1"] = result
			h.jobEnd("run-1", "person-1", "succeeded", "direct")
			h.process() // round 0: review blocks, reopen sends followup-1
			first := h.deliveries(result.JobID)
			if len(first) != 1 || first[0].Round != 0 || first[0].State == StateBlocked || len(reopener.sent) != 1 {
				t.Fatalf("round 0 = %+v, reopens %d", first, len(reopener.sent))
			}

			result.Git.Head = strings.Repeat("c", 40)
			status := "succeeded"
			if outcome == "blocked" {
				status = "failed"
				result.Status, result.Error = StatusFailed, "blocked: approval required"
			}
			h.jobEnd("run-1", "followup-1", status, "direct")
			h.process()

			var d Delivery
			for _, item := range h.deliveries(result.JobID) {
				if item.Followup == "followup-1" {
					d = item
				}
			}
			if d.ID == "" || d.Round != 1 {
				t.Fatalf("round 1 delivery = %+v", d)
			}
			if outcome == "blocked" {
				if d.State != StateBlocked || d.BlockedReason != "blocked: approval required" || len(h.agents.started) != 1 {
					t.Fatalf("genuine blocker lost or review started: %+v", d)
				}
				for _, step := range d.Steps {
					if step.State != StateSkipped {
						t.Fatalf("blocked round ran a step: %+v", d.Steps)
					}
				}
				return
			}
			if d.State != StateSucceeded || d.BlockedReason != "" {
				t.Fatalf("round 1 = state %s reason %q, want succeeded", d.State, d.BlockedReason)
			}
			if len(h.agents.started) != 2 || h.agents.started[1].Head != result.Git.Head || d.Steps[0].State != StateSucceeded {
				t.Fatalf("round 1 head not reviewed: %+v", d)
			}
			for _, step := range d.Steps {
				if step.Reason == stale {
					t.Fatalf("stale blocker reached a step: %+v", d.Steps)
				}
			}
			if d.Steps[2].State != StateSucceeded || !(strings.TrimSpace(readFile(t, filepath.Join(folder, "pushed"))) != "") {
				t.Fatalf("push step did not run: %+v", d.Steps[2])
			}
		})
	}
}

// Requirement: EndedBlocked is true only for a failed run whose error is a
// declared blocker. Threat: error text alone (left by an earlier run) marking
// a successful or cancelled end blocked. Owner: Result.EndedBlocked; a pure
// function test is the narrowest layer for its truth table.
func TestResultEndedBlocked(t *testing.T) {
	for _, tc := range []struct {
		status, err string
		want        bool
	}{
		{StatusFailed, "blocked: approval required", true},
		{StatusSucceeded, "blocked: approval required", false},
		{StatusCancelled, "blocked: approval required", false},
		{StatusFailed, "provider error", false},
		{StatusFailed, "", false},
	} {
		if got := (Result{Status: tc.status, Error: tc.err}).EndedBlocked(); got != tc.want {
			t.Fatalf("EndedBlocked(%s, %q) = %v, want %v", tc.status, tc.err, got, tc.want)
		}
	}
}
