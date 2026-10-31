package api

import (
	"encoding/json"
	"fmt"
	"sort"
	"strings"
	"testing"

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
}

// swarmCatalogOpenGaps are swarm_* inputs and answer fields this stage has
// not moved into the catalog yet. Each is a known, named gap the next stage
// must close before the swarm_* layer is deleted; the test fails when one is
// closed so the entry goes with it. It is not a list of decisions.
var swarmCatalogOpenGaps = map[string]string{
	"swarm_create_worker.project_id":     "manage_workers create is a Worker V2 proposal; the swarm_* immediate worker inputs are not mapped onto it yet",
	"swarm_create_worker.schedule":       "as swarm_create_worker.project_id",
	"swarm_create_worker.scheduled_plan": "as swarm_create_worker.project_id",
	"swarm_create_worker.workspace_path": "as swarm_create_worker.project_id",
	"swarm_deploy_worker.preset":         "as swarm_create_worker.project_id",
	"swarm_deploy_worker.role":           "as swarm_create_worker.project_id",
	"swarm_deploy_worker.worker":         "as swarm_create_worker.project_id",
	"swarm_update_worker.agent":          "manage_workers update does not change a worker's custom agent yet",
	"swarm_list_workers.include_jobs":    "manage_workers list does not add each worker's latest jobs yet",
	"swarm_manage_worker.action":         "cancel_run, enable_schedule and disable_schedule have no manage_workers action yet (pause, resume, archive and delete are actions)",
	"swarm_manage_worker.run_id":         "as swarm_manage_worker.action",
	"swarm_manage_worker.schedule_id":    "as swarm_manage_worker.action",
	"swarm_get_project.wait_seconds":     "manage_projects get has no held wait for project.md generation yet",
	"swarm_run_task.model":               "propose_task has no per-task model choice yet",
	"swarm_run_task.wait_seconds":        "propose_task has no held wait yet (wait_tasks follows the task)",
	"swarm_get_environment.include_log":  "get_environment reads one folder by workspace_path; the swarm_* project/workspace place, log and held wait are not mapped yet",
	"swarm_get_environment.project":      "as swarm_get_environment.include_log",
	"swarm_get_environment.wait_seconds": "as swarm_get_environment.include_log",
	"swarm_get_environment.workspace":    "as swarm_get_environment.include_log",
	"swarm_set_environment.append_setup": "update_environment takes the definition as one environment object (and rebuild_environment rebuilds); the swarm_* flat inputs, place, reset and held wait are not mapped yet",
	"swarm_set_environment.base_image":   "as swarm_set_environment.append_setup",
	"swarm_set_environment.env":          "as swarm_set_environment.append_setup",
	"swarm_set_environment.max_leases":   "as swarm_set_environment.append_setup",
	"swarm_set_environment.project":      "as swarm_set_environment.append_setup",
	"swarm_set_environment.rebuild":      "as swarm_set_environment.append_setup",
	"swarm_set_environment.reset":        "as swarm_set_environment.append_setup",
	"swarm_set_environment.setup":        "as swarm_set_environment.append_setup",
	"swarm_set_environment.unset_env":    "as swarm_set_environment.append_setup",
	"swarm_set_environment.wait_seconds": "as swarm_set_environment.append_setup",
	"swarm_set_environment.workspace":    "as swarm_set_environment.append_setup",

	"swarm_get_worker.answer.id":                    "manage_workers inspect answers the Worker V2 review; swarm_get_worker's flat worker fields are not mapped onto it yet",
	"swarm_get_worker.answer.instructions":          "as swarm_get_worker.answer.id",
	"swarm_get_worker.answer.lifecycle_state":       "as swarm_get_worker.answer.id",
	"swarm_get_worker.answer.name":                  "as swarm_get_worker.answer.id",
	"swarm_get_worker.answer.recent_runs":           "as swarm_get_worker.answer.id",
	"swarm_get_worker.answer.revision":              "as swarm_get_worker.answer.id",
	"swarm_task_result.answer.task_id":              "get_task's compact view (data.task) names the task id, agent and result its own way; swarm_task_result's flat fields are not added yet",
	"swarm_task_result.answer.role":                 "as swarm_task_result.answer.task_id",
	"swarm_task_result.answer.report":               "as swarm_task_result.answer.task_id",
	"swarm_task_result.answer.delivery_state":       "as swarm_task_result.answer.task_id",
	"swarm_task_result.answer.delivery_reason":      "as swarm_task_result.answer.task_id",
	"swarm_task_result.answer.delivery_reason_code": "as swarm_task_result.answer.task_id",
	"swarm_task_result.answer.changes_error":        "as swarm_task_result.answer.task_id",
	"swarm_task_result.answer.next":                 "as swarm_task_result.answer.task_id",
	"swarm_wait_task.answer.task_id":                "wait_tasks answers one row per task (tasks[].task_id, milestone) and its state; swarm_wait_task's one-task flat fields are not added yet",
	"swarm_wait_task.answer.session_id":             "as swarm_wait_task.answer.task_id",
	"swarm_wait_task.answer.settled":                "as swarm_wait_task.answer.task_id",
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
//     its reason; gaps this stage leaves open are named, never silent.
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
	for key := range swarmCatalogOpenGaps {
		if listed[key] {
			t.Fatalf("%s is both a drop and an open gap", key)
		}
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
	"swarm_create_project":         {swarm: map[string]any{"name": "Probe A", "workspaces": []any{map[string]any{"path": "$folder", "role": "primary_code"}}}, catalog: map[string]any{"action": "create", "name": "Probe B", "workspaces": []any{map[string]any{"path": "$folder", "role": "primary_code"}}}, record: "project"},
	"swarm_set_project_workspaces": {swarm: map[string]any{"project_id": "home-project", "workspaces": []any{map[string]any{"path": "$folder", "role": "primary_code"}}}, catalog: map[string]any{"action": "update", "project_id": "home-project", "workspaces": []any{map[string]any{"path": "$folder", "role": "primary_code"}}}, record: "project"},
	"swarm_wait_task":              {swarm: map[string]any{"project_id": "home-project", "task_id": "h3", "wait_seconds": float64(1)}, catalog: map[string]any{"action": "wait_tasks", "project_id": "home-project", "task_id": "h3", "wait_seconds": float64(1)}},
	"swarm_get_project":            {swarm: map[string]any{"project_id": "home-project"}, catalog: map[string]any{"action": "get", "project_id": "home-project"}, record: "project"},
	"swarm_list_workers":           {swarm: map[string]any{}, catalog: map[string]any{"action": "list"}},
	"swarm_get_changes":            {swarm: map[string]any{"session_id": "home-orch"}, catalog: map[string]any{"action": "get_task_changes", "session_id": "home-orch"}},
	"swarm_read_file":              {swarm: map[string]any{"session_id": "home-orch", "path": "."}, catalog: map[string]any{"action": "read_task_file", "session_id": "home-orch", "path": "."}},
	"swarm_retry_project_context":  {swarm: map[string]any{"project_id": "home-project"}, catalog: map[string]any{"action": "update", "project_id": "home-project"}},
	"swarm_update_project_context": {swarm: map[string]any{"project_id": "home-project", "project_md": "# Home"}, catalog: map[string]any{"action": "update", "project_id": "home-project", "project_md": "# Home"}, record: "project"},
	"swarm_task_result":            {swarm: map[string]any{"project_id": "home-project", "task_id": "h1"}, catalog: map[string]any{"action": "get_task", "project_id": "home-project", "task_id": "h1"}, record: "task"},
	"swarm_connect_chatgpt":        {swarm: map[string]any{"action": "status", "login_id": "no-such-login"}, catalog: map[string]any{"action": "connect_chatgpt", "step": "status", "login_id": "no-such-login"}},
	"swarm_connect_claude_code":    {swarm: map[string]any{"action": "status"}, catalog: map[string]any{"action": "connect_claude_code", "step": "status"}},
}

// swarmAnswerUnprobed are pairs the harness cannot give a representative
// answer for: both surfaces refuse the same call (the same handler's
// refusal is checked), with why no answer comes.
var swarmAnswerUnprobed = map[string]string{
	"swarm_approve_task":      "approve_task is not served to Swarm Control (the person's step in their app): there is no Swarm Control answer to compare",
	"swarm_create_key":        "manage_setup create_key makes no key (a new key's secret may reach only the person): its refusal is the answer",
	"swarm_create_client_key": "manage_setup create_client_key makes no key, as create_key: its refusal is the answer",
}

// Purpose:
//   - Requirement: every top-level field a swarm_* tool's answer carries is
//     in its catalog action's envelope data under the same name (or in the
//     data's record, for a swarm_* answer that is that record flat); a
//     swarm_* answer's next hint is the envelope's next_action or next.
//     Deliberate drops and open gaps are listed with their reasons.
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
	place := func(args map[string]any) map[string]any {
		raw, _ := json.Marshal(args)
		var out map[string]any
		_ = json.Unmarshal([]byte(strings.ReplaceAll(string(raw), `"$folder"`, fmt.Sprintf("%q", folder))), &out)
		return out
	}
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
		if reason, unprobed := swarmAnswerUnprobed[name]; unprobed {
			if strings.TrimSpace(reason) == "" {
				t.Fatalf("%s is unprobed without a reason", name)
			}
			// Its catalog action must still refuse (not served, or by
			// design); an answer means the entry went stale.
			if probe.catalog != nil {
				if _, refused := h.controlTool(t, catalogKey, home.tool, probe.catalog); refused == "" {
					t.Fatalf("%s is listed unprobed but %s %s answers: compare its answer instead", name, home.tool, home.action)
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
				t.Fatalf("%s: one surface answered: swarm_* refusal %q; %s %s refusal %q", name, swarmErr, home.tool, home.action, catalogErr)
			}
			t.Logf("%s: both refuse the probe: %q / %q", name, swarmErr, catalogErr)
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
			_, open := swarmCatalogOpenGaps[key]
			switch {
			case found && (dropped || open):
				t.Fatalf("%s is listed but the catalog answer carries %s: remove the entry", key, field)
			case !found && !dropped && !open:
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
	result, rpcErr := c.callTool(params)
	if rpcErr != nil {
		t.Fatalf("%s tools/call: %+v", name, rpcErr)
	}
	content := result["content"].([]map[string]any)
	text, _ := content[0]["text"].(string)
	if result["isError"] == true {
		return "", text
	}
	return text, ""
}
