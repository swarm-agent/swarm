package jobautomation

import (
	"context"
	"strings"
	"testing"
)

// stillRunning is how a Reopener answers while the job's run or follow-up is
// in flight: an error whose Busy reports true (the daemon's BusyError).
type stillRunning struct{}

func (stillRunning) Error() string {
	return "the job worker/coder-1/run-1 is still running (in_progress)"
}
func (stillRunning) Busy() bool { return true }

// settlingReopener answers busy for its first busy calls, as the job's
// session does while a follow-up runs (a job takes one follow-up at a time),
// then accepts like sessionReopener. cancelAfter, when set, answers that the
// job was cancelled once the busy calls are used. onBusy sees each busy call.
type settlingReopener struct {
	sessionReopener
	busy        int
	cancelAfter bool
	calls       int
	onBusy      func(call int)
}

func (s *settlingReopener) Reopen(ctx context.Context, req ReopenRequest) (string, error) {
	s.calls++
	if s.busy < 0 || s.calls <= s.busy {
		if s.onBusy != nil {
			s.onBusy(s.calls)
		}
		return "", stillRunning{}
	}
	if s.cancelAfter {
		return "", &RefusedError{Reason: "the job " + req.JobID + " was cancelled: it is not reopened"}
	}
	return s.sessionReopener.Reopen(ctx, req)
}

func reopenWaitLoop(h *harness, folder string, timeout int) {
	h.t.Helper()
	a := reviewLoop(folder, 2)
	a.Steps[1].TimeoutSeconds = timeout
	h.automation(a)
	h.results.results["run-1"] = workerResult(folder, "run-1")
	h.jobEnd("run-1", "", "succeeded", "direct")
}

// Requirement: a reopen step whose target job is still running (a follow-up
// in flight) waits, within its timeout_seconds, for the run to settle, then
// sends exactly one follow-up; while it waits the delivery shows the step as
// waiting with the reason; the round is counted once, when the follow-up is
// sent.
// Threat (forge run): the step failed with "still running: it is not
// reopened", the delivery ended failed and its findings were never sent, so a
// person had to resend them by hand.
// Owners: Runner.runReopen over the real store and signal feed; the job's
// session is the stubbed host boundary (jobAutomationReopener in the daemon).
func TestReopenWaitsForARunningJobThenSendsOnce(t *testing.T) {
	h := newHarness(t)
	folder := t.TempDir()
	writeScript(t, folder, "push.sh", "echo pushed\n")
	findings := "1. high api/x.go:12 the limit is not checked"
	h.agents.outcomes = []AgentOutcome{{Verdict: VerdictBlock, Findings: findings}}
	var waiting []StepRecord
	reopener := &settlingReopener{busy: 2}
	reopener.onBusy = func(call int) {
		if call < 2 {
			return
		}
		// The second check: the step has been waiting since the first.
		list, err := h.store.JobDeliveries(testAccount, "worker/coder-1/run-1")
		if err != nil || len(list) != 1 {
			t.Errorf("deliveries while waiting = %d %v", len(list), err)
			return
		}
		waiting = append(waiting, list[0].Steps[1])
	}
	h.runner.reopen = reopener
	h.process()
	reopenWaitLoop(h, folder, 30)
	h.process()

	if len(waiting) != 1 || waiting[0].State != "waiting" || !strings.Contains(waiting[0].Reason, "still running") {
		t.Fatalf("while the job ran, the reopen step was %+v, want state waiting with the reason", waiting)
	}
	d := h.deliveries("worker/coder-1/run-1")[0]
	fix := d.Steps[1]
	if d.State != StateSucceeded || fix.State != StateSucceeded || fix.Attempts != 1 || fix.Outcome["followup"] != "followup-1" {
		t.Fatalf("delivery %s, reopen step = %+v", d.State, fix)
	}
	if len(reopener.sent) != 1 || reopener.calls != 3 || !strings.Contains(reopener.sent[0].Message, findings) {
		t.Fatalf("follow-ups sent = %d after %d checks: %+v", len(reopener.sent), reopener.calls, reopener.sent)
	}
	if round, _ := fix.Outcome["round"].(float64); round != 1 {
		if roundInt, _ := fix.Outcome["round"].(int); roundInt != 1 {
			t.Fatalf("round = %v, want 1 (counted once, when sent)", fix.Outcome["round"])
		}
	}
	record, found, err := h.store.GetReopen(testAccount, "worker/coder-1/run-1", "followup-1")
	if err != nil || !found || record.Round != 0 || record.DeliveryID != d.ID {
		t.Fatalf("reopen record = %+v %v %v", record, found, err)
	}
}

// Requirement: a reopen step whose job is still running when its
// timeout_seconds expires fails with a reason that says it timed out waiting
// for the running job, and sends nothing.
// Threat: a wait that never ends, or a follow-up sent into a busy job after
// the step gave up.
// Owner: Runner.runReopen / Runner.runStep.
func TestReopenWaitTimesOutWithAClearReasonAndSendsNothing(t *testing.T) {
	h := newHarness(t)
	folder := t.TempDir()
	writeScript(t, folder, "push.sh", "echo pushed\n")
	h.agents.outcomes = []AgentOutcome{{Verdict: VerdictBlock, Findings: "1. a.go:1 x"}}
	reopener := &settlingReopener{busy: -1}
	h.runner.reopen = reopener
	h.process()
	reopenWaitLoop(h, folder, 1)
	h.process()

	d := h.deliveries("worker/coder-1/run-1")[0]
	fix := d.Steps[1]
	if d.State != StateFailed || fix.State != StateFailed || fix.Attempts != 1 {
		t.Fatalf("delivery %s, reopen step = %+v", d.State, fix)
	}
	if !strings.Contains(fix.Reason, "timed out") || !strings.Contains(fix.Reason, "still running") || !strings.Contains(fix.Reason, "nothing was sent") {
		t.Fatalf("timeout reason = %q", fix.Reason)
	}
	if len(reopener.sent) != 0 || fix.Outcome["followup"] != nil {
		t.Fatalf("a timed-out reopen sent %d follow-ups", len(reopener.sent))
	}
}

// Requirement: a job that is cancelled, before or while a reopen step waits
// for it, fails the step at once with the reason and sends nothing.
// Threat: a wait that outlives a job the person cancelled, or revives it.
// Owner: Runner.runReopen (and the Reopener's refusal).
func TestReopenOfACancelledJobFailsAtOnce(t *testing.T) {
	h := newHarness(t)
	folder := t.TempDir()
	writeScript(t, folder, "push.sh", "echo pushed\n")
	h.agents.outcomes = []AgentOutcome{{Verdict: VerdictBlock, Findings: "1. a.go:1 x"}}
	// Running, then cancelled while the step waits.
	reopener := &settlingReopener{busy: 1, cancelAfter: true}
	h.runner.reopen = reopener
	h.process()
	reopenWaitLoop(h, folder, 30)
	h.process()
	d := h.deliveries("worker/coder-1/run-1")[0]
	fix := d.Steps[1]
	if d.State != StateFailed || fix.State != StateFailed || !strings.Contains(fix.Reason, "cancelled") || len(reopener.sent) != 0 || reopener.calls != 2 {
		t.Fatalf("cancelled while waiting: delivery %s step %+v (sent %d, checks %d)", d.State, fix, len(reopener.sent), reopener.calls)
	}
}
