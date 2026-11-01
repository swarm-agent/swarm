package api

import (
	"strings"
	"testing"

	"swarm/packages/swarmd/internal/jobautomation"
	pebblestore "swarm/packages/swarmd/internal/store/pebble"
)

// Requirement: get_task progress shows what Swarm's rebase did. A delivery
// that rebased carries rebase {outcome rebased, target, onto, from, to} and
// its history says "rebased onto dev (old → new)"; a delivery whose rebase
// conflicted and was not resolved puts the task in progress state
// conflict (not failed), workflow state conflict, outcome "conflict
// <files> (rebase onto dev)" with the files and resolver jobs on the
// delivery, a rebase_conflict task state (count bucket workflow_failed, not
// needs_you: nothing waits on the person) that names the conflict, and a
// "conflict on dev" history line.
// Threat: an unresolved conflict reported as a failed check (the
// Orchestrator "fixes" a passing check), or a rebase nobody can see.
// Owners: computeTaskProgress, deliveryRebase, taskProgressOutcome,
// pebblestore.ProjectTaskState. Layer: the pure progress computation all
// task surfaces (get_task, list_tasks, wake rows) read.
func TestTaskProgressShowsRebasedAndConflict(t *testing.T) {
	from, to, onto := strings.Repeat("a", 40), strings.Repeat("b", 40), strings.Repeat("c", 40)
	task := pebblestore.ProjectTaskRecord{ID: "task-1", ProjectID: "proj-1", Agent: "coder", Status: "needs_review", WorktreeBranch: "agent/task-1", BaseBranch: "dev",
		CreatedAt: 1000, UpdatedAt: 2000}
	base := jobautomation.Delivery{ID: "jdel-1", AutomationID: "wf-1", AutomationName: "land", JobID: "task/proj-1/task-1", CreatedAt: 3000, StartedAt: 3000, FinishedAt: 9000,
		Definition: []jobautomation.Step{{ID: "push", Type: jobautomation.StepRun, Command: "push.sh"}},
		Result:     jobautomation.Result{JobID: "task/proj-1/task-1", Kind: jobautomation.KindTask, Status: jobautomation.StatusSucceeded, FinishedAt: 2500}}

	rebased := base
	rebased.State, rebased.Head = jobautomation.StateRunning, to
	rebased.FinishedAt = 0
	rebased.Steps = []jobautomation.StepRecord{{ID: "push", Type: jobautomation.StepRun, State: jobautomation.StateRunning, StartedAt: 5000}}
	rebased.Rebases = []jobautomation.RebaseRecord{{N: 1, Trigger: jobautomation.RebaseTriggerStart, Target: "dev", Onto: onto, From: from, To: to, Outcome: jobautomation.RebaseRebased, At: 4000, FinishedAt: 4100}}
	p := computeTaskProgress(task, []jobautomation.Delivery{rebased}, nil, 10000)
	if p.Workflow == nil || len(p.Workflow.Deliveries) != 1 {
		t.Fatalf("workflow = %+v", p.Workflow)
	}
	r := p.Workflow.Deliveries[0].Rebase
	if r == nil || r.Outcome != "rebased" || r.Target != "dev" || r.From != from || r.To != to || r.Onto != onto || r.Rebases != 1 {
		t.Fatalf("delivery rebase = %+v", r)
	}
	if !historyHas(p, "rebased onto dev (aaaaaaaaaaaa → bbbbbbbbbbbb)") {
		t.Fatalf("history = %+v", p.History)
	}

	conflict := base
	conflict.State, conflict.Head = jobautomation.StateConflict, from
	conflict.Steps = []jobautomation.StepRecord{{ID: "push", Type: jobautomation.StepRun, State: jobautomation.StateFailed, Reason: "not run: the rebase onto dev conflicted in a.go", FinishedAt: 9000}}
	c := &jobautomation.RebaseConflict{Commit: from, Files: []string{"a.go"}, ResolverJobs: []string{"task/proj-1/task_wf_1", "task/proj-1/task_wf_2"}, Rounds: 2, Outcome: jobautomation.ConflictUnresolved}
	conflict.Rebases = []jobautomation.RebaseRecord{{N: 1, Trigger: jobautomation.RebaseTriggerStart, Target: "dev", Onto: onto, From: from, Outcome: jobautomation.RebaseConflicted, Conflict: c,
		Reason: "2 of 2 resolver rounds used", At: 4000, FinishedAt: 8000}}
	conflict.Conflict = c
	p = computeTaskProgress(task, []jobautomation.Delivery{conflict}, nil, 10000)
	if p.State != progressConflict || p.Workflow.State != workflowConflict || !strings.HasPrefix(p.Outcome, "conflict a.go (rebase onto dev)") {
		t.Fatalf("progress state %s workflow %s outcome %q summary %q", p.State, p.Workflow.State, p.Outcome, p.Summary)
	}
	if !strings.Contains(p.Summary, "conflicted in a.go") || !strings.Contains(p.Summary, "task_wf_2") {
		t.Fatalf("summary = %q", p.Summary)
	}
	r = p.Workflow.Deliveries[0].Rebase
	if r == nil || r.Outcome != "conflict" || len(r.Files) != 1 || len(r.ResolverJobs) != 2 || r.Resolved {
		t.Fatalf("delivery rebase = %+v", r)
	}
	if !historyHas(p, "conflict on dev: a.go (not resolved)") {
		t.Fatalf("history = %+v", p.History)
	}
	state, reason := pebblestore.ProjectTaskState(task, p.Outcome, false, false)
	// Nothing waits on the person: its own state, counted with the
	// workflows that ended unsuccessfully, never needs_you.
	if state != pebblestore.ProjectTaskStateRebaseConflict || !strings.Contains(reason, "conflict a.go") || !strings.Contains(reason, "nothing waits on the person") {
		t.Fatalf("task state %s: %s", state, reason)
	}
	if bucket := pebblestore.ProjectTaskBucket(task, p.Outcome, false, false); bucket != pebblestore.ProjectTaskCountWorkflowFailed {
		t.Fatalf("count bucket %s", bucket)
	}
	if state, _ := pebblestore.ProjectTaskState(task, p.Outcome, false, true); state != pebblestore.ProjectTaskStateNotIntegrated {
		t.Fatalf("idle conflict state %s", state)
	}
	if got := pebblestore.ProjectTaskDeliverableStatus(pebblestore.ProjectTaskDeliverable{Status: "pending"}, pebblestore.ProjectTaskStateRebaseConflict); got != "rebase conflict" {
		t.Fatalf("deliverable status %q", got)
	}
}

func historyHas(p taskProgress, what string) bool {
	for _, e := range p.History {
		if strings.Contains(e.What, what) {
			return true
		}
	}
	return false
}
