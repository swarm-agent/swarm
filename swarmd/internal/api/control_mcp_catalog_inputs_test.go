package api

import (
	"encoding/json"
	"fmt"
	"runtime/debug"
	"sort"
	"strings"
	"testing"
	"time"

	"swarm/packages/swarmd/internal/agentmodelsettings"
	"swarm/packages/swarmd/internal/identity"
	"swarm/packages/swarmd/internal/model"
	pebblestore "swarm/packages/swarmd/internal/store/pebble"
	"swarm/packages/swarmd/internal/tool"
	"swarm/packages/swarmd/internal/workspace"
)

// swarmCatalogDrops are the swarm_* inputs ("tool.input") and answer fields
// ("tool.answer.field") a catalog action deliberately does not take or
// return, each with its one-line reason. Keep it short: a drop is a decision,
// not a gap.
var swarmCatalogDrops = map[string]string{
	"swarm_approve_task.action":        "action is the catalog's own field; approving or rejecting a waiting task card is the person's step in their app (approve_task is not served to Swarm Control)",
	"swarm_connect_chatgpt.action":     "action is the catalog's own field; connect_chatgpt takes it as step",
	"swarm_connect_claude_code.action": "action is the catalog's own field; connect_claude_code takes it as step",
	"swarm_create_key.kind":            "manage_setup's kind is media_models' media kind; create_key takes it as key_kind",
	"swarm_design.edit_path":           "an edit is manage_designs edit, which takes edit_path",
	"swarm_request_media.retry_of":     "a retry is manage_media retry, whose job_id is retry_of",
	"swarm_get_job.include_media":      "MCP image blocks are not envelope data; manage_media read returns a media job's file as base64",
	"swarm_run_task.role":              "manage_projects' role is read_task_session's message role; agent names the task's agent",
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

// swarmCatalogInputs are the inputs a catalog action takes: its own and its
// Swarm Control inputs.
func swarmCatalogInputs(action tool.CatalogAction) map[string]bool {
	out := map[string]bool{}
	for _, name := range append(append([]string(nil), action.Inputs...), action.ControlInputs...) {
		out[name] = true
	}
	return out
}

// Purpose:
//   - Requirement: before the swarm_* layer is deleted, every catalog action
//     takes every input its swarm_* tool takes, under the same name (its
//     own or a Swarm Control input), unless the input is a listed drop with
//     its reason (swarmCatalogDrops, the one exemption table).
//   - Threat/regression: a client losing an input (a wait, a filter, an
//     option) when its swarm_* tool goes, or a drop/gap entry that went
//     stale and hides nothing.
//   - Boundary/authority: controlMCPTools' input schemas against
//     tool.Catalog's declared Inputs and ControlInputs, over
//     swarmToolCatalogHomes.
//   - Narrowest layer: the registries themselves; the answer test below
//     calls both surfaces.
func TestEverySwarmToolInputIsACatalogInput(t *testing.T) {
	swarm := map[string]controlMCPTool{}
	for _, candidate := range controlMCPTools() {
		swarm[candidate.Name] = candidate
	}
	listed := map[string]bool{}
	for key := range swarmCatalogDrops {
		listed[key] = true
	}
	for key, reason := range swarmCatalogDrops {
		if strings.TrimSpace(reason) == "" {
			t.Fatalf("drop %s has no reason", key)
		}
	}
	var missing []string
	seen := map[string]bool{}
	for _, name := range sortedSwarmHomes() {
		home := swarmToolCatalogHomes[name]
		catalog, ok := tool.CatalogToolNamed(home.tool)
		if !ok {
			continue // landing in a parallel stage (TestEverySwarmToolHasACatalogHome)
		}
		action, _ := catalog.Action(home.action)
		inputs := swarmCatalogInputs(action)
		properties, _ := swarm[name].InputSchema["properties"].(map[string]any)
		for input := range properties {
			key := name + "." + input
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
		t.Fatalf("swarm_* inputs no catalog action takes: %v; add each to the action (same name and meaning), or list it in swarmCatalogDrops with its reason", missing)
	}
	for key := range listed {
		if strings.Count(key, ".") == 1 && !seen[key] {
			t.Fatalf("%s is listed but is not an input of that swarm_* tool", key)
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

// swarmAnswerProbe is a representative call of a swarm_* tool and of its
// catalog action, and where the catalog's data holds the swarm_* answer's
// flat record (record: data.task for swarm_task_result), if anywhere.
type swarmAnswerProbe struct {
	swarm, catalog map[string]any
	record         string
}

// swarmAnswerProbes override the probe the parity cases give (their first
// local case: the Orchestrator's arguments for the swarm_* tool, Swarm
// Control's for the catalog action) where the two take other arguments.
var swarmAnswerProbes = map[string]swarmAnswerProbe{
	"swarm_create_project":         {swarm: map[string]any{"name": "Probe A", "workspaces": []any{map[string]any{"path": "$folder", "role": "primary_code"}}}, catalog: map[string]any{"action": "create", "name": "Probe B", "client_request_id": "probe-b", "workspaces": []any{map[string]any{"path": "$folder", "role": "primary_code"}}}, record: "project"},
	"swarm_set_project_workspaces": {swarm: map[string]any{"project_id": "edit-a", "workspaces": []any{map[string]any{"path": "$folder", "role": "primary_code"}}}, catalog: map[string]any{"action": "update", "project_id": "edit-b", "workspaces": []any{map[string]any{"path": "$folder", "role": "primary_code"}}}, record: "project"},
	"swarm_wait_task":              {swarm: map[string]any{"project_id": "home-project", "task_id": "h3", "wait_seconds": float64(1)}, catalog: map[string]any{"action": "wait_tasks", "project_id": "home-project", "task_id": "h3", "wait_seconds": float64(1)}},
	"swarm_get_project":            {swarm: map[string]any{"project_id": "home-project"}, catalog: map[string]any{"action": "get", "project_id": "home-project"}, record: "project"},
	"swarm_list_workers":           {swarm: map[string]any{}, catalog: map[string]any{"action": "list"}},
	"swarm_get_worker":             {swarm: map[string]any{"worker_id": "$worker"}, catalog: map[string]any{"action": "inspect", "worker_id": "$worker"}, record: "worker"},
	"swarm_get_changes":            {swarm: map[string]any{"session_id": "probe-folder"}, catalog: map[string]any{"action": "get_task_changes", "session_id": "probe-folder"}},
	"swarm_read_file":              {swarm: map[string]any{"session_id": "probe-folder", "path": "."}, catalog: map[string]any{"action": "read_task_file", "session_id": "probe-folder", "path": "."}},
	"swarm_retry_project_context":  {swarm: map[string]any{"project_id": "retry-a"}, catalog: map[string]any{"action": "update", "project_id": "retry-b"}},
	"swarm_update_project_context": {swarm: map[string]any{"project_id": "edit-c", "project_md": "# Probe"}, catalog: map[string]any{"action": "update", "project_id": "edit-d", "project_md": "# Probe"}, record: "project"},
	"swarm_run_task":               {swarm: map[string]any{"project_id": "probe-project", "brief": "Probe A.", "role": "swarm", "workspace": "$folder"}, catalog: map[string]any{"action": "propose_task", "project_id": "probe-project", "brief": "Probe B.", "agent": "swarm", "workspace": "$folder"}},
	"swarm_task_result":            {swarm: map[string]any{"project_id": "home-project", "task_id": "h1"}, catalog: map[string]any{"action": "get_task", "project_id": "home-project", "task_id": "h1"}, record: "task"},
	"swarm_connect_chatgpt":        {swarm: map[string]any{"action": "status", "login_id": "no-such-login"}, catalog: map[string]any{"action": "connect_chatgpt", "step": "status", "login_id": "no-such-login"}},
	"swarm_connect_claude_code":    {swarm: map[string]any{"action": "status"}, catalog: map[string]any{"action": "connect_claude_code", "step": "status"}},
	// Seeded by TestEverySwarmAnswerFieldIsInTheCatalogEnvelope: $worker,
	// $job, $jobworker, probe-project (its folder $folder), probe-folder (a
	// session working in it), retry-a and retry-b (a failed project.md),
	// $key_a and $key_b, and the current $perm_* and $limits_* values.
	"swarm_assign_worker_task": {swarm: map[string]any{"worker_id": "$worker", "task": "Probe A.", "idempotency_key": "probe-a"}, catalog: map[string]any{"action": "assign_task", "worker_id": "$worker", "task": "Probe B.", "idempotency_key": "probe-b"}},
	"swarm_message_job":        {swarm: map[string]any{"job_id": "$msg_a", "message": "Probe A."}, catalog: map[string]any{"action": "message_job", "job_id": "$msg_b", "message": "Probe B.", "client_request_id": "probe-b"}},
	"swarm_update_worker":      {swarm: map[string]any{"worker_id": "$worker", "agent": ""}, catalog: map[string]any{"action": "update", "worker_id": "$worker", "agent": ""}},
	"swarm_manage_worker":      {swarm: map[string]any{"worker_id": "$jobworker", "action": "cancel_run", "run_id": "probe-cancel-a"}, catalog: map[string]any{"action": "update", "worker_id": "$jobworker", "run_id": "probe-cancel-b"}},
	"swarm_create_worker":      {swarm: map[string]any{"name": "Probe A", "instructions": "Probe.", "workspace_path": "$folder"}, catalog: map[string]any{"action": "create", "name": "Probe B", "instructions": "Probe.", "workspace_path": "$folder"}},
	"swarm_deploy_worker":      {swarm: map[string]any{"preset": "finder"}, catalog: map[string]any{"action": "create", "preset": "reviewer"}},
	"swarm_give_job":           {swarm: map[string]any{"worker": "parity-worker", "brief": "Probe A.", "project": "probe-project"}, catalog: map[string]any{"action": "give_job", "worker": "parity-worker", "brief": "Probe B.", "project": "probe-project"}},
	"swarm_schedule_job":       {swarm: map[string]any{"worker": "parity-worker", "brief": "Probe A.", "project": "probe-project", "name": "probe-a", "schedule": map[string]any{"kind": "interval", "interval_seconds": float64(3600)}}, catalog: map[string]any{"action": "schedule_job", "worker": "parity-worker", "brief": "Probe B.", "project": "probe-project", "name": "probe-b", "schedule": map[string]any{"kind": "interval", "interval_seconds": float64(3600)}}},
	"swarm_set_agent_model":    {swarm: map[string]any{"role": "system-finder", "provider": "fixture-provider", "model": "fixture-model", "thinking": "low"}, catalog: map[string]any{"action": "set_agent_model", "role": "system-finder", "provider": "fixture-provider", "model": "fixture-model", "thinking": "low"}},
	"swarm_set_usage_limits":   {swarm: map[string]any{"daily_cost_limit_usd": float64(10)}, catalog: map[string]any{"action": "set_usage_limits", "daily_cost_limit_usd": float64(10)}},
	"swarm_set_limits":         {swarm: map[string]any{"expected_revision": "$limits_revision", "limits": "$limits"}, catalog: map[string]any{"action": "set_limits", "expected_revision": "$limits_revision", "limits": "$limits"}},
	"swarm_set_permissions":    {swarm: map[string]any{"expected_revision": "$perm_revision", "client_request_id": "probe-a", "tasks": "approve"}, catalog: map[string]any{"action": "set_permissions", "expected_revision": "$perm_revision", "client_request_id": "probe-b", "tasks": "approve"}},
	"swarm_request_update":     {swarm: map[string]any{"max_seconds": float64(0)}, catalog: map[string]any{"action": "request_update", "max_seconds": float64(0)}},
	"swarm_revoke_key":         {swarm: map[string]any{"key_id": "$key_a"}, catalog: map[string]any{"action": "revoke_key", "key_id": "$key_b"}},
}

// swarmAnswerUnprobed are pairs the hermetic harness cannot give an
// answering call, each with why: both surfaces must refuse the probe (or,
// without a probe, the catalog action must refuse); a pair that answers is
// compared, and an entry whose pair answers fails as stale. Any other pair
// where both surfaces refuse fails: give it a probe that answers.
var swarmAnswerUnprobed = map[string]swarmUnprobed{
	"swarm_approve_task":      {refusedByDesign: true, reason: "approve_task is not served to Swarm Control (the person's step in their app): there is no Swarm Control answer to compare"},
	"swarm_create_key":        {refusedByDesign: true, reason: "manage_setup create_key makes no key (a new key's secret may reach only the person): its refusal is the answer"},
	"swarm_create_client_key": {refusedByDesign: true, reason: "manage_setup create_client_key makes no key, as create_key: its refusal is the answer"},

	// The hermetic harness runs no provider, worktree service, workspace
	// authority, sandbox environments, secret slots or media models, so
	// these refuse on both surfaces with the same handler's reason.
	"swarm_answer_job":           {reason: "no seeded job is waiting on a question: one needs a worker run that asked"},
	"swarm_deliver_job":          {reason: "no seeded job has a result to merge: one needs a finished worker run with commits"},
	"swarm_integrate_task":       {reason: "integration needs a task with a real worktree branch, which the worktree service makes"},
	"swarm_run_task":             {reason: "a task needs the worktree service and the router's model provider"},
	"swarm_assign_worker_task":   {reason: "a run needs a worker bound to a folder, and binding needs the workspace authority"},
	"swarm_create_worker":        {reason: "a new worker is bound to its folder through the workspace authority"},
	"swarm_give_job":             {reason: "a job's folder is checked by the workspace authority"},
	"swarm_schedule_job":         {reason: "a scheduled worker is bound to its folder through the workspace authority"},
	"swarm_get_environment":      {reason: "sandbox environments are not configured"},
	"swarm_set_environment":      {reason: "sandbox environments are not configured"},
	"swarm_list_secrets":         {reason: "secret slots are not configured"},
	"swarm_connect_claude_code":  {reason: "no Claude Code provider is registered"},
	"swarm_request_provider_key": {reason: "no provider that takes an API key is registered"},
	"swarm_request_fleet_key":    {reason: "no provider that takes an API key is registered"},
	"swarm_revoke_fleet_key":     {reason: "no fleet key exists without a provider that takes an API key"},
	"swarm_media_models":         {reason: "media models are not configured"},
	"swarm_set_media_models":     {reason: "media models are not configured"},
	"swarm_read_media":           {reason: "no media job has a file without media models"},
	"swarm_save_media":           {reason: "no media job exists without media models"},
	"swarm_request_media":        {reason: "a media job runs a media model, and none is configured"},
	"swarm_design":               {reason: "a design runs the designer's model provider"},
}

// swarmUnprobed is why a pair has no compared answer. refusedByDesign: the
// catalog action refuses every Swarm Control call (the swarm_* tool may
// answer); otherwise both surfaces refuse the probe in the harness.
type swarmUnprobed struct {
	reason          string
	refusedByDesign bool
}

// Purpose:
//   - Requirement: every top-level field a swarm_* tool's answer carries is
//     in its catalog action's envelope data under the same name (or in the
//     data's record, for a swarm_* answer that is that record flat); a
//     swarm_* answer's next hint is the envelope's next_action or next.
//     Deliberate drops are listed with their reasons; every pair is compared
//     from a call both surfaces answer, or listed unprobed with its reason.
//   - Threat/regression: a client losing an answer field when its swarm_*
//     tool goes.
//   - Boundary/authority: controlMCPCall.callTool for both the swarm_* tool
//     (its handler over the internal routes) and the catalog tool
//     (Runtime.ExecuteControlCatalog), over the in-process parity harness.
//   - Narrowest layer: the two real Swarm Control entry points; no provider
//     runs.
func TestEverySwarmAnswerFieldIsInTheCatalogEnvelope(t *testing.T) {
	h := newCatalogParityHarness(t)
	// $folder in a probe is a real git folder on this machine, which its
	// workspace service registers in place.
	if h.home.s.workspace == nil {
		h.home.s.workspace = workspace.NewService(pebblestore.NewWorkspaceStore(h.home.s.sessions.Store().Underlying()))
	}
	folder := t.TempDir()
	outboxPinGit(t, folder, "init", "-q", "-b", "dev")
	outboxPinGit(t, folder, "commit", "-q", "--allow-empty", "-m", "start")
	place := h.seedSwarmAnswerProbes(t, folder)
	cases := catalogParityCases()
	swarmKey := append(append([]string(nil), catalogParitySetupKey...), "agents:read", "agents:write")
	var missing []string
	for _, name := range sortedSwarmHomes() {
		home := swarmToolCatalogHomes[name]
		if _, ok := tool.CatalogToolNamed(home.tool); !ok {
			continue
		}
		probe, ok := swarmAnswerProbes[name]
		if !ok {
			for _, c := range cases[home.tool][home.action] {
				if machine, _ := c.control["machine"].(string); machine == "" && c.compare != parityNotServed {
					c = h.resolveCase(t, c)
					probe = swarmAnswerProbe{swarm: map[string]any{}, catalog: c.control}
					for key, value := range c.orchestrator {
						if key != "action" {
							probe.swarm[key] = value
						}
					}
					break
				}
			}
		}
		catalogKey := catalogParityFullKey
		if home.tool == "manage_setup" {
			catalogKey = catalogParitySetupKey
		}
		if unprobed, listed := swarmAnswerUnprobed[name]; listed {
			if strings.TrimSpace(unprobed.reason) == "" {
				t.Fatalf("%s is unprobed without a reason", name)
			}
			// Both surfaces must still refuse; an answer means the entry
			// went stale.
			if probe.catalog != nil {
				if _, refused := h.controlTool(t, catalogKey, home.tool, place(probe.catalog)); refused == "" {
					t.Errorf("%s is listed unprobed but %s %s answers: compare its answer instead", name, home.tool, home.action)
				}
			}
			if probe.swarm != nil && !unprobed.refusedByDesign {
				if _, refused := h.swarmTool(t, swarmKey, name, place(probe.swarm)); refused == "" {
					t.Errorf("%s is listed unprobed but the swarm_* tool answers: give the pair a probe that answers on both", name)
				}
			}
			continue
		}
		if probe.catalog == nil {
			t.Fatalf("%s: no representative call; add one to swarmAnswerProbes", name)
		}
		swarmOut, swarmErr := h.swarmTool(t, swarmKey, name, place(probe.swarm))
		catalogOut, catalogErr := h.controlTool(t, catalogKey, home.tool, place(probe.catalog))
		if swarmErr != "" || catalogErr != "" {
			if swarmErr == "" || catalogErr == "" {
				// Answers are not printed: a probe's answer may hold a secret.
				t.Errorf("%s: one surface answered: swarm_* refusal %q; %s %s refusal %q", name, swarmErr, home.tool, home.action, catalogErr)
				continue
			}
			t.Errorf("%s: both surfaces refuse the probe, so no answer is compared (swarm_* %q; %s %s %q): give it a probe that answers, or list it in swarmAnswerUnprobed with the reason", name, swarmErr, home.tool, home.action, catalogErr)
			continue
		}
		var answer map[string]any
		if err := json.Unmarshal([]byte(swarmOut), &answer); err != nil {
			t.Fatalf("%s: swarm_* answer is not an object: %s", name, swarmOut)
		}
		envelope := parityDecode(t, catalogOut)
		data, _ := envelope["data"].(map[string]any)
		record, _ := data[probe.record].(map[string]any)
		for field := range answer {
			key := name + ".answer." + field
			_, inData := data[field]
			_, inRecord := record[field]
			found := inData || inRecord
			if field == "next" {
				found = found || envelope["next_action"] != nil || envelope["next"] != nil
			}
			_, dropped := swarmCatalogDrops[key]
			switch {
			case found && dropped:
				t.Fatalf("%s is listed but the catalog answer carries %s: remove the entry", key, field)
			case !found && !dropped:
				missing = append(missing, key)
			}
		}
	}
	sort.Strings(missing)
	if len(missing) > 0 {
		t.Fatalf("swarm_* answer fields the catalog envelope's data lacks: %v; return each under the same name, or list it in swarmCatalogDrops with its reason", missing)
	}
}

// swarmTool runs a swarm_* tool through Swarm Control's tools/call with the
// internal route handler, as a request does: its answer, or the error text.
func (h *catalogParityHarness) swarmTool(t *testing.T, scopes []string, name string, args map[string]any) (string, string) {
	t.Helper()
	req := h.controlRequest(scopes, "")
	c := &controlMCPCall{request: req, next: h.home.s.internalRouteHandler(), server: h.home.s}
	params, _ := json.Marshal(map[string]any{"name": name, "arguments": args})
	defer func() {
		if r := recover(); r != nil {
			t.Fatalf("%s tools/call panicked: %v\n%s", name, r, debug.Stack())
		}
	}()
	result, rpcErr := c.callTool(params)
	if rpcErr != nil {
		t.Fatalf("%s tools/call: %+v", name, rpcErr)
	}
	content, _ := result["content"].([]map[string]any)
	if len(content) == 0 {
		t.Fatalf("%s tools/call answered no content", name)
	}
	text, _ := content[0]["text"].(string)
	if result["isError"] == true {
		return "", text
	}
	return text, ""
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
	values := map[string]any{"$msg_a": controlMCPJobID(controlMCPJobWorker, h.jobs[""]["worker"], "probe-msg-a"), "$msg_b": controlMCPJobID(controlMCPJobWorker, h.jobs[""]["worker"], "probe-msg-b"), "$folder": folder, "$worker": h.workers[""], "$job": h.jobs[""]["parity"], "$jobworker": h.jobs[""]["worker"], "$jobdone": h.jobs[""]["done"]}
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
