package run

import (
	"encoding/json"
	"errors"

	agentruntime "swarm/packages/swarmd/internal/agent"

	provideriface "swarm/packages/swarmd/internal/provider/interfaces"
	pebblestore "swarm/packages/swarmd/internal/store/pebble"
	"swarm/packages/swarmd/internal/tool"
)

func (s *Service) taskHistoryProfile(scope tool.WorkspaceScope, profile pebblestore.AgentProfile) (pebblestore.AgentProfile, bool) {
	if s == nil || s.tools == nil || profile.ToolContract == nil || profile.Name != "swarm" || profile.Mode != "primary" {
		return profile, false
	}
	if _, _, err := s.tools.TaskHistoryBinding(scope); err != nil {
		return profile, false
	}
	copyProfile := profile
	contract := *profile.ToolContract
	contract.Tools = make(map[string]pebblestore.AgentToolConfig, len(profile.ToolContract.Tools)+1)
	for name, config := range profile.ToolContract.Tools {
		contract.Tools[name] = config
	}
	contract.Tools["manage_projects"] = pebblestore.AgentToolConfig{Enabled: pebblestore.BoolPtr(true)}
	copyProfile.ToolContract = &contract
	return copyProfile, true
}

func taskHistoryToolDefinitions(definitions []provideriface.ToolDefinition) []provideriface.ToolDefinition {
	for i := range definitions {
		if canonicalToolName(definitions[i].Name) != "manage_projects" {
			continue
		}
		definitions[i].Parameters = map[string]any{
			"type": "object", "additionalProperties": false,
			"required": []string{"action", "project_id", "task_id"},
			"properties": map[string]any{
				"action":     map[string]any{"type": "string", "enum": []string{"get_task"}},
				"project_id": map[string]any{"type": "string"},
				"task_id":    map[string]any{"type": "string"},
				"cursor":     map[string]any{"type": "integer", "minimum": 0, "maximum": 1000000},
				"limit":      map[string]any{"type": "integer", "minimum": 1, "maximum": 50},
			},
		}
	}
	return definitions
}

func (s *Service) authorizeProjectHistoryInvocation(scope tool.WorkspaceScope, profile pebblestore.AgentProfile, arguments string) error {
	if agentruntime.IsSwarmOrchestratorAgentName(profile.Name) {
		return nil
	}
	_, allowed := s.taskHistoryProfile(scope, profile)
	if !allowed {
		return errors.New("project tools require Orchestrator or an associated Auto Swarm follow-up")
	}
	projectID, taskID, err := s.tools.TaskHistoryBinding(scope)
	if err != nil {
		return err
	}
	var args map[string]any
	if err := json.Unmarshal([]byte(arguments), &args); err != nil {
		return err
	}
	if args["action"] != "get_task" || args["project_id"] != projectID || args["task_id"] != taskID {
		return errors.New("task follow-up permits only associated get_task history")
	}
	return nil
}

// ResolveTaskHistoryTools is the V3 executor's session-bound capability overlay.
// The ordinary compiled registry remains unchanged for all other sessions.
func (s *Service) ResolveTaskHistoryTools(scope tool.WorkspaceScope, profile pebblestore.AgentProfile, definitions []provideriface.ToolDefinition) (pebblestore.AgentProfile, []provideriface.ToolDefinition, error) {
	resolved, allowed := s.taskHistoryProfile(scope, profile)
	if !allowed {
		return profile, definitions, nil
	}
	_, _, disabled, err := s.compileResolvedAgentToolContract(scope.Principal.AccountScopeID, resolved)
	if err != nil {
		return profile, nil, err
	}
	for _, definition := range definitions {
		if canonicalToolName(definition.Name) == "manage_projects" {
			return resolved, taskHistoryToolDefinitions(definitions), nil
		}
	}
	for _, definition := range filterToolDefinitions(convertToolDefinitions(s.ListAgentToolDefinitionsForAccount(scope.Principal.AccountScopeID)), disabled) {
		if canonicalToolName(definition.Name) == "manage_projects" {
			definitions = append(definitions, definition)
			break
		}
	}
	return resolved, taskHistoryToolDefinitions(definitions), nil
}
