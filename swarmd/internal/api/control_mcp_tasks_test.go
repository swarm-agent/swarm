package api

import (
	"context"
	"encoding/json"
	"strings"
	"testing"
	"time"

	pebblestore "swarm/packages/swarmd/internal/store/pebble"
	"swarm/packages/swarmd/internal/worktree"
)

// Requirement: an outside AI delegates one piece of work in one hop
// (run_task), waits for it (wait_task) and reads what came back
// (task_result), with no Swarm-side orchestrator in between. Threats: a coder
// task started without a repository, a task created in a project the caller
// cannot read, a wait that polls or never wakes, and a result that hides the
// agent's report. Owners: controlMCPRunTask, waitForTask, controlMCPTaskResult
// over POST/GET /v3/projects/{id}/tasks[/{tid}]. The API mux with temporary
// stores runs the tool, the route allowlist, scopes and the task lifecycle
// together; no provider is configured, so a launched task may end failed, and
// the wake path is driven through the store's own task update.
func TestControlMCPDelegatesInOneHop(t *testing.T) {
	server, repo, db := newControlMCPProjectFixture(t)
	server.SetWorktreeService(worktree.NewService(pebblestore.NewWorktreeStore(db), server.workspace, nil))
	p := testPrincipal()
	ctx, cancel := context.WithTimeout(context.Background(), 60*time.Second)
	defer cancel()
	write := controlMCPDirectCall(ctx, p, server.apiMux(), server, "sessions:read", "sessions:write")
	read := controlMCPDirectCall(ctx, p, server.apiMux(), server, "sessions:read")
	store := server.sessions.Store()
	for i := 0; i < 5; i++ {
		if ready, err := store.BackfillRepositoryHistory(100); err != nil {
			t.Fatal(err)
		} else if ready {
			break
		}
	}

	text, isError := controlMCPToolText(t, write, "swarm_create_project", map[string]any{
		"name": "Code", "description": "The person's code", "workspaces": []any{map[string]any{"path": repo}},
	})
	if isError {
		t.Fatalf("create project: %s", text)
	}
	var created struct {
		Project struct {
			ID string `json:"id"`
		} `json:"project"`
	}
	if err := json.Unmarshal([]byte(text), &created); err != nil || created.Project.ID == "" {
		t.Fatalf("create project result: %s", text)
	}
	projectID := created.Project.ID
	if text, isError := controlMCPToolText(t, read, "swarm_get_project", map[string]any{"project_id": projectID, "wait_seconds": 20}); isError || !strings.Contains(text, `"settled":true`) {
		t.Fatalf("generation did not settle: %s", text)
	}

	// Read grants cannot delegate; a coder task needs its repository.
	if text, isError := controlMCPToolText(t, read, "swarm_run_task", map[string]any{"project_id": projectID, "role": "coder", "brief": "x", "workspace": repo}); !isError || !strings.Contains(text, "sessions:write") {
		t.Fatalf("read grant delegated: %s", text)
	}
	if text, isError := controlMCPToolText(t, write, "swarm_run_task", map[string]any{"project_id": projectID, "role": "coder", "brief": "Rename a thing"}); !isError || !strings.Contains(text, "workspace") {
		t.Fatalf("coder without workspace: %s", text)
	}
	if text, isError := controlMCPToolText(t, write, "swarm_run_task", map[string]any{"project_id": projectID, "role": "coder", "brief": "x", "workspace": t.TempDir()}); !isError || !strings.Contains(text, "not registered") {
		t.Fatalf("unregistered workspace: %s", text)
	}

	text, isError = controlMCPToolText(t, write, "swarm_run_task", map[string]any{
		"project_id": projectID, "role": "coder", "workspace": repo,
		"brief": "Add a CONTRIBUTING.md that explains the branch rules.\nCommit on the agent branch; do not push.",
		"files": []any{"CONTRIBUTING.md"},
	})
	if isError {
		t.Fatalf("run_task: %s", text)
	}
	var started struct {
		TaskID    string `json:"task_id"`
		SessionID string `json:"session_id"`
		Status    string `json:"status"`
		Role      string `json:"role"`
		Title     string `json:"title"`
		Next      string `json:"next"`
	}
	if err := json.Unmarshal([]byte(text), &started); err != nil || started.TaskID == "" {
		t.Fatalf("run_task result: %s", text)
	}
	if started.Role != "coder" || started.Title != "Add a CONTRIBUTING.md that explains the branch rules." || started.Next == "" {
		t.Fatalf("run_task result: %s", text)
	}
	record, found, err := store.GetProjectTask(p.AccountScopeID, projectID, started.TaskID)
	if err != nil || !found {
		t.Fatalf("task record: %v", err)
	}
	if record.AutoApprove != true || record.Agent != "coder" || !strings.Contains(record.Description, "Scope: only change files under CONTRIBUTING.md") {
		t.Fatalf("task record: %+v", record)
	}
	if record.Status == "pending_approval" {
		t.Fatalf("delegated task waits for approval: %s", record.Status)
	}
	if started.SessionID != "" && started.SessionID != record.SessionID {
		t.Fatalf("session id %q differs from the record's %q", started.SessionID, record.SessionID)
	}

	// The result names the branch and report fields it has, without failing on
	// a task that could not run (no provider here).
	text, isError = controlMCPToolText(t, read, "swarm_task_result", map[string]any{"project_id": projectID, "task_id": started.TaskID})
	if isError {
		t.Fatalf("task_result: %s", text)
	}
	var result map[string]any
	if err := json.Unmarshal([]byte(text), &result); err != nil || result["task_id"] != started.TaskID || result["role"] != "coder" || result["status"] == nil {
		t.Fatalf("task_result: %s", text)
	}
	if _, isError := controlMCPToolText(t, read, "swarm_task_result", map[string]any{"project_id": projectID, "task_id": "nope"}); !isError {
		t.Fatal("unknown task returned a result")
	}

	// Waiting wakes on the task's own update, not a timer: a task with no
	// session is moved to needs_review through the store while a wait runs.
	waiting := &pebblestore.ProjectTaskRecord{ID: "task_wait", ProjectID: projectID, Title: "Clip", Agent: "video", Status: "in_progress"}
	if err := store.PutProjectTask(p.AccountScopeID, waiting); err != nil {
		t.Fatal(err)
	}
	text, isError = controlMCPToolText(t, read, "swarm_wait_task", map[string]any{"project_id": projectID, "task_id": "task_wait", "wait_seconds": 1})
	if isError || !strings.Contains(text, `"timed_out":true`) || !strings.Contains(text, `"status":"in_progress"`) {
		t.Fatalf("wait on a running task: %s", text)
	}
	done := make(chan string, 1)
	go func() {
		text, _ := controlMCPToolText(t, read, "swarm_wait_task", map[string]any{"project_id": projectID, "task_id": "task_wait", "wait_seconds": 20})
		done <- text
	}()
	time.Sleep(300 * time.Millisecond)
	if _, err := store.UpdateProjectTask(p.AccountScopeID, projectID, "task_wait", func(task *pebblestore.ProjectTaskRecord) error {
		task.Status = "needs_review"
		return nil
	}); err != nil {
		t.Fatal(err)
	}
	select {
	case text := <-done:
		if !strings.Contains(text, `"settled":true`) || !strings.Contains(text, `"status":"needs_review"`) || strings.Contains(text, `"timed_out":true`) || !strings.Contains(text, "integrate_task") {
			t.Fatalf("wait did not settle on the update: %s", text)
		}
	case <-time.After(15 * time.Second):
		t.Fatal("wait did not wake on the task update")
	}
}
