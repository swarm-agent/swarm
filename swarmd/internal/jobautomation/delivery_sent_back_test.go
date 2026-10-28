package jobautomation

import (
	"os"
	"path/filepath"
	"strings"
	"testing"

	pebblestore "swarm/packages/swarmd/internal/store/pebble"
)

// sendBackFlow is review -> fix (reopen on BLOCK) -> push (run on OK,
// allowed to fail) -> rebase (reopen when the push failed), the shape of a
// coder workflow whose reviewer or push can send the work back.
func sendBackFlow(folder string, maxRounds int) Automation {
	return Automation{Name: "ship", Scope: Scope{Folder: folder, Worker: "coder-1"}, Enabled: true, Steps: []Step{
		{ID: "review", Type: StepAgent, Worker: "reviewer", Brief: "Review this job's head."},
		{ID: "fix", Type: StepReopen, From: "review", MaxRounds: maxRounds, If: []Condition{{Step: "review", Verdict: VerdictBlock}}},
		{ID: "push", Type: StepRun, Command: "push.sh", ContinueOnFailure: true, If: []Condition{{Step: "review", Verdict: VerdictOK}}},
		{ID: "rebase", Type: StepReopen, Message: "The push was rejected: {{steps.push.output}}. Rebase onto the target.", MaxRounds: maxRounds,
			If: []Condition{{Step: "push", Status: StateFailed}}},
	}}
}

// finishedSignal is the newest automation.delivery.finished signal for the
// delivery.
func (h *harness) finishedSignal(deliveryID string) pebblestore.Signal {
	h.t.Helper()
	var out pebblestore.Signal
	for _, s := range h.signalsOf("automation.delivery.finished") {
		if s.Refs["delivery_id"] == deliveryID {
			out = s
		}
	}
	if out.Kind == "" {
		h.t.Fatalf("no automation.delivery.finished signal for %s", deliveryID)
	}
	return out
}

// Requirement: a workflow delivery's final state says what happened to the
// work. A delivery whose reopen step sent the work back (a review's BLOCK
// findings, or a failed push sent back to rebase) ends sent_back with the
// reopen step, the round it started and why, never succeeded; the
// follow-up's end starts a fresh delivery. A delivery that runs its last step
// (review OK, push OK) still ends succeeded with no sent_back record.
// Threat: a delivery that sent the work back and pushed nothing reported as
// succeeded, so a person or an outside AI reading deliveries, history or
// the task's progress concludes the work went through; or a failed push
// whose rebase was requested reported only as a plain failure.
// Owners: Runner.runDelivery's final state (sentBack, sentBackReason),
// Runner.finish's signal, Runner.runReopen/runScript over the real Pebble
// store, signal feed and host executor; the reviewer and the session that
// accepts the follow-up are the stubbed daemon boundary. The runner layer is
// the narrowest one that decides a delivery's final state.
func TestDeliveryThatSendsTheWorkBackEndsSentBackNotSucceeded(t *testing.T) {
	h := newHarness(t)
	reopener := &sessionReopener{}
	h.withReopener(reopener)
	folder := t.TempDir()
	// The push is rejected until the work is rebased (the marker file).
	writeScript(t, folder, "push.sh", "if [ -f \"$PWD/rebased\" ]; then echo pushed; else echo 'rejected: non-fast-forward'; exit 1; fi\n")
	h.agents.outcomes = []AgentOutcome{
		{Verdict: VerdictBlock, Findings: "1. high api/upload.go:40 the limit is never checked"},
		{Verdict: VerdictOK},
		{Verdict: VerdictOK},
	}
	h.process() // the consumer's first pass starts the feed cursor
	h.automation(sendBackFlow(folder, 3))
	const jobID = "worker/coder-1/run-1"
	byFollowup := func(followup string) Delivery {
		t.Helper()
		for _, d := range h.deliveries(jobID) {
			if d.Followup == followup {
				return d
			}
		}
		t.Fatalf("no delivery for follow-up %q", followup)
		return Delivery{}
	}

	// Round 0: the review blocks, the work is sent back; nothing is pushed.
	h.results.results["run-1"] = workerResult(folder, "run-1")
	h.jobEnd("run-1", "", "succeeded", "direct")
	h.process()
	first := byFollowup("")
	if first.State != StateSentBack || first.State == StateSucceeded {
		t.Fatalf("review BLOCK delivery state = %s, want %s; steps %+v", first.State, StateSentBack, first.Steps)
	}
	if first.SentBack == nil || first.SentBack.Step != "fix" || first.SentBack.Round != 1 || first.SentBack.Reason != "review verdict block" {
		t.Fatalf("review BLOCK sent_back = %+v, want fix round 1 review verdict block", first.SentBack)
	}
	if first.Steps[2].State != StateSkipped || len(h.host.ran) != 0 || len(reopener.sent) != 1 {
		t.Fatalf("after BLOCK: push %s, runs %d, follow-ups %d; want skipped, 0, 1", first.Steps[2].State, len(h.host.ran), len(reopener.sent))
	}
	sig := h.finishedSignal(first.ID)
	if sig.Attrs["state"] != StateSentBack || sig.Attrs["sent_back_step"] != "fix" || sig.Attrs["round"] != "1" || sig.Severity != pebblestore.SignalSeverityInfo {
		t.Fatalf("finished signal for BLOCK = %+v", sig)
	}

	// Round 1: review OK, the push is rejected and the rebase reopen sends
	// the work back again; the delivery is sent back for rebase, not
	// succeeded and not a bare failure.
	h.results.results["run-1"].Git.Head = strings.Repeat("c", 40)
	h.jobEnd("run-1", "followup-1", "succeeded", "direct")
	h.process()
	second := byFollowup("followup-1")
	if second.State != StateSentBack {
		t.Fatalf("push failed + rebase delivery state = %s, want %s; steps %+v", second.State, StateSentBack, second.Steps)
	}
	if second.Steps[2].State != StateFailed || second.Steps[3].State != StateSucceeded {
		t.Fatalf("push %s rebase %s, want failed then succeeded", second.Steps[2].State, second.Steps[3].State)
	}
	sb := second.SentBack
	if sb == nil || sb.Step != "rebase" || sb.Round != 2 || !strings.HasPrefix(sb.Reason, "push failed: exited with code 1") || !strings.Contains(sb.Reason, "non-fast-forward") {
		t.Fatalf("push failed sent_back = %+v, want rebase round 2 with the push's failure", sb)
	}
	if len(reopener.sent) != 2 || !strings.Contains(reopener.sent[1].Message, "rejected: non-fast-forward") {
		t.Fatalf("rebase follow-up = %+v", reopener.sent)
	}

	// Round 2: review OK and the rebased work pushes; the delivery ran to
	// its last step and is succeeded with no sent_back record.
	if err := os.WriteFile(filepath.Join(folder, "rebased"), []byte("yes\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	h.results.results["run-1"].Git.Head = strings.Repeat("d", 40)
	h.jobEnd("run-1", "followup-2", "succeeded", "direct")
	h.process()
	third := byFollowup("followup-2")
	if third.State != StateSucceeded || third.SentBack != nil || third.Steps[2].State != StateSucceeded || third.Steps[3].State != StateSkipped {
		t.Fatalf("review OK + push OK delivery = %s sent_back %+v steps %+v; want succeeded", third.State, third.SentBack, third.Steps)
	}
	if len(reopener.sent) != 2 {
		t.Fatalf("a succeeded delivery sent a follow-up: %+v", reopener.sent)
	}
	if sig := h.finishedSignal(third.ID); sig.Attrs["state"] != StateSucceeded || sig.Attrs["sent_back_step"] != "" {
		t.Fatalf("finished signal for success = %+v", sig)
	}

	// The record survives a restart as written (round read back from JSON).
	h.restart()
	if again := byFollowup("followup-1"); again.State != StateSentBack || again.SentBack == nil || again.SentBack.Round != 2 || again.SentBack.Step != "rebase" {
		t.Fatalf("after restart: %s %+v", again.State, again.SentBack)
	}
}

// Requirement: when a reopen step has no rounds left the delivery still ends
// blocked (rounds_exhausted), never sent_back: nothing was sent, so nothing
// is on its way back.
// Threat: a delivery that sent nothing reported as sent back, hiding that a
// person must act.
// Owners: Runner.runReopen/sendReopen (rounds) and the delivery's final
// state over the real store; the reviewer and session are stubbed.
func TestDeliveryWithRoundsExhaustedEndsBlockedNotSentBack(t *testing.T) {
	h := newHarness(t)
	reopener := &sessionReopener{}
	h.withReopener(reopener)
	folder := t.TempDir()
	writeScript(t, folder, "push.sh", "echo pushed\n")
	h.agents.outcomes = []AgentOutcome{
		{Verdict: VerdictBlock, Findings: "1. high x.go:3 the limit is not checked"},
		{Verdict: VerdictBlock, Findings: "1. high x.go:3 still not checked"},
	}
	h.process()
	h.automation(sendBackFlow(folder, 1))
	h.results.results["run-1"] = workerResult(folder, "run-1")
	h.jobEnd("run-1", "", "succeeded", "direct")
	h.process()
	h.results.results["run-1"].Git.Head = strings.Repeat("c", 40)
	h.jobEnd("run-1", "followup-1", "succeeded", "direct")
	h.process()

	var first, second Delivery
	for _, d := range h.deliveries("worker/coder-1/run-1") {
		switch d.Followup {
		case "":
			first = d
		case "followup-1":
			second = d
		}
	}
	if first.State != StateSentBack || first.SentBack == nil || first.SentBack.Round != 1 {
		t.Fatalf("first delivery = %s %+v, want sent_back round 1", first.State, first.SentBack)
	}
	if second.State != StateBlocked || second.BlockedReason != ReasonRoundsExhausted || second.SentBack != nil {
		t.Fatalf("exhausted delivery = %s reason %q sent_back %+v, want blocked rounds_exhausted", second.State, second.BlockedReason, second.SentBack)
	}
	if len(reopener.sent) != 1 || len(h.host.ran) != 0 {
		t.Fatalf("follow-ups %d runs %d, want 1 and 0", len(reopener.sent), len(h.host.ran))
	}
}
