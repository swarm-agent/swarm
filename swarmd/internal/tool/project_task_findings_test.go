package tool

import (
	"encoding/json"
	"strings"
	"testing"
	"unicode/utf8"

	"swarm/packages/swarmd/internal/identity"
	pebblestore "swarm/packages/swarmd/internal/store/pebble"
)

// Purpose: the Orchestrator's native get_task on this machine shows why a
// workflow review sent its task back: progress.workflow carries the shown
// delivery's delivery_id and round, the blocking step and verdict, and the
// findings bounded to projectTaskFindingsRunes (valid UTF-8) with
// findings_truncated and a more hint naming the call with the full text;
// the findings come on top of the view budget, so the report is not cut for
// them. A linked machine's answer is bounded the same way and its hint says
// to use that machine. A task whose progress has no workflow is unchanged.
// Threat: get_task said only "review verdict block"; a linked machine could
// return unbounded findings into the agent's context.
// Owners: Runtime.executeManageProjects get_task -> compactProjectTask and
// fitProjectTaskView over a real temp Pebble store with the progress hook
// installed (the API's computeTaskProgress fields), and
// CompactProjectTaskAnswer for the linked path; the tool entry is the
// narrowest layer that proves the agent-visible answer.
func TestManageProjectsGetTaskShowsWorkflowFindings(t *testing.T) {
	db, store := openTaskViewStore(t)
	const account, project, taskID = "acct-review", "proj-review", "task-review"
	if err := store.PutProject(account, &pebblestore.ProjectRecord{ID: project, AccountID: account, Name: "Review"}); err != nil {
		t.Fatal(err)
	}
	task := heavyFinishedTask(account, project, taskID)
	if err := store.PutProjectTask(account, task); err != nil {
		t.Fatal(err)
	}
	// Longer than the cap even if the hook did not cap it (a linked machine).
	findings := "1. high parser.go:12 comments are dropped. " + strings.Repeat("ü界", 2500)
	withFindings := func(string, pebblestore.ProjectTaskRecord) any {
		progress := sentBackProgress("", pebblestore.ProjectTaskRecord{}).(map[string]any)
		d := progress["workflow"].(map[string]any)["deliveries"].([]any)[0].(map[string]any)
		d["findings_step"], d["verdict"], d["findings"], d["findings_truncated"] = "review", "block", findings, true
		return progress
	}
	db.SetProjectTaskProgress(withFindings)
	r := NewRuntime(1)
	defer r.Close()
	r.SetManageProjectStore(store)
	scope := WorkspaceScope{Principal: identity.Principal{Type: identity.PrincipalTypeUser, UserID: "user-review", AccountScopeID: account}}
	raw, _, err := runManageProjects(t, r, scope, map[string]any{"action": "get_task", "project_id": project, "task_id": taskID})
	if err != nil {
		t.Fatal(err)
	}
	var view struct {
		Task ProjectTaskView `json:"task"`
	}
	if err := json.Unmarshal([]byte(raw), &view); err != nil {
		t.Fatal(err)
	}
	wf := view.Task.Progress.Workflow
	if wf == nil || wf.DeliveryID != "dl-1" || wf.Round != 1 || wf.State != "sent_back" || wf.Reason != "review verdict block" || wf.FindingsStep != "review" || wf.Verdict != "block" {
		t.Fatalf("workflow = %+v", wf)
	}
	if !utf8.ValidString(wf.Findings) || utf8.RuneCountInString(wf.Findings) != projectTaskFindingsRunes || !strings.HasPrefix(findings, wf.Findings) || !wf.FindingsTruncated || wf.More != "manage_workflows action=deliveries delivery_id=dl-1" {
		t.Fatalf("findings: %d runes valid %v truncated %v more %q", utf8.RuneCountInString(wf.Findings), utf8.ValidString(wf.Findings), wf.FindingsTruncated, wf.More)
	}
	// The findings come on top of the budget: the report keeps the length it
	// has without them (TestManageProjectsGetTaskCompactByDefault's floor),
	// and still says it was shortened and how to read it all.
	if res := view.Task.Result; res == nil || len(res.Summary) < 750 || !res.ResultTruncated || res.ReadFull == "" {
		t.Fatalf("findings displaced the report: %+v", view.Task.Result)
	}
	if len(raw) > getTaskBudget+len(wf.Findings)*2+400 {
		t.Fatalf("get_task with findings is %d bytes", len(raw))
	}

	// The linked path bounds the machine's findings and names that machine.
	var read map[string]any
	recordJSON, _ := json.Marshal(task)
	if err := json.Unmarshal(recordJSON, &read); err != nil {
		t.Fatal(err)
	}
	read["progress"] = withFindings("", *task)
	answer, _ := json.Marshal(map[string]any{"task": read})
	compact, err := CompactProjectTaskAnswer("get_task", "", answer)
	if err != nil {
		t.Fatal(err)
	}
	if err := json.Unmarshal(compact, &view); err != nil {
		t.Fatal(err)
	}
	if wf := view.Task.Progress.Workflow; wf == nil || utf8.RuneCountInString(wf.Findings) != projectTaskFindingsRunes || !utf8.ValidString(wf.Findings) || !strings.Contains(wf.More, "delivery_id=dl-1") || !strings.Contains(wf.More, "machine") {
		t.Fatalf("linked workflow = %+v", view.Task.Progress.Workflow)
	}

	// A task without a workflow: no workflow in the view.
	db.SetProjectTaskProgress(func(string, pebblestore.ProjectTaskRecord) any {
		return map[string]any{"state": "not_integrated", "summary": "not integrated · no workflow", "history": []any{}, "updated_at": 1}
	})
	raw, _, err = runManageProjects(t, r, scope, map[string]any{"action": "get_task", "project_id": project, "task_id": taskID})
	if err != nil {
		t.Fatal(err)
	}
	if strings.Contains(raw, `"workflow"`) || strings.Contains(raw, "findings") || strings.Contains(raw, "delivery_id") {
		t.Fatalf("no-workflow get_task = %s", raw)
	}
	if len(raw) > getTaskBudget {
		t.Fatalf("no-workflow get_task is %d bytes, budget %d", len(raw), getTaskBudget)
	}
}
