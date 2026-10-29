package tool

import (
	"encoding/json"
	"strings"
	"testing"

	"swarm/packages/swarmd/internal/identity"
	pebblestore "swarm/packages/swarmd/internal/store/pebble"
)

// Purpose: get_task shows every non-media deliverable's status from the
// task state (pebblestore.ProjectTaskDeliverableStatus), in the compact view
// and in detail=full on the task and on its attempts, while a media
// deliverable keeps its real stored status. A finished finder task (run
// ended, outcome "finished": state done) shows its report placeholder as
// ready, a running one as in progress, and the stored record still holds
// "pending". Threat: an AI read detail=full of a finished finder task with
// deliverables [{deliv_audit, report, pending}] and reported the report as
// not ready. Owners: Runtime.executeManageProjects ->
// executeManageProjectTasksContext get_task (compactProjectTask and the
// detail=full record) over a real temp Pebble store with the progress hook
// installed; the tool entry is the narrowest layer that proves the
// agent-visible answer.
func TestGetTaskShowsNonMediaDeliverableStatusFromTheTaskState(t *testing.T) {
	db, store := openTaskViewStore(t)
	const account, project = "acct-deliv", "proj-deliv"
	if err := store.PutProject(account, &pebblestore.ProjectRecord{ID: project, AccountID: account, Name: "Deliverables"}); err != nil {
		t.Fatal(err)
	}
	report := func() []pebblestore.ProjectTaskDeliverable {
		return []pebblestore.ProjectTaskDeliverable{{ID: "deliv_audit", Title: "Comprehensive Audit Report", Kind: "report", Status: "pending"}}
	}
	tasks := []*pebblestore.ProjectTaskRecord{
		{ID: "task-finished", ProjectID: project, AccountID: account, Title: "Audit the parser", Agent: "finder", Status: "needs_review", SessionID: "s-finished", Revision: 1,
			Deliverables: report(), Attempts: []pebblestore.ProjectTaskAttempt{{ID: "attempt-1", Status: "needs_review", SessionID: "s-finished", Deliverables: report()}}, ActiveAttemptID: "attempt-1"},
		{ID: "task-running", ProjectID: project, AccountID: account, Title: "Audit the lexer", Agent: "finder", Status: "in_progress", SessionID: "s-running", Revision: 1,
			Deliverables: report()},
		{ID: "task-image", ProjectID: project, AccountID: account, Title: "Logo", Agent: "image", Status: "needs_review", Revision: 1,
			Deliverables: []pebblestore.ProjectTaskDeliverable{{ID: "deliv_img_slot_1", Title: "Logo variant", Kind: "image", Status: "generating"}}},
	}
	for _, task := range tasks {
		if err := store.PutProjectTask(account, task); err != nil {
			t.Fatal(err)
		}
	}
	db.SetProjectTaskProgress(func(_ string, task pebblestore.ProjectTaskRecord) any {
		switch task.ID {
		case "task-finished", "task-image":
			return map[string]any{"state": "none", "summary": "finished", "outcome": "finished", "history": []any{}, "updated_at": 1}
		}
		return map[string]any{"state": "in_progress", "summary": "working", "history": []any{}, "updated_at": 1}
	})
	r := NewRuntime(1)
	defer r.Close()
	r.SetManageProjectStore(store)
	scope := WorkspaceScope{Principal: identity.Principal{Type: identity.PrincipalTypeUser, UserID: "user-deliv", AccountScopeID: account}}

	compact := func(taskID string) ProjectTaskView {
		t.Helper()
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
		return got.Task
	}
	full := func(taskID string) pebblestore.ProjectTaskRecord {
		t.Helper()
		raw, _, err := runManageProjects(t, r, scope, map[string]any{"action": "get_task", "project_id": project, "task_id": taskID, "detail": "full"})
		if err != nil {
			t.Fatal(err)
		}
		if strings.Contains(raw, `"status":"pending"`) {
			t.Fatalf("detail=full of %s carries the placeholder: %s", taskID, raw)
		}
		var got struct {
			Task pebblestore.ProjectTaskRecord `json:"task"`
		}
		if err := json.Unmarshal([]byte(raw), &got); err != nil {
			t.Fatal(err)
		}
		return got.Task
	}

	// Finished finder task: state done, report ready.
	if v := compact("task-finished"); v.State != pebblestore.ProjectTaskStateDone || v.Result == nil || len(v.Result.Deliverables) != 1 || v.Result.Deliverables[0] != "Comprehensive Audit Report (ready)" {
		t.Fatalf("compact finished finder task: %+v %+v", v, v.Result)
	}
	finished := full("task-finished")
	if len(finished.Deliverables) != 1 || finished.Deliverables[0].Status != "ready" {
		t.Fatalf("detail=full task deliverables: %+v", finished.Deliverables)
	}
	if len(finished.Attempts) != 1 || len(finished.Attempts[0].Deliverables) != 1 || finished.Attempts[0].Deliverables[0].Status != "ready" {
		t.Fatalf("detail=full attempt deliverables: %+v", finished.Attempts)
	}

	// Running finder task: report in progress.
	if v := compact("task-running"); v.State != pebblestore.ProjectTaskStateRunning || v.Result == nil || len(v.Result.Deliverables) != 1 || v.Result.Deliverables[0] != "Comprehensive Audit Report (in progress)" {
		t.Fatalf("compact running finder task: %+v %+v", v, v.Result)
	}
	if running := full("task-running"); len(running.Deliverables) != 1 || running.Deliverables[0].Status != "in progress" {
		t.Fatalf("detail=full running deliverables: %+v", running.Deliverables)
	}

	// Image deliverable keeps its real status.
	if v := compact("task-image"); v.Result == nil || len(v.Result.Deliverables) != 1 || v.Result.Deliverables[0] != "Logo variant (generating)" {
		t.Fatalf("compact image task: %+v", v.Result)
	}
	if image := full("task-image"); len(image.Deliverables) != 1 || image.Deliverables[0].Status != "generating" {
		t.Fatalf("detail=full image deliverables: %+v", image.Deliverables)
	}

	// The stored record is unchanged: showing a status never writes it.
	stored, found, err := store.GetProjectTask(account, project, "task-finished")
	if err != nil || !found {
		t.Fatalf("stored task: %v %v", found, err)
	}
	if stored.Deliverables[0].Status != "pending" || stored.Attempts[0].Deliverables[0].Status != "pending" {
		t.Fatalf("stored deliverables written: %+v / %+v", stored.Deliverables, stored.Attempts)
	}
}
