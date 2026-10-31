package tool

// catalogRequired declares, per catalog tool (manage_projects keeps its own
// table in manageProjectsCatalog), the inputs each action's handler cannot
// run without, here and on a linked machine (CatalogAction.Required). An
// input that is one of a choice (session_id or session_ids, a workflow's id
// or name, title or prompt) or that only some callers need (a worker's
// proposals without a conversation, a new workflow's name) is not listed:
// its handler says when it is missing. TestCatalogRequiredInputsEveryAction
// removes each input from every action's call and fails when a handler
// refuses the call for an input not declared here.
var catalogRequired = map[string]map[string][]string{
	manageWorkflowsToolName: {
		"restore":  {"revision"},
		"stop_all": {"stopped"},
	},
	manageJobsToolName: {
		"get_job":            {"job_id"},
		"rerun_failed_steps": {"job_id"},
		"give_job":           {"worker", "brief"},
		"message_job":        {"job_id", "message"},
		"answer_job":         {"job_id", "answer"},
		"deliver_job":        {"job_id"},
		"stop_job":           {"job_id"},
		"schedule_job":       {"worker", "brief"},
	},
	manageWorkersToolName: {
		"inspect":            {"worker_id"},
		"update":             {"worker_id", "expected_revision"},
		"attach":             {"worker_id", "expected_revision"},
		"disable_automation": {"worker_id", "automation_id", "expected_revision"},
		"delete":             {"worker_id", "expected_revision"},
		"activate":           {"worker_id", "expected_revision"},
		"pause":              {"worker_id", "expected_revision"},
		"resume":             {"worker_id", "expected_revision"},
		"archive":            {"worker_id", "expected_revision"},
		"test":               {"worker_id", "idempotency_key"},
		"request":            {"worker_id", "idempotency_key"},
		"assign_task":        {"worker_id", "task"},
	},
	manageSetupToolName: {
		"set_agent_model":      {"role"},
		"set_limits":           {"expected_revision", "limits"},
		"set_permissions":      {"expected_revision", "client_request_id"},
		"revoke_key":           {"key_id"},
		"request_fleet_key":    {"provider"},
		"revoke_fleet_key":     {"id"},
		"request_provider_key": {"provider"},
	},
	ManageMediaToolName: {
		"generate":   {"kind", "brief"},
		"retry":      {"job_id"},
		"get":        {"job_id"},
		"save":       {"job_id"},
		"transcribe": {"inputs"},
		"read":       {"project_id", "task_id"},
	},
	manageCustomAgentsToolName: {
		"get":     {"name"},
		"define":  {"name", "instructions", "tools"},
		"disable": {"name"},
	},
	manageDesignToolName: {
		"create": {"brief"},
		"edit":   {"brief", "edit_path"},
	},
	manageSessionsToolName: {
		"list_by_state":                       {"state"},
		"get":                                 {"session_id"},
		"read_messages":                       {"session_id"},
		"stop":                                {"session_id"},
		"pause":                               {"session_id"},
		"send_message":                        {"session_id", "prompt"},
		"compact":                             {"session_id"},
		ManageSessionsResolvePermissionAction: {"session_id", "permission_id", "decision"},
		ManageSessionsSetModelAction:          {"session_id", "provider", "model"},
	},
	manageWorkspaceToolName: {
		"update_map":                    {"expected_revision", "content", "intent"},
		"create":                        {"workspace_path"},
		"update":                        {"workspace_id", "workspace_generation"},
		"delete":                        {"workspace_id", "workspace_generation"},
		"set_default":                   {"workspace_id"},
		"add_source_media_directory":    {"workspace_path", "directory_path"},
		"list_source_media_directories": {"workspace_path"},
		"remove_source_media_directory": {"workspace_path", "directory_path"},
	},
}

// catalogRequiredOf is the declared required inputs of a tool's action.
func catalogRequiredOf(toolName, action string) []string {
	return catalogRequired[toolName][action]
}
