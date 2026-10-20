package jobautomation

import (
	"context"
	"strconv"
	"strings"
	"testing"
)

// sessionReopener stands in for the job's own V3 session (the host boundary
// swarm_message_job writes to): it records each follow-up it accepted and
// names it as the session does, by its run id. refuse makes it answer as the
// daemon does for a cancelled or running job.
type sessionReopener struct {
	sent   []ReopenRequest
	refuse string
}

func (s *sessionReopener) Reopen(_ context.Context, req ReopenRequest) (string, error) {
	if s.refuse != "" {
		return "", &RefusedError{Reason: s.refuse}
	}
	for i, prev := range s.sent { // the session replays a repeated request id
		if prev.RequestID == req.RequestID {
			return "followup-" + strconv.Itoa(i+1), nil
		}
	}
	s.sent = append(s.sent, req)
	return "followup-" + strconv.Itoa(len(s.sent)), nil
}

func reviewLoop(folder string, maxRounds int) Automation {
	return Automation{Name: "review loop", Scope: Scope{Folder: folder, Worker: "coder-1"}, Enabled: true, Steps: []Step{
		{ID: "review", Type: StepAgent, Worker: "reviewer", Brief: "Review this job's head."},
		{ID: "fix", Type: StepReopen, From: "review", Message: "The reviewer blocked your work.", MaxRounds: maxRounds, If: []Condition{{Step: "review", Verdict: VerdictBlock}}},
		{ID: "push", Type: StepRun, Command: "push.sh", If: []Condition{{Step: "review", Verdict: VerdictOK}}},
	}}
}

func (h *harness) withReopener(reopener *sessionReopener) {
	h.runner.reopen = reopener
}

// Requirement: a reopen step sends the message, then the findings of the
// step named by from verbatim, then the fixed closing line, into the
// triggering job's own session (the job id, never a new job), succeeds once
// the follow-up is accepted, and records the follow-up; the follow-up's end
// creates a new delivery whose round is the previous round + 1. A restart
// never sends the same reopen twice.
// Threat: findings lost or paraphrased on the way to the coder, a fix made in
// a fresh job and worktree instead of the reviewed one, or a round count that
// never grows so a loop never ends.
// Owners: Runner.runReopen, Store.PutReopen/GetReopen and
// Store.CreateDelivery over the real store and signal feed; the session that
// accepts the follow-up is the stubbed host boundary.
func TestReopenSendsFindingsIntoTheJobsSessionAndTheFollowupEndIsTheNextRound(t *testing.T) {
	h := newHarness(t)
	reopener := &sessionReopener{}
	h.withReopener(reopener)
	folder := t.TempDir()
	writeScript(t, folder, "push.sh", "echo pushed\n")
	findings := "1. high api/x.go:12 the limit is not checked\n2. medium api/x_test.go:40 asserts only the status"
	h.agents.outcomes = []AgentOutcome{{Verdict: VerdictBlock, Findings: findings}, {Verdict: VerdictOK}}
	h.process()
	h.automation(reviewLoop(folder, 2))
	h.results.results["run-1"] = workerResult(folder, "run-1")
	h.jobEnd("run-1", "", "succeeded", "direct")
	h.process()

	first := h.deliveries("worker/coder-1/run-1")[0]
	if first.Round != 0 || first.State != StateSucceeded {
		t.Fatalf("first delivery = round %d state %s", first.Round, first.State)
	}
	fix := first.Steps[1]
	if fix.State != StateSucceeded || fix.Outcome["followup"] != "followup-1" {
		t.Fatalf("reopen step = %+v", fix)
	}
	if first.Steps[2].State != StateSkipped {
		t.Fatalf("push ran after a block: %s", first.Steps[2].State)
	}
	if len(reopener.sent) != 1 {
		t.Fatalf("follow-ups sent = %d", len(reopener.sent))
	}
	sent := reopener.sent[0]
	want := "The reviewer blocked your work.\n\n" + findings + "\n\n" + ReopenClosing
	if sent.JobID != "worker/coder-1/run-1" || sent.Message != want || sent.Account != testAccount || sent.RequestID == "" {
		t.Fatalf("follow-up = %+v\nwant message %q", sent, want)
	}

	// The same delivery processed again (a restart) does not send again.
	h.restart()
	h.withReopener(reopener)
	h.process()
	if len(reopener.sent) != 1 {
		t.Fatalf("a restart sent the reopen again: %d", len(reopener.sent))
	}

	// The follow-up's end is a new job end of the same job: round 1.
	h.results.results["run-1"].Git.Head = strings.Repeat("c", 40)
	h.jobEnd("run-1", "followup-1", "succeeded", "direct")
	h.process()
	list := h.deliveries("worker/coder-1/run-1")
	var second Delivery
	for _, d := range list {
		if d.Followup == "followup-1" {
			second = d
		}
	}
	if second.ID == "" || second.Round != 1 || second.Head != strings.Repeat("c", 40) {
		t.Fatalf("follow-up delivery = %+v", second)
	}
	if second.State != StateSucceeded || second.Steps[2].State != StateSucceeded {
		t.Fatalf("after ok the push should run: %s / %s", second.State, second.Steps[2].State)
	}
	if len(reopener.sent) != 1 {
		t.Fatalf("an ok verdict reopened the job: %d", len(reopener.sent))
	}
	// A follow-up the person sent by hand continues the job's round count:
	// rounds count per automation and job, across deliveries.
	h.results.results["run-1"].Git.Head = strings.Repeat("d", 40)
	h.jobEnd("run-1", "followup-by-hand", "succeeded", "direct")
	h.agents.outcomes = append(h.agents.outcomes, AgentOutcome{Verdict: VerdictOK})
	h.process()
	byHand := false
	for _, d := range h.deliveries("worker/coder-1/run-1") {
		if d.Followup == "followup-by-hand" {
			byHand = true
			if d.Round != 1 {
				t.Fatalf("a follow-up sent by hand got round %d, want 1", d.Round)
			}
		}
	}
	if !byHand {
		t.Fatal("a follow-up on a new head created no delivery")
	}
}

// Requirement: when a reopen step would exceed max_rounds it sends nothing,
// fails with reason rounds_exhausted, the delivery ends blocked and
// automation.delivery.blocked (warning) names the job, the last findings
// (shortened) and the next allowed action. An explicit re-run of the blocked
// delivery runs the loop again with a new round budget.
// Threat: an endless reviewer/coder loop burning tokens, or a stop that
// nobody sees.
// Owners: Runner.runReopen, Runner.runDelivery/finish and Store.Rerun.
func TestRoundsExhaustedBlocksTheDeliveryAndSignalsIt(t *testing.T) {
	h := newHarness(t)
	reopener := &sessionReopener{}
	h.withReopener(reopener)
	folder := t.TempDir()
	writeScript(t, folder, "push.sh", "echo pushed\n")
	long := "1. high a.go:1 " + strings.Repeat("x", 900)
	for i := 0; i < 4; i++ {
		h.agents.outcomes = append(h.agents.outcomes, AgentOutcome{Verdict: VerdictBlock, Findings: long})
	}
	h.process()
	h.automation(reviewLoop(folder, 1))
	h.results.results["run-1"] = workerResult(folder, "run-1")
	h.jobEnd("run-1", "", "succeeded", "direct")
	h.process()
	h.results.results["run-1"].Git.Head = strings.Repeat("c", 40)
	h.jobEnd("run-1", "followup-1", "succeeded", "direct") // round 1 blocks again
	h.process()

	var blocked Delivery
	for _, d := range h.deliveries("worker/coder-1/run-1") {
		if d.Followup == "followup-1" {
			blocked = d
		}
	}
	if blocked.Round != 1 || blocked.State != StateBlocked || blocked.BlockedReason != ReasonRoundsExhausted {
		t.Fatalf("delivery = round %d state %s reason %q", blocked.Round, blocked.State, blocked.BlockedReason)
	}
	fix := blocked.Steps[1]
	if fix.State != StateFailed || fix.Reason != ReasonRoundsExhausted || fix.Attempts != 1 {
		t.Fatalf("reopen step = %+v", fix)
	}
	if blocked.Steps[2].State != StateSkipped {
		t.Fatalf("push after blocked = %s", blocked.Steps[2].State)
	}
	if len(reopener.sent) != 1 {
		t.Fatalf("follow-ups sent = %d, want 1 (max_rounds 1)", len(reopener.sent))
	}
	sigs := h.signalsOf("automation.delivery.blocked")
	if len(sigs) != 1 {
		t.Fatalf("blocked signals = %d", len(sigs))
	}
	sig := sigs[0]
	if sig.Severity != "warning" || sig.Refs["job_id"] != "worker/coder-1/run-1" || sig.Refs["delivery_id"] != blocked.ID || sig.Attrs["reason"] != ReasonRoundsExhausted {
		t.Fatalf("blocked signal = %+v", sig)
	}
	if f := sig.Attrs["findings"]; len(f) > 500 || !strings.HasPrefix(f, "1. high a.go:1 ") {
		t.Fatalf("findings attr (%d bytes) = %q", len(f), f)
	}
	if next := sig.Attrs["next"]; !strings.Contains(next, "swarm_message_job") || !strings.Contains(next, "swarm_deliver_job automation=true") {
		t.Fatalf("next = %q", next)
	}
	if fin := h.signalsOf("automation.delivery.finished"); fin[len(fin)-1].Attrs["state"] != StateBlocked {
		t.Fatalf("finished = %+v", fin[len(fin)-1].Attrs)
	}

	// An explicit re-run (swarm_deliver_job automation=true) gets a new
	// round budget: the review runs again and, still blocked, reopens once.
	if _, ok, err := h.store.Rerun(testAccount, blocked.ID); err != nil || !ok {
		t.Fatal(ok, err)
	}
	h.process()
	again, _ := h.store.GetDelivery(testAccount, blocked.ID)
	if again.Steps[1].State != StateSucceeded || len(reopener.sent) != 2 || again.Reruns != 1 {
		t.Fatalf("re-run = %+v (sent %d)", again.Steps[1], len(reopener.sent))
	}
}

// Requirement: a reopen step never reopens a job that is cancelled or a job
// whose Reopener refuses it; it fails with the reason and is not retried.
// Without findings it sends nothing. (A job that is still running is not
// refused: the step waits for it; see reopen_wait_test.go.)
// Threat: reviving a job the person cancelled.
// Owner: Runner.runReopen (and the Reopener's refusal).
func TestReopenRefusesCancelledOrRunningJobs(t *testing.T) {
	h := newHarness(t)
	reopener := &sessionReopener{refuse: "reopen steps run only for the machine owner's automations"}
	h.withReopener(reopener)
	folder := t.TempDir()
	writeScript(t, folder, "push.sh", "echo pushed\n")
	h.agents.outcomes = []AgentOutcome{{Verdict: VerdictBlock, Findings: "1. a.go:1 x"}, {Verdict: VerdictBlock, Findings: "1. a.go:1 x"}, {Verdict: VerdictBlock}}
	h.process()
	a := reviewLoop(folder, 2)
	a.When, a.Retries = WhenAlways, 2
	h.automation(a)
	h.results.results["run-1"] = workerResult(folder, "run-1")
	h.jobEnd("run-1", "", "succeeded", "direct")
	h.process()
	d := h.deliveries("worker/coder-1/run-1")[0]
	if d.State != StateFailed || d.Steps[1].State != StateFailed || !strings.Contains(d.Steps[1].Reason, "machine owner") || d.Steps[1].Attempts != 1 {
		t.Fatalf("refused reopen = %s %+v", d.State, d.Steps[1])
	}

	reopener.refuse = ""
	h.results.results["run-2"] = workerResult(folder, "run-2")
	h.results.results["run-2"].Status = StatusCancelled
	h.jobEnd("run-2", "", "cancelled", "direct")
	h.process()
	d = h.deliveries("worker/coder-1/run-2")[0]
	if d.Steps[1].State != StateFailed || !strings.Contains(d.Steps[1].Reason, "cancelled") || len(reopener.sent) != 0 {
		t.Fatalf("cancelled job = %+v (sent %d)", d.Steps[1], len(reopener.sent))
	}

	h.results.results["run-3"] = workerResult(folder, "run-3")
	h.jobEnd("run-3", "", "succeeded", "direct")
	h.process()
	d = h.deliveries("worker/coder-1/run-3")[0]
	if d.Steps[1].State != StateFailed || !strings.Contains(d.Steps[1].Reason, "no findings") || len(reopener.sent) != 0 {
		t.Fatalf("no findings = %+v (sent %d)", d.Steps[1], len(reopener.sent))
	}
}

// Requirement: a reopen step names an earlier agent step in from and keeps
// max_rounds within 1..5 (default 2).
// Owner: NormalizeSteps (normalizeReopen).
func TestReopenStepIsValidated(t *testing.T) {
	agent := Step{ID: "review", Type: StepAgent, Worker: "reviewer", Brief: "b"}
	for name, steps := range map[string][]Step{
		"no from":       {agent, {ID: "fix", Type: StepReopen}},
		"from a run":    {{ID: "check", Type: StepRun, Command: "c.sh"}, {ID: "fix", Type: StepReopen, From: "check"}},
		"from later":    {{ID: "fix", Type: StepReopen, From: "review"}, agent},
		"too many":      {agent, {ID: "fix", Type: StepReopen, From: "review", MaxRounds: 6}},
		"with command":  {agent, {ID: "fix", Type: StepReopen, From: "review", Command: "x.sh"}},
		"agent w/ from": {{ID: "review", Type: StepAgent, Worker: "reviewer", Brief: "b", From: "x"}},
	} {
		if err := NormalizeSteps(steps); err == nil {
			t.Fatalf("%s accepted", name)
		}
	}
	steps := []Step{agent, {ID: "fix", Type: StepReopen, From: "review"}}
	if err := NormalizeSteps(steps); err != nil || steps[1].MaxRounds != DefaultMaxRounds {
		t.Fatalf("default max_rounds = %d %v", steps[1].MaxRounds, err)
	}
}

// Requirement: when the from step's review was unreadable (outcome
// review_unreadable, verdict block, no findings), a reopen step sends
// nothing, uses no round, fails with "review unreadable: <reviewer job>",
// and the delivery ends blocked with that reason; automation.delivery.blocked
// names it and tells the reader to open the reviewer's session. Findings on
// an unreadable outcome are dropped, never sent.
// Threat (forge run): the malformed reason sent to the coder as findings,
// the round counted and a second unreadable review exhausting the loop.
// Owners: Runner.runAgent, Runner.runReopen, Runner.runDelivery/finish over
// the real store and signal feed; the agent and session are stubbed hosts.
func TestUnreadableReviewBlocksWithoutReopening(t *testing.T) {
	h := newHarness(t)
	reopener := &sessionReopener{}
	h.withReopener(reopener)
	folder := t.TempDir()
	writeScript(t, folder, "push.sh", "echo pushed\n")
	h.agents.outcomes = []AgentOutcome{{Verdict: VerdictBlock, ReviewUnreadable: true, Findings: "missing or malformed review verdict"}}
	h.process()
	h.automation(reviewLoop(folder, 2))
	h.results.results["run-1"] = workerResult(folder, "run-1")
	h.jobEnd("run-1", "", "succeeded", "direct")
	h.process()

	d := h.deliveries("worker/coder-1/run-1")[0]
	review := d.Steps[0]
	if review.Outcome["verdict"] != VerdictBlock || review.Outcome["review_unreadable"] != true {
		t.Fatalf("review outcome = %+v", review.Outcome)
	}
	if _, ok := review.Outcome["findings"]; ok {
		t.Fatalf("an unreadable review kept findings: %+v", review.Outcome)
	}
	reviewer, _ := review.Outcome["job_id"].(string)
	want := ReasonReviewUnreadable + ": " + reviewer
	if reviewer == "" || d.State != StateBlocked || d.BlockedReason != want || d.Round != 0 {
		t.Fatalf("delivery = state %s reason %q round %d", d.State, d.BlockedReason, d.Round)
	}
	if fix := d.Steps[1]; fix.State != StateFailed || fix.Reason != want || fix.Attempts != 1 || fix.Outcome["followup"] != nil {
		t.Fatalf("reopen step = %+v", fix)
	}
	if d.Steps[2].State != StateSkipped {
		t.Fatalf("push after an unreadable review = %s", d.Steps[2].State)
	}
	if len(reopener.sent) != 0 {
		t.Fatalf("an unreadable review sent %d follow-ups", len(reopener.sent))
	}
	sigs := h.signalsOf("automation.delivery.blocked")
	if len(sigs) != 1 || sigs[0].Attrs["reason"] != want || !strings.Contains(sigs[0].Attrs["next"], "reviewer job's session") {
		t.Fatalf("blocked signals = %+v", sigs)
	}
}
