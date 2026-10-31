package tool

import (
	"reflect"
	"testing"

	"swarm/packages/swarmd/internal/identity"
	"swarm/packages/swarmd/internal/session"
	pebblestore "swarm/packages/swarmd/internal/store/pebble"
)

// Purpose:
//   - Requirement: a manage_projects action takes its swarm_* tool's inputs
//     under the same name and meaning: update's project_id and project_md,
//     wait_tasks' task_id, propose_task's brief, workspace and files (the
//     scope sentence swarm_run_task wrote); the action's own input wins, and
//     the swarm_* names never travel on (a linked machine sees only the
//     action's own inputs, and files is not appended twice).
//   - Threat/regression: a swarm_* input silently ignored, a scope written
//     twice through a link, or a swarm_* name overriding the action's own.
//   - Boundary/authority: manageProjectsSwarmArgs, TaskFilesScope,
//     ProjectContextRetryCall.
//   - Narrowest layer: the pure mapping functions.
func TestManageProjectsTakesTheSwarmToolInputs(t *testing.T) {
	cases := []struct {
		in, want map[string]any
	}{
		{map[string]any{"action": "update", "project_id": "p1", "project_md": "# P"}, map[string]any{"action": "update", "id": "p1", "project_context": "# P"}},
		{map[string]any{"action": "update", "id": "own", "project_id": "p1", "project_context": "mine", "project_md": "theirs"}, map[string]any{"action": "update", "id": "own", "project_context": "mine"}},
		{map[string]any{"action": "wait_tasks", "project_id": "p1", "task_id": "t1"}, map[string]any{"action": "wait_tasks", "project_id": "p1", "task_ids": []any{"t1"}}},
		{map[string]any{"action": "wait_tasks", "task_ids": []any{"t2"}, "task_id": "t1"}, map[string]any{"action": "wait_tasks", "task_ids": []any{"t2"}}},
		{map[string]any{"action": "propose_task", "brief": "Do it", "workspace": "/repo/./a", "files": []any{"src/", " ", "docs"}}, map[string]any{"action": "propose_task", "prompt": "Do it" + TaskFilesScope([]any{"src/", "docs"}), "workspace_path": "/repo/a"}},
		{map[string]any{"action": "propose_task", "prompt": "Own", "brief": "Theirs", "workspace": "ws_1", "workspace_path": "/own"}, map[string]any{"action": "propose_task", "prompt": "Own", "workspace_path": "/own"}},
		{map[string]any{"action": "propose_task", "brief": "Do it", "workspace": "ws_1"}, map[string]any{"action": "propose_task", "prompt": "Do it", "workspace_id": "ws_1"}},
		{map[string]any{"action": "list", "project_id": "p1"}, map[string]any{"action": "list", "project_id": "p1"}},
	}
	for _, c := range cases {
		if got := manageProjectsSwarmArgs(c.in); !reflect.DeepEqual(got, c.want) {
			t.Fatalf("manageProjectsSwarmArgs(%v) = %v, want %v", c.in, got, c.want)
		}
	}
	// The mapped call is mapped again on a linked machine: unchanged.
	once := manageProjectsSwarmArgs(map[string]any{"action": "propose_task", "brief": "Do it", "files": []any{"src/"}})
	if twice := manageProjectsSwarmArgs(once); !reflect.DeepEqual(once, twice) {
		t.Fatalf("mapping twice changed the call: %v -> %v", once, twice)
	}
	if got := TaskFilesScope([]any{"src/"}); got != "\n\nScope: only change files under src/. Anything else is out of scope; say so instead of touching it." {
		t.Fatalf("scope sentence: %q", got)
	}
	if TaskFilesScope(nil) != "" || TaskFilesScope([]any{" "}) != "" {
		t.Fatal("no files must add no scope")
	}
	// update writes project.md again only when it names the project and
	// changes nothing.
	for args, want := range map[*map[string]any]bool{
		{"action": "update", "id": "p1"}: true,
		{"id": "p1"}:                     false,
		{"action": "update", "id": "p1", "description": ""}:      false,
		{"action": "update", "id": "p1", "workspaces": []any{}}:  false,
		{"action": "update", "id": "p1", "project_context": "x"}: false,
		{"action": "update"}:          false,
		{"action": "get", "id": "p1"}: false,
	} {
		if got := ProjectContextRetryCall(*args); got != want {
			t.Fatalf("ProjectContextRetryCall(%v) = %v, want %v", *args, got, want)
		}
	}
}

// Purpose:
//   - Requirement: a conversation's manage_projects call drops the action's
//     Swarm Control inputs (propose_task's model and wait_seconds, get's and
//     wait_tasks' and the folder environments' wait_seconds) before it runs
//     here or goes to a linked machine, which runs a linked call as a Swarm
//     Control one; a Swarm Control caller keeps them.
//   - Threat/regression: the Orchestrator choosing a task's model or holding
//     a call on a linked machine through manage_projects.
//   - Boundary/authority: CatalogConversationArgs over the declared
//     ControlInputs, as executeManageProjects applies it first.
//   - Narrowest layer: the filter over the real declarations.
func TestManageProjectsConversationDropsSwarmControlInputs(t *testing.T) {
	catalog := manageProjectsDeclared()
	checked := 0
	for _, declared := range catalog.Actions {
		if len(declared.ControlInputs) == 0 {
			continue
		}
		checked++
		call := map[string]any{"action": declared.Action, "machine": "box"}
		for _, key := range declared.ControlInputs {
			call[key] = "x"
		}
		conversation := CatalogConversationArgs(declared, false, call)
		for _, key := range declared.ControlInputs {
			if _, kept := conversation[key]; kept {
				t.Fatalf("%s: a conversation kept Swarm Control input %s", declared.Action, key)
			}
		}
		if conversation["machine"] != "box" || len(call) != len(declared.ControlInputs)+2 {
			t.Fatalf("%s: the filter lost an own input or changed the caller's map", declared.Action)
		}
		if control := CatalogConversationArgs(declared, true, call); !reflect.DeepEqual(control, call) {
			t.Fatalf("%s: a Swarm Control call lost inputs", declared.Action)
		}
	}
	if checked < 4 {
		t.Fatalf("only %d manage_projects actions have Swarm Control inputs", checked)
	}
}

// Purpose:
//   - Requirement: propose_task's answer data carries swarm_run_task's
//     answer fields under the same names and values: status (the task's
//     status), role (its agent), title and base_branch, besides task_id,
//     session_id, workspace_path and worktree_branch, in the compact
//     answer the Orchestrator and Swarm Control share.
//   - Threat/regression: a Swarm Control client losing swarm_run_task's
//     status, role, title or base_branch when the swarm_* tool goes.
//   - Boundary/authority: Runtime.executeManageProjects propose_task with
//     ProposedTaskSwarmFields, through compactProjectTaskMutation.
//   - Narrowest layer: the runtime with a real temp store and the recording
//     lifecycle TestManageProjectsTaskMutationsAnswerCompactRow uses (no
//     provider, no worktree service).
func TestProposeTaskAnswersSwarmRunTaskFields(t *testing.T) {
	db, store := openTaskViewStore(t)
	const account, project = "acct-propose-fields", "proj-propose-fields"
	if err := db.PutJSON(pebblestore.KeySession("s"), pebblestore.SessionSnapshot{ID: "s", AccountScopeID: account, UserID: "u"}); err != nil {
		t.Fatal(err)
	}
	if err := store.PutProject(account, &pebblestore.ProjectRecord{ID: project, Name: "P"}); err != nil {
		t.Fatal(err)
	}
	rules := pebblestore.DefaultAccountPermissions()
	rules.Tasks = "auto"
	if _, err := db.SetAccountPermissions(account, 0, rules); err != nil {
		t.Fatal(err)
	}
	r := NewRuntime(1)
	defer r.Close()
	r.SetManageSessionService(session.NewService(store, nil))
	r.SetManageProjectStore(store)
	r.SetProjectTaskLifecycleService(&heavyLifecycle{})
	scope := WorkspaceScope{SessionID: "s", Principal: identity.Principal{Type: identity.PrincipalTypeUser, UserID: "u", AccountScopeID: account}}
	raw, out, err := runManageProjects(t, r, scope, map[string]any{"action": "propose_task", "project_id": project, "title": "Keep comments in the parser", "agent": "image"})
	if err != nil {
		t.Fatal(err)
	}
	task, _ := out["task"].(map[string]any)
	if out["status"] != out["task_status"] || out["status"] == nil || out["role"] != task["agent"] || out["title"] != "Keep comments in the parser" {
		t.Fatalf("propose_task lacks swarm_run_task's status, role or title: %s", raw)
	}
	for _, key := range []string{"task_id", "session_id"} {
		if out[key] == nil || (key == "session_id" && out[key] != task["session_id"]) {
			t.Fatalf("propose_task lacks %s: %s", key, raw)
		}
	}
	record := pebblestore.ProjectTaskRecord{Status: "queued", Agent: "coder", Title: "T", BaseBranch: "dev"}
	if got := ProposedTaskSwarmFields(record); !reflect.DeepEqual(got, map[string]any{"status": "queued", "role": "coder", "title": "T", "base_branch": "dev"}) {
		t.Fatalf("ProposedTaskSwarmFields: %v", got)
	}
	if _, ok := ProposedTaskSwarmFields(pebblestore.ProjectTaskRecord{})["base_branch"]; ok {
		t.Fatal("an empty base_branch is answered, as swarm_run_task does not")
	}
}

// Purpose:
//   - Requirement: only a Swarm Control call runs a swarm_* session tool's
//     handler, and only for that tool's own inputs: create with plan,
//     attachments, project_id or access (swarm_start_session); send_message
//     that starts a run with attachments or a client_request_id
//     (swarm_send_message, whose key is an idempotency key). A conversation
//     never does; attachments on a note are refused; content is prompt.
//   - Threat/regression: a conversation reaching the swarm_* route, or the
//     runtime's own send_message (and its controller) replaced for every
//     Swarm Control message.
//   - Boundary/authority: manageSessionsSwarmRoute, manageSessionsSwarmArgs.
//   - Narrowest layer: the pure routing functions.
func TestManageSessionsSwarmRouteIsSwarmControlsOnly(t *testing.T) {
	cases := []struct {
		action  string
		control bool
		args    map[string]any
		want    string
		refused bool
	}{
		{"create", true, map[string]any{"prompt": "p", "workspace_path": "/w"}, "", false},
		{"create", true, map[string]any{"plan": map[string]any{}}, "start_session", false},
		{"create", true, map[string]any{"project_id": "p", "prompt": "p"}, "start_session", false},
		{"create", true, map[string]any{"access": map[string]any{}}, "start_session", false},
		{"create", false, map[string]any{"plan": map[string]any{}, "access": map[string]any{}}, "", false},
		{"send_message", true, map[string]any{"prompt": "p"}, "", false},
		{"send_message", true, map[string]any{"prompt": "p", "client_request_id": "k"}, "send_message", false},
		{"send_message", true, map[string]any{"prompt": "p", "attachments": []any{}}, "send_message", false},
		{"send_message", true, map[string]any{"prompt": "p", "trigger": false, "client_request_id": "k"}, "", false},
		{"send_message", true, map[string]any{"prompt": "p", "trigger": false, "attachments": []any{}}, "", true},
		{"send_message", false, map[string]any{"prompt": "p", "client_request_id": "k", "attachments": []any{}}, "", false},
		{"get", true, map[string]any{"session_id": "s", "wait_seconds": float64(3)}, "", false},
	}
	for _, c := range cases {
		got, err := manageSessionsSwarmRoute(c.action, c.control, c.args)
		if (err != nil) != c.refused || got != c.want {
			t.Fatalf("%s control=%v %v: %q %v, want %q refused=%v", c.action, c.control, c.args, got, err, c.want, c.refused)
		}
	}
	args := map[string]any{"content": "Go."}
	manageSessionsSwarmArgs("send_message", args)
	if args["prompt"] != "Go." || args["content"] != nil {
		t.Fatalf("content is not prompt: %v", args)
	}
	args = map[string]any{"prompt": "Own.", "content": "Theirs."}
	manageSessionsSwarmArgs("send_message", args)
	if args["prompt"] != "Own." || args["content"] != nil {
		t.Fatalf("content overrode prompt: %v", args)
	}
}
