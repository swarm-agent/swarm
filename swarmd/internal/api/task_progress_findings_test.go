package api

import (
	"context"
	"encoding/json"
	"strings"
	"testing"
	"unicode/utf8"

	"swarm/packages/swarmd/internal/jobautomation"
	"swarm/packages/swarmd/internal/tool"
)

// reviewBlockDelivery is a finished ship delivery whose review step recorded
// verdict block with the given findings and whose fix step sent the work back.
func reviewBlockDelivery(findings string) jobautomation.Delivery {
	review := step("review", jobautomation.StepAgent, jobautomation.StateSucceeded, 4300, 4400)
	review.Outcome = map[string]any{"job_id": "task/proj-1/review-1", "verdict": jobautomation.VerdictBlock, "findings": findings}
	return sendBackDelivery(jobautomation.StateSentBack, &jobautomation.SentBack{Step: "fix", Round: 1, Reason: "review verdict block"},
		review,
		step("fix", jobautomation.StepReopen, jobautomation.StateSucceeded, 4500, 4600),
		step("push", jobautomation.StepRun, jobautomation.StateSkipped, 0, 4700),
		step("rebase", jobautomation.StepReopen, jobautomation.StateSkipped, 0, 4700))
}

// Requirement: when a workflow review blocks a task and sends it back, the
// task's progress (computeTaskProgress, which the task HTTP route, the native
// get_task, the MCP tools and the linked-machine proxy all read) carries the
// latest delivery's delivery_id and round and the blocking step's findings,
// capped to maxProgressFindingsRunes on a rune boundary with
// findings_truncated and a more hint naming the exact call that returns the
// full text; the compact get_task view (tool.CompactProjectTaskAnswer, the
// same compactor the local get_task uses) shows them. Short findings are
// shown whole with no hint; a delivery that passed review shows none; a task
// without a workflow has no workflow at all.
// Threat: an Orchestrator could only read "review verdict block" and had to
// pull every delivery of the workflow (~380 KB, cut off) to learn why; a cut
// in the middle of a multi-byte rune would hand out invalid UTF-8.
// Owners: computeTaskProgress/deliveryFindings (pure, over a delivery) and
// tool.CompactProjectTaskAnswer over the task route's JSON shape; the pure
// layer is the narrowest that proves the computed fields, and the compactor
// proves what the agent sees.
func TestTaskProgressShowsWhyAReviewSentTheTaskBack(t *testing.T) {
	task := progressTask("needs_review")
	task.UnintegratedCommits = 1

	// 4000 runes end inside a run of two- and three-byte runes.
	long := "1. high parser.go:12 comments are dropped. " + strings.Repeat("é界", 3000)
	p := computeTaskProgress(task, []jobautomation.Delivery{reviewBlockDelivery(long)}, evaluated("", "jdel_1"), 9000)
	if p.Workflow == nil || p.Workflow.State != workflowSentBack || len(p.Workflow.Deliveries) != 1 {
		t.Fatalf("workflow = %+v", p.Workflow)
	}
	d := p.Workflow.Deliveries[0]
	if d.ID != "jdel_1" || d.Round != 1 || d.FindingsStep != "review" || d.Verdict != jobautomation.VerdictBlock {
		t.Fatalf("delivery = id %q round %d step %q verdict %q", d.ID, d.Round, d.FindingsStep, d.Verdict)
	}
	if !utf8.ValidString(d.Findings) || utf8.RuneCountInString(d.Findings) != maxProgressFindingsRunes || !strings.HasPrefix(long, d.Findings) || !strings.HasPrefix(d.Findings, "1. high parser.go:12") {
		t.Fatalf("findings: valid %v, %d runes, prefix of the original %v", utf8.ValidString(d.Findings), utf8.RuneCountInString(d.Findings), strings.HasPrefix(long, d.Findings))
	}
	if !d.FindingsTruncated || d.More != "manage_workflows action=deliveries delivery_id=jdel_1" {
		t.Fatalf("truncated %v more %q", d.FindingsTruncated, d.More)
	}

	// What the agent reads: the task route's JSON compacted into get_task.
	view := compactGetTask(t, task, p)
	wf := view.Progress.Workflow
	if wf == nil || wf.DeliveryID != "jdel_1" || wf.Round != 1 || wf.State != workflowSentBack || wf.Reason != "review verdict block" || wf.FindingsStep != "review" || wf.Verdict != jobautomation.VerdictBlock {
		t.Fatalf("get_task workflow = %+v", wf)
	}
	if wf.Findings != d.Findings || !utf8.ValidString(wf.Findings) || !wf.FindingsTruncated || !strings.HasPrefix(wf.More, "manage_workflows action=deliveries delivery_id=jdel_1") {
		t.Fatalf("get_task findings: %d runes, truncated %v, more %q", utf8.RuneCountInString(wf.Findings), wf.FindingsTruncated, wf.More)
	}
	if view.Result == nil || view.Result.Commits != 1 {
		t.Fatalf("the findings must not displace the result: %+v", view.Result)
	}

	// Short findings: whole, no hint.
	p = computeTaskProgress(task, []jobautomation.Delivery{reviewBlockDelivery("1. high a.go:1 the limit is not checked.")}, evaluated("", "jdel_1"), 9000)
	if d := p.Workflow.Deliveries[0]; d.Findings != "1. high a.go:1 the limit is not checked." || d.FindingsTruncated || d.More != "" {
		t.Fatalf("short findings = %+v", d)
	}

	// A delivery that passed review: no findings, verdict or hint.
	ok := sendBackDelivery(jobautomation.StateSucceeded, nil,
		step("review", jobautomation.StepAgent, jobautomation.StateSucceeded, 4300, 4400),
		step("fix", jobautomation.StepReopen, jobautomation.StateSkipped, 0, 4500),
		step("push", jobautomation.StepRun, jobautomation.StateSucceeded, 4600, 4700),
		step("rebase", jobautomation.StepReopen, jobautomation.StateSkipped, 0, 4700))
	ok.Steps[0].Outcome = map[string]any{"verdict": jobautomation.VerdictOK, "findings": "nit: rename x"}
	p = computeTaskProgress(task, []jobautomation.Delivery{ok}, evaluated("", "jdel_1"), 9000)
	if d := p.Workflow.Deliveries[0]; d.Findings != "" || d.Verdict != "" || d.FindingsStep != "" || d.More != "" || d.FindingsTruncated {
		t.Fatalf("passed delivery shows findings: %+v", d)
	}
	if wf := compactGetTask(t, task, p).Progress.Workflow; wf == nil || wf.Findings != "" || wf.More != "" || wf.DeliveryID != "jdel_1" {
		t.Fatalf("passed delivery in get_task = %+v", wf)
	}

	// A task without a workflow: no workflow in progress or get_task.
	p = computeTaskProgress(task, nil, nil, 9000)
	if p.Workflow != nil {
		t.Fatalf("no-workflow task has workflow %+v", p.Workflow)
	}
	if raw, _ := json.Marshal(p); strings.Contains(string(raw), "findings") || strings.Contains(string(raw), "delivery_id") {
		t.Fatalf("no-workflow progress = %s", raw)
	}
	if view := compactGetTask(t, task, p); view.Progress.Workflow != nil {
		t.Fatalf("no-workflow get_task workflow = %+v", view.Progress.Workflow)
	}
}

// compactGetTask is get_task's compact view of task with progress p, as the
// task route answers it.
func compactGetTask(t *testing.T, task any, p taskProgress) tool.ProjectTaskView {
	t.Helper()
	var read map[string]any
	raw, _ := json.Marshal(task)
	if err := json.Unmarshal(raw, &read); err != nil {
		t.Fatal(err)
	}
	read["progress"] = p
	answer, _ := json.Marshal(map[string]any{"task": read})
	compact, err := tool.CompactProjectTaskAnswer("get_task", "", answer)
	if err != nil {
		t.Fatal(err)
	}
	var out struct {
		Task tool.ProjectTaskView `json:"task"`
	}
	if err := json.Unmarshal(compact, &out); err != nil {
		t.Fatal(err)
	}
	return out.Task
}

// Requirement: workflow deliveries filter by the bare task id every other
// tool uses: job_id "<task>" matches that task's job ids
// (task/<project>/<task>), as the full job id does; a job_id that matches
// nothing says so in a note instead of a bare empty list; another account's
// deliveries never match; a worker job's run id is not a task id; and the
// delivery_id get_task points to returns the step's findings past the
// get_task cap.
// Threat: manage_workflows deliveries job_id=<task id> silently returned []
// because only the full job id matched, so an Orchestrator concluded no
// review ran.
// Owners: jobautomation.Store.ListDeliveries/filterDeliveries
// (TaskJobIDMatches), Server.ManageWorkflows deliveries and
// deliveryJobIDNote over the real store; the native boundary is the
// narrowest layer that proves the agent-visible answer.
func TestWorkflowDeliveriesFilterByBareTaskID(t *testing.T) {
	server, store := newRunOwnerServer(t)
	seedWorkflowListing(t, store, 2, 3) // job ids task/proj-1/task-<w>-<i>
	deliveries := func(principal any, args map[string]any) map[string]any {
		t.Helper()
		args["action"] = "deliveries"
		p := testPrincipal()
		if other, ok := principal.(string); ok {
			p.AccountScopeID = other
		}
		out, err := server.ManageWorkflows(context.Background(), p, "session-1", args)
		if err != nil {
			t.Fatal(err)
		}
		return out.(map[string]any)
	}

	bare := deliveries(nil, map[string]any{"job_id": "task-1-2"})
	list := bare["deliveries"].([]map[string]any)
	if len(list) != 1 || list[0]["job_id"] != "task/proj-1/task-1-2" || bare["note"] != nil {
		t.Fatalf("bare task id = %v", bare)
	}
	full := deliveries(nil, map[string]any{"job_id": "task/proj-1/task-1-2"})
	if got := full["deliveries"].([]map[string]any); len(got) != 1 || got[0]["delivery_id"] != list[0]["delivery_id"] || full["note"] != nil {
		t.Fatalf("full job id = %v", full)
	}

	for _, missing := range []string{"task-9-9", "task/proj-1/task-9-9"} {
		none := deliveries(nil, map[string]any{"job_id": missing})
		note, _ := none["note"].(string)
		if len(none["deliveries"].([]map[string]any)) != 0 || !strings.Contains(note, "no deliveries match job_id") || !strings.Contains(note, missing) {
			t.Fatalf("job_id %q matching nothing = %v", missing, none)
		}
	}
	if other := deliveries("other-account", map[string]any{"job_id": "task-1-2"}); len(other["deliveries"].([]map[string]any)) != 0 || other["note"] == nil {
		t.Fatalf("another account's deliveries matched a bare task id: %v", other)
	}
	if jobautomation.TaskJobIDMatches("worker/coder-1/task-1-2", "task-1-2") || jobautomation.TaskJobIDMatches("task/proj-1/task-1-2", "") || !jobautomation.TaskJobIDMatches("design/proj-1/task-1-2", "task-1-2") {
		t.Fatal("TaskJobIDMatches matched the wrong job kinds")
	}

	// The delivery get_task points to returns the full findings.
	one, err := server.ManageWorkflows(context.Background(), testPrincipal(), "session-1", map[string]any{"action": "deliveries", "delivery_id": list[0]["delivery_id"]})
	if err != nil {
		t.Fatal(err)
	}
	review := one.(map[string]any)["delivery"].(map[string]any)["steps"].([]map[string]any)[0]
	if findings, _ := review["outcome"].(map[string]any)["findings"].(string); utf8.RuneCountInString(findings) <= maxProgressFindingsRunes {
		t.Fatalf("delivery_id findings = %d runes, want the full text past the get_task cap", utf8.RuneCountInString(findings))
	}
}
