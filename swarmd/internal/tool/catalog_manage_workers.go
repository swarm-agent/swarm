package tool

// manage_workers in the catalog: the eighteen actions the Orchestrator had,
// plus assign_task, which runs the same function as swarm_assign_worker_task.
// The Orchestrator's definition (manageWorkersV2Definition, and its
// manage_automation alias) and Swarm Control's manage_workers tool are
// generated from this declaration; one handler (Runtime.executeManageWorkers)
// serves both.

const manageWorkersToolName = "manage_workers"

// ManageWorkersToolName is manage_workers' name for the services behind it.
const ManageWorkersToolName = manageWorkersToolName

const manageWorkersDescription = "Manage durable background workers using stable worker IDs. Inspect, create, update, attach automations, run tests, dispatch requests, pause, resume, archive, delete workers, and disable automations. Deployment and activation require explicit user acceptance; AI cannot self-approve activation or capabilities. Exclusive to Swarm Orchestrator in Swarm mode. A worker's own runs have no manage_workers: a worker cannot pause, stop, change or delete itself or another worker, so never plan or promise that it will (\"then turn itself off\"). For a limited test, say the person pauses it from their app, or you pause it here when they ask. A schedule runs one run at a time: a slot that falls due while its last run is unfinished is skipped. Call action='help' for syntax and lifecycle workflows."

func manageWorkersFields() []CatalogField {
	described := func(kind, description string) map[string]any {
		return map[string]any{"type": kind, "description": description}
	}
	objects := func(description string) map[string]any {
		return map[string]any{"type": "array", "description": description, "items": map[string]any{"type": "object"}}
	}
	return []CatalogField{
		{"machine", map[string]any{"type": "string", "description": FleetWorkDescription}},
		{"budget", map[string]any{
			"description": "Daily UTC limits on create/propose/update; zero is unset and null clears. Omission preserves the policy. Budget changes are included in the worker proposal and applied only on user acceptance, including updates to existing workers.",
			"anyOf":       []any{map[string]any{"type": "object", "additionalProperties": false, "required": []string{"daily_usd"}, "properties": map[string]any{"daily_usd": map[string]any{"type": "number", "minimum": 0}, "daily_tokens": map[string]any{"type": "integer", "minimum": 0}}}, map[string]any{"type": "null"}},
		}},
		{"model", map[string]any{"description": "Optional worker model on create, propose or update; null clears the pin to the role default. Requires user acceptance.", "anyOf": []any{map[string]any{"type": "null"}, map[string]any{"type": "object", "additionalProperties": false, "required": []string{"provider", "model", "thinking"}, "properties": map[string]any{"provider": map[string]any{"type": "string"}, "model": map[string]any{"type": "string"}, "thinking": map[string]any{"type": "string"}, "fast": map[string]any{"type": "boolean"}}}}}},
		{"agent", described("string", "Custom agent name for a new worker proposal.")},
		{"worker_id", described("string", "Stable durable worker ID (e.g. worker_...).")},
		{"automation_id", described("string", "Attached automation ID for attach, disable_automation, or scoped test run.")},
		{"expected_revision", map[string]any{"type": "integer", "minimum": 1, "description": "Expected worker revision for optimistic concurrency guards on update, attach, disable, pause, resume, archive, or delete."}},
		{"name", described("string", "Human-readable name for worker or automation.")},
		{"description", described("string", "Description of worker purpose or automation responsibility.")},
		{"instructions", described("string", "Standing worker instructions.")},
		{"requested_capabilities", objects("List of capability requests (tool, permission, network, environment).")},
		{"workspace_requirements", objects("List of required named workspace roles.")},
		{"local_bindings", described("object", "Map of named workspace roles to local workspace IDs.")},
		{"automations", objects("List of attached automation definitions for create or update.")},
		{"automation", described("object", "Single automation definition to attach.")},
		{"idempotency_key", described("string", "Required for action=request or test: caller-supplied stable key for one logical job. Reuse the same key across retries; use a new key for a different job. Optional for unrelated actions.")},
		{"prompt", described("string", "Task prompt for test run or direct request.")},
		{"task", described("string", "assign_task: what the worker does in this run.")},
		{"input", described("object", "Structured input payload for test run or direct request.")},
		{"change_summary", described("string", "Summary of changes for worker revision history.")},
		{"cursor", described("string", "Pagination cursor for list or runs.")},
		{"limit", map[string]any{"type": "integer", "minimum": 1, "maximum": 50, "description": "Maximum number of records to return (1-50)."}},
		{"timezone", described("string", "Timezone for scheduling or progress calculations.")},
		{"include_deleted", described("boolean", "Include soft-deleted/tombstoned workers in list.")},
		{"document", described("object", "Complete executable Worker V2 document for document-based proposals; omit for flat name/instructions proposals.")},
		{"worker_review", described("object", "Exact current review for editing a pending proposal; omit on first proposal.")},
		{"run_id", described("string", "inspect: one run of the worker (its status, session_id, error and deliverables).")},
	}
}

func manageWorkersCatalog() CatalogTool {
	const (
		read   = CatalogLevelRead
		write  = CatalogLevelWrite
		manage = CatalogLevelManage
	)
	core := []CatalogProfile{CatalogProfileEssentials, CatalogProfileFull, CatalogProfileOrchestrator}
	definition := []string{"machine", "name", "description", "instructions", "agent", "model", "budget", "requested_capabilities", "workspace_requirements", "local_bindings", "automations", "document", "idempotency_key"}
	with := func(base []string, more ...string) []string {
		return append(append([]string(nil), base...), more...)
	}
	lifecycle := []string{"machine", "worker_id", "expected_revision"}
	// Levels are remote.ToolScope of the swarm_* worker tool with the same
	// effect: reads are swarm_list_workers' and swarm_get_worker's;
	// create swarm_create_worker's; update and attach swarm_update_worker's;
	// disable_automation, delete, pause, resume, archive and activate
	// swarm_manage_worker's; test, request and assign_task
	// swarm_assign_worker_task's; propose swarm_deploy_worker's.
	actions := []CatalogAction{
		{Action: "help", Inputs: []string{"machine"}, Level: read,
			Description: "The Worker V2 authoring syntax and lifecycle workflows."},
		{Action: "list", Inputs: []string{"machine", "limit", "cursor", "include_deleted"}, Level: read,
			Description: "Workers with their state; next_cursor pages on."},
		{Action: "inspect", Inputs: []string{"machine", "worker_id", "cursor", "run_id"}, Level: read,
			Description: "One worker's definition, revision, schedules and state."},
		{Action: "create", Inputs: with(definition), Level: manage,
			Description: "A new worker from name and instructions (or a document); it waits for the person to accept it in Swarm before it runs."},
		{Action: "update", Inputs: []string{"machine", "worker_id", "expected_revision", "name", "description", "instructions", "requested_capabilities", "workspace_requirements", "automations", "model", "budget", "change_summary"}, Level: manage,
			Description: "Save a worker's definition at expected_revision; model and budget changes wait for the person's acceptance."},
		{Action: "attach", Inputs: []string{"machine", "worker_id", "expected_revision", "automation", "name", "description"}, Level: manage,
			Description: "Attach one automation (schedule or trigger) to a worker at expected_revision."},
		{Action: "disable_automation", Inputs: []string{"machine", "worker_id", "automation_id", "expected_revision"}, Level: manage,
			Description: "Turn off one of a worker's automations at expected_revision."},
		{Action: "delete", Inputs: lifecycle, Level: manage,
			Description: "Delete a worker at expected_revision; its runs stay readable."},
		{Action: "test", Inputs: []string{"machine", "worker_id", "automation_id", "prompt", "input", "idempotency_key"}, Level: write,
			Description: "Run the worker (or one of its automations) once as a test; idempotency_key is required."},
		{Action: "request", Inputs: []string{"machine", "worker_id", "prompt", "input", "idempotency_key"}, Level: write,
			Description: "Dispatch one run of an active worker with prompt; idempotency_key is required."},
		{Action: "activate", Inputs: lifecycle, Level: manage,
			Description: "Refused: activation is the person's acceptance in Swarm, never an AI's."},
		{Action: "pause", Inputs: lifecycle, Level: manage,
			Description: "Pause an active worker at expected_revision: no new runs start."},
		{Action: "resume", Inputs: lifecycle, Level: manage,
			Description: "Resume a paused worker at expected_revision."},
		{Action: "archive", Inputs: lifecycle, Level: manage,
			Description: "Archive a worker at expected_revision."},
		{Action: "propose", Inputs: with(definition, "worker_id", "expected_revision", "worker_review"), Level: write,
			Description: "Propose a worker (or a change to one) as a Worker V2 document; a new worker waits for the person to accept it in Swarm."},
		{Action: "review", Inputs: []string{"machine", "worker_id", "cursor", "timezone"}, Level: read,
			Description: "The current review of a worker (its record and latest runs); in a conversation, without worker_id, its pending proposals."},
		{Action: "context", Inputs: []string{"machine", "worker_id", "cursor", "timezone"}, Level: read,
			Description: "As review: the worker and its latest runs."},
		{Action: "progress", Inputs: []string{"machine", "worker_id", "cursor", "timezone"}, Level: read,
			Description: "As review: the worker and its latest runs, to follow its progress."},
		{Action: "assign_task", Inputs: []string{"machine", "worker_id", "task", "idempotency_key"}, Level: write,
			Description: "Start a run of the worker on this task (the same as swarm_assign_worker_task); read it with inspect or review."},
	}
	nextActions := map[string]string{
		"create":      "The person accepts or denies the worker in Swarm; review with worker_id reads it.",
		"propose":     "The person accepts or denies the proposal in Swarm; review with worker_id reads it.",
		"test":        "review with worker_id reads the run.",
		"request":     "review with worker_id reads the run.",
		"assign_task": "review with worker_id reads the run.",
	}
	for i := range actions {
		actions[i].Tool = manageWorkersToolName
		actions[i].Reach = CatalogReachAny
		actions[i].Profiles = core
		actions[i].Untrusted = true
		actions[i].NextAction = nextActions[actions[i].Action]
		// The worker routes' API scopes: reads automations:read (or
		// write), every change automations:write.
		if actions[i].Level == read {
			actions[i].APIScopes = []string{"automations:read", "automations:write"}
		} else {
			actions[i].APIScopes = []string{"automations:write"}
		}
	}
	return CatalogTool{Name: manageWorkersToolName, Description: manageWorkersDescription, Fields: manageWorkersFields(), Actions: actions}
}
