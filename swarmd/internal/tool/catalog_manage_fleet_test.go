package tool

import (
	"strings"
	"testing"
)

// Purpose:
//   - Requirement: fleet monitoring is its own catalog tool, manage_fleet,
//     with list_machines, status, agents_at_work, activity, list_runs and
//     list_linked_tasks, every one a read in the Orchestrator's definition
//     and Swarm Control's default listing; status and activity reach this
//     machine too. manage_projects keeps its fleet actions for one stage as
//     aliases whose descriptions say they are deprecated, and runs them as
//     before.
//   - Threat/regression: a fleet action dropped in the move, a write level
//     on a read, status or activity still linked-only, or an alias removed
//     (breaking existing conversations and skills) or left undeprecated.
//   - Boundary/authority: manageFleetCatalog, manageProjectsCatalog and
//     manageFleetDefinition.
//   - Narrowest layer: the declarations themselves.
func TestManageFleetCatalogDeclaresTheFleetActions(t *testing.T) {
	fleet := manageFleetCatalog()
	want := []string{"list_machines", "status", FleetAgentsAtWorkAction, "activity", "list_runs", "list_linked_tasks"}
	if got := strings.Join(catalogActionNames(fleet), ","); got != "activity,agents_at_work,list_linked_tasks,list_machines,list_runs,status" {
		t.Fatalf("manage_fleet actions: %s", got)
	}
	enum := manageFleetDefinition().Parameters["properties"].(map[string]any)["action"].(map[string]any)["enum"].([]string)
	if len(enum) != len(want) {
		t.Fatalf("Orchestrator definition lists %v", enum)
	}
	for _, action := range fleet.Actions {
		if action.Level != CatalogLevelRead || !action.Listed(CatalogProfileEssentials) || !action.Listed(CatalogProfileOrchestrator) || !action.Untrusted || len(action.APIScopes) == 0 {
			t.Fatalf("%s: %+v", action.Action, action)
		}
		if (action.Action == "status" || action.Action == "activity") && action.Reach != CatalogReachAny {
			t.Fatalf("%s must reach this machine too: %s", action.Action, action.Reach)
		}
	}
	projects := manageProjectsCatalog()
	for alias, now := range map[string]string{"list_machines": "list_machines", "list_linked_tasks": "list_linked_tasks", FleetAgentsAtWorkAction: FleetAgentsAtWorkAction, FleetMachineStatusAction: "status", FleetMachineActivityAction: "activity"} {
		declared, ok := projects.Action(alias)
		if !ok || !strings.HasPrefix(declared.Description, "Deprecated: use manage_fleet "+now+".") {
			t.Fatalf("manage_projects %s is not a deprecated alias of manage_fleet %s: %+v", alias, now, declared)
		}
		if manageFleetProjectsAlias[now] != alias {
			t.Fatalf("manage_fleet %s does not run manage_projects %s", now, alias)
		}
	}
}
