package tool

// OrchestratorDefinitions returns the tool definitions as the Orchestrator is
// shown them. Tools it shares with other agents keep their handlers and their
// full schemas for those agents; the Orchestrator sees only the actions and
// fields it can use, so its fixed context carries no other agent's tooling,
// duplicate or dead action. Handlers still validate every call.
func OrchestratorDefinitions(definitions []Definition) []Definition {
	out := make([]Definition, 0, len(definitions))
	for _, definition := range definitions {
		switch definition.Name {
		case "bash":
			definition = orchestratorBashDefinition(definition)
		case "manage_workspace":
			definition = orchestratorManageWorkspaceDefinition(definition)
		case "manage-sessions", "manage_sessions":
			definition = catalogOrchestratorDefinition(manageSessionsToolName, definition.Name)
		}
		out = append(out, definition)
	}
	return out
}

// orchestratorWorkspaceActions are the catalog actions of manage_workspace.
// Worktree and session-retargeting actions are refused in project chats and
// are not shown.
var orchestratorWorkspaceActions = []string{"inspect", "list", "inspect_map", "get_map", "update_map", "create", "update", "delete", "set_default", "add_source_media_directory", "list_source_media_directories", "remove_source_media_directory"}

func copyDefinitionProperties(definition Definition, keep func(string) bool) (map[string]any, map[string]any) {
	parameters := make(map[string]any, len(definition.Parameters))
	for key, value := range definition.Parameters {
		parameters[key] = value
	}
	properties := map[string]any{}
	if source, ok := definition.Parameters["properties"].(map[string]any); ok {
		for key, value := range source {
			if keep(key) {
				properties[key] = value
			}
		}
	}
	parameters["properties"] = properties
	return parameters, properties
}

// orchestratorBashProperties are the bash fields the Orchestrator is shown,
// with short descriptions. committed_source (a parent validating a Coder
// child's commit) is Coder lineage tooling and is not shown.
func orchestratorBashProperties() map[string]map[string]any {
	return map[string]map[string]any{
		"command":        {"type": "string"},
		"explanation":    {"type": "array", "items": map[string]any{"type": "string"}, "minItems": 1, "description": "Effects: mutations, processes, network exposure, destructive actions"},
		"category":       {"type": "string", "enum": []string{"read", "write", "update", "delete"}, "description": "Highest-impact effect; delete requires critical=true"},
		"critical":       {"type": "boolean", "description": "True when the person should pay special attention"},
		"workspace_path": {"type": "string", "description": "Authorized repository root to run in"},
		"environment":    {"type": "string", "description": "lease_environment lease id"},
		"timeout_ms":     {"type": "integer", "description": "Default 120000, max 1800000"},
		"background":     {"type": "boolean", "description": "Run in background; its end is reported here"},
		"background_id":  {"type": "string", "description": "With command status or kill"},
	}
}

func orchestratorBashDefinition(definition Definition) Definition {
	shown := orchestratorBashProperties()
	parameters, properties := copyDefinitionProperties(definition, func(key string) bool {
		_, ok := shown[key]
		return ok
	})
	for key := range properties {
		properties[key] = shown[key]
	}
	definition.Description = "Run a shell command in the workspace. Explain its effects and flag what needs the person's attention."
	definition.Parameters = parameters
	return definition
}

func orchestratorManageWorkspaceDefinition(definition Definition) Definition {
	kept := map[string]bool{"action": true, "workspace_id": true, "workspace_generation": true, "workspace_path": true, "workspace_name": true, "intent": true, "expected_revision": true, "content": true, "directory_path": true}
	parameters, properties := copyDefinitionProperties(definition, func(key string) bool { return kept[key] })
	properties["action"] = map[string]any{"type": "string", "enum": append([]string(nil), orchestratorWorkspaceActions...)}
	for _, key := range []string{"workspace_id", "directory_path"} {
		if _, ok := properties[key]; ok {
			properties[key] = map[string]any{"type": "string"}
		}
	}
	definition.Description = "Workspace catalog and Workspace Map. create registers an existing folder; update/delete need workspace_id, workspace_generation and intent; delete keeps files."
	definition.Parameters = parameters
	return definition
}

// catalogOrchestratorDefinition is a catalog tool's Orchestrator definition
// (its orchestrator profile), under the name the runtime registers it by.
// Other agents keep the runtime's full definition of the same tool.
func catalogOrchestratorDefinition(tool, name string) Definition {
	definition := catalogDeclared(tool).OrchestratorDefinition()
	if name != "" {
		definition.Name = name
	}
	return definition
}
