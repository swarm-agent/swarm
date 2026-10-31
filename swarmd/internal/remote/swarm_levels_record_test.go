package remote

// The record of the Swarm Control levels the swarm_* tools needed when they
// were removed (stage 7b): each catalog action that replaced one needs the
// same level, never lower, and is local only exactly when that tool was. The
// tools are gone; the record stays so a catalog change cannot quietly lower a
// level. A tool missing from swarmToolLevels needed write.

var swarmToolLevels = map[string]string{
	"swarm_list_workspaces":        ScopeRead,
	"swarm_list_sessions":          ScopeRead,
	"swarm_get_session":            ScopeRead,
	"swarm_list_projects":          ScopeRead,
	"swarm_get_project":            ScopeRead,
	"swarm_get_theme":              ScopeRead,
	"swarm_get_plan_usage":         ScopeRead,
	"swarm_set_session_access":     ScopeApprove,
	"swarm_get_permissions":        ScopeRead,
	"swarm_set_permissions":        ScopeApprove,
	"swarm_read_media":             ScopeRead,
	"swarm_list_workers":           ScopeRead,
	"swarm_get_worker":             ScopeRead,
	"swarm_get_limits":             ScopeRead,
	"swarm_set_limits":             ScopeWrite,
	"swarm_get_usage":              ScopeRead,
	"swarm_list_models":            ScopeRead,
	"swarm_list_tasks":             ScopeRead,
	"swarm_wait_task":              ScopeRead,
	"swarm_task_result":            ScopeRead,
	"swarm_run_task":               ScopeWrite,
	"swarm_create_workspace":       ScopeWrite,
	"swarm_add_workspace":          ScopeWrite,
	"swarm_start_session":          ScopeWrite,
	"swarm_send_message":           ScopeWrite,
	"swarm_run_plan":               ScopeWrite,
	"swarm_stop_run":               ScopeWrite,
	"swarm_assign_worker_task":     ScopeWrite,
	"swarm_set_session_model":      ScopeWrite,
	"swarm_resolve_permission":     ScopeApprove,
	"swarm_integrate_task":         ScopeWrite,
	"swarm_create_project":         ScopeWrite,
	"swarm_update_project_context": ScopeManage,
	"swarm_set_project_workspaces": ScopeWrite,
	"swarm_retry_project_context":  ScopeManage,
	"swarm_create_worker":          ScopeManage,
	"swarm_update_worker":          ScopeManage,
	"swarm_manage_worker":          ScopeManage,
	"swarm_set_usage_limits":       ScopeManage,
	"swarm_set_agent_model":        ScopeWrite,
	"swarm_set_media_models":       ScopeWrite,
	"swarm_list_agents":            ScopeRead,
	"swarm_define_agent":           ScopeWrite,
	"swarm_create_client_key":      ScopeManage,
	"swarm_create_key":             ScopeManage,
	"swarm_list_keys":              ScopeRead,
	"swarm_revoke_key":             ScopeWrite,
	"swarm_connect_chatgpt":        ScopeWrite, // the person signs in in their own browser; local only
	"swarm_connect_claude_code":    ScopeWrite, // the person signs the CLI in on the machine; local only
	"swarm_list_providers":         ScopeRead,
	"swarm_request_provider_key":   ScopeWrite, // the person types the key on Swarm's own page, never the AI; local only

	// Fleet keys: reading the record is read; asking for a key for linked
	// machines and removing one is approve, like linked machine rules.
	"swarm_list_fleet_keys":   ScopeRead,
	"swarm_request_fleet_key": ScopeApprove, // the person types the key on the home's own page; local only
	"swarm_revoke_fleet_key":  ScopeApprove,

	"swarm_read_file":     ScopeRead,
	"swarm_get_changes":   ScopeRead,
	"swarm_accept_review": ScopeApprove,
	"swarm_approve_task":  ScopeManage,
	// Workers and jobs, the default surface: reading them and the machine's
	// state is read; deploying workers, giving, answering, merging, stopping
	// and scheduling jobs is write. Answering covers a worker's questions only,
	// never a tool-call approval; delivering is a local git merge, or a pull
	// request where the folder's owner set up publishing; Swarm itself never pushes.
	"swarm_request_update":  ScopeManage,
	"swarm_cancel_update":   ScopeManage,
	"swarm_status":          ScopeRead,
	"swarm_activity":        ScopeRead,
	"swarm_list_runs":       ScopeRead,
	"swarm_agents_at_work":  ScopeRead,
	"swarm_get_job":         ScopeRead,
	"swarm_list_secrets":    ScopeRead,
	"swarm_deploy_worker":   ScopeWrite,
	"swarm_give_job":        ScopeWrite,
	"swarm_answer_job":      ScopeWrite,
	"swarm_message_job":     ScopeWrite,
	"swarm_set_environment": ScopeWrite,
	"swarm_get_environment": ScopeRead,
	"swarm_deliver_job":     ScopeWrite,
	"swarm_stop_job":        ScopeWrite,
	"swarm_schedule_job":    ScopeWrite,
	"swarm_request_media":   ScopeWrite,
	"swarm_media_models":    ScopeRead,
	"swarm_save_media":      ScopeWrite,
	"swarm_design":          ScopeWrite,
	// Job automations: the steps a person sets for when a job ends. Setting
	// and deleting them is write like scheduling a job; the machine-wide stop
	// switch is manage.
	"swarm_set_workflow":    ScopeWrite,
	"swarm_import_workflow": ScopeWrite,
	"swarm_export_workflow": ScopeWrite,
	"swarm_list_workflows":  ScopeRead,
	"swarm_delete_workflow": ScopeWrite,
	"swarm_stop_workflows":  ScopeManage,
}

// swarmLocalOnlyTools were box setup only a key on the machine itself could do
// (the device token never carries the owner's admin): they were neither listed
// nor forwarded for relay clients. On the machine, ChatGPT sign-in took a
// write AI key, the rest a full one.
var swarmLocalOnlyTools = map[string]bool{
	"swarm_create_client_key":    true,
	"swarm_create_key":           true,
	"swarm_connect_chatgpt":      true,
	"swarm_connect_claude_code":  true,
	"swarm_request_provider_key": true,
	"swarm_request_fleet_key":    true,
}

// swarmToolLevel is the level the removed swarm_* tool name needed.
func swarmToolLevel(name string) string {
	if scope, ok := swarmToolLevels[name]; ok {
		return scope
	}
	return ScopeWrite
}
