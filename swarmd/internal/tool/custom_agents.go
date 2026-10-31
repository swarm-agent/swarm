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
