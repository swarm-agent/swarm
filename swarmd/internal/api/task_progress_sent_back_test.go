package api

import (
	"strings"
	"testing"

	"swarm/packages/swarmd/internal/jobautomation"
)

// sendBackDelivery is a finished delivery of review -> fix (reopen) -> push
// (run, allowed to fail) -> rebase (reopen) in the given state.
func sendBackDelivery(state string, sent *jobautomation.SentBack, steps ...jobautomation.StepRecord) jobautomation.Delivery {
	def := []jobautomation.Step{{ID: "review", Type: jobautomation.StepAgent, Worker: "reviewer"}, {ID: "fix", Type: jobautomation.StepReopen},
		{ID: "push", Type: jobautomation.StepRun, ContinueOnFailure: true}, {ID: "rebase", Type: jobautomation.StepReopen}}
	return jobautomation.Delivery{ID: "jdel_1", AutomationID: "auto-1", AutomationName: "ship", JobID: "task/proj-1/task-1", State: state, Round: 1,
		Definition: def, Steps: steps, CreatedAt: 4200, FinishedAt: 4900, SentBack: sent,
		Result: jobautomation.Result{JobID: "task/proj-1/task-1", ProjectID: "proj-1", Status: jobautomation.StatusSucceeded, FinishedAt: 4000,
			Git: &jobautomation.ResultGit{Branch: "agent/task-1", BaseBranch: "dev", Commits: []jobautomation.ResultCommit{{SHA: strings.Repeat("b", 40)}}}}}
}

// Requirement: a delivery that ended sent_back is reported as sent back
// everywhere a person or an outside AI reads it: the task's
// progress.workflow (state sent_back, the reopen step, round and reason), the
// progress summary and history, and the deliveries view (state and a
// sent_back object); it is never shown as done or delivered, and its failed
// push (continue_on_failure) never reads as "delivered". A succeeded
// delivery is still done/delivered with no sent_back.
// Threat: progress or deliveries saying "done" or "delivered: push" for work
// that was sent back and never pushed.
// Owners: computeTaskProgress (deliveryProgressState, currentStep,
// sentBackSummary) and jobDeliveryView, pure functions over a delivery; the
// runner tests own how the delivery reached sent_back.
func TestTaskProgressAndDeliveryViewReportSentBack(t *testing.T) {
	task := progressTask("needs_review")
	task.UnintegratedCommits = 1

	blocked := sendBackDelivery(jobautomation.StateSentBack, &jobautomation.SentBack{Step: "fix", Round: 1, Reason: "review verdict block"},
		step("review", jobautomation.StepAgent, jobautomation.StateSucceeded, 4300, 4400),
		step("fix", jobautomation.StepReopen, jobautomation.StateSucceeded, 4500, 4600),
		step("push", jobautomation.StepRun, jobautomation.StateSkipped, 0, 4700),
		step("rebase", jobautomation.StepReopen, jobautomation.StateSkipped, 0, 4700))
	p := computeTaskProgress(task, []jobautomation.Delivery{blocked}, evaluated("", "jdel_1"), 9000)
	if p.Workflow == nil || p.Workflow.State != workflowSentBack || len(p.Workflow.Deliveries) != 1 {
		t.Fatalf("workflow = %+v, want sent_back", p.Workflow)
	}
	view := p.Workflow.Deliveries[0]
	if view.State != workflowSentBack || view.Step != 2 || view.StepType != jobautomation.StepReopen || view.SentBack == nil || view.SentBack.Round != 1 {
		t.Fatalf("delivery view = %+v, want sent_back at step 2 (fix) round 1", view)
	}
	if p.State != progressWorkflow || p.Summary != "workflow ship sent back by fix · round 1: review verdict block" {
		t.Fatalf("progress = %s %q", p.State, p.Summary)
	}
	if !progressHistoryHas(p, "workflow ship sent back by fix · round 1: review verdict block") {
		t.Fatalf("history = %+v", p.History)
	}

	pushed := step("push", jobautomation.StepRun, jobautomation.StateFailed, 4650, 4700)
	pushed.Reason, pushed.Output = "exited with code 1: rejected: non-fast-forward", "rejected: non-fast-forward"
	rebase := sendBackDelivery(jobautomation.StateSentBack, &jobautomation.SentBack{Step: "rebase", Round: 2, Reason: "push failed: exited with code 1: rejected: non-fast-forward"},
		step("review", jobautomation.StepAgent, jobautomation.StateSucceeded, 4300, 4400),
		step("fix", jobautomation.StepReopen, jobautomation.StateSkipped, 0, 4500),
		pushed,
		step("rebase", jobautomation.StepReopen, jobautomation.StateSucceeded, 4750, 4800))
	p = computeTaskProgress(task, []jobautomation.Delivery{rebase}, evaluated("", "jdel_1"), 9000)
	if p.Workflow.State != workflowSentBack || p.Workflow.Deliveries[0].Step != 4 || p.State != progressWorkflow ||
		!strings.Contains(p.Summary, "sent back by rebase · round 2: push failed") || strings.Contains(p.Summary, "delivered") {
		t.Fatalf("push failed progress = %s %q workflow %+v", p.State, p.Summary, p.Workflow)
	}
	dv := jobDeliveryView(rebase)
	sb, _ := dv["sent_back"].(map[string]any)
	if dv["state"] != jobautomation.StateSentBack || sb["step"] != "rebase" || sb["round"] != 2 || !strings.HasPrefix(sb["reason"].(string), "push failed") {
		t.Fatalf("deliveries view = %+v", dv)
	}

	ok := sendBackDelivery(jobautomation.StateSucceeded, nil,
		step("review", jobautomation.StepAgent, jobautomation.StateSucceeded, 4300, 4400),
		step("fix", jobautomation.StepReopen, jobautomation.StateSkipped, 0, 4500),
		step("push", jobautomation.StepRun, jobautomation.StateSucceeded, 4600, 4700),
		step("rebase", jobautomation.StepReopen, jobautomation.StateSkipped, 0, 4700))
	p = computeTaskProgress(task, []jobautomation.Delivery{ok}, evaluated("", "jdel_1"), 9000)
	if p.Workflow.State != workflowDone || p.Workflow.Deliveries[0].SentBack != nil || p.State != progressDelivered || !strings.HasPrefix(p.Summary, "push") {
		t.Fatalf("succeeded progress = %s %q workflow %+v", p.State, p.Summary, p.Workflow)
	}
	if _, has := jobDeliveryView(ok)["sent_back"]; has {
		t.Fatal("a succeeded delivery's view carries sent_back")
	}
}

func progressHistoryHas(p taskProgress, what string) bool {
	for _, e := range p.History {
		if e.What == what {
			return true
		}
	}
	return false
}
