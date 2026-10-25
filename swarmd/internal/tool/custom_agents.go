package tool

import (
	"encoding/json"
	"errors"
)

type CustomAgentLifecycleService interface {
	ManageCustomAgents(account string, args map[string]any) (any, error)
}

func (r *Runtime) SetCustomAgentLifecycleService(s CustomAgentLifecycleService) {
	r.customAgentLifecycle = s
}

func (r *Runtime) executeManageCustomAgents(scope WorkspaceScope, args map[string]any) (string, error) {
	if r.customAgentLifecycle == nil {
		return "", errors.New("custom agent service unavailable")
	}
	result, err := r.customAgentLifecycle.ManageCustomAgents(scope.Principal.AccountScopeID, args)
	if err != nil {
		return "", err
	}
	data, err := json.Marshal(result)
	return string(data), err
}

func manageCustomAgentsDefinition() Definition {
	return Definition{Type: "function", Name: "manage_custom_agents", Description: "List, get, define or disable custom agents. Definitions save immediately; workers still require proposal acceptance.", Parameters: map[string]any{
		"type": "object", "additionalProperties": false, "required": []string{"action"}, "properties": map[string]any{
			"action": map[string]any{"type": "string", "enum": []string{"list", "get", "define", "disable"}},
			"name":   map[string]any{"type": "string"}, "description": map[string]any{"type": "string"}, "instructions": map[string]any{"type": "string"},
			"tools": map[string]any{"oneOf": []any{map[string]any{"type": "array", "items": map[string]any{"type": "string"}}, map[string]any{"type": "string", "enum": []string{"read_only", "read_write", "build"}}}},
		}}}
}
