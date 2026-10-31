package jobautomation

import (
	"strings"
	"testing"

	pebblestore "swarm/packages/swarmd/internal/store/pebble"
)

// taskEnd records a task.run.<status> signal as the daemon does when a run in
// a task's own session ends (api.signalTaskRunEnded): followup is "" for the
// task's first run and the follow-up's run id after that.
func (h *harness) taskEnd(taskID, followup, status, source string) {
	h.t.Helper()
	if _, err := h.feed.Append(pebblestore.Signal{Kind: "task.run." + status, Severity: pebblestore.SignalSeverityInfo, Source: pebblestore.SignalSourceSwarmd,
		Account: testAccount, Summary: "Task run " + status, DedupKey: "task.run:" + taskID + ":" + followup,
		Refs: map[string]string{"project_id": "proj-1", "task_id": taskID}, Attrs: map[string]string{"job_id": "task/proj-1/" + taskID, "role": "coder", "source": source}}); err != nil {
		h.t.Fatal(err)
	}
}

func taskResult(folder, taskID, role string) *Result {
	return &Result{V: ResultVersion, JobID: "task/proj-1/" + taskID, Kind: KindTask, Role: role, RunID: taskID, ProjectID: "proj-1", Folder: folder,
		Status: StatusSucceeded, JobStatus: "needs_review", Brief: "add a logo", Report: "Added it.",
		Git: &ResultGit{Branch: "agent/" + taskID, BaseCommit: strings.Repeat("a", 40), Head: strings.Repeat("b", 40), Commits: []ResultCommit{{SHA: strings.Repeat("b", 40), Subject: "add logo"}}, Files: []string{"logo.png"}}}
}

// Requirement: a workflow follows the work of Swarm's own agents the way it
// follows a worker job. A task the coder finished (task.run.succeeded) starts
// every workflow scoped to its folder and role; a reviewer step reviews it; a
// reopen step sends the findings into the task's own session (job id
// task/<project>/<task>, never a new task); the follow-up's end is the next
// round, and an ok verdict runs the push. A workflow scoped to another role
// does not run, and a task a workflow step started (source automation) never
// starts one.
// Threat: a workflow that only ever sees worker jobs (the coder that comes
// with Swarm cannot be followed), findings sent to a new task instead of the
// coder's own session, or a workflow step's task starting workflows in a
// loop.
// Owners: Runner.Scan/consume (jobEndKinds, followupOf), Automation.Matches
// (scope.role), Runner.runReopen over the real store and signal feed; the
// task result, the reviewer and the session that accepts the follow-up are
// the stubbed daemon boundary.
func TestTaskRunEndsStartWorkflowsScopedToTheirFolderAndRole(t *testing.T) {
	h := newHarness(t)
	reopener := &sessionReopener{}
	h.withReopener(reopener)
	folder := t.TempDir()
	writeScript(t, folder, "push.sh", "echo pushed\n")
	h.agents.outcomes = []AgentOutcome{{Verdict: VerdictBlock, Findings: "1. high x.go:3 the limit is not checked"}, {Verdict: VerdictOK}}
	h.process()
	flow := Automation{Name: "coder flow", Scope: Scope{Folder: folder, Role: "coder"}, Enabled: true, Steps: []Step{
		{ID: "review", Type: StepAgent, Worker: "reviewer", Brief: "Review the coder's work."},
		{ID: "fix", Type: StepReopen, From: "review", MaxRounds: 2, If: []Condition{{Step: "review", Verdict: VerdictBlock}}},
		{ID: "push", Type: StepRun, Command: "push.sh", If: []Condition{{Step: "review", Verdict: VerdictOK}}},
	}}
	h.automation(flow)
	h.automation(Automation{Name: "finder notes", Scope: Scope{Folder: folder, Role: "finder"}, Enabled: true, Steps: []Step{
		{ID: "notes", Type: StepRun, Command: "push.sh"},
	}})
	h.results.results["task-1"] = taskResult(folder, "task-1", "coder")
	h.taskEnd("task-1", "", "succeeded", "task")
	h.process()

	list := h.deliveries("task/proj-1/task-1")
	if len(list) != 1 || list[0].AutomationName != "coder flow" {
		t.Fatalf("deliveries = %+v, want only the coder flow", list)
	}
	first := list[0]
	if first.State != StateSentBack || first.Steps[1].Outcome["followup"] != "followup-1" || first.Steps[2].State != StateSkipped {
		t.Fatalf("first delivery = %s, steps %+v", first.State, first.Steps)
	}
	if len(h.agents.started) != 1 || h.agents.started[0].Result.JobID != "task/proj-1/task-1" || h.agents.started[0].Head != strings.Repeat("b", 40) {
		t.Fatalf("reviewer started = %+v", h.agents.started)
	}
	if len(reopener.sent) != 1 || reopener.sent[0].JobID != "task/proj-1/task-1" || !strings.Contains(reopener.sent[0].Message, "the limit is not checked") {
		t.Fatalf("follow-ups = %+v", reopener.sent)
	}

	// The coder's fix in its own session ends as the task's next run.
	h.results.results["task-1"].Git.Head = strings.Repeat("c", 40)
	h.taskEnd("task-1", "followup-1", "succeeded", "task")
	h.process()
	var second Delivery
	for _, d := range h.deliveries("task/proj-1/task-1") {
		if d.Followup == "followup-1" {
			second = d
		}
	}
	if second.ID == "" || second.Round != 1 || second.Head != strings.Repeat("c", 40) || second.State != StateSucceeded || second.Steps[2].State != StateSucceeded {
		t.Fatalf("follow-up delivery = %+v", second)
	}
	if len(reopener.sent) != 1 || len(h.host.ran) != 1 || h.host.ran[0].Env["SWARM_HEAD"] != strings.Repeat("c", 40) {
		t.Fatalf("after ok: follow-ups %d, runs %+v", len(reopener.sent), h.host.ran)
	}

	// A task a workflow step started never starts a workflow.
	h.results.results["task-2"] = taskResult(folder, "task-2", "coder")
	h.results.results["task-2"].RequestSource = RequestSourceAutomation
	h.taskEnd("task-2", "", "succeeded", RequestSourceAutomation)
	h.process()
	if got := h.deliveries("task/proj-1/task-2"); len(got) != 0 {
		t.Fatalf("a workflow step's task started %d deliveries", len(got))
	}
}

// Requirement: scope.role names one of Swarm's own agents and applies only to
// their tasks; an agent step's worker names Swarm's own agents (swarm,
// coder, finder, designer) or a worker, and fixes stays a reviewer worker's
// option. A task's job id is accepted as scope.job.
// Threat: a role scope that silently matches worker jobs, a role that is not
// an agent, or fixes given to a coder.
// Owners: Scope.normalize, Step.normalizeAgent, Automation.Matches.
func TestScopeRoleAndBuiltinAgentSteps(t *testing.T) {
	folder := t.TempDir()
	valid := func(a Automation) error {
		a.Name = "x"
		if len(a.Steps) == 0 {
			a.Steps = []Step{{Type: StepAgent, Worker: "swarm", Brief: "Summarize the change."}}
		}
		return a.Normalize()
	}
	a := Automation{Scope: Scope{Folder: folder, Role: " Coder "}}
	if err := valid(a); err != nil {
		t.Fatalf("role coder with a swarm step: %v", err)
	}
	for name, bad := range map[string]Automation{
		"role reviewer":    {Scope: Scope{Folder: folder, Role: "reviewer"}},
		"role and worker":  {Scope: Scope{Folder: folder, Role: "coder", Worker: "w1"}},
		"fixes on a coder": {Scope: Scope{Folder: folder}, Steps: []Step{{Type: StepAgent, Worker: "coder", Brief: "Fix it.", Fixes: FixesSecurity}}},
	} {
		if err := valid(bad); err == nil || !IsValidationError(err) {
			t.Fatalf("%s accepted: %v", name, err)
		}
	}
	if err := valid(Automation{Scope: Scope{Folder: folder, Job: "task/proj-1/task-1"}}); err != nil {
		t.Fatalf("a task job id as scope.job: %v", err)
	}
	// A job-end workflow always has steps (only a trigger-only workflow,
	// with on, may have none, and Matches never matches one to a job end).
	steps := []Step{{ID: "summary", Type: StepAgent, Worker: "swarm", Brief: "Summarize the change."}}
	coderFlow := Automation{Enabled: true, When: WhenSucceeded, Scope: Scope{Folder: folder, Role: "coder"}, Steps: steps}
	if !coderFlow.Matches(taskResult(folder, "t1", "coder")) {
		t.Fatal("role coder did not match the coder's task")
	}
	if coderFlow.Matches(taskResult(folder, "t1", "finder")) || coderFlow.Matches(workerResult(folder, "run-1")) {
		t.Fatal("role coder matched another agent's task or a coder worker's job")
	}
	folderFlow := Automation{Enabled: true, When: WhenSucceeded, Scope: Scope{Folder: folder}, Steps: steps}
	if !folderFlow.Matches(taskResult(folder, "t1", "swarm")) || !folderFlow.Matches(workerResult(folder, "run-1")) {
		t.Fatal("a folder scope must match tasks and worker jobs in the folder")
	}
}

// Requirement: an agent step says where its agent works (checkout), and
// without one each agent works where it should: a reviewer in the reviewed
// job's own worktree (same), a reviewer with fixes, the coder and swarm in
// their own worktree (new), finder and designer in the folder (none), as their
// tasks always do. Only a reviewer may read another job's worktree, and no
// agent that changes files may share one.
// Threat: a coder or swarm writing in another job's worktree or straight into
// the person's folder (checkout none), a reviewer given a copy
// of the work by default, or a step whose checkout silently means something
// else than it says.
// Owners: Step.normalizeAgent, CheckoutFor, NonReviewerCheckoutError.
func TestAgentStepCheckout(t *testing.T) {
	folder := t.TempDir()
	set := func(step Step) (Step, error) {
		step.ID, step.Type = "s", StepAgent
		if step.Brief == "" {
			step.Brief = "Look at it."
		}
		a := Automation{Name: "x", Scope: Scope{Folder: folder}, Steps: []Step{step}}
		err := a.Normalize()
		if len(a.Steps) == 0 {
			return Step{}, err
		}
		return a.Steps[0], err
	}
	for _, c := range []struct {
		step Step
		role string
		want string
	}{
		{Step{Worker: "reviewer"}, "", CheckoutSame},
		{Step{Worker: "wkr_1"}, "reviewer", CheckoutSame},
		{Step{Worker: "reviewer", Checkout: " New "}, "", CheckoutNew},
		{Step{Worker: "wkr_reviewer", Fixes: FixesSecurity}, "reviewer", CheckoutNew},
		{Step{Worker: "coder"}, "", CheckoutNew},
		{Step{Worker: "designer"}, "", CheckoutNone},
		{Step{Worker: "finder"}, "", CheckoutNone},
		{Step{Worker: "Swarm"}, "", CheckoutNew},
		{Step{Worker: "swarm", Checkout: CheckoutNew}, "", CheckoutNew},
		{Step{Worker: "wkr_2"}, "coder", CheckoutNew},
	} {
		got, err := set(c.step)
		if err != nil {
			t.Fatalf("%+v: %v", c.step, err)
		}
		if checkout := CheckoutFor(got, c.role); checkout != c.want {
			t.Fatalf("%+v (role %q): checkout %q, want %q", c.step, c.role, checkout, c.want)
		}
	}
	for name, bad := range map[string]Step{
		"unknown value":        {Worker: "reviewer", Checkout: "shared"},
		"fixes in place":       {Worker: "reviewer", Fixes: FixesSecurity, Checkout: CheckoutSame},
		"coder in place":       {Worker: "coder", Checkout: CheckoutSame},
		"coder in the folder":  {Worker: "coder", Checkout: CheckoutNone},
		"swarm in the folder":  {Worker: "swarm", Checkout: CheckoutNone},
		"swarm in place":       {Worker: "swarm", Checkout: CheckoutSame},
		"finder in a worktree": {Worker: "finder", Checkout: CheckoutSame},
		"designer in worktree": {Worker: "designer", Checkout: CheckoutNew},
		"worker in the folder": {Worker: "wkr_1", Checkout: CheckoutNone},
	} {
		if _, err := set(bad); err == nil || !IsValidationError(err) {
			t.Fatalf("%s accepted: %v", name, err)
		}
	}
	for name, step := range map[string]Step{
		"run":    {ID: "r", Type: StepRun, Command: "push.sh", Checkout: CheckoutNew},
		"reopen": {ID: "o", Type: StepReopen, Message: "again", Checkout: CheckoutSame},
	} {
		a := Automation{Name: "x", Scope: Scope{Folder: folder}, Steps: []Step{step}}
		if err := a.Normalize(); err == nil {
			t.Fatalf("a %s step accepted checkout", name)
		}
	}
	if err := NonReviewerCheckoutError("s", "wkr_2", "coder"); !IsValidationError(err) || !strings.Contains(err.Error(), "only for a reviewer") {
		t.Fatalf("NonReviewerCheckoutError = %v", err)
	}
}
