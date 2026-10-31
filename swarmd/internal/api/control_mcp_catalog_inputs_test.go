package api

import (
	"encoding/json"
	"fmt"
	"go/ast"
	"go/parser"
	"go/token"
	"io/fs"
	"os"
	"path/filepath"
	"sort"
	"strconv"
	"strings"
	"testing"
	"time"

	"swarm/packages/swarmd/internal/agentmodelsettings"
	"swarm/packages/swarmd/internal/identity"
	"swarm/packages/swarmd/internal/model"
	"swarm/packages/swarmd/internal/modelprofile"
	pebblestore "swarm/packages/swarmd/internal/store/pebble"
	"swarm/packages/swarmd/internal/tool"
)

// swarmCatalogDrops are the swarm_* inputs ("tool.input") and answer fields
// ("tool.answer.field") a catalog action deliberately does not take or
// return, each with its one-line reason. Keep it short: a drop is a decision,
// not a gap.
var swarmCatalogDrops = map[string]string{
	"swarm_approve_task.action":    "action is the catalog's own field; approving or rejecting a waiting task card is the person's step in their app (approve_task is not served to Swarm Control)",
	"swarm_design.edit_path":       "an edit is manage_designs edit, which takes edit_path",
	"swarm_request_media.retry_of": "a retry is manage_media retry, whose job_id is retry_of",
	"swarm_get_job.include_media":  "MCP image blocks are not envelope data; manage_media read returns a media job's file as base64",
	// swarm_task_result's flat answer is get_task's compact task view
	// (data.task), which the Orchestrator and Swarm Control share.
	"swarm_task_result.answer.task_id":              "data.task.id",
	"swarm_task_result.answer.role":                 "data.task.agent",
	"swarm_task_result.answer.report":               "data.task.result.summary, cut the same way, with next reading the rest",
	"swarm_task_result.answer.delivery_state":       "data.task.state and state_reason say whether and how the task can land",
	"swarm_task_result.answer.delivery_reason":      "as delivery_state",
	"swarm_task_result.answer.delivery_reason_code": "as delivery_state",
	"swarm_task_result.answer.changes_error":        "get_task reads the changes only with include_diff, and then answers changes_error",
	"swarm_task_result.answer.next":                 "the envelope's next is get_task's next step",
	"swarm_get_worker.answer.recent_runs":           "inspect is the worker review the Orchestrator shares (data.worker); a worker's latest runs are list include_jobs' recent_jobs and manage_fleet list_runs",
	"swarm_manage_worker.action":                    "action is the catalog's own field: pause, resume, archive and delete are manage_workers actions; cancel_run is update with run_id, enable_schedule and disable_schedule are update with schedule_id and schedule_enabled",
}

// swarmCatalogRenames are the swarm_* inputs ("tool.input") their catalog
// action takes under another name, because the swarm_* name is taken there
// (most often by the catalog's own action field), with the same meaning.
// The test requires the action to take the new name and not the old one.
var swarmCatalogRenames = map[string]string{
	// resolve_permission's allow_once or deny_once; ManageSessionControl
	// passes decision to swarm_resolve_permission's handler as its action.
	"swarm_resolve_permission.action":  "decision",
	"swarm_connect_chatgpt.action":     "step",
	"swarm_connect_claude_code.action": "step",
	// manage_setup's kind is media_models' media kind.
	"swarm_create_key.kind": "key_kind",
	// manage_projects' role is read_task_session's message role.
	"swarm_run_task.role": "agent",
}

// swarmCatalogInputs are the inputs a catalog action takes: its own and its
// Swarm Control inputs.
func swarmCatalogInputs(action tool.CatalogAction) map[string]bool {
	out := map[string]bool{}
	for _, name := range append(append([]string(nil), action.Inputs...), action.ControlInputs...) {
		out[name] = true
	}
	return out
}

// swarmToolInputs is the record of every swarm_* tool's inputs (its input
// schema's properties) when the swarm_* tools were removed (stage 7b). The
// tools are gone; the record stays so the catalog keeps taking what they took.
var swarmToolInputs = map[string][]string{
	"swarm_accept_review":          {"checkpoint_id", "notes", "session_id"},
	"swarm_activity":               {"after", "limit", "min_severity", "since_cursor", "wait_seconds"},
	"swarm_add_workspace":          {"name", "path"},
	"swarm_agents_at_work":         {"machine"},
	"swarm_answer_job":             {"answer", "job_id", "question_id"},
	"swarm_approve_task":           {"action", "project_id", "task_id"},
	"swarm_assign_worker_task":     {"idempotency_key", "task", "worker_id"},
	"swarm_cancel_update":          {},
	"swarm_connect_chatgpt":        {"action", "login_id", "wait_seconds"},
	"swarm_connect_claude_code":    {"action"},
	"swarm_create_client_key":      {"days", "name"},
	"swarm_create_key":             {"access", "agent", "days", "end_user_max_tokens_per_day", "kind", "max_concurrent_runs", "messages_per_minute", "name", "operator", "sessions_per_hour"},
	"swarm_create_project":         {"description", "name", "workspaces"},
	"swarm_create_worker":          {"description", "instructions", "name", "project_id", "schedule", "scheduled_plan", "workspace_path"},
	"swarm_create_workspace":       {"name"},
	"swarm_define_agent":           {"description", "instructions", "name", "tools", "tools_off", "tools_on"},
	"swarm_delete_workflow":        {"id", "name"},
	"swarm_deliver_job":            {"automation", "job_id"},
	"swarm_deploy_worker":          {"agent", "budget", "description", "instructions", "model", "name", "preset", "role", "worker"},
	"swarm_design":                 {"batch_name", "brief", "count", "edit_path", "folder", "project", "style", "variations", "wait_seconds", "workspace"},
	"swarm_export_workflow":        {"id", "name", "overwrite", "path"},
	"swarm_get_changes":            {"diff_chars", "path", "session_id"},
	"swarm_get_environment":        {"include_log", "project", "wait_seconds", "workspace"},
	"swarm_get_job":                {"detail_chars", "diff_chars", "include_diff", "include_media", "include_transcript", "job_id", "transcript_messages", "wait_seconds"},
	"swarm_get_limits":             {},
	"swarm_get_permissions":        {},
	"swarm_get_plan_usage":         {},
	"swarm_get_project":            {"project_id", "wait_seconds"},
	"swarm_get_session":            {"content_offset", "detail_chars", "message_id", "message_seq", "messages", "session_id", "wait_seconds"},
	"swarm_get_theme":              {"theme_id"},
	"swarm_get_usage":              {"detail", "period"},
	"swarm_get_worker":             {"run_id", "worker_id"},
	"swarm_give_job":               {"brief", "files", "idempotency_key", "model", "project", "review_checkout", "review_job", "then", "wait_seconds", "worker", "workspace"},
	"swarm_import_workflow":        {"definition", "name", "path", "project", "with", "workspace"},
	"swarm_integrate_task":         {"project_id", "task_id"},
	"swarm_list_agents":            {},
	"swarm_list_fleet_keys":        {"id", "wait_seconds"},
	"swarm_list_keys":              {"include_revoked"},
	"swarm_list_models":            {"provider"},
	"swarm_list_projects":          {},
	"swarm_list_providers":         {"provider", "wait_seconds"},
	"swarm_list_runs":              {"cursor", "limit", "since", "status", "until", "worker"},
	"swarm_list_secrets":           {},
	"swarm_list_sessions":          {"limit", "project_id"},
	"swarm_list_tasks":             {"agent", "cursor", "include_linked", "limit", "project_id", "query", "scope", "status"},
	"swarm_list_workers":           {"include_jobs"},
	"swarm_list_workflows":         {"delivery_id", "detail"},
	"swarm_list_workspaces":        {},
	"swarm_manage_worker":          {"action", "run_id", "schedule_id", "worker_id"},
	"swarm_media_models":           {"include_unready", "kind"},
	"swarm_message_job":            {"client_request_id", "job_id", "message", "wait_seconds"},
	"swarm_read_file":              {"path", "session_id", "workspace_path"},
	"swarm_read_media":             {"file", "length", "offset", "project_id", "task_id"},
	"swarm_request_fleet_key":      {"include_home", "label", "machines", "provider"},
	"swarm_request_media":          {"aspect_ratio", "brief", "count", "duration_seconds", "format", "inputs", "kind", "language", "model", "operation", "project", "resolution", "retry_of", "style", "text", "title", "voice", "wait_seconds", "workspace"},
	"swarm_request_provider_key":   {"label", "provider"},
	"swarm_request_update":         {"max_seconds"},
	"swarm_resolve_permission":     {"action", "answer", "permission_id", "reason", "result", "session_id"},
	"swarm_restore_workflow":       {"id", "name", "revision"},
	"swarm_retry_project_context":  {"project_id"},
	"swarm_revoke_fleet_key":       {"id", "include_home", "machines"},
	"swarm_revoke_key":             {"key_id"},
	"swarm_run_plan":               {"plan", "session_id"},
	"swarm_run_task":               {"brief", "files", "model", "project_id", "role", "title", "wait_seconds", "workspace"},
	"swarm_save_media":             {"folder", "job_id"},
	"swarm_schedule_job":           {"brief", "name", "project", "schedule", "then", "worker", "workspace"},
	"swarm_send_message":           {"attachments", "client_request_id", "content", "session_id", "wait_seconds"},
	"swarm_set_agent_model":        {"fast", "model", "provider", "role", "thinking"},
	"swarm_set_environment":        {"append_setup", "base_image", "env", "max_leases", "project", "rebuild", "reset", "setup", "unset_env", "wait_seconds", "workspace"},
	"swarm_set_limits":             {"expected_revision", "limits"},
	"swarm_set_media_models":       {"audio", "image", "transcription", "video", "video_iteration"},
	"swarm_set_permissions":        {"client_request_id", "expected_revision", "linked", "tasks"},
	"swarm_set_project_workspaces": {"project_id", "workspaces"},
	"swarm_set_session_access":     {"machine", "session_id", "tasks"},
	"swarm_set_session_model":      {"model", "provider", "session_id", "thinking"},
	"swarm_set_usage_limits":       {"daily_cost_limit_usd", "daily_tokens_limit", "enabled"},
	"swarm_set_workflow":           {"enabled", "inputs", "name", "on", "project", "result", "retries", "rotate_key", "scope", "steps", "when", "with", "workspace"},
	"swarm_start_session":          {"access", "agent", "attachments", "model", "plan", "project_id", "prompt", "provider", "thinking", "title", "wait_seconds", "workspace_path"},
	"swarm_status":                 {"timezone"},
	"swarm_stop_job":               {"job_id", "reason"},
	"swarm_stop_run":               {"reason", "session_id"},
	"swarm_stop_workflows":         {"stopped"},
	"swarm_task_result":            {"diff_chars", "include_diff", "project_id", "task_id"},
	"swarm_update_project_context": {"project_id", "project_md"},
	"swarm_update_worker":          {"agent", "description", "instructions", "name", "worker_id"},
	"swarm_wait_task":              {"project_id", "task_id", "wait_seconds"},
	"swarm_workflow_history":       {"id", "name", "revision"},
}

// Purpose:
//   - Requirement: every catalog action takes every input of the swarm_*
//     tool it replaced (swarmToolInputs, the record taken when those tools
//     were removed), under the same name (its own or a Swarm Control input)
//     or the name swarmCatalogRenames gives, unless the input is a listed
//     drop with its reason (swarmCatalogDrops, the one exemption table).
//   - Threat/regression: a later catalog change dropping an input a client
//     of the removed tool relied on (a wait, a filter, an option), or a
//     record, drop or rename entry that went stale and hides nothing.
//   - Boundary/authority: tool.Catalog's declared Inputs and ControlInputs
//     against the static record, over swarmToolCatalogHomes; the record
//     names exactly the frozen swarm_* tools (controlMCPFrozenSwarmTools).
//   - Narrowest layer: the declarations and the record themselves.
func TestEverySwarmToolInputIsACatalogInput(t *testing.T) {
	frozen := map[string]bool{}
	for _, name := range controlMCPFrozenSwarmTools {
		frozen[name] = true
		if _, recorded := swarmToolInputs[name]; !recorded {
			t.Fatalf("%s has no input record", name)
		}
	}
	for name := range swarmToolInputs {
		if !frozen[name] {
			t.Fatalf("%s is recorded but is not a swarm_* tool", name)
		}
	}
	listed := map[string]bool{}
	for key, reason := range swarmCatalogDrops {
		listed[key] = true
		if strings.TrimSpace(reason) == "" {
			t.Fatalf("drop %s has no reason", key)
		}
		if _, mapped := swarmToolCatalogHomes[key[:strings.Index(key, ".")]]; !mapped {
			t.Fatalf("drop %s names a swarm_* tool with no catalog home", key)
		}
	}
	var missing []string
	seen := map[string]bool{}
	for _, name := range sortedSwarmHomes() {
		home := swarmToolCatalogHomes[name]
		catalog, ok := tool.CatalogToolNamed(home.tool)
		if !ok {
			t.Fatalf("%s maps to %s, which is not a catalog tool", name, home.tool)
		}
		action, _ := catalog.Action(home.action)
		inputs := swarmCatalogInputs(action)
		for _, input := range swarmToolInputs[name] {
			key := name + "." + input
			if renamed, ok := swarmCatalogRenames[key]; ok {
				if listed[key] {
					t.Fatalf("%s is both renamed and dropped", key)
				}
				if !inputs[renamed] {
					t.Errorf("%s is renamed %s, but %s %s does not take %s", key, renamed, home.tool, home.action, renamed)
				}
				if inputs[input] && input != "action" {
					t.Errorf("%s is renamed %s, but %s %s still takes %s: remove the rename", key, renamed, home.tool, home.action, input)
				}
				seen[key] = true
				continue
			}
			if inputs[input] {
				if listed[key] {
					t.Fatalf("%s is listed but %s %s takes %s: remove the entry", key, home.tool, home.action, input)
				}
				continue
			}
			if listed[key] {
				seen[key] = true
				continue
			}
			missing = append(missing, fmt.Sprintf("%s (%s %s)", key, home.tool, home.action))
		}
	}
	sort.Strings(missing)
	if len(missing) > 0 {
		t.Fatalf("inputs of removed swarm_* tools no catalog action takes: %v; add each to the action (same name and meaning), or list it in swarmCatalogDrops with its reason", missing)
	}
	for key := range listed {
		if strings.Count(key, ".") == 1 && !seen[key] {
			t.Fatalf("%s is listed but is not a recorded input of that swarm_* tool", key)
		}
	}
	for key := range swarmCatalogRenames {
		if !seen[key] {
			t.Fatalf("%s is renamed but is not a recorded input of that swarm_* tool", key)
		}
	}
}

func sortedSwarmHomes() []string {
	names := make([]string, 0, len(swarmToolCatalogHomes))
	for name := range swarmToolCatalogHomes {
		names = append(names, name)
	}
	sort.Strings(names)
	return names
}

// swarmHandlerRecord is how a catalog action keeps the effect of a removed
// swarm_* tool: the catalog code servedBy runs the tool's handler, or the
// answer code shared that both used, or, for an action the Orchestrator's
// own code answers, every field of the handler swarmAnswer's answer (its out
// map) is one of catalogFields or of the maps catalogAnswer returns.
type swarmHandlerRecord struct {
	handler       string
	servedBy      string
	shared        string
	swarmAnswer   string
	catalogFields []string
	catalogAnswer string
}

// catalogSwarmHandlerRecord is the record of the swarm_* handlers the
// catalog runs ("The swarm_* handler service", docs/tool-catalog.md), for the
// removed tools whose answers the stage 7a harness could not compare by
// execution (no provider, worktree service, workspace authority, sandbox
// environments, secret slots or media models run there).
var catalogSwarmHandlerRecord = map[string]swarmHandlerRecord{
	"swarm_answer_job":           {handler: "controlMCPAnswerJob", servedBy: "manageJobsAs"},
	"swarm_deliver_job":          {handler: "controlMCPDeliverJob", servedBy: "manageJobsAs"},
	"swarm_give_job":             {handler: "controlMCPGiveJob", servedBy: "manageJobsAs"},
	"swarm_schedule_job":         {handler: "controlMCPScheduleJob", servedBy: "manageJobsAs"},
	"swarm_assign_worker_task":   {handler: "controlMCPAssignWorkerTask", servedBy: "assignWorkerTask"},
	"swarm_create_worker":        {handler: "controlMCPCreateWorker", servedBy: "workerControlRoutes"},
	"swarm_list_secrets":         {handler: "controlMCPListSecrets", servedBy: "runSetupAction"},
	"swarm_connect_claude_code":  {handler: "controlMCPConnectClaudeCode", servedBy: "runSetupAction"},
	"swarm_request_provider_key": {handler: "controlMCPRequestProviderKey", servedBy: "runSetupAction"},
	"swarm_request_fleet_key":    {handler: "controlMCPRequestFleetKey", servedBy: "runSetupAction"},
	"swarm_revoke_fleet_key":     {handler: "controlMCPRevokeFleetKey", servedBy: "runSetupAction"},
	"swarm_media_models":         {handler: "controlMCPListMediaModels", servedBy: "catalogSwarmHandlers"},
	"swarm_set_media_models":     {handler: "controlMCPSetMediaModels", servedBy: "runSetupAction"},
	"swarm_read_media":           {handler: "controlMCPReadMedia", servedBy: "catalogSwarmHandlers"},
	"swarm_save_media":           {handler: "controlMCPSaveMedia", servedBy: "catalogSwarmHandlers"},
	"swarm_request_media":        {handler: "controlMCPRequestMedia", servedBy: "catalogSwarmHandlers"},
	"swarm_design":               {handler: "controlMCPDesign", servedBy: "catalogSwarmHandlers"},
	// Swarm Control's create with plan, attachments, project_id or access
	// runs swarm_start_session's handler (ManageSessionSwarm); stop and that
	// tool's handler share controlMCPStopReport.
	"swarm_start_session": {handler: "controlMCPStartSession", servedBy: "ManageSessionSwarm"},
	"swarm_stop_run":      {handler: "controlMCPStopRun", shared: "controlMCPStopReport", servedBy: "catalogStopReport"},
	// propose_task and integrate_task are the Orchestrator's own actions:
	// the swarm_* answer's field names (read from its handler) are the
	// catalog answer's (read from the code that makes it).
	"swarm_run_task":       {swarmAnswer: "controlMCPRunTask", catalogFields: tool.ProposeTaskSwarmAnswerFields},
	"swarm_integrate_task": {swarmAnswer: "controlMCPIntegrate", catalogAnswer: "integrateProjectTask"},
}

// Purpose:
//   - Requirement: the catalog actions that replaced swarm_* tools whose
//     answers were never compared by execution still run those tools' code:
//     the catalog code servedBy references the handler (or both use the
//     shared answer code), so the catalog's data is that handler's answer;
//     or every field of the handler's answer is one the catalog answer
//     makes. Each record names a mapped swarm_* tool.
//   - Threat/regression: deleting or bypassing a handler the catalog runs
//     ("delete a handler only when nothing else calls it"), or a catalog
//     answer losing a field a removed tool answered.
//   - Boundary/authority: the api package's non-test source (go/ast) against
//     catalogSwarmHandlerRecord.
//   - Narrowest layer: source and record; the mapped answers are read from
//     source and not executed by this test.
func TestCatalogActionsRunTheSwarmHandlers(t *testing.T) {
	fset := token.NewFileSet()
	packages, err := parser.ParseDir(fset, ".", func(info fs.FileInfo) bool { return !strings.HasSuffix(info.Name(), "_test.go") }, 0)
	if err != nil {
		t.Fatal(err)
	}
	declarations := map[string]ast.Node{}
	for _, pkg := range packages {
		for _, file := range pkg.Files {
			for _, decl := range file.Decls {
				switch d := decl.(type) {
				case *ast.FuncDecl:
					declarations[d.Name.Name] = d
				case *ast.GenDecl:
					for _, spec := range d.Specs {
						if value, ok := spec.(*ast.ValueSpec); ok {
							for _, name := range value.Names {
								declarations[name.Name] = value
							}
						}
					}
				}
			}
		}
	}
	uses := func(declaration, name string) bool {
		node, ok := declarations[declaration]
		found := false
		if ok {
			ast.Inspect(node, func(n ast.Node) bool {
				if ident, ok := n.(*ast.Ident); ok && ident.Name == name {
					found = true
				}
				return !found
			})
		}
		return found
	}
	names := make([]string, 0, len(catalogSwarmHandlerRecord))
	for name := range catalogSwarmHandlerRecord {
		names = append(names, name)
	}
	sort.Strings(names)
	for _, name := range names {
		record := catalogSwarmHandlerRecord[name]
		if _, mapped := swarmToolCatalogHomes[name]; !mapped {
			t.Errorf("%s is recorded but has no catalog home", name)
		}
		switch {
		case record.shared != "":
			if !uses(record.handler, record.shared) || !uses(record.servedBy, record.shared) {
				t.Errorf("%s: %s and %s do not both run %s", name, record.handler, record.servedBy, record.shared)
			}
		case record.handler != "":
			if _, ok := declarations[record.handler].(*ast.FuncDecl); !ok {
				t.Errorf("%s: its handler %s is not a function in the api package", name, record.handler)
				continue
			}
			if _, ok := declarations[record.servedBy]; !ok {
				t.Errorf("%s: %s is not declared in the api package", name, record.servedBy)
				continue
			}
			if !uses(record.servedBy, record.handler) {
				t.Errorf("%s: the catalog code %s does not run %s", name, record.servedBy, record.handler)
			}
		case record.swarmAnswer != "":
			swarmFn, ok := declarations[record.swarmAnswer].(*ast.FuncDecl)
			if !ok {
				t.Errorf("%s: %s is not a function in the api package", name, record.swarmAnswer)
				continue
			}
			catalog := map[string]bool{}
			for _, field := range record.catalogFields {
				catalog[field] = true
			}
			if record.catalogAnswer != "" {
				catalogFn, ok := declarations[record.catalogAnswer].(*ast.FuncDecl)
				if !ok {
					t.Errorf("%s: %s is not a function in the api package", name, record.catalogAnswer)
					continue
				}
				for field := range returnedMapKeys(catalogFn) {
					catalog[field] = true
				}
			}
			fields := answerMapKeys(swarmFn, "out")
			if len(fields) == 0 {
				t.Errorf("%s: no answer fields read from %s", name, record.swarmAnswer)
			}
			for field := range fields {
				if field != "next" && !catalog[field] {
					t.Errorf("%s answers %s, which the catalog answer lacks", name, field)
				}
			}
		default:
			t.Errorf("%s is recorded with neither a handler nor a mapped answer", name)
		}
	}
}

// seedSwarmAnswerProbes seeds what the answer probes name and returns the
// function that puts the seeded values in a probe ($folder, $worker, $job,
// $jobworker, $key_a, $key_b; $perm_* and $limits_* read when placed, so a
// probe after another change sees the current revision).
func (h *catalogParityHarness) seedSwarmAnswerProbes(t *testing.T, folder string) func(map[string]any) map[string]any {
	t.Helper()
	store := h.home.s.sessions.Store()
	p := identity.Principal{Type: identity.PrincipalTypeUser, UserID: h.home.user, AccountScopeID: h.home.account}
	added, err := h.home.s.workspace.AddForPrincipal(p, folder, "probe", "", false)
	if err != nil {
		t.Fatal(err)
	}
	if err := store.PutProject(h.home.account, &pebblestore.ProjectRecord{ID: "probe-project", AccountID: h.home.account, Name: "Probe project", Workspaces: []pebblestore.ProjectWorkspaceRef{{WorkspaceID: added.WorkspaceID, Path: folder, Role: "primary_code"}}}); err != nil {
		t.Fatal(err)
	}
	in := pebblestore.V3SessionMutationInput{SessionID: "probe-folder", Kind: pebblestore.V3SessionMutationCreateSession, ClientRequestID: "create-probe-folder", Session: &pebblestore.SessionSnapshot{ID: "probe-folder", Mode: "auto", WorkspacePath: folder}}
	in.AccountScopeID, in.UserID, in.PayloadHash = h.home.account, h.home.user, in.ClientRequestID
	if _, err := store.ApplyV3SessionMutation(in); err != nil {
		t.Fatal(err)
	}
	for _, id := range []string{"edit-a", "edit-b", "edit-c", "edit-d"} {
		if err := store.PutProject(h.home.account, &pebblestore.ProjectRecord{ID: id, AccountID: h.home.account, Name: "Edit " + id}); err != nil {
			t.Fatal(err)
		}
	}
	for _, id := range []string{"retry-a", "retry-b"} {
		if err := store.PutProject(h.home.account, &pebblestore.ProjectRecord{ID: id, AccountID: h.home.account, Name: "Retry " + id, ContextGeneration: &pebblestore.ProjectContextGeneration{Status: "failed", Attempt: 1, Error: "earlier failure"}}); err != nil {
			t.Fatal(err)
		}
	}
	for _, id := range []string{"probe-cancel-a", "probe-cancel-b"} {
		session := pebblestore.SessionSnapshot{ID: "worker-execution-" + id, WorkspacePath: folder, AccountScopeID: h.home.account, UserID: h.home.user, Metadata: map[string]any{"worker_id": h.jobs[""]["worker"], "worker_execution_run_id": id}}
		if err := store.CreateSessionForAccount(session, h.home.user, h.home.account); err != nil {
			t.Fatal(err)
		}
		run := pebblestore.WorkerRunRecord{ID: id, WorkerID: h.jobs[""]["worker"], UserID: h.home.user, Role: pebblestore.WorkerRoleCoder, SessionID: session.ID, Status: "running", RequestSource: "direct", Input: map[string]any{"prompt": "Probe."}, CreatedAt: 1, StartedAt: 1}
		if _, err := store.WorkerStore().RecordWorkerRun(h.home.account, run); err != nil {
			t.Fatal(err)
		}
	}
	for _, id := range []string{"probe-msg-a", "probe-msg-b"} {
		session := pebblestore.SessionSnapshot{ID: "worker-execution-" + id, WorkspacePath: folder, AccountScopeID: h.home.account, UserID: h.home.user, Metadata: map[string]any{"worker_id": h.jobs[""]["worker"], "worker_execution_run_id": id}}
		if err := store.CreateSessionForAccount(session, h.home.user, h.home.account); err != nil {
			t.Fatal(err)
		}
		run := pebblestore.WorkerRunRecord{ID: id, WorkerID: h.jobs[""]["worker"], UserID: h.home.user, Role: pebblestore.WorkerRoleCoder, SessionID: session.ID, Status: "running", RequestSource: "direct", Input: map[string]any{"prompt": "Probe."}, CreatedAt: 1, StartedAt: 1}
		if _, err := store.WorkerStore().RecordWorkerRun(h.home.account, run); err != nil {
			t.Fatal(err)
		}
	}
	h.wireModelCatalog(t)
	// Delegated child sessions swarm_stop_run and manage_sessions stop
	// stop through the subagent route.
	for _, id := range []string{"probe-sub-a", "probe-sub-b", "probe-model-a", "probe-model-b"} {
		metadata := map[string]any{"lineage_kind": "delegated_subagent"}
		if strings.HasPrefix(id, "probe-model") {
			metadata = nil
		}
		in := pebblestore.V3SessionMutationInput{SessionID: id, Kind: pebblestore.V3SessionMutationCreateSession, ClientRequestID: "create-" + id, Session: &pebblestore.SessionSnapshot{ID: id, Mode: "auto", WorkspacePath: folder, Metadata: metadata}}
		in.AccountScopeID, in.UserID, in.PayloadHash = h.home.account, h.home.user, in.ClientRequestID
		if _, err := store.ApplyV3SessionMutation(in); err != nil {
			t.Fatal(err)
		}
	}
	// The model profiles behind a session's model, and the run controller
	// behind the runtime's own send_message and create (a recording stand-in).
	h.home.s.SetModelProfileService(modelprofile.NewService(pebblestore.NewModelProfileStore(store.Underlying())))
	h.runtime.SetManageSessionController(&catalogSessionController{t: t, f: h.home})
	// Folders with content to register, and this machine's workspace
	// folder for new repositories.
	addFolders := map[string]string{}
	for _, name := range []string{"$addfolder_a", "$addfolder_b"} {
		dir := t.TempDir()
		if err := os.WriteFile(filepath.Join(dir, "README.md"), []byte("probe\n"), 0o600); err != nil {
			t.Fatal(err)
		}
		addFolders[name] = dir
	}
	if h.home.s.workspaceRoot == "" {
		h.home.s.workspaceRoot = t.TempDir()
	}
	// The folder environment store and a fake image builder (no Docker).
	h.home.s.SetSandboxEnvironmentStore(pebblestore.NewSandboxEnvironmentStore(store.Underlying()))
	h.home.s.envBuilder = &fakeEnvironmentBuilder{}
	values := map[string]any{"$perm1": h.sessionPermissions["ms-perm-1"], "$perm2": h.sessionPermissions["ms-perm-2"], "$sessfolder": h.sessionFolders["home"], "$addfolder_a": addFolders["$addfolder_a"], "$addfolder_b": addFolders["$addfolder_b"], "$msg_a": controlMCPJobID(controlMCPJobWorker, h.jobs[""]["worker"], "probe-msg-a"), "$msg_b": controlMCPJobID(controlMCPJobWorker, h.jobs[""]["worker"], "probe-msg-b"), "$folder": folder, "$worker": h.workers[""], "$job": h.jobs[""]["parity"], "$jobworker": h.jobs[""]["worker"], "$jobdone": h.jobs[""]["done"]}
	if h.home.s.security != nil {
		for _, name := range []string{"$key_a", "$key_b"} {
			_, record, err := h.home.s.security.CreateScopedToken("probe "+name, []string{"swarm:read"}, h.home.account, h.home.user, time.Hour, "", "")
			if err != nil {
				t.Fatal(err)
			}
			values[name] = record.ID
		}
	}
	current := func(name string) (any, bool) {
		switch name {
		case "$perm_revision", "$perm_tasks":
			access, err := store.GetAccountPermissions(h.home.account)
			if err != nil {
				t.Fatal(err)
			}
			if name == "$perm_tasks" {
				return string(access.Tasks), true
			}
			return float64(access.Revision), true
		case "$worker_revision":
			// Read when placed: the swarm_* probe before it changes it.
			record, found, err := store.WorkerStore().GetWorker(h.home.account, h.workers[""])
			if err != nil || !found {
				t.Fatalf("worker: %v %v", found, err)
			}
			return float64(record.Revision), true
		case "$limits_revision", "$limits":
			limits, err := store.GetLimits(h.home.account)
			if err != nil {
				t.Fatal(err)
			}
			if name == "$limits_revision" {
				return float64(limits.Revision), true
			}
			raw, _ := json.Marshal(limits)
			var out map[string]any
			_ = json.Unmarshal(raw, &out)
			delete(out, "revision")
			return out, true
		}
		value, ok := values[name]
		return value, ok
	}
	var walk func(any) any
	walk = func(value any) any {
		switch v := value.(type) {
		case string:
			if resolved, ok := current(v); ok {
				return resolved
			}
			return v
		case map[string]any:
			out := make(map[string]any, len(v))
			for key, item := range v {
				out[key] = walk(item)
			}
			return out
		case []any:
			out := make([]any, len(v))
			for i, item := range v {
				out[i] = walk(item)
			}
			return out
		}
		return value
	}
	return func(args map[string]any) map[string]any {
		if args == nil {
			return nil
		}
		return walk(args).(map[string]any)
	}
}

// wireModelCatalog gives the home the model catalog and account model
// settings behind the model routes (the fixture model seedWorkers records;
// no provider is called).
func (h *catalogParityHarness) wireModelCatalog(t *testing.T) {
	t.Helper()
	if h.home.s.model != nil {
		return
	}
	db := h.home.s.sessions.Store().Underlying()
	h.home.s.model = model.NewService(pebblestore.NewModelStore(db), nil, model.NewCatalogService(pebblestore.NewModelCatalogStore(db)))
	if h.home.s.agentModelSettings == nil {
		settings := pebblestore.NewAgentModelSettingsStore(db)
		h.home.s.SetAgentModelSettingsService(agentmodelsettings.NewService(settings), settings)
	}
}

// answerMapKeys are the string keys a function puts in its answer map
// variable: a map literal or controlMCPPick assigned to it, and index
// assignments on it.
func answerMapKeys(fn *ast.FuncDecl, variable string) map[string]bool {
	keys := map[string]bool{}
	literal := func(expr ast.Expr) (string, bool) {
		lit, ok := expr.(*ast.BasicLit)
		if !ok || lit.Kind != token.STRING {
			return "", false
		}
		text, err := strconv.Unquote(lit.Value)
		return text, err == nil
	}
	mapKeys := func(expr ast.Expr) {
		if composite, ok := expr.(*ast.CompositeLit); ok {
			for _, element := range composite.Elts {
				if pair, ok := element.(*ast.KeyValueExpr); ok {
					if key, ok := literal(pair.Key); ok {
						keys[key] = true
					}
				}
			}
		}
		if call, ok := expr.(*ast.CallExpr); ok {
			if ident, ok := call.Fun.(*ast.Ident); ok && ident.Name == "controlMCPPick" {
				for _, arg := range call.Args[1:] {
					if key, ok := literal(arg); ok {
						keys[key] = true
					}
				}
			}
		}
	}
	ast.Inspect(fn, func(node ast.Node) bool {
		assign, ok := node.(*ast.AssignStmt)
		if !ok || len(assign.Lhs) != len(assign.Rhs) {
			return true
		}
		for i, lhs := range assign.Lhs {
			switch target := lhs.(type) {
			case *ast.Ident:
				if target.Name == variable {
					mapKeys(assign.Rhs[i])
				}
			case *ast.IndexExpr:
				if ident, ok := target.X.(*ast.Ident); ok && ident.Name == variable {
					if key, ok := literal(target.Index); ok {
						keys[key] = true
					}
				}
			}
		}
		return true
	})
	return keys
}

// returnedMapKeys are the string keys of the map literals a function
// returns.
func returnedMapKeys(fn *ast.FuncDecl) map[string]bool {
	keys := map[string]bool{}
	ast.Inspect(fn, func(node ast.Node) bool {
		ret, ok := node.(*ast.ReturnStmt)
		if !ok || len(ret.Results) == 0 {
			return true
		}
		if composite, ok := ret.Results[0].(*ast.CompositeLit); ok {
			for _, element := range composite.Elts {
				if pair, ok := element.(*ast.KeyValueExpr); ok {
					if lit, ok := pair.Key.(*ast.BasicLit); ok && lit.Kind == token.STRING {
						if key, err := strconv.Unquote(lit.Value); err == nil {
							keys[key] = true
						}
					}
				}
			}
		}
		return true
	})
	return keys
}
