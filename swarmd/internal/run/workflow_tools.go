package run

import (
	"context"
	"errors"

	agentruntime "swarm/packages/swarmd/internal/agent"
	"swarm/packages/swarmd/internal/identity"
	pebblestore "swarm/packages/swarmd/internal/store/pebble"
	"swarm/packages/swarmd/internal/tool"
)

// SetWorkflowLifecycleService wires the shared workflow authority. The tool
// runtime serves manage_workflows from the catalog with it (the same handler
// Swarm Control runs); linked worker management uses its fleet routes.
func (s *Service) SetWorkflowLifecycleService(service tool.WorkflowLifecycleService) {
	s.workflowLifecycle = service
	if s.tools != nil {
		s.tools.SetWorkflowLifecycleService(service)
	}
}

// executeManageWorkflowsTool runs the Orchestrator's manage_workflows call
// through the catalog handler (tool.Runtime), as the session's own principal:
// its answer is the catalog envelope, the same as Swarm Control's.
func (s *Service) executeManageWorkflowsTool(ctx context.Context, sessionID, arguments string, profile pebblestore.AgentProfile) (string, error) {
	current, _, err := s.automationV2ToolSession(sessionID)
	if err != nil {
		return "", err
	}
	if !agentruntime.IsOrchestratorAgentName(profile.Name) {
		return "", errors.New("workflow management requires the Orchestrator")
	}
	if s.tools == nil {
		return "", errors.New("workflow service unavailable")
	}
	if ctx == nil {
		ctx = context.Background()
	}
	principal := identity.Principal{AccountScopeID: current.AccountScopeID, UserID: current.UserID, Type: identity.PrincipalTypeUser}
	return s.tools.ExecuteForWorkspaceScopeWithRuntime(ctx, tool.WorkspaceScope{SessionID: sessionID, Principal: principal}, tool.Call{Name: "manage_workflows", Arguments: arguments})
}
