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

// Requirement: a delivery already at a job's head covers that head (another
// end there creates no delivery) only when it is a real outcome. A delivery
// stored blocked although its own job end did not end blocked (Result status
// succeeded with an earlier run's "blocked: " text, as recorded before
// EndedBlocked required a failed status) covers nothing: the next end at that
// head creates a fresh pending delivery that runs its steps at the same round
// (no round consumed). A genuinely blocked delivery (status failed with a
// "blocked: " error) still covers its head.
// Threat (socials machine, 2026-10-30): a round-1 delivery created blocked by
// the stale-error bug kept covering its head, so the job's next end there was
// skipped as duplicate_head and the fixed work could never be re-reviewed
// without a new commit.
// Owners: Delivery.CoversHead in Store.CreateDelivery and Runner.consume
// (skippedDuplicateHead) over the real store, runner and signal feed; the
// pre-fix delivery is written through the store's own put, and only the
// reviewer job and the job's session are stubbed. Layer: the consumer over
// the real store is where job ends become (or skip) deliveries.
func TestStaleBlockedDeliveryDoesNotCoverItsHead(t *testing.T) {
	const stale = "blocked: Narration can't be generated"
	for _, tc := range []struct {
		name, status, err string
		covers            bool
	}{
		{"stale", StatusSucceeded, stale, false},
		{"genuine", StatusFailed, "blocked: approval required", true},
	} {
		t.Run(tc.name, func(t *testing.T) {
			h := newHarness(t)
			reopener := &sessionReopener{}
			h.withReopener(reopener)
			folder := t.TempDir()
			writeScript(t, folder, "push.sh", "echo \"$SWARM_HEAD\" >> \"$PWD/pushed\"\n")
			h.agents.outcomes = []AgentOutcome{{Verdict: VerdictBlock, Findings: "1. high a.go:1 narration missing"}, {Verdict: VerdictOK}}
			h.process()
			a := reviewLoop(folder, 2)
			a.When = WhenSucceeded
			a = h.automation(a)

			result := workerResult(folder, "run-1")
			h.results.results["run-1"] = result
			h.jobEnd("run-1", "", "succeeded", "direct")
			h.process() // round 0: review blocks, reopen sends followup-1
			if len(reopener.sent) != 1 {
				t.Fatalf("round 0 reopens = %d", len(reopener.sent))
			}

			// followup-1's delivery at head c, stored blocked with every step
			// skipped (as the pre-fix daemon recorded it).
			head := strings.Repeat("c", 40)
			end := *result
			git := *result.Git
			git.Head = head
			end.Git, end.Followup, end.Status, end.Error = &git, "followup-1", tc.status, tc.err
			blocked, created, err := h.store.CreateDelivery(a, end)
			if err != nil || !created || blocked.Round != 1 {
				t.Fatalf("seed delivery = %+v created %v err %v", blocked, created, err)
			}
			blocked.State, blocked.BlockedReason = StateBlocked, tc.err
			for i := range blocked.Steps {
				blocked.Steps[i].State, blocked.Steps[i].Reason = StateSkipped, tc.err
			}
			h.store.mu.Lock()
			err = h.store.putDeliveryLocked(blocked)
			h.store.mu.Unlock()
			if err != nil {
				t.Fatal(err)
			}

			// The job ends again at the same head, succeeded, its result
			// still carrying the stale text.
			again := *result
			again.Git, again.Error = &git, stale
			h.results.results["run-1"] = &again
			h.jobEnd("run-1", "person-2", "succeeded", "direct")
			h.process()

			list := h.deliveries(result.JobID)
			skipped := h.signalsOf("automation.delivery.skipped")
			if tc.covers {
				if len(list) != 2 || len(h.agents.started) != 1 {
					t.Fatalf("genuine blocker no longer covers its head: %d deliveries, %d reviews", len(list), len(h.agents.started))
				}
				if len(skipped) != 1 || skipped[0].Refs["delivery_id"] != blocked.ID || skipped[0].Attrs["reason"] != ReasonDuplicateHead ||
					skipped[0].Attrs["head"] != head || skipped[0].Attrs["followup"] != "person-2" {
					t.Fatalf("skipped signals = %+v", skipped)
				}
				for _, d := range list {
					if d.ID == blocked.ID && (d.State != StateBlocked || d.BlockedReason != tc.err) {
						t.Fatalf("genuine blocked delivery changed: %+v", d)
					}
				}
				return
			}
			if len(skipped) != 0 {
				t.Fatalf("stale blocked delivery covered its head: %+v", skipped)
			}
			var d Delivery
			for _, item := range list {
				if item.Followup == "person-2" {
					d = item
				}
				if item.ID == blocked.ID && item.State != StateBlocked {
					t.Fatalf("stale delivery rewritten: %+v", item)
				}
			}
			if len(list) != 3 || d.ID == "" {
				t.Fatalf("no fresh delivery at the stale head: %+v", list)
			}
			if d.Round != 1 || d.TriggerHead() != head {
				t.Fatalf("fresh delivery round %d head %s, want round 1 at %s", d.Round, d.TriggerHead(), head)
			}
			if d.State != StateSucceeded || d.BlockedReason != "" || len(reopener.sent) != 1 {
				t.Fatalf("fresh delivery = state %s reason %q, reopens %d", d.State, d.BlockedReason, len(reopener.sent))
			}
			if len(h.agents.started) != 2 || h.agents.started[1].Head != head || d.Steps[0].State != StateSucceeded {
				t.Fatalf("head not re-reviewed: %+v", d)
			}
			if d.Steps[2].State != StateSucceeded || strings.TrimSpace(readFile(t, filepath.Join(folder, "pushed"))) != head {
				t.Fatalf("push step did not run at the head: %+v", d.Steps[2])
			}
		})
	}
}

// Requirement: a delivery the runner blocked mid-loop still covers its head:
// another end of the job at that head creates no delivery, starts no
// reviewer job and records one duplicate_head skip. This holds for rounds
// exhausted (reason rounds_exhausted: step fix) and for an unreadable review
// (reason review unreadable: <job>, which waits for a manual re-run), even
// when the job end carries an earlier run's "blocked: " text on a succeeded
// status.
// Threat: CoversHead treating every blocked delivery with an ordinary job end
// as the stale-error shape, so each end at an exhausted or unreadable head
// starts another paid review that blocks again with a new warning.
// Owners: Delivery.CoversHead in Store.CreateDelivery, Runner.runReopen
// (which sets these blocked reasons) and Runner.consume over the real store,
// runner and signal feed; only the reviewer job and the job's session are
// stubbed. Layer: the deliveries must be blocked by the real runner, not
// seeded, to prove the shape CoversHead sees.
func TestRunnerBlockedDeliveryStillCoversItsHead(t *testing.T) {
	const stale = "blocked: Narration can't be generated"
	for _, tc := range []struct {
		name     string
		outcomes []AgentOutcome
		reopens  int
		reason   func(Delivery) string
	}{
		{"rounds_exhausted",
			[]AgentOutcome{{Verdict: VerdictBlock, Findings: "1. high a.go:1 x"}, {Verdict: VerdictBlock, Findings: "1. high a.go:1 x"}},
			1, func(Delivery) string { return RoundsExhaustedReason("fix") }},
		{"review_unreadable",
			[]AgentOutcome{{Verdict: VerdictBlock, ReviewUnreadable: true}},
			0, func(d Delivery) string {
				reviewer, _ := d.Steps[0].Outcome["job_id"].(string)
				return ReasonReviewUnreadable + ": " + reviewer
			}},
	} {
		t.Run(tc.name, func(t *testing.T) {
			h := newHarness(t)
			reopener := &sessionReopener{}
			h.withReopener(reopener)
			folder := t.TempDir()
			writeScript(t, folder, "push.sh", "echo pushed\n")
			h.agents.outcomes = tc.outcomes
			h.process()
			h.automation(reviewLoop(folder, 1))
			result := workerResult(folder, "run-1")
			result.Error = stale
			h.results.results["run-1"] = result
			h.jobEnd("run-1", "", "succeeded", "direct")
			h.process()
			if tc.reopens == 1 {
				result.Git.Head = strings.Repeat("c", 40)
				h.jobEnd("run-1", "followup-1", "succeeded", "direct") // round 1: rounds run out
				h.process()
			}
			head := result.Git.Head
			list := h.deliveries(result.JobID)
			blocked := list[len(list)-1]
			for _, d := range list {
				if d.CreatedAt > blocked.CreatedAt {
					blocked = d
				}
			}
			if blocked.State != StateBlocked || blocked.BlockedReason != tc.reason(blocked) || blocked.TriggerHead() != head || len(reopener.sent) != tc.reopens {
				t.Fatalf("runner did not block at %s: state %s reason %q, reopens %d", head, blocked.State, blocked.BlockedReason, len(reopener.sent))
			}
			deliveries, reviews := len(list), len(h.agents.started)

			// A person's follow-up ends at the same head without a commit.
			h.jobEnd("run-1", "person-2", "succeeded", "direct")
			h.process()

			after := h.deliveries(result.JobID)
			if len(after) != deliveries || len(h.agents.started) != reviews || len(reopener.sent) != tc.reopens {
				t.Fatalf("blocked head re-delivered: deliveries %d->%d, reviews %d->%d, reopens %d", deliveries, len(after), reviews, len(h.agents.started), len(reopener.sent))
			}
			skipped := h.signalsOf("automation.delivery.skipped")
			if len(skipped) != 1 || skipped[0].Refs["delivery_id"] != blocked.ID || skipped[0].Attrs["reason"] != ReasonDuplicateHead ||
				skipped[0].Attrs["head"] != head || skipped[0].Attrs["followup"] != "person-2" {
				t.Fatalf("skipped signals = %+v", skipped)
			}
			for _, d := range after {
				if d.ID == blocked.ID && (d.State != StateBlocked || d.BlockedReason != blocked.BlockedReason) {
					t.Fatalf("blocked delivery changed: %+v", d)
				}
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
