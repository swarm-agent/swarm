package run

import (
	"context"
	"errors"
	"fmt"
	"strings"

	"swarm/packages/swarmd/internal/permission"
	sessionruntime "swarm/packages/swarmd/internal/session"
	pebblestore "swarm/packages/swarmd/internal/store/pebble"
	"swarm/packages/swarmd/internal/tool"
)

// authorizeTaskPlanPublication separates publishing a task-card review from
// accepting a standalone session plan. Metadata alone never grants this path.
func (s *Service) authorizeTaskPlanPublication(ctx context.Context, config providerToolInvokerConfig, call tool.Call) (bool, error) {
	if canonicalToolName(call.Name) != "exit_plan_mode" || s.sessions == nil {
		return false, nil
	}
	current, found, err := s.sessions.GetSession(config.sessionID)
	if err != nil {
		return false, err
	}
	if !found {
		return false, errors.New("plan publication session unavailable")
	}
	projectID, taskID := strings.TrimSpace(mapString(current.Metadata, "project_id")), strings.TrimSpace(mapString(current.Metadata, "task_id"))
	if projectID == "" && taskID == "" {
		return false, nil
	}
	if projectID == "" || taskID == "" {
		return false, errors.New("incomplete task plan publication identity")
	}
	principal := providerManagedExecutionPrincipal(ctx, config)
	if !config.providerManagedV3 || config.applySessionMutation == nil || !principal.Valid() || principal.SessionID != current.ID || principal.AccountScopeID != current.AccountScopeID || principal.UserID != current.UserID || (config.permissionSessionID != "" && config.permissionSessionID != current.ID) {
		return false, errors.New("task plan publication principal mismatch")
	}
	if sessionruntime.NormalizeMode(current.Mode) != sessionruntime.ModePlan || sessionruntime.NormalizeMode(config.sessionMode) != sessionruntime.ModePlan || !pebblestore.AgentExitPlanModeEnabled(config.agentProfile) || pebblestore.AgentProfileRuntimeMode(config.agentProfile) != pebblestore.AgentRuntimeModePlanAuto {
		return false, errors.New("task plan publication requires eligible Plan runtime")
	}
	task, found, err := s.sessions.Store().GetProjectTask(current.AccountScopeID, projectID, taskID)
	if err != nil {
		return false, err
	}
	if !found || task == nil || task.AccountID != current.AccountScopeID || task.ProjectID != projectID || task.SessionID != current.ID || task.Archived || (task.Status != "planning" && task.Status != "pending_approval") || task.ExecutionRunID() != config.runID {
		return false, errors.New("task plan publication does not own active task")
	}
	task.EnsureTaskAttempts()
	attempt := task.ActiveAttempt()
	if attempt == nil || attempt.SessionID != current.ID || (attempt.UserID != "" && attempt.UserID != principal.UserID) || (mapString(current.Metadata, "task_attempt_id") != "" && mapString(current.Metadata, "task_attempt_id") != attempt.ID) {
		return false, errors.New("task plan publication attempt mismatch")
	}
	active, found, err := s.sessions.Store().GetV3SessionActiveRunIntent(current.ID)
	if err != nil {
		return false, err
	}
	if !found || active.RunID != config.runID || active.AccountScopeID != current.AccountScopeID || (active.Status != pebblestore.V3RunIntentRunning && active.Status != pebblestore.V3RunIntentPendingExecutor) {
		return false, errors.New("task plan publication run is stale")
	}
	if err := rejectMalformedToolCallArguments(call); err != nil {
		return false, err
	}
	input, _, _, err := s.prepareExitPlanModeLifecycleInput(current.ID, call.Arguments, "")
	if err != nil {
		return false, err
	}
	if input.Document == nil || input.Document.Automation != nil {
		return false, errors.New("task plan publication requires a structured session plan")
	}
	if err := sessionruntime.ValidateExecutablePlanDocument(input.Document); err != nil {
		return false, err
	}
	if err := pebblestore.ValidateTaskPlanSources(task, input.Document); err != nil {
		return false, err
	}
	for _, checkpoint := range input.Document.Checkpoints {
		if checkpoint.TaskProgram == nil {
			continue
		}
		for _, job := range checkpoint.TaskProgram.Jobs {
			if job.AgentType != "coder" && job.AgentType != "finder" {
				continue
			}
			path := strings.TrimSpace(job.WorkspacePath)
			if path == "" {
				path = task.SourceWorkspace.Path
			}
			if s.workspace == nil {
				return false, errors.New("plan publication workspace catalog unavailable")
			}
			scope, err := s.workspace.ScopeForPathForPrincipal(principal, path)
			if err != nil || !scope.Matched || scope.WorkspacePath != path || scope.ResolvedPath != path {
				return false, fmt.Errorf("plan job %q source %q is not an authorized canonical catalog root", job.ID, path)
			}
			resolved := pebblestore.ProjectTaskSource{Path: path, WorkspaceID: scope.WorkspaceID, WorkspaceGeneration: scope.WorkspaceGeneration}
			matched := false
			for _, admitted := range append([]pebblestore.ProjectTaskSource{task.SourceWorkspace}, task.ProgramSources...) {
				matched = matched || resolved.SameIdentity(admitted)
			}
			if !matched {
				return false, fmt.Errorf("plan job %q source %q has stale catalog admission; explicitly resubmit the plan as a new task for source review", job.ID, path)
			}
		}
	}
	if s.permissions == nil {
		return false, errors.New("permission service is not configured")
	}
	explain, err := s.permissions.ExplainPendingReviewPublication(current.AccountScopeID, current.Mode, call.Name, call.Arguments, config.policy)
	if err != nil {
		return false, err
	}
	if explain.Decision == permission.PolicyDecisionDeny {
		return false, errors.New(explain.Reason)
	}
	return true, nil
}
