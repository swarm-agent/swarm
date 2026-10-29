package api

import (
	"testing"

	"swarm/packages/swarmd/internal/jobautomation"
	pebblestore "swarm/packages/swarmd/internal/store/pebble"
)

// Purpose: a task's work is not reported handed over before the step that
// hands it over has recorded its result. Requirement: a delivery the runner
// marked succeeded whose push step is still running is workflow running and
// task_state running (not done, not not_integrated); once the push step
// records its result the workflow is done and task_state is landed with the
// push output, the same derived state get_task shows. Threat (observed live):
// a watch woke the Orchestrator with integration_state not_integrated while
// get_task moments later said landed (pushed), because the settle check ran
// between the delivery's and the push step's writes. Owner: computeTaskProgress
// and markTaskProgressAttention (pure), the narrowest layer for the rule; the
// wait reads the same progress (taskWaitProgressFor).
func TestTaskProgressWaitsForTheFinalStepResult(t *testing.T) {
	task := progressTask("needs_review")
	task.UnintegratedCommits = 2
	delivery := func(push jobautomation.StepRecord) jobautomation.Delivery {
		return jobautomation.Delivery{ID: "jdel_3", AutomationID: "auto-3", AutomationName: "review and push", State: jobautomation.StateSucceeded,
			Definition: []jobautomation.Step{{ID: "review", Type: jobautomation.StepAgent, Worker: "reviewer"}, {ID: "push", Type: jobautomation.StepRun}},
			Steps:      []jobautomation.StepRecord{step("review", jobautomation.StepAgent, jobautomation.StateSucceeded, 4300, 4400), push}}
	}
	progress := func(d jobautomation.Delivery) taskProgress {
		p := computeTaskProgress(task, []jobautomation.Delivery{d}, evaluated("", d.ID), 9000)
		markTaskProgressAttention(&p, task)
		return p
	}
	for _, state := range []string{jobautomation.StateRunning, jobautomation.StatePending, jobautomation.StateWaiting} {
		p := progress(delivery(jobautomation.StepRecord{ID: "push", Type: jobautomation.StepRun, State: state, StartedAt: 4500}))
		if p.Workflow == nil || p.Workflow.State != workflowRunning || p.TaskState != pebblestore.ProjectTaskStateRunning {
			t.Fatalf("push step %s reported as settled: workflow %+v task_state %q", state, p.Workflow, p.TaskState)
		}
		if wait := pebblestore.ProjectTaskWaitProgressFrom(p); wait == nil || wait.WorkflowState != workflowRunning || wait.TaskState != pebblestore.ProjectTaskStateRunning {
			t.Fatalf("wait progress for push step %s: %+v", state, wait)
		}
	}
	p := progress(delivery(jobautomation.StepRecord{ID: "push", Type: jobautomation.StepRun, State: jobautomation.StateSucceeded, StartedAt: 4500, FinishedAt: 4600, Output: "PUSHED 7ec9a8e"}))
	if p.Workflow == nil || p.Workflow.State != workflowDone || p.TaskState != pebblestore.ProjectTaskStateLanded || p.TaskStateReason == "" {
		t.Fatalf("recorded push: workflow %+v task_state %q (%q)", p.Workflow, p.TaskState, p.TaskStateReason)
	}
	wait := pebblestore.ProjectTaskWaitProgressFrom(p)
	if wait == nil || wait.WorkflowState != workflowDone || wait.TaskState != pebblestore.ProjectTaskStateLanded || wait.TaskStateReason != p.TaskStateReason {
		t.Fatalf("wait progress after the push: %+v", wait)
	}
}
