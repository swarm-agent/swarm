package tool

import (
	"context"
	"encoding/json"
	"strings"
	"testing"

	"swarm/packages/swarmd/internal/identity"
	pebblestore "swarm/packages/swarmd/internal/store/pebble"
)

// linkedRecordsService is a lifecycle service holding only the home's
// records of tasks sent to linked machines.
type linkedRecordsService struct {
	ProjectTaskLifecycleService
	bySession map[string][]pebblestore.FleetLinkedTask
	bySender  map[string][]pebblestore.FleetLinkedTask
}

func (s linkedRecordsService) RecordFleetLinkedTask(context.Context, identity.Principal, FleetLinkedTaskOrigin, string, string, json.RawMessage) (pebblestore.FleetLinkedTask, error) {
	return pebblestore.FleetLinkedTask{}, nil
}

func (s linkedRecordsService) ListFleetLinkedTasks(_ context.Context, _ identity.Principal, sessionID, _ string) ([]pebblestore.FleetLinkedTask, error) {
	return s.bySession[sessionID], nil
}

func (s linkedRecordsService) ReadFleetLinkedTaskSession(context.Context, identity.Principal, string, string, string, FleetTaskSessionRead) (map[string]any, error) {
	return nil, nil
}

func (s linkedRecordsService) ListFleetLinkedTasksBySender(_ context.Context, _ identity.Principal, sender string) ([]pebblestore.FleetLinkedTask, error) {
	return s.bySender[sender], nil
}

// Purpose: a task action named by its task id alone is accepted where the
// machine can find the task's project: here from the account's projects
// (get_task end to end), on a linked machine from the home's records of the
// tasks this conversation, or this Swarm Control caller, sent there. Threat:
// the parity defect where wait_tasks/get_task/read_task_session over MCP on a
// linked machine failed with "project_id is required" though the task id was
// enough; and a lookup that reaches another account's task or another
// caller's sends, or guesses between two projects. Negative cases: an unknown
// id, another account's task and another caller's send leave the call as it
// was (the action refuses it with its own reason); an id in two projects is
// refused naming them; a given project_id is never replaced.
// Authority: Runtime.locateTaskProject in manageProjectsAnswer over a temp
// Pebble store and the linked-records interfaces the API server implements.
func TestTaskActionsFindTheProjectFromTheTaskID(t *testing.T) {
	_, store := openTaskViewStore(t)
	const account = "acct-locate"
	for _, project := range []string{"proj-a", "proj-b"} {
		if err := store.PutProject(account, &pebblestore.ProjectRecord{ID: project, AccountID: account, Name: project}); err != nil {
			t.Fatal(err)
		}
	}
	if err := store.PutProject("acct-other", &pebblestore.ProjectRecord{ID: "proj-x", AccountID: "acct-other", Name: "x"}); err != nil {
		t.Fatal(err)
	}
	put := func(account, project, id string) {
		t.Helper()
		if err := store.PutProjectTask(account, heavyFinishedTask(account, project, id)); err != nil {
			t.Fatal(err)
		}
	}
	put(account, "proj-b", "task-only-b")
	put(account, "proj-a", "task-twice")
	put(account, "proj-b", "task-twice")
	put("acct-other", "proj-x", "task-other")

	r := NewRuntime(1)
	defer r.Close()
	r.SetManageProjectStore(store)
	r.SetProjectTaskLifecycleService(linkedRecordsService{
		bySession: map[string][]pebblestore.FleetLinkedTask{"orch-1": {{Machine: "box", ProjectID: "remote-proj", TaskID: "rt-1"}, {Machine: "other-box", ProjectID: "elsewhere", TaskID: "rt-2"}}},
		bySender:  map[string][]pebblestore.FleetLinkedTask{"key:k1": {{Machine: "box", ProjectID: "remote-proj-2", TaskID: "rt-3"}}},
	})
	scope := WorkspaceScope{SessionID: "orch-1", Principal: identity.Principal{Type: identity.PrincipalTypeUser, UserID: "user-locate", AccountScopeID: account}}
	locate := func(scope WorkspaceScope, args map[string]any) (any, error) {
		t.Helper()
		out, err := r.locateTaskProject(context.Background(), scope, args)
		return out["project_id"], err
	}

	// Here: the account's one project holding the task; get_task answers.
	if project, err := locate(scope, map[string]any{"action": "get_task", "task_id": "task-only-b"}); err != nil || project != "proj-b" {
		t.Fatalf("local: %v %v", project, err)
	}
	raw, _, err := runManageProjects(t, r, scope, map[string]any{"action": "get_task", "task_id": "task-only-b"})
	if err != nil || !strings.Contains(raw, `"task-only-b"`) {
		t.Fatalf("get_task without project_id: %s %v", raw, err)
	}
	if project, err := locate(scope, map[string]any{"action": "wait_tasks", "task_ids": []any{"task-only-b"}}); err != nil || project != "proj-b" {
		t.Fatalf("wait_tasks: %v %v", project, err)
	}
	// Two projects: refused, naming both; nothing guessed.
	if _, err := locate(scope, map[string]any{"action": "get_task", "task_id": "task-twice"}); err == nil || !strings.Contains(err.Error(), "proj-a, proj-b") || !strings.Contains(err.Error(), "pass project_id") {
		t.Fatalf("ambiguous: %v", err)
	}
	// Unknown, or another account's: left for the action to refuse.
	for _, id := range []string{"task-missing", "task-other"} {
		if project, err := locate(scope, map[string]any{"action": "get_task", "task_id": id}); err != nil || project != nil {
			t.Fatalf("%s: %v %v", id, project, err)
		}
	}
	if _, _, err := runManageProjects(t, r, scope, map[string]any{"action": "get_task", "task_id": "task-other"}); err == nil || !strings.Contains(err.Error(), "project_id is required") {
		t.Fatalf("another account's task: %v", err)
	}
	// A given project_id is kept; actions that do not find it are untouched.
	if project, _ := locate(scope, map[string]any{"action": "get_task", "project_id": "proj-a", "task_id": "task-only-b"}); project != "proj-a" {
		t.Fatalf("given project_id replaced: %v", project)
	}
	if project, _ := locate(scope, map[string]any{"action": "report_task", "task_id": "task-only-b"}); project != nil {
		t.Fatalf("report_task located: %v", project)
	}

	// On a linked machine: this conversation's sends there.
	if project, err := locate(scope, map[string]any{"action": "read_task_session", "machine": "box", "task_id": "rt-1"}); err != nil || project != "remote-proj" {
		t.Fatalf("linked: %v %v", project, err)
	}
	if project, _ := locate(scope, map[string]any{"action": "get_task", "machine": "box", "task_id": "rt-2"}); project != nil {
		t.Fatalf("a send to another machine matched: %v", project)
	}
	// A Swarm Control caller: its own sends, never the conversation's.
	control := WorkspaceScope{ControlCaller: true, ControlSender: "key:k1", Principal: scope.Principal}
	if project, err := locate(control, map[string]any{"action": "wait_tasks", "machine": "box", "task_ids": []any{"rt-3"}}); err != nil || project != "remote-proj-2" {
		t.Fatalf("control linked: %v %v", project, err)
	}
	if project, _ := locate(control, map[string]any{"action": "get_task", "machine": "box", "task_id": "rt-1"}); project != nil {
		t.Fatalf("a control caller found another sender's task: %v", project)
	}
}

// Purpose: every client knows before calling which inputs an action needs:
// the generated per-action schema lists them as required, the Swarm Control
// action descriptions and the Orchestrator's action field state them, and a
// task action says project_id may be omitted. Threat: the parity defect where
// declared descriptions never said get_task, wait_tasks and
// read_task_session need inputs a linked machine refused without. Every
// required input is one the action takes. Authority: CatalogAction.Required,
// RequirementNote, CatalogTool.InputSchema/ControlSchema and the
// Orchestrator definition, generated from manageProjectsCatalog.
func TestCatalogDeclaresRequiredInputs(t *testing.T) {
	catalog := manageProjectsCatalog()
	for _, tool := range Catalog() {
		for _, action := range tool.Actions {
			for _, input := range action.Required {
				if !envelopeTakes(action, input) {
					t.Fatalf("%s %s requires %q, which it does not take", tool.Name, action.Action, input)
				}
			}
			// Every tool: the generated schema lists the declared inputs
			// as required and the description has its "Requires:" line;
			// a call missing one is refused, a call with all of them is not.
			if len(action.Required) == 0 {
				continue
			}
			required, _ := tool.InputSchema(action)["required"].([]string)
			if strings.Join(required, ",") != "action,"+strings.Join(action.Required, ",") {
				t.Fatalf("%s %s schema required = %v, declared %v", tool.Name, action.Action, required, action.Required)
			}
			if !strings.Contains(action.RequirementNote(), "Requires: "+strings.Join(action.Required, ", ")) {
				t.Fatalf("%s %s note = %q", tool.Name, action.Action, action.RequirementNote())
			}
			full := map[string]any{"action": action.Action}
			for _, input := range action.Required {
				full[input] = "x"
			}
			if err := CatalogCallRequired("", tool.Name, full); err != nil {
				t.Fatalf("%s %s refused a call with every required input: %v", tool.Name, action.Action, err)
			}
			delete(full, action.Required[0])
			if err := CatalogCallRequired("", strings.ReplaceAll(tool.Name, "_", "-"), full); err == nil || !strings.Contains(err.Error(), action.Required[0]+" is required for "+tool.Name+" "+action.Action) {
				t.Fatalf("%s %s without %s: %v", tool.Name, action.Action, action.Required[0], err)
			}
			// Another agent's call to an Orchestrator-only action is left to
			// the handler's authority check, which refuses it first; any
			// other action is checked for every agent (manage_jobs grants
			// name the agent, the Orchestrator's own session is "swarm").
			if err := CatalogCallRequired("system-coder", tool.Name, full); (err == nil) != catalogOrchestratorOnlyAction(tool.Name, action.Action) {
				t.Fatalf("%s %s: another agent's call without %s: %v", tool.Name, action.Action, action.Required[0], err)
			}
			if err := CatalogCallRequired("swarm-orchestrator", tool.Name, full); err == nil {
				t.Fatalf("%s %s: the Orchestrator's call without %s passed", tool.Name, action.Action, action.Required[0])
			}
		}
	}
	for name, want := range map[string][]string{
		"get_task": {"task_id"}, "wait_tasks": {"task_ids"}, "read_task_session": {"machine", "task_id"}, "message_task": {"task_id", "message"}, "list_tasks": {"project_id"},
	} {
		action, _ := catalog.Action(name)
		required, _ := catalog.InputSchema(action)["required"].([]string)
		if strings.Join(required, ",") != "action,"+strings.Join(want, ",") {
			t.Fatalf("%s schema required = %v", name, required)
		}
		note := action.RequirementNote()
		if !strings.Contains(note, "Requires: "+strings.Join(want, ", ")) {
			t.Fatalf("%s note = %q", name, note)
		}
		if name != "list_tasks" && !strings.Contains(note, "project_id may be omitted") {
			t.Fatalf("%s does not say project_id may be omitted: %q", name, note)
		}
	}
	schema, _ := catalog.ControlSchema(func(a CatalogAction) bool { return a.ControlCallable() })
	control := schema["properties"].(map[string]any)["action"].(map[string]any)["description"].(string)
	if !strings.Contains(control, "get_task: Read a task's state") || !strings.Contains(control, "Requires: task_id; project_id may be omitted") {
		t.Fatalf("Swarm Control descriptions: %s", control)
	}
	orchestrator, _ := manageProjectsDefinition().Parameters["properties"].(map[string]any)["action"].(map[string]any)["description"].(string)
	if !strings.Contains(orchestrator, "Required inputs: ") || !strings.Contains(orchestrator, "get_task task_id") || !strings.Contains(orchestrator, "project_id may be omitted") {
		t.Fatalf("Orchestrator action field: %q", orchestrator)
	}
}
