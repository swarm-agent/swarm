package jobautomation

import (
	"context"
	"strings"
	"testing"
	"time"
)

// limitWaitingAgents scripts one worker-backed job across Wait calls: the
// job's run first waits for its provider's limit to reset (it reruns itself,
// as a direct-role worker run in status waiting does), then succeeds.
type limitWaitingAgents struct {
	started []AgentRequest
	waits   []string
	script  []AgentOutcome
}

func (a *limitWaitingAgents) Start(_ context.Context, req AgentRequest) (string, error) {
	a.started = append(a.started, req)
	return "worker/author-1/run-1", nil
}

func (a *limitWaitingAgents) Wait(_ context.Context, _, jobID string) (AgentOutcome, error) {
	a.waits = append(a.waits, jobID)
	out := a.script[min(len(a.waits), len(a.script))-1]
	out.JobID = jobID
	return out, nil
}

// Requirement: an agent step backed by a custom direct-role worker, whose
// worker run waits for the provider's limit to reset (the run reruns itself,
// status waiting, retry_at the reset), waits too: the step is "waiting for
// the provider's limit to reset (until …)" until that time with no verdict,
// keeps the same job (no new job is started), uses none of its retries,
// reads nothing before the reset, and after it reads the same job's outcome
// and carries the delivery on.
// Threat (review finding): Wait blocked inside the step's timeout (600 s by
// default) while the run waited hours for a reset, so the step failed "timed
// out" and the delivery failed with later steps skipped.
// Owners: Runner.runAgent / providerLimitWait (AgentOutcome.LimitUntil),
// Runner.runStep and runDelivery over the real store; the worker job is the
// scripted AgentRunner boundary (the API side, jobAutomationAgents.Wait, is
// covered by TestAgentWaitReturnsAWorkerRunWaitingOnAProviderLimit).
func TestWorkerStepWaitsWhileItsRunWaitsForAProviderLimit(t *testing.T) {
	h := newHarness(t)
	folder := t.TempDir()
	clock := time.Date(2026, 10, 27, 12, 58, 58, 0, time.UTC)
	h.runner.now = func() time.Time { return clock }
	until := time.Date(2026, 10, 27, 16, 1, 0, 0, time.UTC)
	reason := "waiting for the provider's limit to reset (until 2026-10-27T16:01:00Z): claude code turn failed (success): You've hit your session limit · resets 4pm (UTC)"
	agents := &limitWaitingAgents{script: []AgentOutcome{
		{Status: "waiting", Error: reason, LimitUntil: until.UnixMilli()},
		{Status: StatusSucceeded},
	}}
	h.runner.agent = agents
	writeScript(t, folder, "after.sh", "echo after\n")
	h.process()
	h.automation(Automation{Name: "content plan", Scope: Scope{Folder: folder}, Enabled: true, Steps: []Step{
		{ID: "write", Type: StepAgent, Worker: "author", Brief: "Write the plan"},
		{ID: "after", Type: StepRun, Command: "after.sh"},
	}})
	h.results.results["run-1"] = workerResult(folder, "run-1")
	h.jobEnd("run-1", "", "succeeded", "direct")
	h.process()

	d := h.deliveries("worker/coder-1/run-1")[0]
	step := d.Steps[0]
	if Terminal(d.State) || step.State != StateWaiting || step.NextAttemptAt != until.UnixMilli() || step.Reason != reason {
		t.Fatalf("the step did not wait with its run: delivery %s, step %+v", d.State, step)
	}
	if step.Outcome["job_id"] != "worker/author-1/run-1" || step.Outcome["verdict"] != nil {
		t.Fatalf("waiting step outcome = %+v", step.Outcome)
	}
	if d.Steps[1].State != StatePending || len(h.signalsOf("automation.step.failed")) != 0 {
		t.Fatalf("the step after a waiting run ran, or the step failed: %+v", d.Steps[1])
	}

	clock = time.Date(2026, 10, 27, 15, 59, 0, 0, time.UTC)
	h.process()
	if len(agents.waits) != 1 {
		t.Fatalf("the job was read again before the reset: %v", agents.waits)
	}

	clock = time.Date(2026, 10, 27, 16, 2, 0, 0, time.UTC)
	h.process()
	d = h.deliveries("worker/coder-1/run-1")[0]
	if len(agents.started) != 1 || len(agents.waits) != 2 || agents.waits[1] != "worker/author-1/run-1" {
		t.Fatalf("after the reset: started %d, waits %v", len(agents.started), agents.waits)
	}
	if d.State != StateSucceeded || d.Steps[0].State != StateSucceeded || d.Steps[1].State != StateSucceeded {
		t.Fatalf("the delivery did not carry on after the reset: %s %+v", d.State, d.Steps)
	}
	if strings.Contains(d.Steps[0].Reason, "timed out") {
		t.Fatalf("step reason = %q", d.Steps[0].Reason)
	}
}
