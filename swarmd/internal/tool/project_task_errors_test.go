package tool_test

import (
	"context"
	"encoding/json"
	"strings"
	"testing"
	"time"

	"swarm/packages/swarmd/internal/identity"
	sessionruntime "swarm/packages/swarmd/internal/session"
	pebblestore "swarm/packages/swarmd/internal/store/pebble"
	"swarm/packages/swarmd/internal/tool"
)

// sourcesLifecycle answers list_sources for a folder without a repository;
// task lifecycle calls are not used here.
type sourcesLifecycle struct{}

func (sourcesLifecycle) CreateProjectTask(context.Context, identity.Principal, string, tool.ProjectTaskCreateInput) (*pebblestore.ProjectTaskRecord, error) {
	return nil, nil
}
func (sourcesLifecycle) DeployProjectTask(context.Context, identity.Principal, string, string) error {
	return nil
}
func (sourcesLifecycle) ApproveProjectTask(context.Context, identity.Principal, string, string, ...tool.ProjectTaskApprovalGuards) (*pebblestore.ProjectTaskRecord, error) {
	return nil, nil
}
func (sourcesLifecycle) SubmitProjectTaskPlan(context.Context, sessionruntime.ProjectTaskPlanSubmissionInput) (sessionruntime.ProjectTaskPlanSubmissionResult, error) {
	return sessionruntime.ProjectTaskPlanSubmissionResult{}, nil
}
func (sourcesLifecycle) InspectProjectSources(context.Context, identity.Principal, string, string, string, int64, bool) (map[string]any, error) {
	return map[string]any{"ready": false, "repository": map[string]any{"state": "not_repository"}}, nil
}

// Purpose: the Orchestrator's task errors say what to do next. Requirement:
// a missing project_id names the listing action; a wrong project or task id
// is echoed with the action that lists the right ones; local wait_tasks
// checks every id before recording anything and names the bad one (unknown,
// listed twice, archived, or pending_approval, which the person starts in the
// app; never "linked" wording on this machine, never "obtain approval");
// list_sources for a folder without a repository asks the person to ready it
// in the app, never naming request_workspace_setup, a tool the Orchestrator
// does not have.
// Threat: the live failures: errors the agent cannot act on, an agent that
// chases approval, and a next_step naming a tool it cannot call.
// Owners: executeManageProjectTasksContext (projectNotFoundError,
// projectTaskNotFoundError, checkProjectTaskWaitIDs), the store's wait
// registration (ProjectTaskNotStartedReason) and executeManageProjects
// list_sources, over real Pebble session and task stores through the tool
// entry, the narrowest layer that returns the agent-visible text.
func TestOrchestratorTaskErrorsNameTheFix(t *testing.T) {
	f := newProjectFolderFixture(t)
	account := f.scope.Principal.AccountScopeID
	for _, task := range []pebblestore.ProjectTaskRecord{
		{ID: "waiting-approval", Title: "Waits for the person", Agent: "coder", Status: "pending_approval"},
		{ID: "old-one", Title: "Archived", Agent: "coder", Status: "in_progress", Archived: true, SessionID: "old-session", ActiveAttemptID: "a1", Attempts: []pebblestore.ProjectTaskAttempt{{ID: "a1", SessionID: "old-session", Role: "coder", Status: "in_progress"}}},
		{ID: "running", Title: "Running", Agent: "coder", Status: "in_progress", SessionID: "run-session", ActiveAttemptID: "a2", Attempts: []pebblestore.ProjectTaskAttempt{{ID: "a2", SessionID: "run-session", Role: "coder", Status: "in_progress"}}},
	} {
		task.ProjectID, task.AccountID = "proj-folders", account
		if err := f.store.PutProjectTask(account, &task); err != nil {
			t.Fatal(err)
		}
	}
	ctx := tool.WithVideoRunContext(context.Background(), tool.VideoRunContext{SessionID: f.scope.SessionID, RunID: "run-1"})
	manage := func(args map[string]any) (string, error) {
		t.Helper()
		raw, _ := json.Marshal(args)
		callCtx, cancel := context.WithTimeout(ctx, 20*time.Second)
		defer cancel()
		return f.runtime.ExecuteForWorkspaceScopeWithRuntime(callCtx, f.scope, tool.Call{Name: "manage_projects", Arguments: string(raw)})
	}
	expect := func(name string, args map[string]any, want []string, never []string) {
		t.Helper()
		out, err := manage(args)
		if err == nil {
			t.Fatalf("%s: accepted: %s", name, out)
		}
		for _, text := range want {
			if !strings.Contains(err.Error(), text) {
				t.Fatalf("%s: error %q does not say %q", name, err, text)
			}
		}
		for _, text := range never {
			if strings.Contains(err.Error(), text) {
				t.Fatalf("%s: error %q says %q", name, err, text)
			}
		}
	}
	expect("missing project_id", map[string]any{"action": "list_tasks"}, []string{"project_id is required", "manage_projects action=list"}, nil)
	expect("wrong project", map[string]any{"action": "list_tasks", "project_id": "proj-nope"}, []string{`project "proj-nope" not found`, "manage_projects action=list"}, nil)
	expect("wrong task", map[string]any{"action": "get_task", "project_id": "proj-folders", "task_id": "task-nope"}, []string{`task "task-nope" not found in project "proj-folders"`, "action=list_tasks"}, nil)

	wait := func(ids ...string) map[string]any {
		return map[string]any{"action": "wait_tasks", "project_id": "proj-folders", "task_ids": ids}
	}
	local := []string{"linked", "obtain task approval", "deployment"}
	expect("unknown wait id", wait("running", "task-nope"), []string{`task "task-nope" not found`, "list_tasks"}, local)
	expect("pending wait id", wait("waiting-approval"), []string{"task waiting-approval is pending_approval", "the person starts it in the app", "Do not ask for or chase the approval"}, local)
	expect("duplicate wait id", wait("running", "running"), []string{"lists running twice"}, local)
	expect("archived wait id", wait("old-one"), []string{"task old-one is archived", "unarchive_task"}, local)
	// Nothing was recorded for the refused waits.
	if _, found, err := f.store.GetV3SessionRunIntent(f.scope.SessionID, "run-1"); err != nil || found {
		t.Fatalf("a refused wait recorded a run intent: %v %v", err, found)
	}

	f.runtime.SetProjectTaskLifecycleService(sourcesLifecycle{})
	out, err := manage(map[string]any{"action": "list_sources", "project_id": "proj-folders"})
	if err != nil {
		t.Fatal(err)
	}
	// The action's own answer is the envelope's data; its next step is
	// also the envelope's next_action.
	data, err := tool.EnvelopeData(out)
	if err != nil {
		t.Fatal(err)
	}
	var sources map[string]any
	if err := json.Unmarshal(data, &sources); err != nil {
		t.Fatal(err)
	}
	next, _ := sources["next_step"].(string)
	if !strings.Contains(next, "Ask the person to ready this folder in the app") || strings.Contains(next, "request_workspace_setup") {
		t.Fatalf("list_sources next_step = %q", next)
	}
	var envelope tool.Envelope
	if err := json.Unmarshal([]byte(out), &envelope); err != nil || envelope.NextAction != next {
		t.Fatalf("list_sources envelope next_action = %q, want the next step: %v", envelope.NextAction, err)
	}
}
