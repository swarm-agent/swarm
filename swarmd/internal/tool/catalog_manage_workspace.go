package tool

// manage_workspace in the catalog: the workspace catalog and Workspace Map
// actions the Orchestrator had, each served to Swarm Control from the same
// declaration. The handler is the run service's (WorkspaceCatalogService):
// a conversation's call runs it with the conversation's session; a Swarm
// Control call has no conversation and runs the same catalog functions
// without one. The Swarm agent's session tooling (set_session and the
// worktree actions) needs a conversation: it is declared with no profile,
// keeps the runtime's full definition, and no listing shows it.

const manageWorkspaceToolName = "manage_workspace"

const manageWorkspaceCatalogDescription = "Workspace catalog and Workspace Map. create registers an existing folder; update/delete need workspace_id, workspace_generation and intent; delete keeps files. machine: a linked machine's workspaces (manage_projects list_machines)."

func manageWorkspaceFields() []CatalogField {
	str := map[string]any{"type": "string"}
	return []CatalogField{
		{"machine", map[string]any{"type": "string", "description": "Linked machine from manage_projects list_machines"}},
		{"workspace_id", str},
		{"workspace_generation", map[string]any{"type": "integer", "minimum": 1}},
		{"workspace_path", str},
		{"workspace_name", str},
		{"intent", map[string]any{"type": "string", "maxLength": 500}},
		{"expected_revision", map[string]any{"type": "integer", "minimum": 1}},
		{"content", map[string]any{"type": "string", "maxLength": 32768, "description": "update_map replacement Markdown beginning with '# Workspace Map'"}},
		{"directory_path", str},
		// swarm_add_workspace's and swarm_create_workspace's inputs, for
		// create over Swarm Control.
		{"path", map[string]any{"type": "string", "maxLength": 4096, "description": "create over Swarm Control: an existing, non-empty folder's absolute path (as swarm_add_workspace); name is then its display name"}},
		{"name", map[string]any{"type": "string", "maxLength": 100, "description": "create over Swarm Control: with path, its display name; alone, a new, empty repository of this name in this machine's workspace folder (as swarm_create_workspace)"}},
		// The Swarm agent's own session tooling (no listing shows it).
		{"theme_id", str},
		{"workspace_ids", map[string]any{"type": "array", "items": str}},
		{"primary_workspace_id", str},
		{"owner_session_id", str},
		{"ownership_revision", map[string]any{"type": "integer", "minimum": 1}},
		{"head", str},
		{"fingerprint", str},
		{"operation_id", str},
		{"files", map[string]any{"type": "array", "items": str}},
		{"worktree_name", str},
		{"worktree_path", str},
		{"expected_worktree_path", str},
	}
}

func manageWorkspaceCatalog() CatalogTool {
	const (
		read   = CatalogLevelRead
		write  = CatalogLevelWrite
		manage = CatalogLevelManage
		anyM   = CatalogReachAny
		home   = CatalogReachHome
	)
	core := []CatalogProfile{CatalogProfileEssentials, CatalogProfileFull, CatalogProfileOrchestrator}
	var unlisted []CatalogProfile
	m := func(more ...string) []string { return append([]string{"machine"}, more...) }
	// Levels are remote.ToolScope of the swarm_* tool with the same effect:
	// swarm_list_workspaces (read) for the reads, swarm_add_workspace
	// (write) for create. The other changes have no swarm_* tool: renaming,
	// the default, the Workspace Map and source media folders are write;
	// delete removes a workspace from the catalog, manage (as manage_projects
	// delete).
	actions := []CatalogAction{
		{Action: "inspect", Inputs: m(), Level: read, Reach: anyM, Profiles: core,
			Description: "The registered workspaces and the actions."},
		{Action: "list", Inputs: m(), Level: read, Reach: anyM, Profiles: core,
			Description: "The registered workspaces with their id, generation, path and default."},
		{Action: "inspect_map", Inputs: m(), Level: read, Reach: anyM, Profiles: core, Untrusted: true,
			Description: "The account's Workspace Map and its revision."},
		{Action: "get_map", Inputs: m(), Level: read, Reach: anyM, Profiles: core, Untrusted: true,
			Description: "Alias of inspect_map."},
		{Action: "update_map", Inputs: m("expected_revision", "content", "intent"), Level: write, Reach: anyM, Profiles: core,
			Description: "Replace the Workspace Map at expected_revision; intent says why."},
		{Action: "create", Inputs: m("workspace_path", "workspace_name", "intent"), ControlInputs: []string{"path", "name"}, Level: write, Reach: anyM, Profiles: core,
			Description: "Register an existing folder as a workspace (no files change)."},
		{Action: "update", Inputs: m("workspace_id", "workspace_generation", "workspace_path", "workspace_name", "intent"), Level: write, Reach: anyM, Profiles: core,
			Description: "Rename or move a workspace's catalog entry."},
		{Action: "delete", Inputs: m("workspace_id", "workspace_generation", "intent"), Level: manage, Reach: anyM, Profiles: core,
			Description: "Unlink a workspace from the catalog; its files stay."},
		{Action: "set_default", Inputs: m("workspace_id", "workspace_generation"), Level: write, Reach: anyM, Profiles: core,
			Description: "The workspace later sessions start in."},
		{Action: "add_source_media_directory", Inputs: m("workspace_path", "directory_path"), Level: write, Reach: anyM, Profiles: core,
			Description: "Add a source media folder to a workspace."},
		{Action: "list_source_media_directories", Inputs: m("workspace_path"), Level: read, Reach: anyM, Profiles: core,
			Description: "A workspace's source media folders."},
		{Action: "remove_source_media_directory", Inputs: m("workspace_path", "directory_path"), Level: write, Reach: anyM, Profiles: core,
			Description: "Remove a source media folder from a workspace."},
		{Action: "set_session", Inputs: []string{"workspace_id", "workspace_generation", "workspace_ids", "primary_workspace_id"}, Level: write, Reach: home, Profiles: unlisted,
			Description: "The Swarm agent's move of its own session to other workspaces."},
		{Action: "adopt_worktree", Inputs: []string{"workspace_id", "workspace_generation", "worktree_name", "worktree_path", "expected_worktree_path"}, Level: write, Reach: home, Profiles: unlisted,
			Description: "The Swarm agent's adoption of a worktree for its session."},
		{Action: "discover_worktrees", Inputs: []string{"workspace_id", "worktree_path"}, Level: read, Reach: home, Profiles: unlisted,
			Description: "The Swarm agent's discovery of recoverable worktrees."},
		{Action: "reclaim_worktree", Inputs: []string{"worktree_path", "owner_session_id", "ownership_revision", "head", "fingerprint", "operation_id"}, Level: write, Reach: home, Profiles: unlisted,
			Description: "The Swarm agent's approved reclaim of a worktree."},
		{Action: "copy_worktree", Inputs: []string{"worktree_path", "owner_session_id", "ownership_revision", "head", "fingerprint", "operation_id", "files"}, Level: write, Reach: home, Profiles: unlisted,
			Description: "The Swarm agent's approved copy from a worktree."},
		{Action: "cancel_worktree_recovery", Inputs: []string{"operation_id"}, Level: write, Reach: home, Profiles: unlisted,
			Description: "The Swarm agent's cancel of a worktree recovery."},
	}
	// The API scopes of the routes behind each action (/v1/workspace and the
	// routes swarm_list_workspaces and swarm_add_workspace call): reads take
	// sessions:read or sessions:write, changes sessions:write.
	readScopes, writeScopes := []string{"sessions:read", "sessions:write"}, []string{"sessions:write"}
	nextActions := map[string]string{
		"inspect":     "update, delete and set_default take a workspace's id and generation from this list.",
		"list":        "update, delete and set_default take a workspace's id and generation from this list.",
		"inspect_map": "update_map takes this revision as expected_revision.",
		"get_map":     "update_map takes this revision as expected_revision.",
		"create":      "manage_sessions create starts a session in it.",
	}
	for i := range actions {
		actions[i].Tool = manageWorkspaceToolName
		actions[i].NextAction = nextActions[actions[i].Action]
		if actions[i].Level == CatalogLevelRead {
			actions[i].APIScopes = readScopes
		} else {
			actions[i].APIScopes = writeScopes
		}
	}
	return CatalogTool{Name: manageWorkspaceToolName, Description: manageWorkspaceCatalogDescription, Fields: manageWorkspaceFields(), Actions: actions}
}
