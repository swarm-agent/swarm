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
	if s == nil || s.tools == nil || profile.ToolContract == nil {
		return profile, false
	}
	if (profile.Name == "swarm" && profile.Mode != "primary") || (profile.Name != "swarm" && profile.Name != "system-coder" && profile.Name != "system-finder") {
		return profile, false
	}
	_, _, historyErr := s.tools.TaskHistoryBinding(scope)
	_, _, reportErr := s.tools.TaskReportBinding(scope)
	if historyErr != nil && reportErr != nil {
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

func taskHistoryToolDefinitions(definitions []provideriface.ToolDefinition, reportOnly ...bool) []provideriface.ToolDefinition {
	actions := []string{"get_task", "report_task"}
	if len(reportOnly) > 0 && reportOnly[0] {
		actions = []string{"report_task"}
	}
	for i := range definitions {
		if canonicalToolName(definitions[i].Name) != "manage_projects" {
			continue
		}
		definitions[i].Description = "Authenticated linked task control: report_task records progress, attention or a wake_request with summary and stable client_request_id. Ownership comes from the current run. Recorded is not delivered, awakened or accepted. Do not use session messaging or timers to notify the Orchestrator. get_task, when allowed, reads associated follow-up history."
		definitions[i].Parameters = map[string]any{
			"type": "object", "additionalProperties": false,
			"required": []string{"action", "project_id", "task_id"},
			"properties": map[string]any{
				"action":            map[string]any{"type": "string", "enum": actions},
				"project_id":        map[string]any{"type": "string"},
				"task_id":           map[string]any{"type": "string"},
				"update_kind":       map[string]any{"type": "string", "enum": []string{"progress", "attention", "wake_request"}},
				"summary":           map[string]any{"type": "string", "maxLength": 4000},
				"client_request_id": map[string]any{"type": "string", "maxLength": 128},
				"cursor":            map[string]any{"type": "integer", "minimum": 0, "maximum": 1000000},
				"limit":             map[string]any{"type": "integer", "minimum": 1, "maximum": 50},
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
	var reportArgs map[string]any
	if err := json.Unmarshal([]byte(arguments), &reportArgs); err != nil {
		return err
	}
	if reportArgs["action"] == "report_task" {
		projectID, taskID, err := s.tools.TaskReportBinding(scope)
		if err != nil {
			return err
		}
		if reportArgs["project_id"] != projectID || reportArgs["task_id"] != taskID {
			return errors.New("report_task must target the current linked task")
		}
		return nil
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
	_, _, historyErr := s.tools.TaskHistoryBinding(scope)
	for _, definition := range definitions {
		if canonicalToolName(definition.Name) == "manage_projects" {
			return resolved, taskHistoryToolDefinitions(definitions, historyErr != nil), nil
		}
	}
	for _, definition := range filterToolDefinitions(convertToolDefinitions(s.ListAgentToolDefinitionsForAccount(scope.Principal.AccountScopeID)), disabled) {
		if canonicalToolName(definition.Name) == "manage_projects" {
			definitions = append(definitions, definition)
			break
		}
	}
	return resolved, taskHistoryToolDefinitions(definitions, historyErr != nil), nil
}
