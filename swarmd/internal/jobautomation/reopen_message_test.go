package jobautomation

import (
	"strings"
	"testing"
)

const rebaseMessage = "Your work passed review, but it could not be pushed: dev moved. Rebase onto dev, resolve the conflicts, run the checks and commit. The push said: {{steps.push.output}}"

// rebaseLoop is the forge review loop: send findings back on block, push on
// ok (continuing on failure), and send the job back to rebase when an OK
// review's push failed.
func rebaseLoop(folder string, maxRounds int) Automation {
	return Automation{Name: "review loop", Scope: Scope{Folder: folder, Worker: "coder-1"}, Enabled: true, Steps: []Step{
		{ID: "review", Type: StepAgent, Worker: "reviewer", Brief: "Review this job's head."},
		{ID: "send-back", Type: StepReopen, From: "review", Message: "The reviewer blocked your work.", MaxRounds: maxRounds, If: []Condition{{Step: "review", Verdict: VerdictBlock}}},
		{ID: "push", Type: StepRun, Command: "push.sh", ContinueOnFailure: true, If: []Condition{{Step: "review", Verdict: VerdictOK}}},
		{ID: "rebase", Type: StepReopen, From: "review", Message: rebaseMessage, MaxRounds: maxRounds,
			If: []Condition{{Step: "review", Verdict: VerdictOK}, {Step: "push", Status: StateFailed}}},
	}}
}

// Requirement: a reopen step whose from step recorded no findings (an OK
// review) still sends when it has a message: the follow-up is the message
// alone, with {{steps.push.output}} replaced by the push step's recorded
// output line, then the closing line, into the job's own session. It counts
// as a round, and max_rounds still bounds it.
// Threat (forge run): a job that passed review but whose push failed was
// never sent back to rebase ("recorded no findings to send"), or such a loop
// that never ends.
// Owners: NormalizeSteps (normalizeReopen), Runner.runReopen/reopenMessage,
// Runner.sendReopen and Store.ReopenCount over the real store, signal feed
// and host executor; only the reviewer's outcome (the model) and the push
// script are stubs.
func TestOKReviewWithFailedPushSendsTheRebaseMessageAndCountsARound(t *testing.T) {
	h := newHarness(t)
	reopener := &sessionReopener{}
	h.withReopener(reopener)
	folder := t.TempDir()
	writeScript(t, folder, "push.sh", "echo 'pushing to dev'\necho '! [rejected] dev -> dev (fetch first)'\nexit 1\n")
	h.agents.outcomes = []AgentOutcome{{Verdict: VerdictOK}, {Verdict: VerdictOK}}
	h.process()
	saved := h.automation(rebaseLoop(folder, 1))
	h.results.results["run-1"] = workerResult(folder, "run-1")
	h.jobEnd("run-1", "", "succeeded", "direct")
	h.process()

	first := h.deliveries("worker/coder-1/run-1")[0]
	if first.Steps[1].State != StateSkipped {
		t.Fatalf("send-back ran after an ok review: %+v", first.Steps[1])
	}
	if push := first.Steps[2]; push.State != StateFailed || push.Output != "! [rejected] dev -> dev (fetch first)" {
		t.Fatalf("push = %+v", push)
	}
	rebase := first.Steps[3]
	if rebase.State != StateSucceeded || rebase.Outcome["followup"] != "followup-1" {
		t.Fatalf("rebase = %+v", rebase)
	}
	if len(reopener.sent) != 1 {
		t.Fatalf("follow-ups sent = %d, want 1", len(reopener.sent))
	}
	want := "Your work passed review, but it could not be pushed: dev moved. Rebase onto dev, resolve the conflicts, run the checks and commit. The push said: ! [rejected] dev -> dev (fetch first)\n\n" + ReopenClosing
	if sent := reopener.sent[0]; sent.JobID != "worker/coder-1/run-1" || sent.Message != want {
		t.Fatalf("follow-up = %+v\nwant message %q", sent, want)
	}
	if n, err := h.store.ReopenCount(testAccount, "worker/coder-1/run-1", saved.ID); err != nil || n != 1 {
		t.Fatalf("rounds used = %d %v, want 1", n, err)
	}

	// The follow-up's end is round 1: the review is ok again and the push
	// fails again, but max_rounds 1 is used up: nothing is sent and the
	// delivery ends blocked.
	h.results.results["run-1"].Git.Head = strings.Repeat("c", 40)
	h.jobEnd("run-1", "followup-1", "succeeded", "direct")
	h.process()
	var second Delivery
	for _, d := range h.deliveries("worker/coder-1/run-1") {
		if d.Followup == "followup-1" {
			second = d
		}
	}
	if second.Round != 1 || second.State != StateBlocked || second.BlockedReason != RoundsExhaustedReason("rebase") {
		t.Fatalf("second delivery = round %d state %s reason %q", second.Round, second.State, second.BlockedReason)
	}
	if r := second.Steps[3]; r.State != StateFailed || r.Reason != RoundsExhaustedReason("rebase") || r.Outcome["followup"] != nil {
		t.Fatalf("rebase past max_rounds = %+v", r)
	}
	if len(reopener.sent) != 1 {
		t.Fatalf("max_rounds 1 sent %d follow-ups", len(reopener.sent))
	}
}

// Requirement: a reopen step with neither findings from its from step nor a
// message still sends nothing and fails, saying why; one with a message and
// no from sends the message alone.
// Threat: an empty follow-up sent into the coder's session, or a reopen
// step that silently does nothing.
// Owners: Runner.runReopen over the real store and host executor.
func TestReopenWithoutFindingsNeedsAMessage(t *testing.T) {
	h := newHarness(t)
	reopener := &sessionReopener{}
	h.withReopener(reopener)
	folder := t.TempDir()
	writeScript(t, folder, "push.sh", "echo 'rejected'\nexit 1\n")
	h.agents.outcomes = []AgentOutcome{{Verdict: VerdictOK}}
	h.process()
	a := rebaseLoop(folder, 2)
	a.Steps[3].Message = ""
	h.automation(a)
	h.results.results["run-1"] = workerResult(folder, "run-1")
	h.jobEnd("run-1", "", "succeeded", "direct")
	h.process()
	d := h.deliveries("worker/coder-1/run-1")[0]
	r := d.Steps[3]
	if r.State != StateFailed || !strings.Contains(r.Reason, "recorded no findings to send") || !strings.Contains(r.Reason, "no message") || r.Outcome["followup"] != nil {
		t.Fatalf("reopen without findings or message = %+v", r)
	}
	if len(reopener.sent) != 0 {
		t.Fatalf("sent %d follow-ups", len(reopener.sent))
	}

	// No from: the message alone, its placeholder replaced.
	other := t.TempDir()
	writeScript(t, other, "push.sh", "echo 'rejected again'\nexit 1\n")
	h.automation(Automation{Name: "push only", Scope: Scope{Folder: other, Worker: "coder-1"}, Enabled: true, Steps: []Step{
		{ID: "push", Type: StepRun, Command: "push.sh", ContinueOnFailure: true},
		{ID: "rebase", Type: StepReopen, Message: "Rebase onto dev: {{steps.push.output}}", If: []Condition{{Step: "push", Status: StateFailed}}},
	}})
	h.results.results["run-2"] = workerResult(other, "run-2")
	h.jobEnd("run-2", "", "succeeded", "direct")
	h.process()
	if len(reopener.sent) != 1 || reopener.sent[0].Message != "Rebase onto dev: rejected again\n\n"+ReopenClosing {
		t.Fatalf("message-only follow-ups = %+v", reopener.sent)
	}
}

// Requirement: from is optional only when a message is set; a message's
// {{steps.<id>.output}} must name an earlier step; any other text in the
// message is sent as written.
// Owner: NormalizeSteps (normalizeReopen).
func TestReopenMessageIsValidated(t *testing.T) {
	agent := Step{ID: "review", Type: StepAgent, Worker: "reviewer", Brief: "b"}
	run := Step{ID: "push", Type: StepRun, Command: "push.sh"}
	for name, steps := range map[string][]Step{
		"neither from nor message": {agent, {ID: "fix", Type: StepReopen}},
		"blank message, no from":   {agent, {ID: "fix", Type: StepReopen, Message: "   "}},
		"placeholder later step":   {agent, {ID: "fix", Type: StepReopen, Message: "x {{steps.push.output}}"}, run},
		"placeholder unknown step": {run, {ID: "fix", Type: StepReopen, Message: "x {{steps.nope.output}}"}},
		"placeholder itself":       {run, {ID: "fix", Type: StepReopen, Message: "x {{steps.fix.output}}"}},
	} {
		if err := NormalizeSteps(steps); err == nil {
			t.Fatalf("%s accepted", name)
		}
	}
	steps := []Step{run, {ID: "fix", Type: StepReopen, Message: "rebase: {{steps.push.output}} {{other}}"}}
	if err := NormalizeSteps(steps); err != nil || steps[1].From != "" || steps[1].MaxRounds != DefaultMaxRounds {
		t.Fatalf("message-only reopen = %+v %v", steps[1], err)
	}
	d := Delivery{Steps: []StepRecord{{ID: "push", Output: "rejected {{steps.push.output}}"}, {ID: "fix"}}}
	if got := reopenMessage(d, "a {{steps.push.output}} b {{steps.fix.output}} {{other}}"); got != "a rejected {{steps.push.output}} b (no output) {{other}}" {
		t.Fatalf("reopenMessage = %q", got)
	}
}
