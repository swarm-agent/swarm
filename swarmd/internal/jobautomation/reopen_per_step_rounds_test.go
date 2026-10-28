package jobautomation

import (
	"strings"
	"testing"
)

// Requirement: max_rounds bounds each reopen step on its own ("how many times
// this loop may reopen the job"): rounds count per (automation, job, reopen
// step id), and the blocked reason names the step that ran out. The job's
// overall bound is the sum of the reopen steps' max_rounds.
// Threat (forge run, 2026-10-28): rounds counted per job across every reopen
// step, so a task sent back twice by review that then passed review and
// failed its push was blocked at once by the rebase step ("after 2 rounds"),
// although rebase had never run; a person had to send each rebase by hand.
// Owners: Runner.runReopen / sendReopen / roundsExhausted, Store.PutReopen,
// Store.ReopenRounds and Store.CreateDelivery over the real store, runner,
// signal feed and host executor; only the reviewer's verdicts (the model), the
// job's session and the push script are stubs.
// Layer: the runner over the real store is where job ends become deliveries
// and reopens are counted.
func TestRebaseRunsAfterTwoSendBacksAndCountsItsOwnRounds(t *testing.T) {
	h := newHarness(t)
	reopener := &sessionReopener{}
	h.withReopener(reopener)
	folder := t.TempDir()
	writeScript(t, folder, "push.sh", "echo '! [rejected] dev -> dev (fetch first)'\nexit 1\n")
	block := AgentOutcome{Verdict: VerdictBlock, Findings: "1. high a.go:1 the limit is not checked"}
	h.agents.outcomes = []AgentOutcome{block, block, {Verdict: VerdictOK}, {Verdict: VerdictOK}, {Verdict: VerdictOK}}
	h.process()
	saved := h.automation(rebaseLoop(folder, 2))
	result := workerResult(folder, "run-1")
	h.results.results["run-1"] = result
	const job = "worker/coder-1/run-1"
	end := func(followup, head string) Delivery {
		t.Helper()
		result.Git.Head = strings.Repeat(head, 40)
		h.jobEnd("run-1", followup, "succeeded", "direct")
		h.process()
		for _, d := range h.deliveries(job) {
			if d.Followup == followup {
				return d
			}
		}
		t.Fatalf("no delivery for end %q", followup)
		return Delivery{}
	}

	// Two review send-backs use send-back's two rounds.
	for i, c := range []struct{ followup, head, sent string }{{"", "b", "followup-1"}, {"followup-1", "c", "followup-2"}} {
		d := end(c.followup, c.head)
		if d.State != StateSentBack || d.SentBack == nil || d.SentBack.Step != "send-back" || d.Steps[1].Outcome["followup"] != c.sent {
			t.Fatalf("send-back %d = state %s sent_back %+v step %+v", i+1, d.State, d.SentBack, d.Steps[1])
		}
	}

	// The review is ok and the push fails: rebase has its own budget and runs.
	third := end("followup-2", "d")
	if third.State != StateSentBack || third.SentBack == nil || third.SentBack.Step != "rebase" || third.BlockedReason != "" {
		t.Fatalf("ok review with failed push = state %s reason %q sent_back %+v steps %+v", third.State, third.BlockedReason, third.SentBack, third.Steps)
	}
	if push := third.Steps[2]; push.State != StateFailed {
		t.Fatalf("push = %+v, want failed", push)
	}
	if rebase := third.Steps[3]; rebase.State != StateSucceeded || rebase.Outcome["followup"] != "followup-3" {
		t.Fatalf("rebase = %+v, want the follow-up sent", rebase)
	}
	if len(reopener.sent) != 3 || !strings.HasPrefix(reopener.sent[2].Message, "Your work passed review, but it could not be pushed") {
		t.Fatalf("follow-ups = %d (%+v), want the rebase message third", len(reopener.sent), reopener.sent)
	}
	rounds, err := h.store.ReopenRounds(testAccount, job, saved.ID)
	if err != nil || rounds.Total != 3 || rounds.Steps["send-back"] != 2 || rounds.Steps["rebase"] != 1 || rounds.Unattributed != 0 {
		t.Fatalf("rounds = %+v %v, want send-back 2, rebase 1", rounds, err)
	}

	// Rebase's second round is still its own; its third is refused and the
	// blocked reason names rebase.
	if d := end("followup-3", "e"); d.State != StateSentBack || d.SentBack == nil || d.SentBack.Step != "rebase" {
		t.Fatalf("second rebase = state %s reason %q sent_back %+v", d.State, d.BlockedReason, d.SentBack)
	}
	last := end("followup-4", "f")
	if last.State != StateBlocked || last.BlockedReason != RoundsExhaustedReason("rebase") || last.Steps[3].Outcome["followup"] != nil {
		t.Fatalf("third rebase = state %s reason %q rebase %+v", last.State, last.BlockedReason, last.Steps[3])
	}
	if !strings.Contains(last.Steps[3].Output, "2 of 2 reopens of step rebase") {
		t.Fatalf("rebase output = %q", last.Steps[3].Output)
	}
	if len(reopener.sent) != 4 {
		t.Fatalf("follow-ups in total = %d, want 4 (2 send-back + 2 rebase)", len(reopener.sent))
	}
	sigs := h.signalsOf("automation.delivery.blocked")
	if len(sigs) != 1 || sigs[0].Attrs["reason"] != RoundsExhaustedReason("rebase") || !strings.Contains(sigs[0].Summary, "rounds_exhausted: step rebase") {
		t.Fatalf("blocked signals = %+v", sigs)
	}
}

// Requirement: reopen records written before rounds counted per step carry no
// step; ReopenRounds attributes each to the reopen step of its delivery that
// recorded the follow-up, and one it cannot attribute counts against every
// step (never a silently larger budget). The job's overall bound (the sum of
// the reopen steps' max_rounds) still applies to the total.
// Threat: an upgrade that resets every in-flight job's rounds to zero, or a
// per-step budget that lets a job reopen without an overall limit.
// Owners: Store.ReopenRounds / legacyReopenStep and jobRoundBound over the
// real store. Layer: the store is where reopen records are read.
func TestReopenRoundsAttributeLegacyRecordsAndBoundTheJob(t *testing.T) {
	h := newHarness(t)
	folder := t.TempDir()
	saved := h.automation(rebaseLoop(folder, 2))
	result := workerResult(folder, "run-1")
	result.JobID = "worker/coder-1/run-1"
	d, created, err := h.store.CreateDelivery(saved, *result)
	if err != nil || !created {
		t.Fatalf("create delivery: %v %v", created, err)
	}
	d.Steps[3].State, d.Steps[3].Outcome = StateSucceeded, map[string]any{"followup": "followup-old"}
	if err := h.store.SaveDelivery(d); err != nil {
		t.Fatal(err)
	}
	for followup, rec := range map[string]ReopenRecord{
		"followup-old":     {DeliveryID: d.ID, AutomationID: saved.ID},           // legacy, attributable
		"followup-lost":    {DeliveryID: "jdel_missing", AutomationID: saved.ID}, // legacy, not attributable
		"followup-new":     {DeliveryID: d.ID, AutomationID: saved.ID, Step: "send-back"},
		"followup-another": {DeliveryID: d.ID, AutomationID: "jauto_other", Step: "send-back"},
	} {
		if err := h.store.PutReopen(testAccount, result.JobID, followup, rec); err != nil {
			t.Fatal(err)
		}
	}
	rounds, err := h.store.ReopenRounds(testAccount, result.JobID, saved.ID)
	if err != nil {
		t.Fatal(err)
	}
	if rounds.Total != 3 || rounds.Steps["rebase"] != 1 || rounds.Steps["send-back"] != 1 || rounds.Unattributed != 1 {
		t.Fatalf("rounds = %+v, want total 3, rebase 1, send-back 1, unattributed 1", rounds)
	}
	if rounds.Step("rebase") != 2 || rounds.Step("send-back") != 2 {
		t.Fatalf("an unattributed reopen must count against every step: %+v", rounds)
	}
	if n, err := h.store.ReopenCount(testAccount, result.JobID, saved.ID); err != nil || n != 3 {
		t.Fatalf("ReopenCount = %d %v, want 3", n, err)
	}
	if bound := jobRoundBound(saved.Steps, saved.Steps[1]); bound != 4 {
		t.Fatalf("job bound = %d, want 4 (2 + 2)", bound)
	}
}
