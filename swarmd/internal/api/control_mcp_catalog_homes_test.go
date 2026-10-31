package api

import (
	"context"
	"encoding/json"
	"sort"
	"strings"
	"testing"
	"time"

	agentruntime "swarm/packages/swarmd/internal/agent"
	"swarm/packages/swarmd/internal/tool"
)

// swarmToolCatalogHome is the catalog tool and action a swarm_* tool's effect
// lives in.
type swarmToolCatalogHome struct{ tool, action string }

// swarmToolCatalogHomes maps every frozen swarm_* tool to its catalog home.
// The next stage deletes the swarm_* layer: a swarm_* tool without a home
// here (or in swarmToolsRetired) would lose its capability.
var swarmToolCatalogHomes = map[string]swarmToolCatalogHome{
	"swarm_activity": {"manage_fleet", "activity"}, "swarm_agents_at_work": {"manage_fleet", "agents_at_work"},
	"swarm_list_runs": {"manage_fleet", "list_runs"}, "swarm_status": {"manage_fleet", "status"},

	"swarm_media_models": {"manage_media", "models"}, "swarm_request_media": {"manage_media", "generate"},
	"swarm_read_media": {"manage_media", "read"}, "swarm_save_media": {"manage_media", "save"},
	"swarm_design": {"manage_designs", "create"}, "swarm_get_theme": {"manage_theme", "get"},
	"swarm_list_agents": {"manage_custom_agents", "list"}, "swarm_define_agent": {"manage_custom_agents", "define"},

	"swarm_create_project": {"manage_projects", "create"}, "swarm_get_project": {"manage_projects", "get"},
	"swarm_list_projects": {"manage_projects", "list"}, "swarm_update_project_context": {"manage_projects", "update"},
	"swarm_set_project_workspaces": {"manage_projects", "update"}, "swarm_retry_project_context": {"manage_projects", "update"},
	"swarm_run_task": {"manage_projects", "propose_task"}, "swarm_wait_task": {"manage_projects", "wait_tasks"},
	"swarm_task_result": {"manage_projects", "get_task"}, "swarm_list_tasks": {"manage_projects", "list_tasks"},
	"swarm_approve_task": {"manage_projects", "approve_task"}, "swarm_integrate_task": {"manage_projects", "integrate_task"},
	"swarm_get_changes": {"manage_projects", "get_task_changes"}, "swarm_read_file": {"manage_projects", "read_task_file"},
	"swarm_get_environment": {"manage_projects", "get_environment"}, "swarm_set_environment": {"manage_projects", "update_environment"},

	"swarm_answer_job": {"manage_jobs", "answer_job"}, "swarm_deliver_job": {"manage_jobs", "deliver_job"},
	"swarm_get_job": {"manage_jobs", "get_job"}, "swarm_give_job": {"manage_jobs", "give_job"},
	"swarm_message_job": {"manage_jobs", "message_job"}, "swarm_schedule_job": {"manage_jobs", "schedule_job"},
	"swarm_stop_job": {"manage_jobs", "stop_job"},

	"swarm_assign_worker_task": {"manage_workers", "assign_task"}, "swarm_create_worker": {"manage_workers", "create"},
	"swarm_deploy_worker": {"manage_workers", "create"}, "swarm_get_worker": {"manage_workers", "inspect"},
	"swarm_list_workers": {"manage_workers", "list"}, "swarm_manage_worker": {"manage_workers", "update"},
	"swarm_update_worker": {"manage_workers", "update"},

	"swarm_delete_workflow": {"manage_workflows", "delete"}, "swarm_export_workflow": {"manage_workflows", "export"},
	"swarm_import_workflow": {"manage_workflows", "import"}, "swarm_list_workflows": {"manage_workflows", "list"},
	"swarm_restore_workflow": {"manage_workflows", "restore"}, "swarm_set_workflow": {"manage_workflows", "propose"},
	"swarm_stop_workflows": {"manage_workflows", "stop_all"}, "swarm_workflow_history": {"manage_workflows", "history"},

	"swarm_cancel_update": {"manage_setup", "cancel_update"}, "swarm_connect_chatgpt": {"manage_setup", "connect_chatgpt"},
	"swarm_connect_claude_code": {"manage_setup", "connect_claude_code"}, "swarm_create_client_key": {"manage_setup", "create_client_key"},
	"swarm_create_key": {"manage_setup", "create_key"}, "swarm_get_limits": {"manage_setup", "limits"},
	"swarm_get_permissions": {"manage_setup", "permissions"}, "swarm_get_plan_usage": {"manage_setup", "plan_usage"},
	"swarm_get_usage": {"manage_setup", "usage"}, "swarm_list_fleet_keys": {"manage_setup", "list_fleet_keys"},
	"swarm_list_keys": {"manage_setup", "list_keys"}, "swarm_list_models": {"manage_setup", "models"},
	"swarm_list_providers": {"manage_setup", "providers"}, "swarm_list_secrets": {"manage_setup", "list_secrets"},
	"swarm_request_fleet_key": {"manage_setup", "request_fleet_key"}, "swarm_request_provider_key": {"manage_setup", "request_provider_key"},
	"swarm_request_update": {"manage_setup", "request_update"}, "swarm_revoke_fleet_key": {"manage_setup", "revoke_fleet_key"},
	"swarm_revoke_key": {"manage_setup", "revoke_key"}, "swarm_set_agent_model": {"manage_setup", "set_agent_model"},
	"swarm_set_limits": {"manage_setup", "set_limits"}, "swarm_set_media_models": {"manage_setup", "set_media_models"},
	"swarm_set_permissions": {"manage_setup", "set_permissions"}, "swarm_set_usage_limits": {"manage_setup", "set_usage_limits"},

	"swarm_get_session": {"manage_sessions", "get"}, "swarm_list_sessions": {"manage_sessions", "list"},
	"swarm_start_session": {"manage_sessions", "create"}, "swarm_send_message": {"manage_sessions", "send_message"},
	"swarm_stop_run": {"manage_sessions", "stop"}, "swarm_resolve_permission": {"manage_sessions", "resolve_permission"},
	"swarm_set_session_model": {"manage_sessions", "set_model"},
	"swarm_add_workspace":     {"manage_workspace", "create"}, "swarm_create_workspace": {"manage_workspace", "create"},
	"swarm_list_workspaces": {"manage_workspace", "list"},
}

// swarmToolsRetired are swarm_* tools that get no catalog home, with why.
var swarmToolsRetired = map[string]string{
	"swarm_set_session_access": "retired: a session's access is the account's permissions (manage_setup set_permissions)",
	"swarm_run_plan":           "retired: plans are not the default; work is delegated as tasks (manage_projects propose_task)",
	"swarm_accept_review":      "retired with swarm_run_plan: it closes a plan checkpoint's review",
}

// swarmToolCatalogLanding are catalog tools landing in a parallel stage: a
// home in one of them is held to the catalog once the tool is in it.
var swarmToolCatalogLanding = map[string]bool{"manage_sessions": true, "manage_workspace": true}

// Purpose:
//   - Requirement: after stage 6 every swarm_* tool has a catalog home (a
//     declared catalog tool and action with the same effect) or is listed
//     as retired with its reason, so deleting the swarm_* layer next stage
//     loses no capability.
//   - Threat/regression: a swarm_* tool left without a catalog equivalent,
//     a home naming an action the catalog does not declare, or a retired
//     tool quietly mapped.
//   - Boundary/authority: controlMCPFrozenSwarmTools (the frozen registry,
//     TestControlMCPSwarmToolNamesFrozen) against tool.Catalog.
//   - Narrowest layer: the two registries themselves.
func TestEverySwarmToolHasACatalogHome(t *testing.T) {
	var unmapped []string
	for _, name := range controlMCPFrozenSwarmTools {
		home, mapped := swarmToolCatalogHomes[name]
		reason, retired := swarmToolsRetired[name]
		switch {
		case mapped && retired:
			t.Fatalf("%s is both mapped to %s %s and retired (%s)", name, home.tool, home.action, reason)
		case retired:
			if strings.TrimSpace(reason) == "" {
				t.Fatalf("%s is retired without a reason", name)
			}
			continue
		case !mapped:
			unmapped = append(unmapped, name)
			continue
		}
		catalog, ok := tool.CatalogToolNamed(home.tool)
		if !ok {
			if swarmToolCatalogLanding[home.tool] {
				t.Logf("%s: %s is landing in a parallel stage; held once it is in the catalog", name, home.tool)
				continue
			}
			t.Fatalf("%s maps to %s, which is not a catalog tool", name, home.tool)
		}
		if declared, ok := catalog.Action(home.action); !ok || declared.Action != home.action {
			t.Fatalf("%s maps to %s %s, which the catalog does not declare", name, home.tool, home.action)
		}
	}
	sort.Strings(unmapped)
	if len(unmapped) > 0 {
		t.Fatalf("swarm_* tools without a catalog home: %v; map each in swarmToolCatalogHomes or list it in swarmToolsRetired", unmapped)
	}
	frozen := map[string]bool{}
	for _, name := range controlMCPFrozenSwarmTools {
		frozen[name] = true
	}
	for name := range swarmToolCatalogHomes {
		if !frozen[name] {
			t.Fatalf("%s is mapped but is not a swarm_* tool", name)
		}
	}
	for name := range swarmToolsRetired {
		if !frozen[name] {
			t.Fatalf("%s is retired but is not a swarm_* tool", name)
		}
	}
}

// Purpose:
//   - Requirement: manage_fleet status and activity work for the home
//     machine itself (no machine), which manage_projects' machine_status and
//     activity refuse as linked-only; both surfaces answer the swarm_status
//     and swarm_activity data in the envelope, named for this machine. A
//     task's session and a key without the routes' scopes are refused, and
//     the deprecated manage_projects alias still answers for a linked
//     machine.
//   - Threat/regression: home status falling through as an unknown action
//     again, a task's agent reading the machine's status, or a key reading
//     the activity feed without signals:read or sessions:read.
//   - Boundary/authority: Runtime.executeManageFleet and
//     Server.ManageCatalogAction (controlMCPStatus, controlMCPActivity), via
//     Runtime.ExecuteForWorkspaceScopeWithRuntime and controlMCPCall.callTool
//     over the linked parity harness.
//   - Narrowest layer: the two real entry points over in-process daemons;
//     no provider runs.
func TestManageFleetHomeStatusAndActivity(t *testing.T) {
	h := newCatalogParityHarness(t)
	for _, args := range []map[string]any{{"action": "status"}, {"action": "activity", "since_cursor": "0"}} {
		orch, err := h.orchestratorTool(t, "manage_fleet", args)
		if err != nil {
			t.Fatalf("Orchestrator %v: %v", args, err)
		}
		control, refused := h.controlTool(t, catalogParityFullKey, "manage_fleet", args)
		if refused != "" {
			t.Fatalf("Swarm Control %v: %s", args, refused)
		}
		for _, out := range []string{orch, control} {
			if err := tool.CheckEnvelope(out); err != nil {
				t.Fatalf("%v: %v\n%s", args, err, out)
			}
			envelope := parityDecode(t, out)
			if envelope["tool"] != "manage_fleet" || envelope["action"] != args["action"] || envelope["machine"] != h.home.s.HomeMachineName() && envelope["machine"] != tool.HomeMachineFallback || envelope["state"] != "ok" {
				t.Fatalf("%v: envelope %s", args, out)
			}
			data, _ := envelope["data"].(map[string]any)
			if args["action"] == "status" && data["status"] == nil {
				t.Fatalf("home status has no status: %s", out)
			}
		}
	}
	// The manage_projects machine_status alias stays linked-only.
	if _, err := h.orchestratorTool(t, "manage_projects", map[string]any{"action": "machine_status"}); err == nil {
		t.Fatal("manage_projects machine_status without machine answered; it is the linked read")
	}
	if out, err := h.orchestratorTool(t, "manage_projects", map[string]any{"action": "machine_status", "machine": "box"}); err != nil || tool.CheckEnvelope(out) != nil {
		t.Fatalf("deprecated alias machine_status@box: %v %s", err, out)
	}
	// A key with neither signals:read nor sessions:read cannot read status.
	if _, refused := h.controlTool(t, []string{"swarm:read", "projects:read"}, "manage_fleet", map[string]any{"action": "status"}); !strings.Contains(refused, "signals:read or sessions:read") {
		t.Fatalf("a key without the routes' scopes read status: %q", refused)
	}
	// A task's session cannot read the machine's status.
	h.createGoal(t, "home-task-session", map[string]any{"task_id": "h1", "project_id": "home-project"})
	ctx, cancel := context.WithTimeout(tool.WithCatalogAgent(context.Background(), agentruntime.SwarmOrchestratorAgentID), 20*time.Second)
	defer cancel()
	raw, _ := json.Marshal(map[string]any{"action": "status"})
	if _, err := h.runtime.ExecuteForWorkspaceScopeWithRuntime(ctx, tool.WorkspaceScope{SessionID: "home-task-session", Principal: h.p}, tool.Call{Name: "manage_fleet", Arguments: string(raw)}); err == nil || !strings.Contains(err.Error(), "task's session") {
		t.Fatalf("a task's session read the machine's status: %v", err)
	}
}
