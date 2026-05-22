package tool

import (
	"strings"

	agentruntime "swarm/packages/swarmd/internal/agent"
	pebblestore "swarm/packages/swarmd/internal/store/pebble"
)

func (r *Runtime) customToolAccountScopeID(scope WorkspaceScope) string {
	if !scope.Principal.Valid() {
		return ""
	}
	return strings.TrimSpace(scope.Principal.AccountScopeID)
}

func (r *Runtime) listStateForScope(scope WorkspaceScope, limit int) (agentruntime.State, error) {
	accountScopeID := r.customToolAccountScopeID(scope)
	if accountScopeID != "" {
		return r.agents.ListStateForAccount(accountScopeID, limit)
	}
	return r.agents.ListState(limit)
}

func (r *Runtime) getCustomToolForScope(scope WorkspaceScope, name string) (pebblestore.AgentCustomToolDefinition, bool, error) {
	accountScopeID := r.customToolAccountScopeID(scope)
	if accountScopeID != "" {
		return r.agents.GetCustomToolForAccount(accountScopeID, name)
	}
	return r.agents.GetCustomTool(name)
}

func (r *Runtime) putCustomToolForScope(scope WorkspaceScope, definition pebblestore.AgentCustomToolDefinition) (pebblestore.AgentCustomToolDefinition, error) {
	accountScopeID := r.customToolAccountScopeID(scope)
	if accountScopeID != "" {
		return r.agents.PutCustomToolForAccount(accountScopeID, definition)
	}
	return r.agents.PutCustomTool(definition)
}

func (r *Runtime) deleteCustomToolForScope(scope WorkspaceScope, name string) (bool, error) {
	accountScopeID := r.customToolAccountScopeID(scope)
	if accountScopeID != "" {
		return r.agents.DeleteCustomToolForAccount(accountScopeID, name)
	}
	return r.agents.DeleteCustomTool(name)
}

func (r *Runtime) assignCustomToolForScope(scope WorkspaceScope, agentName, toolName string) (pebblestore.AgentProfile, int64, *pebblestore.EventEnvelope, error) {
	accountScopeID := r.customToolAccountScopeID(scope)
	if accountScopeID != "" {
		return r.agents.AssignCustomToolForAccount(accountScopeID, agentName, toolName)
	}
	return r.agents.AssignCustomTool(agentName, toolName)
}

func (r *Runtime) unassignCustomToolForScope(scope WorkspaceScope, agentName, toolName string) (pebblestore.AgentProfile, int64, *pebblestore.EventEnvelope, error) {
	accountScopeID := r.customToolAccountScopeID(scope)
	if accountScopeID != "" {
		return r.agents.UnassignCustomToolForAccount(accountScopeID, agentName, toolName)
	}
	return r.agents.UnassignCustomTool(agentName, toolName)
}
