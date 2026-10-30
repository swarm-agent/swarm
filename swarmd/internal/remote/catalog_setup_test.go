package remote

import (
	"strings"
	"testing"

	"swarm/packages/swarmd/internal/tool"
)

// Purpose:
//   - Requirement: each manage_setup action needs the level of the swarm_*
//     setup tool with the same effect (ToolScope), never lower, and is
//     local_only (and home-only) exactly when that tool is in localOnlyTools,
//     so moving setup into the catalog neither widens what a key may do nor
//     lets a relay client reach sign-in, provider keys or key creation. The
//     relay forwards manage_setup with the client's levels (a catalog tool)
//     and lists it for Swarm Control to filter per action.
//   - Threat/regression: a write key raising usage limits or draining the
//     machine, a level silently lowered in the move, or a relayed client
//     reaching local-only setup.
//   - Boundary/authority: tool.Catalog's manage_setup declarations against
//     ToolScope and localOnlyTools; catalogTool and filterTools.
//   - Narrowest layer: the two tables and the relay's catalog gate.
func TestManageSetupLevelsAreTheSwarmToolsLevels(t *testing.T) {
	swarmTool := map[string]string{
		"models": "swarm_list_models", "providers": "swarm_list_providers", "usage": "swarm_get_usage", "plan_usage": "swarm_get_plan_usage",
		"limits": "swarm_get_limits", "permissions": "swarm_get_permissions", "list_keys": "swarm_list_keys", "list_fleet_keys": "swarm_list_fleet_keys",
		"list_secrets": "swarm_list_secrets", "media_models": "swarm_media_models", "set_agent_model": "swarm_set_agent_model",
		"set_media_models": "swarm_set_media_models", "set_limits": "swarm_set_limits", "set_usage_limits": "swarm_set_usage_limits",
		"set_permissions": "swarm_set_permissions", "create_key": "swarm_create_key", "create_client_key": "swarm_create_client_key",
		"request_fleet_key": "swarm_request_fleet_key", "revoke_key": "swarm_revoke_key", "revoke_fleet_key": "swarm_revoke_fleet_key",
		"request_provider_key": "swarm_request_provider_key", "connect_chatgpt": "swarm_connect_chatgpt",
		"connect_claude_code": "swarm_connect_claude_code", "request_update": "swarm_request_update", "cancel_update": "swarm_cancel_update",
	}
	setup, ok := tool.CatalogToolNamed("manage_setup")
	if !ok {
		t.Fatal("manage_setup is not a catalog tool")
	}
	if len(setup.Actions) != len(swarmTool) {
		t.Fatalf("manage_setup declares %d actions; %d have a swarm_* tool", len(setup.Actions), len(swarmTool))
	}
	for _, action := range setup.Actions {
		name, ok := swarmTool[action.Action]
		if !ok {
			t.Fatalf("%s has no swarm_* tool to take its level from", action.Action)
		}
		if _, known := toolScopes[name]; !known {
			t.Fatalf("%s has no level of its own in toolScopes", name)
		}
		if action.Level.Scope() != ToolScope(name) {
			t.Fatalf("manage_setup %s needs %s; %s needs %s", action.Action, action.Level.Scope(), name, ToolScope(name))
		}
		if action.LocalOnly != localOnlyTools[name] {
			t.Fatalf("manage_setup %s local_only %v; %s local only %v", action.Action, action.LocalOnly, name, localOnlyTools[name])
		}
		if action.LocalOnly && action.Reach != tool.CatalogReachHome {
			t.Fatalf("local-only %s reaches %s", action.Action, action.Reach)
		}
		// A relayed client never reaches a local-only action, at any level.
		if action.LocalOnly && tool.CatalogLevelsAllow(action, []string{ScopeRead, ScopeWrite, ScopeApprove, ScopeManage}, true) {
			t.Fatalf("a relayed client reaches local-only %s", action.Action)
		}
	}
	if !catalogTool("manage_setup") {
		t.Fatal("the relay gates manage_setup by name")
	}
	if listed := string(filterTools([]byte(`{"result":{"tools":[{"name":"manage_setup"}]}}`), []string{ScopeRead})); !strings.Contains(listed, "manage_setup") {
		t.Fatalf("relay listing drops manage_setup for a read client: %s", listed)
	}
}
