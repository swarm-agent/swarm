package tool

// manage_workflows in the catalog: every action the Orchestrator had, plus
// export, import, restore and stop_all, which call the same service
// functions as the swarm_* workflow tools' routes. The Orchestrator's
// definition (manageWorkflowsDefinition) and Swarm Control's manage_workflows
// tool are generated from this declaration.

const manageWorkflowsToolName = "manage_workflows"

const manageWorkflowsDescription = "Manage workflows that run when tasks or worker jobs end. Name a workflow by its id or name everywhere. Propose new workflows or changes for the person to accept or discard; never self-accept. A change of an existing workflow (id or name) sends only the fields that change, merged onto its current revision (expected_revision guards it; each step by its id); replace: true sends the whole definition instead. Fields propose does not take are refused. Enable and disable existing workflows immediately; a disabled workflow does not run until enabled. Call help for examples. for_each queues one independent worker job per object in a committed JSON array with bounded parallelism. Fork, reuse and roll back: export returns a workflow's portable JSON (path also writes it into its folder); import adds a definition or file (name makes a copy); history lists revisions and restore makes a retained one current; stop_all with stopped true or false turns the machine-wide stop switch on or off. " + WorkflowTriggerDescription + FleetWorkDescription

func manageWorkflowsFields() []CatalogField {
	str := func(description string) map[string]any {
		if description == "" {
			return map[string]any{"type": "string"}
		}
		return map[string]any{"type": "string", "description": description}
	}
	return []CatalogField{
		{"machine", map[string]any{"type": "string", "description": FleetWorkDescription}},
		{"inputs", WorkflowInputsSchema()},
		{"with", WorkflowWithSchema()},
		{"on", WorkflowTriggerSchema()},
		{"rotate_key", map[string]any{"type": "boolean", "description": "Webhook key rotation is not yet supported for native proposals; use the person's HTTP save or MCP path."}},
		{"id", str("a workflow's id or name")},
		{"name", str("a workflow's name (or id); propose: the name of a new workflow, or a new name for the workflow id names")},
		{"description", str("what the workflow is for (optional, at most 2000 characters)")},
		{"expected_revision", map[string]any{"type": "integer", "description": "propose: the revision a change is made on (get shows it); refused if the workflow changed since"}},
		{"replace", map[string]any{"type": "boolean", "description": "propose: true sends the whole definition, replacing the workflow; default merges the given fields onto its current revision"}},
		{"job_id", str("task id or job id")},
		{"delivery_id", str("list or deliveries: one delivery's steps, findings in full (get_task shows them capped). Otherwise one line per delivery: job, state, round, head, each step's state and verdict.")},
		{"detail", map[string]any{"type": "string", "enum": []string{"deliveries"}, "description": "list: each workflow's last 3 deliveries with their steps' outcomes (bounded), instead of one line each."}},
		{"project", str("")},
		{"workspace", str("")},
		{"scope", map[string]any{"type": "object", "properties": map[string]any{"role": map[string]any{"type": "string"}, "worker": map[string]any{"type": "string"}, "job": map[string]any{"type": "string"}, "schedule": map[string]any{"type": "string"}}}},
		{"when", map[string]any{"type": "string", "enum": []string{"succeeded", "failed", "always"}}},
		{"enabled", map[string]any{"type": "boolean"}},
		{"retries", map[string]any{"type": "integer"}},
		{"revision", map[string]any{"type": "integer"}},
		{"result", map[string]any{"type": "object"}},
		{"steps", map[string]any{"type": "array", "items": map[string]any{"type": "object", "properties": map[string]any{"for_each": WorkflowForEachSchema()}}, "description": "Ordered agent/reopen/run/deliver steps; for_each queues one independent worker job per committed JSON item."}},
		{"path", str("export: relative destination in the workflow's folder (empty: .swarm/workflows/<name>.json); import: relative file or directory in the project folder")},
		{"overwrite", map[string]any{"type": "boolean", "description": "export: replace a different file at path"}},
		{"definition", map[string]any{"type": "object", "description": "import: portable JSON object with swarm_workflow: 1 (from export or history with revision); mutually exclusive with path"}},
		{"stopped", map[string]any{"type": "boolean", "description": "stop_all: true stops every new workflow step on the machine; false resumes"}},
	}
}

func manageWorkflowsCatalog() CatalogTool {
	const (
		read   = CatalogLevelRead
		write  = CatalogLevelWrite
		manage = CatalogLevelManage
		anyM   = CatalogReachAny
		home   = CatalogReachHome
	)
	core := []CatalogProfile{CatalogProfileEssentials, CatalogProfileFull, CatalogProfileOrchestrator}
	ref := []string{"machine", "id", "name"}
	with := func(base []string, more ...string) []string {
		return append(append([]string(nil), base...), more...)
	}
	// Levels are remote.ToolScope of the swarm_* tool with the same effect
	// (swarm_list_workflows for list/get/deliveries, swarm_set_workflow for
	// propose/enable/disable, swarm_delete_workflow, swarm_workflow_history,
	// swarm_export_workflow, swarm_import_workflow, swarm_restore_workflow,
	// swarm_stop_workflows); help has no swarm_* tool and only reads.
	actions := []CatalogAction{
		{Action: "list", Inputs: []string{"machine", "detail", "delivery_id"}, Level: read, Reach: anyM, Profiles: core, Untrusted: true,
			Description: "List workflows, each as it runs, with its last deliveries one line each and the stop switch."},
		{Action: "get", Inputs: ref, Level: read, Reach: anyM, Profiles: core, Untrusted: true,
			Description: "Read one workflow by id or name."},
		{Action: "deliveries", Inputs: with(ref[:2], "job_id", "delivery_id"), Level: read, Reach: anyM, Profiles: core, Untrusted: true,
			Description: "A workflow's or job's deliveries one line each; delivery_id reads one delivery's steps with full findings."},
		{Action: "history", Inputs: with(ref, "revision"), Level: write, Reach: anyM, Profiles: core, Untrusted: true,
			Description: "A workflow's last 100 changes, newest first, even after delete, and its pending proposals; revision reads that revision's portable definition."},
		{Action: "propose", Inputs: []string{"machine", "id", "name", "description", "expected_revision", "replace", "project", "workspace", "scope", "when", "enabled", "retries", "result", "steps", "on", "inputs", "with", "rotate_key"}, Level: write, Reach: anyM, Profiles: core,
			Description: "Propose a new workflow (its full definition) or a change of one (id or name: only the changed fields, merged onto its current revision, expected_revision as the guard; replace: true for a whole definition) for the person to accept or discard; never self-accept. Unknown fields are refused."},
		{Action: "disable", Inputs: ref, Level: write, Reach: anyM, Profiles: core,
			Description: "Disable an existing workflow now."},
		{Action: "enable", Inputs: ref, Level: write, Reach: anyM, Profiles: core,
			Description: "Enable an existing workflow now."},
		{Action: "delete", Inputs: ref, Level: write, Reach: anyM, Profiles: core,
			Description: "Delete a workflow; its history stays and restore recreates it."},
		{Action: "help", Level: read, Reach: home, Profiles: core,
			Description: "The workflow guide with examples."},
		{Action: "export", Inputs: with(ref, "path", "overwrite"), Level: write, Reach: anyM, Profiles: core, Untrusted: true,
			Description: "A workflow's portable JSON (swarm_workflow: 1) with its description and scope (its project); path also writes it into the workflow's folder."},
		{Action: "import", Inputs: []string{"machine", "definition", "path", "project", "workspace", "with", "name"}, Level: write, Reach: anyM, Profiles: core, States: []string{EnvelopeStateRefused},
			Description: "Add or update (by name) a workflow from a portable definition or a project file or directory; name makes a copy, project or workspace chooses its folder (else the definition's project). Run steps need the machine owner: such an import answers state refused."},
		{Action: "restore", Inputs: with(ref, "revision"), Level: write, Reach: anyM, Profiles: core, States: []string{EnvelopeStateRefused},
			Description: "Make a retained revision current as a new revision, recreating a deleted workflow. Run steps need the machine owner: such a restore answers state refused."},
		{Action: "stop_all", Inputs: []string{"machine", "stopped"}, Level: manage, Reach: anyM, Profiles: core,
			Description: "Turn the machine-wide stop switch on (stopped true: no new workflow step starts) or off (false: workflows run again)."},
	}
	// The API scopes of the route behind each action (/v3/workflows,
	// /v3/job-deliveries): reads (and export, whose route takes read scope;
	// writing a file needs automations:write there too) take automations:read
	// or automations:write; changes take automations:write.
	readScopes, writeScopes := []string{"automations:read", "automations:write"}, []string{"automations:write"}
	routeRead := map[string]bool{"list": true, "get": true, "deliveries": true, "history": true, "help": true, "export": true}
	nextActions := map[string]string{
		"list":       "get with a workflow's id reads it; deliveries reads its runs.",
		"get":        "history lists its revisions; propose sends a change for the person to accept.",
		"deliveries": "deliveries with a delivery_id reads one delivery's steps and full findings.",
		"history":    "history with a revision reads that definition; restore with it makes it current.",
		"export":     "import with this definition (and a new name for a copy) adds it here or on another machine.",
		"import":     "get or history reads it; propose changes it; enable runs it if it is disabled.",
		"restore":    "history lists its revisions.",
		"propose":    "The person accepts or discards the proposal in their Swarm app; an AI cannot accept it. history lists it as pending.",
		"delete":     "restore with a revision from history recreates it.",
	}
	for i := range actions {
		actions[i].Tool = manageWorkflowsToolName
		actions[i].NextAction = nextActions[actions[i].Action]
		if routeRead[actions[i].Action] {
			actions[i].APIScopes = readScopes
		} else {
			actions[i].APIScopes = writeScopes
		}
	}
	return CatalogTool{Name: manageWorkflowsToolName, Description: manageWorkflowsDescription, Fields: manageWorkflowsFields(), Actions: actions}
}

func manageWorkflowsDefinition() Definition {
	return manageWorkflowsCatalog().OrchestratorDefinition()
}
