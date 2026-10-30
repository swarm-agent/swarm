package api

import (
	"encoding/json"
	"strings"
	"testing"

	"swarm/packages/swarmd/internal/jobautomation"
	pebblestore "swarm/packages/swarmd/internal/store/pebble"
)

// reviewPushDelivery is a delivery of the shipping workflow shape: review
// (agent) -> fix (reopen if the review blocked) -> push (run, allowed to
// fail, only after an ok verdict) -> rebase (reopen if the review passed and
// the push failed), at the given delivery round.
func reviewPushDelivery(state string, round int, sent *jobautomation.SentBack, steps ...jobautomation.StepRecord) jobautomation.Delivery {
	def := []jobautomation.Step{
		{ID: "review", Type: jobautomation.StepAgent, Worker: "reviewer"},
		{ID: "fix", Type: jobautomation.StepReopen, From: "review", If: []jobautomation.Condition{{Step: "review", Verdict: jobautomation.VerdictBlock}}},
		{ID: "push", Type: jobautomation.StepRun, ContinueOnFailure: true, If: []jobautomation.Condition{{Step: "review", Verdict: jobautomation.VerdictOK}}},
		{ID: "rebase", Type: jobautomation.StepReopen, If: []jobautomation.Condition{{Step: "review", Verdict: jobautomation.VerdictOK}, {Step: "push", Status: jobautomation.StateFailed}}},
	}
	return jobautomation.Delivery{ID: "jdel_1", AutomationID: "auto-1", AutomationName: "ship", JobID: "task/proj-1/task-1", State: state, Round: round,
		Definition: def, Steps: steps, CreatedAt: 4200, FinishedAt: 4900, SentBack: sent,
		Result: jobautomation.Result{JobID: "task/proj-1/task-1", ProjectID: "proj-1", Status: jobautomation.StatusSucceeded, FinishedAt: 4000,
			Git: &jobautomation.ResultGit{Branch: "agent/task-1", BaseBranch: "dev", Commits: []jobautomation.ResultCommit{{SHA: strings.Repeat("b", 40)}}}}}
}

func verdictStep(id, verdict string) jobautomation.StepRecord {
	rec := step(id, jobautomation.StepAgent, jobautomation.StateSucceeded, 4300, 4400)
	rec.Outcome = map[string]any{"verdict": verdict}
	return rec
}

func failedPush(reason string) jobautomation.StepRecord {
	rec := step("push", jobautomation.StepRun, jobautomation.StateFailed, 4500, 4600)
	rec.Reason = reason
	return rec
}

// reopenWaitView is what wait_tasks reads from a progress value, through the
// JSON a linked machine's get_task also carries.
func reopenWaitView(t *testing.T, p taskProgress) *pebblestore.V3ProjectTaskWaitProgress {
	t.Helper()
	raw, err := json.Marshal(p)
	if err != nil {
		t.Fatal(err)
	}
	w := pebblestore.ProjectTaskWaitProgressFrom(json.RawMessage(raw))
	if w == nil {
		t.Fatalf("no wait progress from %s", raw)
	}
	return w
}

// Requirement: a task its workflow reopened says why in its progress, so the
// orchestrating AI reports it truthfully from the wake alone. A passed
// review whose push then failed (the reopen conditioned on the push's
// failure) reads as a rebase: outcome "sent_back (round n) rebase: push
// failed: ...", task state reason "rebase (round n): review passed; push
// failed: ...", delivery reopen_cause push_failed with the push's short
// failure; a review block keeps "sent_back (round n)" and "the agent
// addresses the findings" with reopen_cause review_block and no failure.
// Rounds are the follow-up's round (SentBack.Round, or the delivery round +1
// while the reopen step runs), the same in the outcome, reason and wait row.
// Threat: a failed push after an approved review reported to the person as
// "sent back" by the reviewer (the regression this fixes), or a review block
// reported as a rebase; a round in the milestone off by one from the reason.
// Owners: jobautomation.ReopenCause, deliveryReopen, taskProgressOutcome,
// pebblestore.ProjectTaskState (ProjectTaskReopen) and
// pebblestore.ProjectTaskWaitProgressFrom: pure functions over durable
// delivery records, so the progress computation is the narrowest layer that
// reaches every surface (routes, native tools, MCP, linked machines).
func TestTaskProgressReopenCauseSeparatesRebaseFromReviewBlock(t *testing.T) {
	task := progressTask("needs_review")
	task.UnintegratedCommits = 1
	pushFailure := "exited with code 1: NOT PUSHED: rebase needed: dev moved and changed files this job also changed: a.go"

	// (a) review ok, push failed, rebase reopen sent the work back (round 1).
	rebase := reviewPushDelivery(jobautomation.StateSentBack, 0,
		&jobautomation.SentBack{Step: "rebase", Round: 1, Reason: "review verdict ok; push failed: " + pushFailure},
		verdictStep("review", jobautomation.VerdictOK),
		step("fix", jobautomation.StepReopen, jobautomation.StateSkipped, 0, 4450),
		failedPush(pushFailure),
		step("rebase", jobautomation.StepReopen, jobautomation.StateSucceeded, 4700, 4800))
	p := computeTaskProgress(task, []jobautomation.Delivery{rebase}, evaluated("", "jdel_1"), 9000)
	markTaskProgressAttention(&p, task)
	view := p.Workflow.Deliveries[0]
	if view.ReopenCause != pebblestore.ProjectTaskReopenPushFailed || view.ReopenRound != 1 || !strings.HasPrefix(view.ReopenFailure, "push failed: exited with code 1: NOT PUSHED: rebase needed") {
		t.Fatalf("rebase delivery view = %+v, want push_failed round 1 with the push's failure", view)
	}
	if !strings.HasPrefix(p.Outcome, "sent_back (round 1) rebase: push failed: exited with code 1") {
		t.Fatalf("rebase outcome = %q", p.Outcome)
	}
	if p.TaskState != pebblestore.ProjectTaskStateRunning || !strings.HasPrefix(p.TaskStateReason, "rebase (round 1): review passed; push failed: exited with code 1: NOT PUSHED") ||
		strings.Contains(p.TaskStateReason, "findings") || strings.Contains(p.TaskStateReason, "sent_back") {
		t.Fatalf("rebase task state = %q %q", p.TaskState, p.TaskStateReason)
	}
	if p.NeedsAttention {
		t.Fatal("a rebase round needs the person's attention; the agent works on it")
	}
	w := reopenWaitView(t, p)
	if w.WorkflowState != "sent_back" || w.ReopenCause != pebblestore.ProjectTaskReopenPushFailed || w.WorkflowRound != 1 || !strings.HasPrefix(w.ReopenFailure, "push failed: ") || w.TaskStateReason != p.TaskStateReason {
		t.Fatalf("rebase wait progress = %+v", w)
	}

	// (b) review block, fix reopen sent the work back (round 1): unchanged.
	blocked := reviewPushDelivery(jobautomation.StateSentBack, 0,
		&jobautomation.SentBack{Step: "fix", Round: 1, Reason: "review verdict block"},
		verdictStep("review", jobautomation.VerdictBlock),
		step("fix", jobautomation.StepReopen, jobautomation.StateSucceeded, 4500, 4600),
		step("push", jobautomation.StepRun, jobautomation.StateSkipped, 0, 4600),
		step("rebase", jobautomation.StepReopen, jobautomation.StateSkipped, 0, 4600))
	p = computeTaskProgress(task, []jobautomation.Delivery{blocked}, evaluated("", "jdel_1"), 9000)
	markTaskProgressAttention(&p, task)
	view = p.Workflow.Deliveries[0]
	if view.ReopenCause != pebblestore.ProjectTaskReopenReviewBlock || view.ReopenFailure != "" || view.ReopenRound != 1 || view.SentBack == nil || view.SentBack.Reason != "review verdict block" {
		t.Fatalf("block delivery view = %+v", view)
	}
	if p.Outcome != "sent_back (round 1)" || p.TaskStateReason != "sent_back (round 1); the agent addresses the findings" {
		t.Fatalf("block outcome %q reason %q", p.Outcome, p.TaskStateReason)
	}
	if p.Summary != "workflow ship sent back by fix · round 1: review verdict block" {
		t.Fatalf("block summary = %q", p.Summary)
	}
	w = reopenWaitView(t, p)
	if w.ReopenCause != pebblestore.ProjectTaskReopenReviewBlock || w.ReopenFailure != "" || w.WorkflowRound != 1 {
		t.Fatalf("block wait progress = %+v", w)
	}

	// (c) rounds: a second-round rebase reads round 2 everywhere.
	second := reviewPushDelivery(jobautomation.StateSentBack, 1,
		&jobautomation.SentBack{Step: "rebase", Round: 2, Reason: "review verdict ok; push failed: exited with code 1"},
		verdictStep("review", jobautomation.VerdictOK),
		step("fix", jobautomation.StepReopen, jobautomation.StateSkipped, 0, 4450),
		failedPush("exited with code 1"),
		step("rebase", jobautomation.StepReopen, jobautomation.StateSucceeded, 4700, 4800))
	p = computeTaskProgress(task, []jobautomation.Delivery{second}, evaluated("", "jdel_1"), 9000)
	markTaskProgressAttention(&p, task)
	if p.Outcome != "sent_back (round 2) rebase: push failed: exited with code 1" || p.TaskStateReason != "rebase (round 2): review passed; push failed: exited with code 1" {
		t.Fatalf("second round outcome %q reason %q", p.Outcome, p.TaskStateReason)
	}
	if w = reopenWaitView(t, p); w.WorkflowRound != 2 {
		t.Fatalf("second round wait round = %d, want 2", w.WorkflowRound)
	}

	// The fix step running after a block (not sent yet): the follow-up's
	// round (delivery round + 1, as it reads once sent), a review block.
	running := reviewPushDelivery(jobautomation.StateRunning, 0, nil,
		verdictStep("review", jobautomation.VerdictBlock),
		step("fix", jobautomation.StepReopen, jobautomation.StateRunning, 4500, 0))
	p = computeTaskProgress(task, []jobautomation.Delivery{running}, evaluated("", "jdel_1"), 9000)
	view = p.Workflow.Deliveries[0]
	if view.State != workflowRunning || view.StepType != jobautomation.StepReopen || view.ReopenCause != pebblestore.ProjectTaskReopenReviewBlock || view.ReopenRound != 1 {
		t.Fatalf("running fix view = %+v", view)
	}
	if w = reopenWaitView(t, p); w.WorkflowState != "running" || w.WorkflowStepType != jobautomation.StepReopen || w.WorkflowRound != 1 || w.ReopenCause != pebblestore.ProjectTaskReopenReviewBlock {
		t.Fatalf("running fix wait progress = %+v", w)
	}

	// The rebase step running after a failed push: the delivery stands at
	// the failed push (currentStep), so no reopen is claimed yet and the
	// wait row is at no reopen milestone (never "sent back").
	rebasing := reviewPushDelivery(jobautomation.StateRunning, 0, nil,
		verdictStep("review", jobautomation.VerdictOK),
		step("fix", jobautomation.StepReopen, jobautomation.StateSkipped, 0, 4450),
		failedPush("exited with code 1"),
		step("rebase", jobautomation.StepReopen, jobautomation.StateRunning, 4700, 0))
	p = computeTaskProgress(task, []jobautomation.Delivery{rebasing}, evaluated("", "jdel_1"), 9000)
	if w = reopenWaitView(t, p); w.WorkflowStepType == jobautomation.StepReopen && w.ReopenCause != pebblestore.ProjectTaskReopenPushFailed {
		t.Fatalf("running rebase wait progress reads as a review reopen: %+v", w)
	}

	// The review step running: no reopen is claimed.
	reviewing := reviewPushDelivery(jobautomation.StateRunning, 0, nil, step("review", jobautomation.StepAgent, jobautomation.StateRunning, 4300, 0))
	p = computeTaskProgress(task, []jobautomation.Delivery{reviewing}, evaluated("", "jdel_1"), 9000)
	if view = p.Workflow.Deliveries[0]; view.ReopenCause != "" || view.ReopenRound != 0 {
		t.Fatalf("review running view claims a reopen: %+v", view)
	}
}
