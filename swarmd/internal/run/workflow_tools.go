package run

import (
	"context"
	"encoding/json"
	"errors"
	agentruntime "swarm/packages/swarmd/internal/agent"
	"swarm/packages/swarmd/internal/identity"
	pebblestore "swarm/packages/swarmd/internal/store/pebble"
	"swarm/packages/swarmd/internal/tool"
)

func (s *Service) SetWorkflowLifecycleService(service tool.WorkflowLifecycleService) {
	s.workflowLifecycle = service
}

func (s *Service) executeManageWorkflowsTool(sessionID, arguments string, profile pebblestore.AgentProfile) (string, error) {
	current, _, err := s.automationV2ToolSession(sessionID)
	if err != nil {
		return "", err
	}
	if !agentruntime.IsOrchestratorAgentName(profile.Name) {
		return "", errors.New("workflow management requires the Orchestrator")
	}
	if s.workflowLifecycle == nil {
		return "", errors.New("workflow service unavailable")
	}
	var args map[string]any
	if err := json.Unmarshal([]byte(arguments), &args); err != nil {
		return "", err
	}
	if rotate, _ := args["rotate_key"].(bool); rotate {
		return "", errors.New("webhook key rotation is not supported for native workflow proposals; use the person's HTTP save or MCP path")
	}
	out, err := s.workflowLifecycle.ManageWorkflows(context.Background(), identity.Principal{AccountScopeID: current.AccountScopeID, UserID: current.UserID, Type: identity.PrincipalTypeUser}, sessionID, args)
	if err != nil {
		return "", err
	}
	data, err := json.Marshal(out)
	return string(data), err
}
