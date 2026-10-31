package api

import (
	"reflect"
	"strings"
	pebblestore "swarm/packages/swarmd/internal/store/pebble"
	"testing"
)

// Purpose:
//   - Requirement: manage_projects takes the swarm_* project tools' Swarm
//     Control inputs with those tools' own code: get's wait_seconds answers
//     swarm_get_project's waited, propose_task's model is checked as
//     swarm_run_task checks it, get_task stays the Orchestrator's answer,
//     and a folder environment named by project/workspace (or none) runs
//     swarm_get_environment's handler. A conversation's call never gets
//     them: the Orchestrator keeps its own compact answers.
//   - Threat/regression: a Swarm Control client losing a wait, a model
//     choice or an answer field when its swarm_* tool goes; an unchecked
//     model creating a task; Swarm Control answers drifting from the
//     Orchestrator's.
//   - Boundary/authority: Runtime.manageProjectsControlBefore and
//     manageProjectsControlAfter over Server.ControlSwarmAnswer and
//     ControlModelPreference; folderEnvironmentSwarmShaped over
//     catalogSwarmEnvironment.
//   - Narrowest layer: Swarm Control's tools/call and the Orchestrator's
//     runtime call on the in-process harness; no provider runs.
func TestManageProjectsSwarmControlInputsRunTheSwarmToolsCode(t *testing.T) {
	h := newCatalogParityHarness(t)
	data := func(out string) map[string]any {
		t.Helper()
		object, _ := parityDecode(t, out)["data"].(map[string]any)
		return object
	}
	// get wait_seconds: swarm_get_project's waited (project.md is not
	// generating, so it settles at once).
	out, refused := h.controlTool(t, catalogParityFullKey, "manage_projects", map[string]any{"action": "get", "project_id": "home-project", "wait_seconds": float64(1)})
	if refused != "" {
		t.Fatalf("get wait_seconds: %s", refused)
	}
	if waited, _ := data(out)["waited"].(map[string]any); waited["settled"] != true {
		t.Fatalf("get wait_seconds answered no settled wait: %s", out)
	}
	if out, refused := h.controlTool(t, catalogParityFullKey, "manage_projects", map[string]any{"action": "get", "project_id": "home-project"}); refused != "" || data(out)["waited"] != nil {
		t.Fatalf("get without wait_seconds waited: %q %s", refused, out)
	}

	// get_task stays the Orchestrator's answer (TestCatalogParityOrchestratorAndControl):
	// swarm_task_result's fields are not merged into it.
	out, refused = h.controlTool(t, catalogParityFullKey, "manage_projects", map[string]any{"action": "get_task", "project_id": "home-project", "task_id": "h1"})
	if refused != "" {
		t.Fatalf("get_task: %s", refused)
	}
	if _, merged := data(out)["role"]; merged {
		t.Fatalf("Swarm Control get_task carries swarm_task_result's flat fields: %s", out)
	}

	// propose_task model: refused as swarm_run_task refuses it, and no
	// task is made.
	h.wireModelCatalog(t)
	store := h.home.s.sessions.Store()
	before, err := store.ListProjectTasks(h.home.account, "home-project", 500)
	if err != nil {
		t.Fatal(err)
	}
	_, refused = h.controlTool(t, catalogParityFullKey, "manage_projects", map[string]any{"action": "propose_task", "project_id": "home-project", "prompt": "Probe.", "agent": "swarm", "model": map[string]any{"provider": "no-such-provider", "model": "no-such-model", "thinking": "low"}})
	if !strings.Contains(refused, "is not in the catalog") {
		t.Fatalf("an unknown model was not refused by swarm_run_task's check: %q", refused)
	}
	after, err := store.ListProjectTasks(h.home.account, "home-project", 500)
	if err != nil {
		t.Fatal(err)
	}
	if len(after) != len(before) {
		t.Fatalf("a refused model still made a task: %d -> %d", len(before), len(after))
	}

	// A folder environment without workspace_path is swarm_get_environment's
	// (it names the project's folder); the folder environment service would
	// ask for workspace_path instead.
	_, refused = h.controlTool(t, catalogParityFullKey, "manage_projects", map[string]any{"action": "get_environment"})
	if !strings.Contains(refused, "name the project") {
		t.Fatalf("get_environment without a folder did not run swarm_get_environment's handler: %q", refused)
	}
	if _, refused = h.controlTool(t, catalogParityFullKey, "manage_projects", map[string]any{"action": "get_environment", "workspace_path": t.TempDir()}); strings.Contains(refused, "name the project") {
		t.Fatalf("a folder named by workspace_path went to swarm_get_environment's handler: %q", refused)
	}
}

// Purpose:
//   - Requirement: manage_workers takes the swarm_* worker tools' inputs:
//     list's include_jobs (each worker's latest jobs) for every caller, and
//     the immediate changes (create's folder or preset, update's agent,
//     run_id and schedule_id) for a Swarm Control caller only, run by those
//     tools' own handlers. Every input given is applied (update's agent with
//     every other field in one write at the caller's expected_revision) or
//     the call is refused naming the inputs that would not apply, with no
//     change. A conversation's call never passes the Swarm Control
//     inputs to the run service, and inputs of two handlers in one call are
//     refused rather than one silently winning.
//   - Threat/regression: an Orchestrator activating or changing a worker
//     without the person's acceptance through a Swarm Control input; a
//     schedule switched the wrong way; a client losing a worker field.
//   - Boundary/authority: Server.ManageWorkers, workerControlCall,
//     workerConversationArgs, workerAnswerWith.
//   - Narrowest layer: Swarm Control's tools/call on the in-process harness
//     and the argument filter itself.
func TestManageWorkersSwarmControlInputsRunTheSwarmToolsCode(t *testing.T) {
	h := newCatalogParityHarness(t)
	data := func(out string) map[string]any {
		t.Helper()
		object, _ := parityDecode(t, out)["data"].(map[string]any)
		return object
	}
	out, refused := h.controlTool(t, catalogParityFullKey, "manage_workers", map[string]any{"action": "list", "include_jobs": true})
	if refused != "" {
		t.Fatalf("list include_jobs: %s", refused)
	}
	jobs, _ := data(out)["recent_jobs"].(map[string]any)
	if list, _ := jobs[h.jobs[""]["worker"]].([]any); len(list) == 0 {
		t.Fatalf("list include_jobs has no jobs for the seeded worker: %s", out)
	}
	if _, refused = h.controlTool(t, catalogParityFullKey, "manage_workers", map[string]any{"action": "update", "worker_id": h.workers[""], "schedule_id": "sched-1"}); !strings.Contains(refused, "schedule_enabled") {
		t.Fatalf("schedule_id without schedule_enabled was not refused: %q", refused)
	}
	if _, refused = h.controlTool(t, catalogParityFullKey, "manage_workers", map[string]any{"action": "update", "worker_id": h.workers[""], "run_id": "run-1", "schedule_id": "s", "schedule_enabled": true}); !strings.Contains(refused, "separate requests") {
		t.Fatalf("two swarm_* handlers' inputs in one call were not refused: %q", refused)
	}

	// Every input given is applied or the call is refused, naming what
	// would not apply; a refused call changes nothing.
	workers := pebblestore.NewWorkerStore(h.home.s.sessions.Store().Underlying())
	worker := func(id string) pebblestore.WorkerRecord {
		t.Helper()
		record, found, err := workers.GetWorker(h.home.account, id)
		if err != nil || !found {
			t.Fatalf("worker %s: %v %v", id, found, err)
		}
		return record
	}
	target := h.workers[""]
	before := worker(target)
	unchanged := func(what string) {
		t.Helper()
		if after := worker(target); after.Revision != before.Revision || after.Name != before.Name || after.Description != before.Description || after.Instructions != before.Instructions || after.Agent != before.Agent {
			t.Fatalf("%s changed the worker: %+v -> %+v", what, before, after)
		}
	}
	// A stale expected_revision is refused (agent no longer bypasses it).
	if _, refused = h.controlTool(t, catalogParityFullKey, "manage_workers", map[string]any{"action": "update", "worker_id": target, "expected_revision": float64(before.Revision + 7), "agent": "", "name": "Stale"}); refused == "" {
		t.Fatal("a stale expected_revision was not refused")
	}
	unchanged("a stale update")
	// run_id or schedule_id with a definition change: refused, naming it.
	for _, args := range []map[string]any{
		{"run_id": "probe-cancel-x", "name": "Renamed", "model": map[string]any{"provider": "p", "model": "m", "thinking": "low"}},
		{"schedule_id": "sched-1", "schedule_enabled": true, "description": "Changed"},
		{"run_id": "probe-cancel-x", "agent": ""},
		{"schedule_enabled": true, "name": "Orphan"},
	} {
		call := map[string]any{"action": "update", "worker_id": target, "expected_revision": float64(before.Revision)}
		for key, value := range args {
			call[key] = value
		}
		_, refused = h.controlTool(t, catalogParityFullKey, "manage_workers", call)
		named := false
		for _, key := range []string{"name", "model", "description", "agent", "schedule_enabled"} {
			if _, given := args[key]; given && strings.Contains(refused, key) {
				named = true
			}
		}
		if !named {
			t.Fatalf("update %v was not refused naming the input it would drop: %q", args, refused)
		}
		unchanged("a refused combined update")
	}
	// A combined update with agent applies every field in one write.
	if _, refused = h.controlTool(t, catalogParityFullKey, "manage_workers", map[string]any{"action": "update", "worker_id": target, "expected_revision": float64(before.Revision), "agent": "", "name": "Combined", "description": "Both applied.", "instructions": "Check the build twice."}); refused != "" {
		t.Fatalf("a combined update was refused: %s", refused)
	}
	if after := worker(target); after.Name != "Combined" || after.Description != "Both applied." || after.Instructions != "Check the build twice." || after.Agent != "" || after.Revision != before.Revision+1 {
		t.Fatalf("a combined update did not apply every field in one write: %+v", after)
	}

	// create: a deploy by role keeps every definition input it takes
	// (description, budget); one it cannot take is refused, naming it, and
	// nothing is created.
	count := func() int {
		t.Helper()
		listed, err := workers.ListWorkers(h.home.account, pebblestore.ListWorkersQuery{Limit: 500})
		if err != nil {
			t.Fatal(err)
		}
		return len(listed.Workers)
	}
	made := count()
	for _, args := range []map[string]any{
		{"role": "finder", "name": "Refused A", "instructions": "Probe.", "requested_capabilities": []any{}},
		{"preset": "reviewer", "automations": []any{}},
		{"workspace_path": t.TempDir(), "name": "Refused B", "instructions": "Probe.", "local_bindings": map[string]any{"primary": "x"}},
		{"workspace_path": t.TempDir(), "name": "Refused C", "instructions": "Probe.", "workspace_requirements": []any{}, "document": map[string]any{}},
	} {
		call := map[string]any{"action": "create"}
		for key, value := range args {
			call[key] = value
		}
		_, refused = h.controlTool(t, catalogParityFullKey, "manage_workers", call)
		if !strings.Contains(refused, "do not go with") {
			t.Fatalf("create %v was not refused naming the inputs it would drop: %q", args, refused)
		}
		for _, key := range []string{"requested_capabilities", "automations", "local_bindings", "workspace_requirements", "document"} {
			if _, given := args[key]; given && !strings.Contains(refused, key) {
				t.Fatalf("create's refusal does not name %s: %q", key, refused)
			}
		}
	}
	if got := count(); got != made {
		t.Fatalf("a refused create made a worker: %d -> %d", made, got)
	}
	out, refused = h.controlTool(t, catalogParityFullKey, "manage_workers", map[string]any{"action": "create", "role": "finder", "name": "Combined create", "instructions": "Find the flaky test.", "description": "Kept with the role.", "budget": map[string]any{"daily_usd": float64(3)}})
	if refused != "" {
		t.Fatalf("a combined create was refused: %s", refused)
	}
	var created pebblestore.WorkerRecord
	listed, err := workers.ListWorkers(h.home.account, pebblestore.ListWorkersQuery{Limit: 500})
	if err != nil {
		t.Fatal(err)
	}
	for _, record := range listed.Workers {
		if record.Name == "Combined create" {
			created = record
		}
	}
	if created.ID == "" || created.Description != "Kept with the role." || created.Instructions != "Find the flaky test." || created.Role != pebblestore.WorkerRoleFinder {
		t.Fatalf("a combined create lost a definition input: %+v (%s)", created, out)
	}
	if budget, err := h.home.s.sessions.Store().GetWorkerBudget(h.home.account, created.ID); err != nil || budget.DailyCostLimitUSD != 3 {
		t.Fatalf("a combined create lost its budget: %+v %v", budget, err)
	}

	// The folder create carries its other definition inputs in the one
	// create body (the harness cannot bind a folder, so the body is read).
	body, _, err := controlMCPWorkerCreateBody(map[string]any{"name": "N", "instructions": "I", "agent": "helper", "idempotency_key": "key-1", "requested_capabilities": []any{map[string]any{"kind": "x"}}, "automations": []any{map[string]any{"name": "a"}}})
	if err != nil {
		t.Fatal(err)
	}
	if body["agent"] != "helper" || body["idempotency_key"] != "key-1" || body["requested_capabilities"] == nil || len(body["automations"].([]any)) != 1 {
		t.Fatalf("the folder create body dropped a definition input: %v", body)
	}

	update, _ := manageWorkersAction("update")
	create, _ := manageWorkersAction("create")
	call := map[string]any{"action": "update", "worker_id": "w1", "name": "N", "agent": "a", "run_id": "r", "schedule_id": "s", "schedule_enabled": true}
	conversation := workerConversationArgs(update, false, call)
	for _, key := range update.ControlInputs {
		if _, kept := conversation[key]; kept {
			t.Fatalf("a conversation's update kept Swarm Control input %s: %v", key, conversation)
		}
	}
	if conversation["name"] != "N" || conversation["worker_id"] != "w1" {
		t.Fatalf("a conversation's update lost its own inputs: %v", conversation)
	}
	if _, ok := call["agent"]; !ok {
		t.Fatal("the filter changed the caller's arguments")
	}
	if control := workerConversationArgs(update, true, call); !reflect.DeepEqual(control, call) {
		t.Fatalf("a Swarm Control update lost inputs: %v", control)
	}
	for _, key := range []string{"workspace_path", "preset", "role", "worker", "schedule", "scheduled_plan", "project_id"} {
		if !containsString(create.ControlInputs, key) || containsString(create.Inputs, key) {
			t.Fatalf("create's %s is not a Swarm Control input only", key)
		}
	}
	if !containsString(create.Inputs, "name") {
		t.Fatal("create lost its own inputs")
	}
}

func containsString(list []string, want string) bool {
	for _, item := range list {
		if item == want {
			return true
		}
	}
	return false
}
