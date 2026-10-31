package jobautomation

import (
	"context"
	"strconv"
	"strings"
	"testing"
)

// runCheckingSession is the job's session (the stubbed host boundary) that
// also answers whether a follow-up it accepted is carried by a run, as the
// daemon's reopener does (FollowupRunChecker). noRun lists the follow-ups it
// accepts without starting a run.
type runCheckingSession struct {
	sessionReopener
	noRun   map[string]bool
	checked []string
}

func (s *runCheckingSession) FollowupRunStarted(_ context.Context, _, _, followup string) (bool, error) {
	s.checked = append(s.checked, followup)
	return !s.noRun[followup], nil
}

const rebaseNeededPush = "echo 'NOT PUSHED: rebase needed: dev moved and changed files this job also changed'; exit 1\n"

// endAt records the job's follow-up end at a new head (a rebase moves it).
func (h *harness) endAt(folder, followup string, n int) {
	h.t.Helper()
	result := workerResult(folder, "run-1")
	head := strings.Repeat(strconv.Itoa(n%10), 40)
	result.Git.Head, result.Git.Commits = head, []ResultCommit{{SHA: head, Subject: "round " + strconv.Itoa(n)}}
	h.results.results["run-1"] = result
	h.jobEnd("run-1", followup, "succeeded", "direct")
	h.process()
}

func (h *harness) deliveryFor(jobID, followup string) Delivery {
	h.t.Helper()
	for _, d := range h.deliveries(jobID) {
		if d.Followup == followup {
			return d
		}
	}
	h.t.Fatalf("no delivery for follow-up %q", followup)
	return Delivery{}
}

// assertNoSentBackWithoutRun fails when any delivery of the job ended
// sent_back with a follow-up the session did not accept with a run.
func assertNoSentBackWithoutRun(t *testing.T, h *harness, jobID string, session *runCheckingSession) {
	t.Helper()
	for _, d := range h.deliveries(jobID) {
		if d.State != StateSentBack {
			continue
		}
		var followup string
		for _, rec := range d.Steps {
			if f, _ := rec.Outcome["followup"].(string); rec.Type == StepReopen && f != "" {
				followup = f
			}
		}
		n, err := strconv.Atoi(strings.TrimPrefix(followup, "followup-"))
		if followup == "" || err != nil || n < 1 || n > len(session.sent) || session.noRun[followup] {
			t.Fatalf("delivery %s ended sent_back with follow-up %q that no run carries (sent %d)", d.ID, followup, len(session.sent))
		}
	}
}

// Requirement: a workflow reopen it records either starts a run on the job's
// session or ends the delivery blocked (the task's needs_you) with a reason
// naming the step, the round and why it did not start; never sent_back with
// no run. A reopen sent because the push needs a rebase (review passed, dev
// moved) has its own bound, RebaseMaxRounds, past the reopen step's
// max_rounds: reviewed work is not stranded by a busy dev, and when the
// rebase bound is used up the delivery blocks with rebase_rounds_exhausted,
// which is not a review rounds_exhausted the stall check would answer with
// a review round.
// Threat (seen live): review ok, push refused "rebase needed" again and
// again; the workflow recorded a rebase reopen (sent_back, round 3) and the
// task sat "running" with no run until the stall detector fired.
// Owners: Runner.runReopen, roundsExhausted, sendReopen (FollowupRunChecker),
// IsRebaseReopen, Store.ReopenRounds/PutReopen/CreateDelivery over the real
// Pebble store, signal feed and host executor (the push is a real script);
// the reviewer and the session that accepts follow-ups are the stubbed
// daemon boundary. The runner is the narrowest layer that decides whether a
// reopen is recorded, sent or blocked.
func TestRebaseReopenPastTheOldBoundStartsARunOrBlocksWithAReason(t *testing.T) {
	h := newHarness(t)
	session := &runCheckingSession{noRun: map[string]bool{}}
	h.runner.reopen = session
	folder := t.TempDir()
	writeScript(t, folder, "push.sh", rebaseNeededPush)
	h.agents.outcomes = []AgentOutcome{{Verdict: VerdictBlock, Findings: "1. high api/upload.go:40 the limit is never checked"}}
	for i := 0; i < 6; i++ {
		h.agents.outcomes = append(h.agents.outcomes, AgentOutcome{Verdict: VerdictOK})
	}
	h.process()
	// The live workflow's shape and the old bound: max_rounds 2 on both
	// reopen steps.
	saved := h.automation(sendBackFlow(folder, DefaultMaxRounds))
	const jobID = "worker/coder-1/run-1"

	// Round 1: the review blocks; the fix is sent.
	h.endAt(folder, "", 0)
	if d := h.deliveryFor(jobID, ""); d.State != StateSentBack || d.SentBack == nil || d.SentBack.Step != "fix" || d.SentBack.Round != 1 {
		t.Fatalf("review block delivery = %s %+v, want sent_back fix round 1", d.State, d.SentBack)
	}
	// Rounds 2-4: review ok, push refused for a rebase, the rebase reopen
	// is sent and a run carries it, past the old bound of 2.
	for n := 1; n <= RebaseMaxRounds; n++ {
		followup := "followup-" + strconv.Itoa(n)
		h.endAt(folder, followup, n)
		d := h.deliveryFor(jobID, followup)
		round := n + 1
		if d.State != StateSentBack || d.SentBack == nil || d.SentBack.Step != "rebase" || d.SentBack.Round != round {
			t.Fatalf("rebase round %d: delivery %s %+v (blocked %q), want sent_back rebase round %d", round, d.State, d.SentBack, d.BlockedReason, round)
		}
		if !IsRebaseReopen(d, "rebase") || len(session.sent) != round || !strings.Contains(session.sent[round-1].Message, "rebase needed") {
			t.Fatalf("rebase round %d: sent %d follow-ups, want %d carrying the push's refusal", round, len(session.sent), round)
		}
		if last := session.checked[len(session.checked)-1]; last != "followup-"+strconv.Itoa(round) {
			t.Fatalf("rebase round %d: run checked for %q, want the follow-up just sent", round, last)
		}
	}
	rounds, err := h.store.ReopenRounds(testAccount, jobID, saved.ID)
	if err != nil || rounds.Total != 1+RebaseMaxRounds || rounds.Rebase != RebaseMaxRounds || rounds.Step("fix") != 1 || rounds.Step("rebase") != 0 {
		t.Fatalf("rounds = %+v %v, want 1 review round and %d rebase rounds", rounds, err, RebaseMaxRounds)
	}
	// The rebase bound is used up: nothing is sent, the delivery blocks
	// with a reason naming the step, the round and why.
	followup := "followup-" + strconv.Itoa(RebaseMaxRounds+1)
	h.endAt(folder, followup, RebaseMaxRounds+1)
	d := h.deliveryFor(jobID, followup)
	want := ReasonRebaseRoundsExhausted + ": step rebase: round " + strconv.Itoa(RebaseMaxRounds+2) + " not started: 3 of 3 rebase reopens used"
	if d.State != StateBlocked || d.SentBack != nil || !strings.HasPrefix(d.BlockedReason, want) || !strings.Contains(d.BlockedReason, "rebase needed") {
		t.Fatalf("rebase bound used up: delivery %s %+v reason %q, want blocked %q…", d.State, d.SentBack, d.BlockedReason, want)
	}
	if strings.HasPrefix(d.BlockedReason, ReasonRoundsExhausted) {
		t.Fatalf("rebase exhaustion %q reads as a review rounds_exhausted", d.BlockedReason)
	}
	if len(session.sent) != 1+RebaseMaxRounds {
		t.Fatalf("follow-ups = %d after the rebase bound, want %d (nothing sent)", len(session.sent), 1+RebaseMaxRounds)
	}
	assertNoSentBackWithoutRun(t, h, jobID, session)
}

// Requirement: a follow-up the job's session accepted without starting a run
// is never recorded as sent back: the delivery blocks with
// reopen_not_started naming the step and the round, and no round is used.
// Threat: sent_back recorded on an accepted message no run carries, so the
// task looks busy with nothing running.
// Owners: Runner.sendReopen with a FollowupRunChecker over the real store.
func TestReopenAcceptedWithoutARunBlocksInsteadOfSentBack(t *testing.T) {
	h := newHarness(t)
	session := &runCheckingSession{noRun: map[string]bool{"followup-1": true}}
	h.runner.reopen = session
	folder := t.TempDir()
	writeScript(t, folder, "push.sh", rebaseNeededPush)
	h.agents.outcomes = []AgentOutcome{{Verdict: VerdictOK}}
	h.process()
	saved := h.automation(sendBackFlow(folder, DefaultMaxRounds))
	const jobID = "worker/coder-1/run-1"
	h.endAt(folder, "", 0)
	d := h.deliveryFor(jobID, "")
	want := ReasonReopenNotStarted + ": step rebase: round 1 not started"
	if d.State != StateBlocked || d.SentBack != nil || !strings.HasPrefix(d.BlockedReason, want) {
		t.Fatalf("delivery %s %+v reason %q, want blocked %q…", d.State, d.SentBack, d.BlockedReason, want)
	}
	if f, _ := d.Steps[3].Outcome["followup"].(string); f != "" {
		t.Fatalf("reopen recorded follow-up %q no run carries", f)
	}
	if rounds, err := h.store.ReopenRounds(testAccount, jobID, saved.ID); err != nil || rounds.Total != 0 {
		t.Fatalf("rounds = %+v %v, want none used", rounds, err)
	}
	assertNoSentBackWithoutRun(t, h, jobID, session)
}

// Requirement: rebase reopens use none of the review send-back rounds: after
// more rebase reopens than the fix step's max_rounds and the job's overall
// review bound, a review that blocks still gets its fix round.
// Threat: a busy dev uses up the review rounds, so the reviewer's findings
// are never sent and the reviewed work is blocked as review-exhausted.
// Owners: Runner.roundsExhausted, jobRoundBound and Store.ReopenRounds over
// the real store and host executor.
func TestRebaseReopensDoNotUseReviewRounds(t *testing.T) {
	h := newHarness(t)
	session := &runCheckingSession{noRun: map[string]bool{}}
	h.runner.reopen = session
	folder := t.TempDir()
	writeScript(t, folder, "push.sh", rebaseNeededPush)
	h.agents.outcomes = []AgentOutcome{{Verdict: VerdictOK}, {Verdict: VerdictOK}, {Verdict: VerdictBlock, Findings: "1. high a.go:1 still wrong"}}
	h.process()
	// max_rounds 1 on each reopen step: the job's review bound is 2.
	saved := h.automation(sendBackFlow(folder, 1))
	const jobID = "worker/coder-1/run-1"
	h.endAt(folder, "", 0)
	h.endAt(folder, "followup-1", 1)
	for i, followup := range []string{"", "followup-1"} {
		if d := h.deliveryFor(jobID, followup); d.State != StateSentBack || d.SentBack.Step != "rebase" {
			t.Fatalf("rebase %d: delivery %s %+v (blocked %q), want sent_back rebase", i+1, d.State, d.SentBack, d.BlockedReason)
		}
	}
	h.endAt(folder, "followup-2", 2)
	d := h.deliveryFor(jobID, "followup-2")
	if d.State != StateSentBack || d.SentBack == nil || d.SentBack.Step != "fix" || d.SentBack.Round != 3 {
		t.Fatalf("review block after 2 rebases: delivery %s %+v (blocked %q), want sent_back fix round 3", d.State, d.SentBack, d.BlockedReason)
	}
	if !strings.Contains(session.sent[2].Message, "still wrong") {
		t.Fatalf("fix follow-up = %q, want the reviewer's findings", session.sent[2].Message)
	}
	rounds, err := h.store.ReopenRounds(testAccount, jobID, saved.ID)
	if err != nil || rounds.Total != 3 || rounds.Rebase != 2 || rounds.Review() != 1 || rounds.Step("fix") != 1 || rounds.Step("rebase") != 0 {
		t.Fatalf("rounds = %+v %v, want 2 rebase and 1 review round", rounds, err)
	}
	// A push refused for another reason (not a rebase) still counts as a
	// round of its reopen step: it is not a rebase reopen.
	other := d
	other.Steps = append([]StepRecord(nil), d.Steps...)
	other.Steps[2] = StepRecord{ID: "push", Type: StepRun, State: StateFailed, Reason: "exited with code 1: rejected: hook declined"}
	other.Steps[0].Outcome = map[string]any{"verdict": VerdictOK}
	if IsRebaseReopen(other, "rebase") {
		t.Fatal("a push refused by a hook reads as a rebase reopen")
	}
	assertNoSentBackWithoutRun(t, h, jobID, session)
}
