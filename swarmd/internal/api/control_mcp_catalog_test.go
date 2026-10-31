package api

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"sort"
	"strings"
	"testing"
	"time"

	agentruntime "swarm/packages/swarmd/internal/agent"
	"swarm/packages/swarmd/internal/identity"
	"swarm/packages/swarmd/internal/jobautomation"
	pebblestore "swarm/packages/swarmd/internal/store/pebble"
	"swarm/packages/swarmd/internal/tool"
	"swarm/packages/swarmd/internal/workspace"
)

// controlMCPFrozenSwarmTools is every swarm_* Swarm Control tool. The set is
// frozen: a new tool or action is a catalog entry (tool.Catalog) with its
// parity case, never a new swarm_* tool. Later stages move these into the
// catalog and remove them from this list.
var controlMCPFrozenSwarmTools = []string{
	"swarm_accept_review", "swarm_activity", "swarm_add_workspace", "swarm_agents_at_work", "swarm_answer_job",
	"swarm_approve_task", "swarm_assign_worker_task", "swarm_cancel_update", "swarm_connect_chatgpt", "swarm_connect_claude_code",
	"swarm_create_client_key", "swarm_create_key", "swarm_create_project", "swarm_create_worker", "swarm_create_workspace",
	"swarm_define_agent", "swarm_deliver_job", "swarm_deploy_worker", "swarm_design", "swarm_get_changes",
	"swarm_get_environment", "swarm_get_job", "swarm_get_limits", "swarm_get_permissions", "swarm_get_plan_usage",
	"swarm_get_project", "swarm_get_session", "swarm_get_theme", "swarm_get_usage", "swarm_get_worker",
	"swarm_give_job", "swarm_integrate_task", "swarm_list_agents", "swarm_list_fleet_keys", "swarm_list_keys",
	"swarm_list_models", "swarm_list_projects", "swarm_list_providers", "swarm_list_runs", "swarm_list_secrets",
	"swarm_list_sessions", "swarm_list_tasks", "swarm_list_workers", "swarm_list_workspaces", "swarm_manage_worker",
	"swarm_media_models", "swarm_message_job", "swarm_read_file", "swarm_read_media", "swarm_request_fleet_key",
	"swarm_request_media", "swarm_request_provider_key", "swarm_request_update", "swarm_resolve_permission", "swarm_retry_project_context",
	"swarm_revoke_fleet_key", "swarm_revoke_key", "swarm_run_plan", "swarm_run_task", "swarm_save_media",
	"swarm_schedule_job", "swarm_send_message", "swarm_set_agent_model", "swarm_set_environment", "swarm_set_limits",
	"swarm_set_media_models", "swarm_set_permissions", "swarm_set_project_workspaces", "swarm_set_session_access", "swarm_set_session_model",
	"swarm_set_usage_limits", "swarm_start_session", "swarm_status", "swarm_stop_job", "swarm_stop_run",
	"swarm_task_result", "swarm_update_project_context", "swarm_update_worker", "swarm_wait_task",
	"swarm_delete_workflow", "swarm_export_workflow", "swarm_import_workflow", "swarm_list_workflows", "swarm_restore_workflow",
	"swarm_set_workflow", "swarm_stop_workflows", "swarm_workflow_history",
}

// Purpose: the swarm_* tools are frozen; nothing adds one. Threat: a new
// capability built as a second hand-written MCP tool beside the catalog,
// which drifts from the Orchestrator's. Boundary: controlMCPTools, the whole
// swarm_* registry. Narrowest layer: the registry itself.
func TestControlMCPSwarmToolNamesFrozen(t *testing.T) {
	var names []string
	for _, candidate := range controlMCPTools() {
		if strings.HasPrefix(candidate.Name, "swarm_") {
			names = append(names, candidate.Name)
		} else {
			t.Fatalf("hand-written Swarm Control tool %q is not a swarm_* tool; catalog tools come from tool.Catalog", candidate.Name)
		}
		if _, ok := tool.CatalogToolNamed(candidate.Name); ok {
			t.Fatalf("%s is both a catalog tool and a hand-written tool", candidate.Name)
		}
	}
	sort.Strings(names)
	want := append([]string(nil), controlMCPFrozenSwarmTools...)
	sort.Strings(want)
	if strings.Join(names, ",") != strings.Join(want, ",") {
		added, removed := []string{}, []string{}
		have := map[string]bool{}
		for _, name := range names {
			have[name] = true
		}
		frozen := map[string]bool{}
		for _, name := range want {
			frozen[name] = true
			if !have[name] {
				removed = append(removed, name)
			}
		}
		for _, name := range names {
			if !frozen[name] {
				added = append(added, name)
			}
		}
		t.Fatalf("swarm_* tools changed: added %v, removed %v. A new tool or action is a catalog entry plus its parity case (docs/tool-catalog.md); a tool moved into the catalog leaves this list in the same change", added, removed)
	}
}

// catalogParityHarness is a home linked to a machine "box" (two in-process
// daemons over the real pinned link), its own project and tasks, an
// Orchestrator conversation home-orch, and Swarm Control served by the same
// runtime.
type catalogParityHarness struct {
	*remoteWaitHarness
	// homeWorkflows and remoteWorkflows are each machine's workflow store;
	// workflowFolder is the home's workflow project folder (seedWorkflows).
	homeWorkflows, remoteWorkflows *jobautomation.Store
	workflowFolder                 string
	// jobs are each machine's seeded job ids by name (seedJobs): "" is the
	// home, "box" the linked machine.
	jobs map[string]map[string]string
	// workers are each machine's seeded active worker (seedWorkers).
	workers map[string]string
}

func newCatalogParityHarness(t *testing.T) *catalogParityHarness {
	t.Helper()
	h := newRemoteWaitHarness(t, "t1", "t2")
	h.createGoal(t, "home-orch", map[string]any{})
	attentionSent(t, h, "t1")
	h.runtime.SetManageProjectStore(h.home.s.sessions.Store())
	store := h.home.s.sessions.Store()
	if err := store.PutProject(h.home.account, &pebblestore.ProjectRecord{ID: "home-project", AccountID: h.home.account, Name: "Home project"}); err != nil {
		t.Fatal(err)
	}
	for _, task := range []struct {
		id, status string
		archived   bool
	}{{"h1", "needs_review", false}, {"h2", "needs_review", false}, {"h3", "completed", false}, {"h4", "completed", false}, {"h5", "completed", true}, {"h6", "completed", true}, {"h7", "completed", false}, {"h8", "completed", false}, {"u1", "completed", false}, {"u2", "completed", false}, {"r1", "completed", false}, {"r2", "completed", false}, {"d1", "completed", true}, {"d2", "completed", true}} {
		record := &pebblestore.ProjectTaskRecord{ID: task.id, ProjectID: "home-project", AccountID: h.home.account, Title: "Home task", Agent: "coder", Status: task.status, Archived: task.archived, Revision: 1, SessionID: "hs-" + task.id, ActiveAttemptID: "attempt-" + task.id, Attempts: []pebblestore.ProjectTaskAttempt{{ID: "attempt-" + task.id, SessionID: "hs-" + task.id, Role: "coder", Status: task.status, Summary: "Done."}}}
		if strings.HasPrefix(task.id, "d") {
			// Deletable: archived, with no retained execution.
			record.SessionID, record.ActiveAttemptID, record.Attempts = "", "", nil
		}
		if err := store.PutProjectTask(h.home.account, record); err != nil {
			t.Fatal(err)
		}
	}
	for _, id := range []string{"del-a", "del-b"} {
		if err := store.PutProject(h.home.account, &pebblestore.ProjectRecord{ID: id, AccountID: h.home.account, Name: "Delete me"}); err != nil {
			t.Fatal(err)
		}
	}
	// A watch of this machine's tasks belongs to the project's own
	// Orchestrator conversation and pins each task's attempt session.
	if err := store.CreateSessionForAccount(pebblestore.SessionSnapshot{ID: "hs-w1", AccountScopeID: h.home.account, UserID: h.home.user, Metadata: map[string]any{"project_id": "home-project", "task_id": "w1"}}, h.home.user, h.home.account); err != nil {
		t.Fatal(err)
	}
	if err := store.PutProjectTask(h.home.account, &pebblestore.ProjectTaskRecord{ID: "w1", ProjectID: "home-project", AccountID: h.home.account, Title: "Watched task", Agent: "coder", Status: "running", Revision: 1, SessionID: "hs-w1", ActiveAttemptID: "attempt-w1", Attempts: []pebblestore.ProjectTaskAttempt{{ID: "attempt-w1", SessionID: "hs-w1", Role: "coder", Status: "running"}}}); err != nil {
		t.Fatal(err)
	}
	h.createGoal(t, "home-project-orch", map[string]any{"swarm_v3_project_id": "home-project", "project_id": "home-project", "agent_name": "system-orchestrator", "resolved_agent_name": "system-orchestrator"})
	// The linked machine's project has a real folder, so a send to it
	// starts a task there (as in TestFleetTaskAnswersCompactForOrchestrator).
	h.remote.s.workspace = workspace.NewService(pebblestore.NewWorkspaceStore(h.remote.s.sessions.Store().Underlying()))
	folder := t.TempDir()
	outboxPinGit(t, folder, "init", "-q", "-b", "dev")
	outboxPinGit(t, folder, "commit", "-q", "--allow-empty", "-m", "start")
	added, err := h.remote.s.workspace.AddForPrincipal(identity.Principal{Type: identity.PrincipalTypeUser, UserID: h.remote.user, AccountScopeID: h.remote.account}, folder, "project", "", false)
	if err != nil {
		t.Fatal(err)
	}
	if err := h.remote.s.sessions.Store().PutProject(h.remote.account, &pebblestore.ProjectRecord{ID: "remote-project", Name: "Remote project", Workspaces: []pebblestore.ProjectWorkspaceRef{{WorkspaceID: added.WorkspaceID, Path: folder, Role: "primary_code"}}}); err != nil {
		t.Fatal(err)
	}
	h.home.s.SetToolCatalogRuntime(h.runtime)
	parity := &catalogParityHarness{remoteWaitHarness: h}
	parity.seedWorkflows(t)
	parity.seedJobs(t)
	parity.seedWorkers(t)
	parity.seedSetup(t)
	parity.seedStage6(t)
	return parity
}

// orchestrator runs the manage_projects call as the home's Orchestrator, in
// its running turn.
func (h *catalogParityHarness) orchestrator(t *testing.T, args map[string]any) (string, error) {
	t.Helper()
	return h.orchestratorTool(t, "manage_projects", args)
}

// orchestratorTool runs a catalog tool's call as the home's Orchestrator.
func (h *catalogParityHarness) orchestratorTool(t *testing.T, name string, args map[string]any) (string, error) {
	t.Helper()
	session := "home-orch"
	if machine, _ := args["machine"].(string); args["action"] == "wait_tasks" && machine == "" {
		session = "home-project-orch"
	}
	// The run service passes the session's agent (manage_jobs grants name
	// it; manage_workers is the Orchestrator's only), as
	// Service.executeManageJobsTool and executeManageWorkersCatalogTool do.
	agent := catalogParityAgent
	if name == "manage_workers" || name == "manage_setup" || catalogStage6OrchestratorCall(name, args) {
		agent = agentruntime.SwarmOrchestratorAgentID
	}
	base := tool.WithVideoRunContext(context.Background(), tool.VideoRunContext{SessionID: session, RunID: "run-1"})
	if name == "manage_setup" {
		// What the run's person-decision gate gives a call the person
		// approved (run.Service.executeManageSetupTool); refusal without it
		// is TestManageSetupHandlerHoldsChangesToThePersonsDecision and the
		// run's TestManageSetupChangeAlwaysWaitsForThePerson.
		base = tool.WithPersonDecision(base)
	}
	ctx, cancel := context.WithTimeout(tool.WithCatalogAgent(base, agent), 20*time.Second)
	defer cancel()
	raw, _ := json.Marshal(args)
	return h.runtime.ExecuteForWorkspaceScopeWithRuntime(ctx, tool.WorkspaceScope{SessionID: session, Principal: h.p}, tool.Call{Name: name, Arguments: string(raw)})
}

// controlRequest is a Swarm Control request by the home's owner with a key
// of scopes.
func (h *catalogParityHarness) controlRequest(scopes []string, agentName string) *http.Request {
	req := requestWithTestPrincipalForAccount(httptest.NewRequest(http.MethodPost, "/mcp", nil), h.home.user, h.home.account)
	return requestWithScopedToken(req, &pebblestore.ScopedTokenRecord{ID: "key-parity", Scopes: scopes, AgentName: agentName})
}

// control runs the call through Swarm Control's tools/call: the envelope, or
// the error text the MCP client is shown.
func (h *catalogParityHarness) control(t *testing.T, scopes []string, args map[string]any) (string, string) {
	t.Helper()
	return h.controlTool(t, scopes, "manage_projects", args)
}

// controlTool runs a catalog tool's call through Swarm Control's tools/call.
func (h *catalogParityHarness) controlTool(t *testing.T, scopes []string, name string, args map[string]any) (string, string) {
	t.Helper()
	req := h.controlRequest(scopes, "")
	ctx, cancel := context.WithTimeout(req.Context(), 20*time.Second)
	defer cancel()
	c := &controlMCPCall{request: req.WithContext(ctx), server: h.home.s}
	params, _ := json.Marshal(map[string]any{"name": name, "arguments": args})
	result, rpcErr := c.callTool(params)
	if rpcErr != nil {
		t.Fatalf("tools/call: %+v", rpcErr)
	}
	content := result["content"].([]map[string]any)
	text, _ := content[0]["text"].(string)
	if result["isError"] == true {
		return "", text
	}
	return text, ""
}

// catalogParityFullKey is a key at every level with the API scopes behind
// manage_projects, manage_workflows and manage_jobs.
var catalogParityFullKey = []string{"swarm:read", "swarm:write", "swarm:approve", "swarm:manage", "sessions:read", "sessions:write", "projects:read", "projects:write", "automations:read", "automations:write", "signals:read"}

// catalogParityCompare is how a parity case compares the two answers.
type catalogParityCompare string

const (
	// parityExact: the same envelope (time fields aside) or the same error.
	parityExact catalogParityCompare = "exact"
	// parityShape: a mutation run on twin records: the same envelope
	// fields and data keys, or the same error with the twin's id.
	parityShape catalogParityCompare = "shape"
	// parityWatch: wait_tasks. The Orchestrator's turn ends and a new one
	// brings outcomes, so its answer is a watch (watching); a Swarm Control
	// caller has no turn, so its call waits for the next milestone or
	// wait_seconds (here 1: timed_out, or settled) and returns the tasks as
	// get_task reads them. Everything else in the envelope is the same.
	parityWatch catalogParityCompare = "watch"
	// parityNotServed: an action no Swarm Control listing serves; MCP
	// refuses it and names the actions it serves.
	parityNotServed catalogParityCompare = "not_served"
)

type catalogParityCase struct {
	orchestrator, control map[string]any
	compare               catalogParityCompare
	// twin maps the control call's record IDs to the Orchestrator's.
	twin map[string]string
}

func parityArgs(args ...any) map[string]any {
	out := map[string]any{}
	for i := 0; i+1 < len(args); i += 2 {
		out[args[i].(string)] = args[i+1]
	}
	return out
}

// catalogParityCases holds, per catalog tool, one case for every action (see
// TestCatalogParityCoversEveryAction).
func catalogParityCases() map[string]map[string][]catalogParityCase {
	projects := manageProjectsParityCases()
	for action, cases := range manageProjectsTaskWorkParityCases() {
		projects[action] = cases
	}
	return map[string]map[string][]catalogParityCase{"manage_projects": projects, "manage_workflows": manageWorkflowsParityCases(), "manage_jobs": manageJobsParityCases(), "manage_workers": manageWorkersParityCases(), "manage_setup": manageSetupParityCases(),
		"manage_fleet": manageFleetParityCases(), "manage_media": manageMediaParityCases(), "manage_custom_agents": manageCustomAgentsParityCases(), "manage_themes": manageThemeParityCases(), "manage_designs": manageDesignsParityCases()}
}

// catalogParityToolNames are the tools with parity cases, sorted.
func catalogParityToolNames(cases map[string]map[string][]catalogParityCase) []string {
	names := make([]string, 0, len(cases))
	for name := range cases {
		names = append(names, name)
	}
	sort.Strings(names)
	return names
}

// catalogParityActionNames are a tool's actions with parity cases, sorted.
func catalogParityActionNames(cases map[string][]catalogParityCase) []string {
	names := make([]string, 0, len(cases))
	for name := range cases {
		names = append(names, name)
	}
	sort.Strings(names)
	return names
}

func manageProjectsParityCases() map[string][]catalogParityCase {
	same := func(compare catalogParityCompare, args map[string]any) []catalogParityCase {
		return []catalogParityCase{{orchestrator: args, control: args, compare: compare}}
	}
	both := func(local, linked map[string]any) []catalogParityCase {
		return []catalogParityCase{{orchestrator: local, control: local, compare: parityExact}, {orchestrator: linked, control: linked, compare: parityExact}}
	}
	twin := func(orch, control map[string]any, ids map[string]string) []catalogParityCase {
		return []catalogParityCase{{orchestrator: orch, control: control, compare: parityShape, twin: ids}}
	}
	remote := func(action string, more ...any) map[string]any {
		return parityArgs(append([]any{"action", action, "machine", "box", "project_id", "remote-project"}, more...)...)
	}
	local := func(action string, more ...any) map[string]any {
		return parityArgs(append([]any{"action", action, "project_id", "home-project"}, more...)...)
	}
	notServed := func(action string) []catalogParityCase {
		return same(parityNotServed, local(action, "task_id", "h1"))
	}
	return map[string][]catalogParityCase{
		"list":              both(parityArgs("action", "list"), parityArgs("action", "list", "machine", "box")),
		"get":               both(parityArgs("action", "get", "id", "home-project"), parityArgs("action", "get", "machine", "box", "id", "remote-project")),
		"list_tasks":        both(local("list_tasks"), remote("list_tasks")),
		"get_task":          both(local("get_task", "task_id", "h1"), remote("get_task", "task_id", "t1")),
		"wait_tasks":        {{orchestrator: local("wait_tasks", "task_ids", []any{"w1"}), control: local("wait_tasks", "task_ids", []any{"w1"}, "wait_seconds", float64(1)), compare: parityWatch}, {orchestrator: remote("wait_tasks", "task_ids", []any{"t1"}), control: remote("wait_tasks", "task_ids", []any{"t1"}, "wait_seconds", float64(1)), compare: parityWatch}},
		"read_task_session": both(local("read_task_session", "task_id", "h1"), remote("read_task_session", "task_id", "t1", "limit", float64(2))),
		"message_task":      append(twin(local("message_task", "task_id", "h1", "message", "more"), local("message_task", "task_id", "h2", "message", "more"), map[string]string{"h2": "h1", "hs-h2": "hs-h1", "attempt-h2": "attempt-h1"}), twin(remote("message_task", "task_id", "t1", "message", "more"), remote("message_task", "task_id", "t2", "message", "more"), map[string]string{"t2": "t1", "rs-t2": "rs-t1"})...),
		"stop_task":         append(twin(local("stop_task", "task_id", "h1"), local("stop_task", "task_id", "h2"), map[string]string{"h2": "h1", "hs-h2": "hs-h1"}), twin(remote("stop_task", "task_id", "t1"), remote("stop_task", "task_id", "t2"), map[string]string{"t2": "t1", "rs-t2": "rs-t1"})...),
		"propose_task":      append(twin(local("propose_task", "title", "Parity", "agent", "finder"), local("propose_task", "title", "Parity", "agent", "finder"), nil), twin(remote("propose_task", "title", "Parity", "agent", "finder"), remote("propose_task", "title", "Parity", "agent", "finder"), nil)...),
		"reopen_task":       twin(local("reopen_task", "task_id", "r1", "feedback", "again", "expected_revision", float64(1), "client_request_id", "reopen-a"), local("reopen_task", "task_id", "r2", "feedback", "again", "expected_revision", float64(1), "client_request_id", "reopen-a"), map[string]string{"r2": "r1", "hs-r2": "hs-r1", "attempt-r2": "attempt-r1"}),
		"list_machines":     same(parityExact, parityArgs("action", "list_machines")),
		"list_linked_tasks": same(parityExact, parityArgs("action", "list_linked_tasks", "project_id", "home-project")),
		"agents_at_work":    both(parityArgs("action", "agents_at_work"), parityArgs("action", "agents_at_work", "machine", "box")),
		"machine_status":    same(parityExact, parityArgs("action", "machine_status", "machine", "box")),
		"get_task_session":  same(parityExact, remote("get_task_session", "task_id", "t1")),
		"activity":          same(parityExact, parityArgs("action", "activity", "machine", "box", "since_cursor", "0")),
		"resolve_task_permission": both(local("resolve_task_permission", "task_id", "h1", "permission_id", "no-such-request", "decision", "approve"),
			remote("resolve_task_permission", "task_id", "t1", "permission_id", "no-such-request", "decision", "approve")),
		"integrate_task":              same(parityExact, local("integrate_task", "task_id", "h1")),
		"update_task":                 twin(local("update_task", "task_id", "u1", "expected_revision", float64(1), "title", "Renamed"), local("update_task", "task_id", "u2", "expected_revision", float64(1), "title", "Renamed"), map[string]string{"u2": "u1", "hs-u2": "hs-u1"}),
		"archive_task":                twin(local("archive_task", "task_id", "h3", "expected_revision", float64(1)), local("archive_task", "task_id", "h4", "expected_revision", float64(1)), map[string]string{"h4": "h3", "hs-h4": "hs-h3"}),
		"unarchive_task":              twin(local("unarchive_task", "task_id", "h5", "expected_revision", float64(1)), local("unarchive_task", "task_id", "h6", "expected_revision", float64(1)), map[string]string{"h6": "h5", "hs-h6": "hs-h5"}),
		"delete_task":                 twin(local("delete_task", "task_id", "d1", "expected_revision", float64(1)), local("delete_task", "task_id", "d2", "expected_revision", float64(1)), map[string]string{"d2": "d1"}),
		"create":                      twin(parityArgs("action", "create", "name", "Parity project", "client_request_id", "create-a"), parityArgs("action", "create", "name", "Parity project", "client_request_id", "create-b"), map[string]string{"create-b": "create-a"}),
		"update":                      same(parityShape, parityArgs("action", "update", "id", "home-project", "description", "Parity")),
		"delete":                      twin(parityArgs("action", "delete", "id", "del-a"), parityArgs("action", "delete", "id", "del-b"), map[string]string{"del-b": "del-a"}),
		"list_sources":                same(parityExact, local("list_sources")),
		"inspect_source":              same(parityExact, local("inspect_source", "workspace_path", "/no-such-folder")),
		tool.FolderEnvironmentList:    both(parityArgs("action", tool.FolderEnvironmentList), parityArgs("action", tool.FolderEnvironmentList, "machine", "box")),
		tool.FolderEnvironmentGet:     same(parityExact, parityArgs("action", tool.FolderEnvironmentGet, "workspace_path", "/no-such-folder")),
		tool.FolderEnvironmentUpdate:  same(parityExact, parityArgs("action", tool.FolderEnvironmentUpdate, "workspace_path", "/no-such-folder", "environment", map[string]any{"base_image": "debian:12"})),
		tool.FolderEnvironmentRebuild: same(parityExact, parityArgs("action", tool.FolderEnvironmentRebuild, "workspace_path", "/no-such-folder")),
		"cancel_watch":                notServed("cancel_watch"),
		"inspect_files":               notServed("inspect_files"),
		"create_task":                 notServed("create_task"),
		"approve_task":                notServed("approve_task"),
		"accept_task":                 notServed("accept_task"),
		"deploy_task":                 notServed("deploy_task"),
		"refine_task":                 notServed("refine_task"),
		"edit_requirements":           notServed("edit_requirements"),
		"reconcile_tasks":             notServed("reconcile_tasks"),
		"list_media":                  notServed("list_media"),
		"get_media":                   notServed("get_media"),
		"report_task":                 notServed("report_task"),
	}
}

// Purpose: no catalog action is left without a parity case, so a new action
// cannot reach MCP or the Orchestrator untested against the other. Threat: a
// catalog entry served by one surface and drifting from the other. Boundary:
// tool.Catalog against catalogParityCases.
func TestCatalogParityCoversEveryAction(t *testing.T) {
	all := catalogParityCases()
	for _, catalog := range tool.Catalog() {
		cases, ok := all[catalog.Name]
		if !ok {
			t.Fatalf("catalog tool %s has no parity cases: add them to catalogParityCases", catalog.Name)
		}
		for _, action := range catalog.Actions {
			list, ok := cases[action.Action]
			if !ok || len(list) == 0 {
				t.Fatalf("%s %s has no parity case: add one to catalogParityCases", catalog.Name, action.Action)
			}
			linked := false
			for _, c := range list {
				if (c.compare == parityNotServed) == action.ControlCallable() {
					t.Fatalf("%s %s: parity case %s for an action Swarm Control serves=%v", catalog.Name, action.Action, c.compare, action.ControlCallable())
				}
				if c.orchestrator["action"] != action.Action || c.control["action"] != action.Action {
					t.Fatalf("%s %s: parity case calls %v / %v", catalog.Name, action.Action, c.orchestrator["action"], c.control["action"])
				}
				if machine, _ := c.orchestrator["machine"].(string); machine != "" {
					linked = true
				}
			}
			// manage_workflows and manage_jobs: every action that reaches a
			// linked machine is held to parity there too.
			if catalog.Name != "manage_projects" && action.Reach == tool.CatalogReachAny && !linked {
				t.Fatalf("%s %s reaches linked machines but has no linked parity case", catalog.Name, action.Action)
			}
			delete(cases, action.Action)
		}
		for action := range cases {
			t.Fatalf("parity case for %s %q, which the catalog does not declare", catalog.Name, action)
		}
		delete(all, catalog.Name)
	}
	for name := range all {
		t.Fatalf("parity cases for %q, which is not a catalog tool", name)
	}
}

// parityVolatile are time and clock fields that differ between two calls a
// moment apart.
func parityVolatile(key string) bool {
	return key == "at" || strings.HasSuffix(key, "_at") || strings.HasSuffix(key, "_ms") || strings.Contains(key, "seconds") || strings.HasSuffix(key, "_age") || key == "age" || key == "elapsed"
}

func parityStrip(value any) any {
	switch v := value.(type) {
	case map[string]any:
		// A machine row's link health (online, last contact error) is
		// live: an earlier call that reached the machine can change it.
		_, access := v["access"]
		_, named := v["name"]
		out := make(map[string]any, len(v))
		for key, item := range v {
			if parityVolatile(key) || (access && named && (key == "online" || key == "last_error")) {
				continue
			}
			out[key] = parityStrip(item)
		}
		return out
	case []any:
		out := make([]any, len(v))
		for i, item := range v {
			out[i] = parityStrip(item)
		}
		return out
	}
	return value
}

func parityTwin(text string, twin map[string]string) string {
	for from, to := range twin {
		text = strings.ReplaceAll(text, `"`+from+`"`, `"`+to+`"`)
		text = strings.ReplaceAll(text, " "+from+" ", " "+to+" ")
		text = strings.ReplaceAll(text, `\"`+from+`\"`, `\"`+to+`\"`)
	}
	return text
}

func parityDecode(t *testing.T, raw string) map[string]any {
	t.Helper()
	if _, err := tool.EnvelopeData(raw); err != nil {
		t.Fatal(err)
	}
	var envelope map[string]any
	if err := json.Unmarshal([]byte(raw), &envelope); err != nil {
		t.Fatal(err)
	}
	return envelope
}

func parityKeys(value any) string {
	object, _ := value.(map[string]any)
	keys := make([]string, 0, len(object))
	for key := range object {
		keys = append(keys, key)
	}
	sort.Strings(keys)
	return strings.Join(keys, ",")
}

// Purpose: an MCP client gets exactly the Orchestrator's manage_projects:
// for every catalog action, the same inputs through the Orchestrator's
// runtime and through Swarm Control's tools/call give the same envelope (or
// the same error), on this machine and with machine= on the linked machine;
// mutations run on twin records and give the same envelope and data shape;
// wait_tasks differs only as documented (a Swarm Control caller has no turn
// to wake); actions no listing serves are refused by MCP. Threat: two
// implementations of one tool drifting apart. Boundary:
// Runtime.ExecuteForWorkspaceScopeWithRuntime (Orchestrator) and
// controlMCPCall.callTool -> Runtime.ExecuteControlCatalog (MCP) over two
// in-process daemons linked like production. No provider runs.
func TestCatalogParityOrchestratorAndControl(t *testing.T) {
	h := newCatalogParityHarness(t)
	all := catalogParityCases()
	served := 0
	for _, toolName := range catalogParityToolNames(all) {
		for _, action := range catalogParityActionNames(all[toolName]) {
			served += h.runParityCases(t, toolName, action, all[toolName][action])
		}
	}
	if served == 0 {
		t.Fatal("no parity case ran")
	}
}

// runParityCases runs one action's parity cases and returns how many ran
// through both surfaces.
func (h *catalogParityHarness) runParityCases(t *testing.T, toolName, action string, cases []catalogParityCase) int {
	t.Helper()
	served := 0
	{
		for i, c := range cases {
			name := action
			if toolName != "manage_projects" {
				name = toolName + " " + action
			}
			if machine, _ := c.orchestrator["machine"].(string); machine != "" {
				name += "@" + machine
			}
			c = h.resolveCase(t, c)
			orchOut, orchErr := h.orchestratorTool(t, toolName, c.orchestrator)
			key := catalogParityFullKey
			if toolName == "manage_setup" {
				key = catalogParitySetupKey
			}
			controlOut, controlErr := h.controlTool(t, key, toolName, c.control)
			if c.compare == parityNotServed {
				if controlErr == "" || !strings.Contains(controlErr, "for Swarm Control") {
					t.Fatalf("%s: Swarm Control served an unlisted action: %s %s", name, controlOut, controlErr)
				}
				continue
			}
			served++
			if c.compare == paritySessionBound {
				// Documented difference (paritySessionBound): the same
				// outcome kind, and every envelope keeps the contract.
				if (orchErr != nil) != (controlErr != "") {
					t.Fatalf("%s case %d: Orchestrator %v %s; Swarm Control %q %s", name, i, orchErr, orchOut, controlErr, controlOut)
				}
				if orchErr == nil {
					orch, control := parityDecode(t, orchOut), parityDecode(t, controlOut)
					for _, key := range []string{"tool", "action", "machine"} {
						if orch[key] != control[key] {
							t.Fatalf("%s: %s differs: %v / %v", name, key, orch[key], control[key])
						}
					}
				}
				t.Logf("%s: session-bound: %v / %s", name, orchErr, controlErr)
				continue
			}
			if orchErr != nil || controlErr != "" {
				if orchErr == nil || controlErr == "" || parityTwin(controlErr, c.twin) != orchErr.Error() {
					t.Fatalf("%s case %d: Orchestrator %v %s; Swarm Control %q %s", name, i, orchErr, orchOut, controlErr, controlOut)
				}
				t.Logf("%s: the same error: %s", name, orchErr)
				continue
			}
			t.Logf("%s: %s envelopes", name, c.compare)
			orch, control := parityDecode(t, orchOut), parityDecode(t, parityTwin(controlOut, c.twin))
			switch c.compare {
			case parityExact:
				a, _ := json.Marshal(parityStrip(orch))
				b, _ := json.Marshal(parityStrip(control))
				if string(a) != string(b) {
					t.Fatalf("%s: envelopes differ\nOrchestrator:  %s\nSwarm Control: %s", name, a, b)
				}
			case parityShape:
				for _, key := range []string{"tool", "action", "machine", "state", "untrusted_content", "next_action"} {
					if orch[key] != control[key] {
						t.Fatalf("%s: %s differs: %v / %v\n%s\n%s", name, key, orch[key], control[key], orchOut, controlOut)
					}
				}
				if parityKeys(orch["data"]) != parityKeys(control["data"]) {
					t.Fatalf("%s: data keys differ\n%s\n%s", name, orchOut, controlOut)
				}
			case parityWatch:
				for _, key := range []string{"tool", "action", "machine", "untrusted_content"} {
					if orch[key] != control[key] {
						t.Fatalf("%s: %s differs: %v / %v", name, key, orch[key], control[key])
					}
				}
				if orch["state"] != "watching" || (control["state"] != tool.ControlTaskWaitTimedOut && control["state"] != tool.ControlTaskWaitSettled) || control["next_action"] == nil {
					t.Fatalf("%s: states %v / %v\n%s", name, orch["state"], control["state"], controlOut)
				}
				// The tasks are what get_task answers for each.
				ids, _ := c.control["task_ids"].([]any)
				rows, _ := envelopeDataMap(control)["tasks"].([]any)
				if len(rows) != len(ids) {
					t.Fatalf("%s: snapshot rows %v", name, rows)
				}
				for j, id := range ids {
					get := map[string]any{"action": "get_task", "project_id": c.control["project_id"], "task_id": id}
					if machine, ok := c.control["machine"]; ok {
						get["machine"] = machine
					}
					getOut, getErr := h.control(t, catalogParityFullKey, get)
					if getErr != "" {
						t.Fatal(getErr)
					}
					want := envelopeDataMap(parityDecode(t, getOut))["task"]
					if machine, ok := c.control["machine"]; ok && machine != "" {
						want = envelopeDataMap(parityDecode(t, getOut))
					}
					a, _ := json.Marshal(parityStrip(rows[j].(map[string]any)["task"]))
					b, _ := json.Marshal(parityStrip(want))
					if string(a) != string(b) {
						t.Fatalf("%s: snapshot of %v\n%s\nget_task\n%s", name, id, a, b)
					}
				}
			}
		}
	}
	return served
}

// Purpose: Swarm Control lists manage_projects in its default listing,
// generated from the catalog, with only the actions the caller's key may
// call: a read key sees read actions only and is refused a write action
// before anything runs; a key without the API scopes behind it, an
// agent-bound key, or a server without the catalog runtime sees no
// manage_projects at all. Threat: a key level bypassed through the catalog
// tool, or a tool listed that the caller cannot call. Boundary:
// controlMCPCall.visibleTools/catalogTools/callCatalogTool.
func TestControlMCPListsManageProjectsByKeyLevel(t *testing.T) {
	h := newCatalogParityHarness(t)
	listed := func(scopes []string, agentName string) (map[string]bool, bool) {
		t.Helper()
		c := &controlMCPCall{request: h.controlRequest(scopes, agentName), server: h.home.s}
		for _, candidate := range c.visibleTools() {
			if candidate.Name != "manage_projects" {
				continue
			}
			properties := candidate.InputSchema["properties"].(map[string]any)
			enum := properties["action"].(map[string]any)["enum"].([]string)
			actions := map[string]bool{}
			for _, action := range enum {
				actions[action] = true
			}
			return actions, true
		}
		return nil, false
	}
	full, ok := listed(catalogParityFullKey, "")
	if !ok {
		t.Fatal("manage_projects is not in the default listing")
	}
	for _, want := range []string{"list_tasks", "get_task", "wait_tasks", "read_task_session", "message_task", "stop_task", "propose_task"} {
		if !full[want] {
			t.Fatalf("default listing lacks %s: %v", want, full)
		}
	}
	if full["report_task"] || full["approve_task"] || full["update_task"] {
		t.Fatalf("default listing shows unlisted or full-only actions: %v", full)
	}
	readOnly, ok := listed([]string{"swarm:read", "sessions:read", "sessions:write"}, "")
	if !ok || !readOnly["get_task"] || readOnly["message_task"] || readOnly["stop_task"] || readOnly["propose_task"] || readOnly[tool.ResolveTaskPermissionAction] {
		t.Fatalf("read key listing: %v %v", readOnly, ok)
	}
	if _, ok := listed([]string{"workspaces:read"}, ""); ok {
		t.Fatal("a key without project or session scopes lists manage_projects")
	}
	if _, ok := listed(catalogParityFullKey, "a-worker"); ok {
		t.Fatal("an agent-bound key lists manage_projects")
	}
	// A read key is refused message_task before it runs: no message lands.
	store := h.home.s.sessions.Store()
	before, _, _ := store.GetProjectTask(h.home.account, "home-project", "h1")
	_, refused := h.control(t, []string{"swarm:read", "sessions:read", "sessions:write"}, map[string]any{"action": "message_task", "project_id": "home-project", "task_id": "h1", "message": "more"})
	if !strings.Contains(refused, "needs swarm:write") {
		t.Fatalf("read key message_task: %q", refused)
	}
	_, refused = h.control(t, []string{"sessions:read"}, map[string]any{"action": "stop_task", "project_id": "home-project", "task_id": "h1"})
	if !strings.Contains(refused, "projects:write or sessions:write") {
		t.Fatalf("read-scope key stop_task: %q", refused)
	}
	after, _, _ := store.GetProjectTask(h.home.account, "home-project", "h1")
	if after.Revision != before.Revision || after.Status != before.Status || len(after.Attempts) != len(before.Attempts) {
		t.Fatalf("a refused call changed the task: %+v -> %+v", before, after)
	}
	// Without the catalog runtime, nothing is listed or callable.
	h.home.s.SetToolCatalogRuntime(nil)
	if _, ok := listed(catalogParityFullKey, ""); ok {
		t.Fatal("manage_projects listed without the catalog runtime")
	}
}
