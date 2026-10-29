package tool

import (
	"encoding/json"
	"strings"
	"testing"

	"swarm/packages/swarmd/internal/identity"
	pebblestore "swarm/packages/swarmd/internal/store/pebble"
)

// pushedProgress is a progress as the API's progress hook computes it for a
// coder task whose workflow pushed its work: the run ended (needs_review),
// the workflow is done and the work was delivered.
func pushedProgress(string, pebblestore.ProjectTaskRecord) any {
	return map[string]any{
		"state": "delivered", "summary": "push: PUSHED 5711ae410abc", "outcome": "pushed 5711ae410abc",
		"workflow": map[string]any{"state": "done", "deliveries": []any{map[string]any{
			"id": "dl-1", "workflow": "forge", "state": "succeeded", "step": 3, "steps": 3, "step_type": "run", "round": 1,
		}}},
		"integration": map[string]any{"state": "delivered", "branch": "agent/keep-comments", "commit": "5711ae410abc"},
		"history":     []any{},
		"updated_at":  1,
	}
}

// Purpose: list_tasks rows and get_task lead with the task's state
// (pebblestore.ProjectTaskState) and name the record's status run_status: a
// coder task whose workflow pushed (run status needs_review, workflow done,
// integration delivered) reads landed with the pushed SHA, is not
// needs_attention, and its stage is the terminal "Landed", not its
// implementation step ("1 of 2"); its code deliverable placeholder (pending)
// reads delivered in detail=files. Threat: pushed work reported as waiting for
// review because the run status was labelled the task's status, and a stale
// stage claiming implementation is still under way. Owners:
// Runtime.executeManageProjects -> executeManageProjectTasksContext
// list_tasks (projectTaskSummary + ProjectTaskState) and get_task
// (compactProjectTask, projectTaskFilesDetail) over a real temp Pebble store
// with the progress hook installed; the tool entry is the narrowest layer
// that proves the agent-visible answer.
func TestManageProjectsTaskViewsShowTheTaskState(t *testing.T) {
	db, store := openTaskViewStore(t)
	const account, project, taskID = "acct-state", "proj-state", "task-state"
	if err := store.PutProject(account, &pebblestore.ProjectRecord{ID: project, AccountID: account, Name: "State"}); err != nil {
		t.Fatal(err)
	}
	task := &pebblestore.ProjectTaskRecord{
		ID: taskID, ProjectID: project, AccountID: account, Title: "Fix the parser", Agent: "coder", Status: "needs_review",
		SessionID: "task-session-1", Revision: 1, WorktreeBranch: "agent/keep-comments",
		PipelineStages: []string{"Code Implementation", "Verification & Pull Request"}, CurrentStageIndex: 0,
		Deliverables: []pebblestore.ProjectTaskDeliverable{{ID: "deliv_code", Title: "Code PR & Verified Tests", Kind: "pr", Status: "pending"}},
	}
	if err := store.PutProjectTask(account, task); err != nil {
		t.Fatal(err)
	}
	db.SetProjectTaskProgress(pushedProgress)
	r := NewRuntime(1)
	defer r.Close()
	r.SetManageProjectStore(store)
	scope := WorkspaceScope{Principal: identity.Principal{Type: identity.PrincipalTypeUser, UserID: "user-state", AccountScopeID: account}}

	raw, _, err := runManageProjects(t, r, scope, map[string]any{"action": "get_task", "project_id": project, "task_id": taskID})
	if err != nil {
		t.Fatal(err)
	}
	var got struct {
		Task ProjectTaskView `json:"task"`
	}
	if err := json.Unmarshal([]byte(raw), &got); err != nil {
		t.Fatal(err)
	}
	v := got.Task
	if v.State != pebblestore.ProjectTaskStateLanded || !strings.Contains(v.StateReason, "pushed 5711ae410abc") || v.RunStatus != "needs_review" || v.NeedsAttention {
		t.Fatalf("get_task state: %s", raw)
	}
	if v.Progress.Stage != "Landed" || strings.Contains(raw, "1 of 2") {
		t.Fatalf("stage of a delivered workflow = %q: %s", v.Progress.Stage, raw)
	}
	// The state is the first thing a reader sees, and the raw status is
	// never labelled the task's status.
	if !strings.Contains(raw, `"task":{"state":"landed","state_reason":"pushed 5711ae410abc"`) {
		t.Fatalf("state is not first in the task view: %s", raw)
	}
	if strings.Contains(raw, `"status":"needs_review"`) {
		t.Fatalf("run status exposed as status: %s", raw)
	}
	files, _, err := runManageProjects(t, r, scope, map[string]any{"action": "get_task", "project_id": project, "task_id": taskID, "detail": "files"})
	if err != nil || !strings.Contains(files, `"status":"delivered"`) || strings.Contains(files, `"status":"pending"`) {
		t.Fatalf("detail=files deliverable: %v %s", err, files)
	}

	_, out, err := runManageProjects(t, r, scope, map[string]any{"action": "list_tasks", "project_id": project})
	if err != nil {
		t.Fatal(err)
	}
	rows, _ := out["tasks"].([]any)
	if len(rows) != 1 {
		t.Fatalf("list_tasks rows: %v", out)
	}
	row := rows[0].(map[string]any)
	if row["state"] != pebblestore.ProjectTaskStateLanded || row["run_status"] != "needs_review" || row["status"] != nil || row["needs_attention"] != nil {
		t.Fatalf("list_tasks row: %v", row)
	}
	if counts, _ := out["outcome_counts"].(map[string]any); counts["done"] != float64(1) || counts["needs_attention"] != float64(0) {
		t.Fatalf("counts disagree with the landed row: %v", out["outcome_counts"])
	}
}

// Purpose: a linked machine's get_task answer (the task route's JSON) is
// compacted by the same compactProjectTask, so a pushed task read from
// another machine shows the same state, reason and terminal stage. Threat: a
// forge task on a linked machine read as needs_review. Owner:
// CompactProjectTaskAnswer (pure; executeFleetProjects calls it).
func TestLinkedGetTaskShowsTheTaskState(t *testing.T) {
	record := pebblestore.ProjectTaskRecord{ID: "remote-task", Title: "Fix the parser", Agent: "coder", Status: "needs_review",
		PipelineStages: []string{"Code Implementation", "Verification & Pull Request"}}
	answer, _ := json.Marshal(map[string]any{"task": map[string]any{
		"id": record.ID, "title": record.Title, "agent": record.Agent, "status": record.Status,
		"pipeline_stages": record.PipelineStages, "current_stage_index": 0, "progress": pushedProgress("", record),
	}})
	compact, err := CompactProjectTaskAnswer("get_task", "", answer)
	if err != nil {
		t.Fatal(err)
	}
	var got struct {
		Task ProjectTaskView `json:"task"`
	}
	if err := json.Unmarshal(compact, &got); err != nil {
		t.Fatal(err)
	}
	if v := got.Task; v.State != pebblestore.ProjectTaskStateLanded || !strings.Contains(v.StateReason, "5711ae410abc") || v.RunStatus != "needs_review" || v.Progress.Stage != "Landed" || v.NeedsAttention {
		t.Fatalf("linked get_task: %s", compact)
	}
}
