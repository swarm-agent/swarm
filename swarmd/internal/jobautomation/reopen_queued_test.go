package jobautomation

import (
	"context"
	"strings"
	"testing"
)

// queueingSession stands in for a busy task's own session (the daemon's
// jobAutomationReopener.ReopenOrQueue): while a run is in flight it queues
// the follow-up for that run, as the message path does, and names that run.
// refuse makes it answer as the daemon does for a task that must not be
// reopened. Reopen is the plain path; the runner must not use it.
type queueingSession struct {
	running string
	refuse  string
	queued  []ReopenRequest
	plain   int
}

func (s *queueingSession) Reopen(context.Context, ReopenRequest) (string, error) {
	s.plain++
	return "", stillRunning{}
}

func (s *queueingSession) ReopenOrQueue(_ context.Context, req ReopenRequest) (ReopenSent, error) {
	if s.refuse != "" {
		return ReopenSent{}, &RefusedError{Reason: s.refuse}
	}
	s.queued = append(s.queued, req)
	return ReopenSent{Followup: s.running, Queued: true}, nil
}

func taskReviewLoop(folder string) Automation {
	return Automation{Name: "coder flow", Scope: Scope{Folder: folder, Role: "coder"}, Enabled: true, Steps: []Step{
		{ID: "review", Type: StepAgent, Worker: "reviewer", Brief: "Review the coder's work."},
		{ID: "fix", Type: StepReopen, From: "review", MaxRounds: 2, If: []Condition{{Step: "review", Verdict: VerdictBlock}}},
		{ID: "push", Type: StepRun, Command: "push.sh", If: []Condition{{Step: "review", Verdict: VerdictOK}}},
	}}
}

// Requirement: a send-back (reopen step) to a task that is busy with a run
// (a follow-up the person sent after the reviewed run ended) is queued for
// that run and the step succeeds as "sent (queued)" at once, the round
// counted, the delivery sent back; it never waits for the task to settle.
// The queued run's end is the task's next job end and starts the next
// review round (round 1) at the head it ends at, even when the task
// committed more meanwhile, so the findings are never dropped and the loop
// carries on to the push.
// Threat (seen live): the step waited for the busy task, timed out after its
// timeout_seconds, failed the delivery, skipped push and rebase, and the
// reviewer's findings never reached the coder.
// Owners: Runner.runReopen / sendReopen (QueueingReopener), Store.PutReopen,
// ReopenRounds and CreateDelivery over the real store and signal feed; the
// task's session is the stubbed daemon boundary (its queueing is proven in
// api.TestReopenQueuesFindingsForABusyTaskAndRefusesAStoppedOrDeletedOne).
func TestReopenQueuesFindingsForABusyTaskAndItsRunEndIsTheNextRound(t *testing.T) {
	h := newHarness(t)
	session := &queueingSession{running: "run-person-1"}
	h.runner.reopen = session
	folder := t.TempDir()
	writeScript(t, folder, "push.sh", "echo pushed\n")
	findings := "1. high x.go:3 the limit is not checked"
	h.agents.outcomes = []AgentOutcome{{Verdict: VerdictBlock, Findings: findings}, {Verdict: VerdictOK}}
	h.process()
	a := taskReviewLoop(folder)
	a.Steps[1].TimeoutSeconds = 1 // a wait would time out: queuing must not wait
	h.automation(a)
	h.results.results["task-1"] = taskResult(folder, "task-1", "coder")
	h.taskEnd("task-1", "", "succeeded", "task")
	h.process()

	list := h.deliveries("task/proj-1/task-1")
	if len(list) != 1 {
		t.Fatalf("deliveries = %+v", list)
	}
	first := list[0]
	fix := first.Steps[1]
	if first.State != StateSentBack || fix.State != StateSucceeded || fix.Reason != "" || fix.Outcome["followup"] != "run-person-1" || fix.Outcome["queued"] != true {
		t.Fatalf("delivery %s, send-back step = %+v, want sent (queued) for the running run", first.State, fix)
	}
	if !strings.Contains(fix.Output, "sent (queued)") || !strings.Contains(fix.Output, "run-person-1") {
		t.Fatalf("send-back output = %q", fix.Output)
	}
	if first.Steps[2].State != StateSkipped || len(h.host.ran) != 0 {
		t.Fatalf("push after a block: %s, runs %+v", first.Steps[2].State, h.host.ran)
	}
	if session.plain != 0 || len(session.queued) != 1 || session.queued[0].JobID != "task/proj-1/task-1" || !strings.Contains(session.queued[0].Message, findings) {
		t.Fatalf("queued %+v (plain sends %d), want the findings queued once into the task", session.queued, session.plain)
	}
	if rounds, err := h.store.ReopenRounds(testAccount, "task/proj-1/task-1", first.AutomationID); err != nil || rounds.Total != 1 || rounds.Step("fix") != 1 {
		t.Fatalf("rounds = %+v %v, want one counted", rounds, err)
	}
	if rec, found, err := h.store.GetReopen(testAccount, "task/proj-1/task-1", "run-person-1"); err != nil || !found || rec.DeliveryID != first.ID || rec.Step != "fix" {
		t.Fatalf("reopen record = %+v %v %v", rec, found, err)
	}

	// The person's run ends having committed more than the reviewed head:
	// its end is the next round, reviewing the new head.
	newHead := strings.Repeat("d", 40)
	h.results.results["task-1"].Git.Head = newHead
	h.taskEnd("task-1", "run-person-1", "succeeded", "task")
	h.process()
	var second Delivery
	for _, d := range h.deliveries("task/proj-1/task-1") {
		if d.Followup == "run-person-1" {
			second = d
		}
	}
	if second.ID == "" || second.Round != 1 || second.TriggerHead() != newHead || second.State != StateSucceeded {
		t.Fatalf("next round = %+v, want round 1 at the new head, succeeded", second)
	}
	if len(h.agents.started) != 2 || h.agents.started[1].Head != newHead {
		t.Fatalf("reviews started = %+v, want the second at the new head", h.agents.started)
	}
	if second.Steps[2].State != StateSucceeded || len(h.host.ran) != 1 || h.host.ran[0].Env["SWARM_HEAD"] != newHead || len(session.queued) != 1 {
		t.Fatalf("after ok: push %s runs %+v, queued %d", second.Steps[2].State, h.host.ran, len(session.queued))
	}
}

// Requirement: only a real refusal fails a send-back to a busy task: a task
// stopped by the person or deleted fails the step at once with that reason,
// sends nothing, counts no round and skips the rest of the loop.
// Threat: a refusal swallowed as a queued message, or a refused send-back
// that waits out its timeout instead of saying why.
// Owners: Runner.runReopen / sendReopen over the real store; the refusal is
// the daemon boundary's answer (api.reopenTaskSent).
func TestReopenToAStoppedOrDeletedTaskFailsWithTheReason(t *testing.T) {
	for _, reason := range []string{
		"the task task/proj-1/task-1 was stopped by the person: it is not reopened",
		"the task task/proj-1/task-1 no longer exists (deleted): it is not reopened",
	} {
		t.Run(reason, func(t *testing.T) {
			h := newHarness(t)
			session := &queueingSession{running: "run-person-1", refuse: reason}
			h.runner.reopen = session
			folder := t.TempDir()
			writeScript(t, folder, "push.sh", "echo pushed\n")
			h.agents.outcomes = []AgentOutcome{{Verdict: VerdictBlock, Findings: "1. a.go:1 x"}}
			h.process()
			a := taskReviewLoop(folder)
			a.Steps[1].TimeoutSeconds = 30
			h.automation(a)
			h.results.results["task-1"] = taskResult(folder, "task-1", "coder")
			h.taskEnd("task-1", "", "succeeded", "task")
			h.process()

			d := h.deliveries("task/proj-1/task-1")[0]
			fix := d.Steps[1]
			if d.State != StateFailed || fix.State != StateFailed || fix.Reason != reason || fix.Outcome["followup"] != nil {
				t.Fatalf("delivery %s, send-back step = %+v, want failed with %q", d.State, fix, reason)
			}
			if d.Steps[2].State != StateSkipped || len(h.host.ran) != 0 || len(session.queued) != 0 || session.plain != 0 {
				t.Fatalf("after a refusal: push %s, runs %+v, queued %d", d.Steps[2].State, h.host.ran, len(session.queued))
			}
			if rounds, err := h.store.ReopenRounds(testAccount, "task/proj-1/task-1", d.AutomationID); err != nil || rounds.Total != 0 {
				t.Fatalf("rounds = %+v %v, want none counted", rounds, err)
			}
		})
	}
}
