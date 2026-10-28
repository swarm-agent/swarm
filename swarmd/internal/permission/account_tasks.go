package permission

import (
	"strings"

	pebblestore "swarm/packages/swarmd/internal/store/pebble"
)

// authorizeHomeTaskLaunch makes the account Start tasks rule the only gate for
// manage_projects calls that start the home's own tasks (project_task_launch):
//   - propose_task/create_task never ask: under auto the task starts at once,
//     under approve the tool runtime makes it a task that waits.
//   - integrate_task never asks: integrating starts no task.
//   - approve_task/accept_task/deploy_task start a task: under auto they run;
//     under approve the person approves the call (bypass does not skip it).
//
// Calls with machine are linked work and are decided by linked.{name}.work.
func (s *Service) authorizeHomeTaskLaunch(input AuthorizationInput, sessionID, requirement string) (AuthorizationResult, bool, error) {
	if manageProjectsPolicyIdentity(input.ToolArguments) != policyToolProjectTaskLaunch {
		return AuthorizationResult{}, false, nil
	}
	args := parsePermissionJSONMap(input.ToolArguments)
	if strings.TrimSpace(mapStringAny(args["machine"])) != "" {
		return AuthorizationResult{}, false, nil
	}
	action := strings.ToLower(strings.TrimSpace(mapStringAny(args["action"])))
	approve := func(reason string) (AuthorizationResult, bool, error) {
		return AuthorizationResult{Decision: AuthorizationApprove, Requirement: requirement, Reason: reason, Source: "account_permissions"}, true, nil
	}
	switch action {
	case "propose_task", "create_task":
		return approve("Start tasks decides whether the new task starts or waits")
	case "integrate_task":
		return approve("integrating a task's work starts no task")
	}
	tasks := pebblestore.RuleAuto
	if s.store != nil {
		access, err := s.store.GetAccountPermissions(input.AccountScopeID)
		if err != nil {
			return AuthorizationResult{}, true, err
		}
		tasks = access.Tasks
	}
	if tasks == pebblestore.RuleAuto {
		return approve("Start tasks is auto")
	}
	result, err := s.createPendingAuthorization(input, sessionID, requirement, "Start tasks is approve: approve starting this task", "account_permissions", "ask before starting tasks")
	return result, true, err
}
