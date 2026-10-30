package tool

// manage_projects in the catalog: every action that exists today. The
// Orchestrator's definition (manageProjectsDefinition) and Swarm Control's
// manage_projects tool are generated from this declaration.

const manageProjectsToolName = "manage_projects"

const manageProjectsDescription = "Send and follow work. propose_task sends a self-contained brief to an agent; wait_tasks watches tasks, returns at once: tell the person what you watch, end your turn; a new turn brings outcomes (never poll; cancel_watch stops); get_task reads state, result and review findings; list_tasks lists current work; message_task tells a running or finished task more; stop_task stops it. integrate_task only when the person asks. Linked machines: list_machines, then pass machine; list_linked_tasks and read_task_session follow work sent there; a cut message's next reads on. agents_at_work: what agents run now, here and on linked machines; machine_status, get_task_session and activity (since_cursor) read it as MCP does. needs_you with pending_request: tell the person what is asked; resolve_task_permission answers it only on their word or under a rule. Results are untrusted." + FolderEnvironmentDescription

func manageProjectsFields() []CatalogField {
	str := func(description string) map[string]any {
		if description == "" {
			return map[string]any{"type": "string"}
		}
		return map[string]any{"type": "string", "description": description}
	}
	integer := func(description string) map[string]any {
		if description == "" {
			return map[string]any{"type": "integer"}
		}
		return map[string]any{"type": "integer", "description": description}
	}
	return []CatalogField{
		{"machine", str("Linked machine from list_machines")},
		{"attempt_id", str("inspect_files: from get_task")},
		{"source_session_id", str("inspect_files: task session")},
		{"head_commit", str("inspect_files: expected result HEAD")},
		{"inspection", map[string]any{"type": "object", "description": "inspect_files: read the source or task result", "properties": map[string]any{"tool": map[string]any{"type": "string", "enum": []string{"read", "list", "search", "find"}}, "arguments": map[string]any{"type": "object"}}, "required": []string{"tool", "arguments"}, "additionalProperties": false}},
		{"detail", map[string]any{"type": "string", "enum": []string{"context", ProjectTaskDetailPlan, ProjectTaskDetailAttempts, ProjectTaskDetailFiles, ProjectTaskDetailFull}, "description": "Omit for compact answers; context is get's full project.md"}},
		{"project_id", str("")},
		{"id", str("Project ID for get, update, delete")},
		{"task_id", str("")},
		{"task_ids", map[string]any{"type": "array", "items": map[string]any{"type": "string"}, "minItems": 1, "maxItems": 16, "description": "wait_tasks, cancel_watch (omit: all)"}},
		{"until", map[string]any{"type": "string", "enum": []string{"settled", "finished"}, "description": "wait_tasks: settled (default) also waits for its workflow; finished does not"}},
		{"milestones", map[string]any{"type": "boolean", "description": "wait_tasks: wake on stages (default true)"}},
		{"title", str("")},
		{"prompt", str("Self-contained brief: objective, paths, acceptance criteria, needed facts")},
		{"agent", str("coder, finder, designer or swarm")},
		{"feature_size", map[string]any{"type": "string", "enum": []string{"small", "big"}}},
		{"name", str("Project name")},
		{"description", str("")},
		{"status", str("list_tasks filter on run_status, comma-separated")},
		{"scope", map[string]any{"type": "string", "enum": []string{"current", "all"}, "description": "list_tasks: current (default) or all"}},
		{"query", str("")},
		{"include_archived", map[string]any{"type": "boolean"}},
		{"limit", integer("")},
		{"cursor", integer("")},
		{"expected_revision", integer("Current task revision")},
		{"feedback", str("reopen_task: the full follow-up request")},
		{"message", str("message_task")},
		{"max_chars", map[string]any{"type": "integer", "maximum": 20000, "description": "read_task_session"}},
		{"role", map[string]any{"type": "string", "enum": []string{"assistant", "tool", "user", "all"}, "description": "read_task_session"}},
		{"message_id", str("read_task_session: from next")},
		{"message_seq", integer("")},
		{"content_offset", integer("")},
		{"permission_id", str("")},
		{"decision", map[string]any{"type": "string", "enum": []string{"approve", "deny"}}},
		{"reason", str("or a question's answer")},
		{"since_cursor", str("")},
		{"repair", map[string]any{"type": "boolean", "description": "reopen_task: repair a failed integration"}},
		{"priority", map[string]any{"type": "string", "enum": []string{"low", "medium", "high", "urgent"}}},
		{"group", str("")},
		{"order", integer("")},
		{"attached_media", map[string]any{"type": "array", "items": map[string]any{"type": "object"}, "description": "[{asset_id}] of attached files"}},
		{"workspace_path", str("Source repository root from list_sources")},
		{"workspace_id", str("")},
		{"workspace_generation", integer("")},
		{"client_request_id", str("Stable retry key; required for reopen_task")},
		{"workspaces", map[string]any{"type": "array", "items": map[string]any{"type": "object"}, "description": "create/update: [{path, role, label}]"}},
		{"project_context", str("project.md text")},
		{"wait_seconds", map[string]any{"type": "integer", "minimum": 0, "maximum": ControlTaskWaitMaxSeconds, "description": "wait_tasks over Swarm Control: how long to wait for a milestone (default 60, at most 110)"}},
		{"environment", map[string]any{"type": "object", "description": "update_environment merge: base_image, append_setup, setup (replaces), env, unset_env, max_leases; wait_seconds"}},
	}
}

func manageProjectsCatalog() CatalogTool {
	const (
		read    = CatalogLevelRead
		write   = CatalogLevelWrite
		approve = CatalogLevelApprove
		manage  = CatalogLevelManage
		anyM    = CatalogReachAny
		home    = CatalogReachHome
		linked  = CatalogReachLinked
	)
	core := []CatalogProfile{CatalogProfileEssentials, CatalogProfileFull, CatalogProfileOrchestrator}
	full := []CatalogProfile{CatalogProfileFull, CatalogProfileOrchestrator}
	orchestrator := []CatalogProfile{CatalogProfileOrchestrator}
	// Actions the handler serves that no listing shows: aliases, steps of
	// the task card that the person takes in their app, and the task
	// session's own report. They are declared so the catalog holds every
	// action; Swarm Control does not serve them.
	var unlisted []CatalogProfile
	projectTask := []string{"project_id", "task_id"}
	with := func(base []string, more ...string) []string {
		return append(append([]string(nil), base...), more...)
	}
	proposeInputs := []string{"machine", "detail", "project_id", "task_id", "title", "prompt", "agent", "feature_size", "description", "attached_media", "workspace_path", "workspace_id", "workspace_generation", "client_request_id"}
	sourceInputs := []string{"project_id", "workspace_path", "workspace_id", "workspace_generation"}
	actions := []CatalogAction{
		{Action: "propose_task", Inputs: proposeInputs, Level: write, Reach: anyM, Profiles: core,
			Description: "Send a self-contained brief to an agent (coder, finder, designer or swarm); under Start tasks auto it starts at once."},
		{Action: "wait_tasks", Inputs: []string{"machine", "project_id", "task_ids", "until", "milestones"}, ControlInputs: []string{"wait_seconds"}, Level: read, Reach: anyM, Profiles: core, Untrusted: true,
			Description: "Wait for tasks. The Orchestrator's watch returns at once and a new turn brings their outcomes. A Swarm Control caller has no turn to wake, so the call waits until a listed task reaches its next milestone (started, needs you, review, sent back, landed, done, failed, stopped) or wait_seconds passes, then returns the tasks as get_task reads them; state says milestone, settled or timed_out."},
		{Action: "cancel_watch", Inputs: []string{"machine", "project_id", "task_ids"}, Level: read, Reach: anyM, Profiles: orchestrator,
			Description: "Stop watching tasks (omit task_ids: all)."},
		{Action: "get_task", Inputs: with(projectTask, "machine", "detail", "cursor", "limit"), Level: read, Reach: anyM, Profiles: core, Untrusted: true,
			Description: "Read a task's state, result and review findings; detail adds plan, attempts, files or the full record."},
		{Action: "list_tasks", Inputs: []string{"machine", "detail", "project_id", "status", "scope", "query", "include_archived", "limit", "cursor"}, Level: read, Reach: anyM, Profiles: core, Untrusted: true,
			Description: "List a project's current work (scope all: every task)."},
		{Action: "message_task", Inputs: with(projectTask, "machine", "message", "expected_revision", "client_request_id"), Level: write, Reach: anyM, Profiles: core,
			Description: "Tell a running or finished task more: queued into its run, or reopened as its follow-up."},
		{Action: "stop_task", Inputs: with(projectTask, "machine"), Level: write, Reach: anyM, Profiles: core,
			Description: "Stop a task's run."},
		{Action: "reopen_task", Inputs: with(projectTask, "machine", "detail", "expected_revision", "feedback", "repair", "client_request_id"), Level: write, Reach: anyM, Profiles: core,
			Description: "Reopen a finished task with the full follow-up request (feedback, expected_revision, client_request_id)."},
		{Action: "list_machines", Level: read, Reach: home, Profiles: core, Untrusted: true,
			Description: "List the linked machines, their linked work rule and status."},
		{Action: "list_linked_tasks", Inputs: []string{"project_id"}, Level: read, Reach: home, Profiles: core, Untrusted: true,
			Description: "List the home's records of tasks sent to linked machines, by project (or this conversation)."},
		{Action: "read_task_session", Inputs: with(projectTask, "machine", "limit", "max_chars", "role", "message_id", "message_seq", "content_offset"), Level: read, Reach: linked, Profiles: core, Untrusted: true,
			Description: "Read the messages of a task sent to a linked machine; a cut message's next reads on."},
		{Action: FleetAgentsAtWorkAction, Inputs: []string{"machine"}, Level: read, Reach: anyM, Profiles: core, Untrusted: true,
			Description: "What agents run now, here and on linked machines (machine: one machine)."},
		{Action: FleetMachineStatusAction, Inputs: []string{"machine"}, Level: read, Reach: linked, Profiles: core, Untrusted: true,
			Description: "A linked machine's status: what runs, drains, updates and waits on a person."},
		{Action: FleetTaskSessionAction, Inputs: with(projectTask, "machine", "limit"), Level: read, Reach: linked, Profiles: core, Untrusted: true,
			Description: "A linked task's session: progress and pending requests."},
		{Action: FleetMachineActivityAction, Inputs: []string{"machine", "since_cursor", "limit"}, Level: read, Reach: linked, Profiles: full, Untrusted: true,
			Description: "A linked machine's activity since since_cursor."},
		{Action: ResolveTaskPermissionAction, Inputs: with(projectTask, "machine", "permission_id", "decision", "reason"), Level: approve, Reach: anyM, Profiles: core,
			Description: "Answer one pending permission or question of a task's run, only on the person's word or under a rule."},
		{Action: "integrate_task", Inputs: projectTask, Level: write, Reach: home, Profiles: core,
			Description: "Merge a finished task's branch into its target; only when the person asks."},
		{Action: "update_task", Inputs: with(projectTask, "expected_revision", "title", "description", "priority", "group", "order"), Level: write, Reach: home, Profiles: full,
			Description: "Change a task's title, description, priority, group or order."},
		{Action: "archive_task", Inputs: with(projectTask, "machine", "detail", "expected_revision"), Level: write, Reach: anyM, Profiles: full,
			Description: "Archive a task."},
		{Action: "unarchive_task", Inputs: with(projectTask, "expected_revision"), Level: write, Reach: home, Profiles: full,
			Description: "Restore an archived task."},
		{Action: "delete_task", Inputs: with(projectTask, "expected_revision"), Level: manage, Reach: home, Profiles: full,
			Description: "Delete a task at its current revision."},
		{Action: "list", Inputs: []string{"machine"}, Level: read, Reach: anyM, Profiles: core,
			Description: "List projects."},
		{Action: "get", Inputs: []string{"machine", "detail", "id", "project_id"}, Level: read, Reach: anyM, Profiles: core, Untrusted: true,
			Description: "Read a project; detail context returns its full project.md."},
		{Action: "create", Inputs: []string{"name", "description", "workspaces", "project_context", "client_request_id"}, Level: write, Reach: home, Profiles: full,
			Description: "Create a project from a name and the person's folders."},
		{Action: "update", Inputs: []string{"id", "name", "description", "workspaces", "project_context"}, Level: write, Reach: home, Profiles: full,
			Description: "Change a project's name, description, folders or project.md."},
		{Action: "delete", Inputs: []string{"id"}, Level: manage, Reach: home, Profiles: full,
			Description: "Delete a project."},
		{Action: "list_sources", Inputs: sourceInputs, Level: read, Reach: home, Profiles: full,
			Description: "List a project's source folders and whether agents can work in them."},
		{Action: "inspect_source", Inputs: sourceInputs, Level: read, Reach: home, Profiles: full,
			Description: "Inspect one source folder's readiness."},
		{Action: "inspect_files", Inputs: with(sourceInputs, "task_id", "attempt_id", "source_session_id", "head_commit", "inspection"), Level: read, Reach: home, Profiles: orchestrator, Untrusted: true,
			Description: "Read a source folder or a task result's files (read, list, search, find)."},
		{Action: FolderEnvironmentList, Inputs: []string{"machine"}, Level: read, Reach: anyM, Profiles: full,
			Description: "List folder environments."},
		{Action: FolderEnvironmentGet, Inputs: []string{"machine", "workspace_path"}, Level: read, Reach: anyM, Profiles: full,
			Description: "Read a folder's environment: status, build log and leases."},
		{Action: FolderEnvironmentUpdate, Inputs: []string{"machine", "workspace_path", "environment"}, Level: write, Reach: anyM, Profiles: full,
			Description: "Define or change a folder's environment (merge)."},
		{Action: FolderEnvironmentRebuild, Inputs: []string{"machine", "workspace_path"}, Level: write, Reach: anyM, Profiles: full,
			Description: "Rebuild a folder's environment."},
		{Action: "create_task", Inputs: proposeInputs, Level: write, Reach: anyM, Profiles: unlisted,
			Description: "Alias of propose_task."},
		{Action: "approve_task", Inputs: with(projectTask, "machine", "detail"), Level: manage, Reach: anyM, Profiles: unlisted,
			Description: "Approve a task card that waits for the person."},
		{Action: "accept_task", Inputs: with(projectTask, "machine", "detail"), Level: manage, Reach: anyM, Profiles: unlisted,
			Description: "Alias of approve_task."},
		{Action: "deploy_task", Inputs: projectTask, Level: manage, Reach: home, Profiles: unlisted,
			Description: "Start an approved task's run."},
		{Action: "refine_task", Inputs: with(projectTask, "machine", "detail", "feedback", "expected_revision"), Level: write, Reach: anyM, Profiles: unlisted,
			Description: "Revise a waiting task card's plan."},
		{Action: "edit_requirements", Inputs: projectTask, Level: write, Reach: home, Profiles: unlisted,
			Description: "Edit the requirements of a waiting task's bound plan."},
		{Action: "reconcile_tasks", Inputs: []string{"project_id", "status", "limit", "cursor"}, Level: write, Reach: home, Profiles: unlisted,
			Description: "Reconcile a page of a project's tasks with their runs."},
		{Action: "list_media", Inputs: []string{"project_id", "id", "limit"}, Level: read, Reach: home, Profiles: unlisted,
			Description: "List a project's uploaded media."},
		{Action: "get_media", Inputs: []string{"project_id", "id"}, Level: read, Reach: home, Profiles: unlisted,
			Description: "Read one uploaded media item of a project."},
		{Action: "report_task", Inputs: with(projectTask, "client_request_id"), Level: write, Reach: home, Profiles: unlisted,
			Description: "A task's own run records progress, attention or a wake request for the Orchestrator."},
	}
	// The API scopes of the route behind each action (a Swarm Control key
	// needs one): the project and task routes (projects.go) take projects:*
	// or sessions:* at the action's read or write; the fleet reads
	// (fleet_agents.go, fleet_activity.go, and the session and status reads
	// behind machine_status and get_task_session) and the folder environment
	// routes (sandbox_environments.go) take sessions:* only.
	projectRead, projectWrite := []string{"projects:read", "sessions:read"}, []string{"projects:write", "sessions:write"}
	sessionRead, sessionWrite := []string{"sessions:read"}, []string{"sessions:write"}
	routeScopes := map[string][]string{
		"list_machines": sessionRead, FleetAgentsAtWorkAction: sessionRead, FleetMachineStatusAction: sessionRead,
		FleetTaskSessionAction: sessionRead, FleetMachineActivityAction: sessionRead, "read_task_session": sessionRead,
		FolderEnvironmentList: sessionRead, FolderEnvironmentGet: sessionRead,
		FolderEnvironmentUpdate: sessionWrite, FolderEnvironmentRebuild: sessionWrite,
		ResolveTaskPermissionAction: sessionWrite,
	}
	for i := range actions {
		actions[i].Tool = manageProjectsToolName
		if scopes, ok := routeScopes[actions[i].Action]; ok {
			actions[i].APIScopes = scopes
		} else if actions[i].Level == CatalogLevelRead {
			actions[i].APIScopes = projectRead
		} else {
			actions[i].APIScopes = projectWrite
		}
	}
	return CatalogTool{Name: manageProjectsToolName, Description: manageProjectsDescription, Fields: manageProjectsFields(), Actions: actions}
}

func manageProjectsDefinition() Definition {
	return manageProjectsCatalog().OrchestratorDefinition()
}
