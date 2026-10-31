package remote

import (
	"strings"
	"testing"

	"swarm/packages/swarmd/internal/tool"
)

// Purpose:
//   - Requirement: each action of the stage 6 catalog tools (manage_fleet,
//     manage_media, manage_custom_agents, manage_themes, manage_designs, and
//     manage_projects' get_task_changes and read_task_file) needs the level
//     of the swarm_* tool with the same effect (ToolScope), never lower, and
//     none is local-only machine setup a relay client may not reach; the
//     relay forwards each with the client's levels and lists it for Swarm
//     Control to filter per action.
//   - Threat/regression: a read key generating media or designs, or
//     defining an agent, through a level silently lowered in the move; a
//     relay gating a new catalog tool by name.
//   - Boundary/authority: tool.Catalog's declarations against toolScopes
//     and localOnlyTools; catalogTool and filterTools.
//   - Narrowest layer: the two tables and the relay's catalog gate.
func TestStage6CatalogLevelsAreTheSwarmToolsLevels(t *testing.T) {
	swarmTool := map[string]map[string]string{
		"manage_fleet": {"list_machines": "swarm_agents_at_work", "status": "swarm_status", "agents_at_work": "swarm_agents_at_work",
			"activity": "swarm_activity", "list_runs": "swarm_list_runs", "list_linked_tasks": "swarm_agents_at_work"},
		"manage_media": {"models": "swarm_media_models", "generate": "swarm_request_media", "retry": "swarm_request_media", "get": "swarm_get_job",
			"save": "swarm_save_media", "read": "swarm_read_media", "list": "swarm_read_media", "transcribe": "swarm_request_media"},
		"manage_custom_agents": {"list": "swarm_list_agents", "get": "swarm_list_agents", "define": "swarm_define_agent", "disable": "swarm_define_agent"},
		"manage_themes":        {"get": "swarm_get_theme"},
		"manage_designs":       {"create": "swarm_design", "edit": "swarm_design"},
		"manage_projects":      {"get_task_changes": "swarm_get_changes", "read_task_file": "swarm_read_file"},
	}
	for toolName, actions := range swarmTool {
		catalog, ok := tool.CatalogToolNamed(toolName)
		if !ok {
			t.Fatalf("%s is not a catalog tool", toolName)
		}
		if toolName != "manage_projects" && len(catalog.Actions) != len(actions) {
			t.Fatalf("%s declares %d actions; %d have a swarm_* tool to take their level from", toolName, len(catalog.Actions), len(actions))
		}
		for action, name := range actions {
			declared, ok := catalog.Action(action)
			if !ok {
				t.Fatalf("%s does not declare %s", toolName, action)
			}
			if _, known := toolScopes[name]; !known {
				t.Fatalf("%s has no level of its own in toolScopes", name)
			}
			if declared.Level.Scope() != ToolScope(name) {
				t.Fatalf("%s %s needs %s; %s needs %s", toolName, action, declared.Level.Scope(), name, ToolScope(name))
			}
			if declared.LocalOnly || localOnlyTools[name] {
				t.Fatalf("%s %s: local-only %v / %s local-only %v", toolName, action, declared.LocalOnly, name, localOnlyTools[name])
			}
			// A read-level relay client never reaches a write action.
			if declared.Level != tool.CatalogLevelRead && tool.CatalogLevelsAllow(declared, []string{ScopeRead}, true) {
				t.Fatalf("a read relay client reaches %s %s", toolName, action)
			}
		}
		if !catalogTool(toolName) {
			t.Fatalf("the relay gates %s by name", toolName)
		}
		if listed := string(filterTools([]byte(`{"result":{"tools":[{"name":"`+toolName+`"}]}}`), []string{ScopeRead})); !strings.Contains(listed, toolName) {
			t.Fatalf("relay listing drops %s for a read client: %s", toolName, listed)
		}
	}
}
