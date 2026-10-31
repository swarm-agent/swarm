package tool

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"strings"

	"swarm/packages/swarmd/internal/design"
)

// manage_custom_agents, manage_theme and manage_designs in the catalog (not
// manage_design, the retired tool swarm_design replaced).
//
//   - manage_custom_agents (list, get, define, disable) runs the account's
//     custom agent service (CustomAgentLifecycleService, the API server's
//     ManageCustomAgents) for the Orchestrator and Swarm Control alike; it
//     answers what swarm_list_agents and swarm_define_agent answered for.
//   - manage_theme (get) runs swarm_get_theme's handler (CatalogActionService).
//     The theme catalog's other actions stay the manage-theme tool of the
//     agents that have it.
//   - manage_designs (create, edit) runs swarm_design's handler
//     (CatalogActionService): standalone HTML designs in a project folder.
//     The Orchestrator's own run makes designs with the swarm_design tool the
//     run service executes (run/design_tool.go).

const (
	manageCustomAgentsToolName = "manage_custom_agents"
	manageThemeToolName        = "manage_theme"
	manageDesignToolName       = "manage_designs"
)

// ManageThemeToolName and ManageDesignToolName name the catalog tools for the
// services behind them.
const (
	ManageThemeToolName  = manageThemeToolName
	ManageDesignToolName = manageDesignToolName
)

const manageCustomAgentsDescription = "List, get, define or disable custom agents. Definitions save immediately; workers still require proposal acceptance."

func manageCustomAgentsCatalog() CatalogTool {
	fields := []CatalogField{
		{"name", map[string]any{"type": "string"}},
		{"description", map[string]any{"type": "string"}},
		{"instructions", map[string]any{"type": "string"}},
		{"tools", map[string]any{"oneOf": []any{map[string]any{"type": "array", "items": map[string]any{"type": "string"}}, map[string]any{"type": "string", "enum": []string{"read_only", "read_write", "build"}}}}},
	}
	full := []CatalogProfile{CatalogProfileFull, CatalogProfileOrchestrator}
	// Levels are remote.ToolScope of swarm_list_agents (read) and
	// swarm_define_agent (write); disable changes an agent like define. API
	// scopes are those of /v3/agents (handleCustomAgents).
	read, write := []string{"agents:read", "swarm:read"}, []string{"agents:write", "swarm:write"}
	actions := []CatalogAction{
		{Action: "list", Level: CatalogLevelRead, APIScopes: read, Description: "The account's custom agents (swarm_list_agents).", NextAction: "get with a custom agent's name reads it."},
		{Action: "get", Inputs: []string{"name"}, Level: CatalogLevelRead, APIScopes: read, Description: "One custom agent's definition."},
		{Action: "define", Inputs: []string{"name", "description", "instructions", "tools"}, Level: CatalogLevelWrite, APIScopes: write, Untrusted: false, Description: "Create or replace a custom agent: its instructions and which tools it may use (swarm_define_agent)."},
		{Action: "disable", Inputs: []string{"name"}, Level: CatalogLevelWrite, APIScopes: write, Description: "Disable a custom agent."},
	}
	for i := range actions {
		actions[i].Tool = manageCustomAgentsToolName
		actions[i].Reach = CatalogReachHome
		actions[i].Profiles = full
		// Instructions and descriptions are written by agents or people.
		actions[i].Untrusted = actions[i].Level == CatalogLevelRead
	}
	return CatalogTool{Name: manageCustomAgentsToolName, Description: manageCustomAgentsDescription, Fields: fields, Actions: actions}
}

func manageCustomAgentsDefinition() Definition {
	return manageCustomAgentsCatalog().OrchestratorDefinition()
}

// executeManageCustomAgentsCatalog runs one manage_custom_agents action and
// returns its answer in the catalog envelope.
func (r *Runtime) executeManageCustomAgentsCatalog(scope WorkspaceScope, args map[string]any) (string, error) {
	catalog := catalogDeclared(manageCustomAgentsToolName)
	name := strings.ToLower(strings.TrimSpace(asString(args["action"])))
	declared, ok := catalog.Action(name)
	if !ok {
		return "", fmt.Errorf("unknown manage_custom_agents action %q; use one of: %s", name, strings.Join(catalogActionNames(catalog), ", "))
	}
	// The account is the authority over its custom agents
	// (ManageCustomAgents), for every caller.
	if p := scope.Principal; !p.Valid() || strings.TrimSpace(p.AccountScopeID) == "" {
		return "", errors.New("manage_custom_agents requires an authenticated account")
	}
	if strings.TrimSpace(asString(args["machine"])) != "" {
		return "", errors.New("manage_custom_agents manages this machine's agents only; omit machine")
	}
	call := make(map[string]any, len(args))
	for key, value := range args {
		call[key] = value
	}
	call["action"] = declared.Action
	answer, err := r.executeManageCustomAgents(scope, call)
	if err != nil {
		return "", err
	}
	return r.catalogEnvelope(catalog, declared.Action, call, answer, false)
}

func manageThemeCatalog() CatalogTool {
	return CatalogTool{
		Name:        manageThemeToolName,
		Description: "Read a theme from Swarm's catalog: its id, name and palette.",
		Fields:      []CatalogField{{"theme_id", map[string]any{"type": "string", "maxLength": 200, "description": "A theme id, e.g. a project's theme_id; omit for the account's active theme."}}},
		Actions: []CatalogAction{{
			Tool: manageThemeToolName, Action: "get", Inputs: []string{"theme_id"}, Level: CatalogLevelRead, Reach: CatalogReachHome,
			// /v1/ui/settings has no scope of its own; a key reading
			// sessions or holding the read level may read it.
			APIScopes: []string{"sessions:read", "swarm:read"},
			// No Orchestrator profile: the run canonicalizes manage_theme and
			// manage-theme to one tool, the agents' theme tool, which the
			// Orchestrator's code-owned contract keeps off; a second
			// definition by that name cannot exist beside it.
			Profiles:    []CatalogProfile{CatalogProfileFull},
			Description: "A theme's id, name and palette (#RRGGBB colors): the given theme_id, else the account's active theme, else Swarm's default (swarm_get_theme).",
		}},
	}
}

func manageDesignCatalog() CatalogTool {
	str := func(description string, maxLength int) map[string]any {
		return map[string]any{"type": "string", "description": description, "maxLength": maxLength}
	}
	fields := []CatalogField{
		{"machine", str("A linked machine's name (manage_fleet list_machines): make the design there.", 63)},
		{"project", str("The project (id or name) whose folder the designs go in.", 200)},
		{"workspace", str("Which of the project's folders, when it has several.", 4096)},
		{"brief", str("What to design: the page or screen, its content, audience and must-haves (for an edit: the change to make).", design.MaxBriefRunes)},
		{"count", map[string]any{"type": "integer", "minimum": 1, "maximum": design.MaxCount, "description": "How many designs. Default 1."}},
		{"style", str("Shared visual direction for every design (palette, type, mood).", design.MaxStyleRunes)},
		{"variations", map[string]any{"type": "array", "maxItems": design.MaxVariations, "items": map[string]any{"type": "string", "maxLength": design.MaxVariationRunes}, "description": "Optional direction per design, used in order."}},
		{"batch_name", str("Short name for the batch folder. Default: from the brief.", 200)},
		{"folder", str("Folder for batches, relative to the workspace. Default designs.", 1000)},
		{"edit_path", str("The existing .html file to edit in place (relative to the workspace).", 1000)},
		{"wait_seconds", map[string]any{"type": "integer", "minimum": 0, "maximum": 110, "description": "Wait up to this many seconds for the job to settle."}},
	}
	profiles := []CatalogProfile{CatalogProfileEssentials, CatalogProfileFull, CatalogProfileOrchestrator}
	actions := []CatalogAction{
		{Action: "create", Inputs: []string{"machine", "project", "workspace", "brief", "count", "style", "variations", "batch_name", "folder", "wait_seconds"},
			Description: "Make count standalone HTML designs from one brief, in parallel: one model call each, a browser check, and one repair call only for designs with errors. Files land in <folder>/<batch>-<id>/01.html … with results.json; nothing to merge. Every design costs money with the provider (swarm_design)."},
		{Action: "edit", Inputs: []string{"machine", "project", "workspace", "brief", "style", "edit_path", "wait_seconds"},
			Description: "Edit one existing .html file in place with the brief (replaced only if it did not change meanwhile) (swarm_design with edit_path)."},
	}
	for i := range actions {
		actions[i].Tool = manageDesignToolName
		// remote.ToolScope of swarm_design; its handler and the design job
		// route need sessions:write.
		actions[i].Level = CatalogLevelWrite
		actions[i].APIScopes = []string{"sessions:write"}
		actions[i].Reach = CatalogReachAny
		actions[i].Profiles = profiles
		actions[i].Untrusted = true
		actions[i].NextAction = "manage_jobs get_job with the job_id reads each design's status, check errors and cost."
	}
	return CatalogTool{Name: manageDesignToolName, Description: "Make standalone HTML designs, or edit one existing HTML file, in a project's folder. Report each design's path and status exactly as returned.", Fields: fields, Actions: actions}
}

// manageDesignsDefinition is the Orchestrator's manage_designs, generated
// from the catalog declaration.
func manageDesignsDefinition() Definition {
	return manageDesignCatalog().OrchestratorDefinition()
}

// executeManageDesign runs one manage_designs action: edit is swarm_design
// with edit_path, create without it.
func (r *Runtime) executeManageDesign(ctx context.Context, scope WorkspaceScope, args map[string]any) (string, error) {
	action := strings.ToLower(strings.TrimSpace(asString(args["action"])))
	if action == "create" && strings.TrimSpace(asString(args["edit_path"])) != "" {
		return "", errors.New("manage_designs create makes new designs; edit_path is for edit")
	}
	if action == "edit" && strings.TrimSpace(asString(args["edit_path"])) == "" {
		return "", errors.New("manage_designs edit needs edit_path, the .html file to change")
	}
	return r.executeCatalogServiceTool(ctx, scope, manageDesignToolName, args)
}

// catalogServiceJSON is a convenience for handlers that hand a service answer
// to the envelope.
func catalogServiceJSON(value any) (string, error) {
	raw, err := json.Marshal(value)
	return string(raw), err
}
