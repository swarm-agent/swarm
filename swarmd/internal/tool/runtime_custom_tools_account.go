package tool

import (
	"strings"

	agentruntime "swarm/packages/swarmd/internal/agent"
	pebblestore "swarm/packages/swarmd/internal/store/pebble"
)

func (r *Runtime) customToolAccountScopeID(scope WorkspaceScope) string {
	return r.agentAccountScopeID(scope)
}

func (r *Runtime) agentAccountScopeID(scope WorkspaceScope) string {
	if !scope.Principal.Valid() {
		return ""
	}
	return strings.TrimSpace(scope.Principal.AccountScopeID)
}

func (r *Runtime) listStateForScope(scope WorkspaceScope, limit int) (agentruntime.State, error) {
	accountScopeID := r.agentAccountScopeID(scope)
	if accountScopeID != "" {
		return r.agents.ListStateForAccount(accountScopeID, limit)
	}
	return r.agents.ListState(limit)
}

func (r *Runtime) getAgentProfileForScope(scope WorkspaceScope, name string) (pebblestore.AgentProfile, bool, error) {
	accountScopeID := r.agentAccountScopeID(scope)
	if accountScopeID != "" {
		return r.agents.GetProfileForAccount(accountScopeID, name)
	}
	return r.agents.GetProfile(name)
}

func (r *Runtime) previewUpsertAgentForScope(scope WorkspaceScope, input agentruntime.UpsertInput) (agentruntime.PreviewUpsertResult, error) {
	accountScopeID := r.agentAccountScopeID(scope)
	if accountScopeID != "" {
		return r.agents.PreviewUpsertForAccount(accountScopeID, input)
	}
	return r.agents.PreviewUpsert(input)
}

func (r *Runtime) upsertAgentForScope(scope WorkspaceScope, input agentruntime.UpsertInput) (pebblestore.AgentProfile, int64, *pebblestore.EventEnvelope, error) {
	accountScopeID := r.agentAccountScopeID(scope)
	if accountScopeID != "" {
		return r.agents.UpsertForAccount(accountScopeID, input)
	}
	return r.agents.Upsert(input)
}

func (r *Runtime) deleteAgentForScope(scope WorkspaceScope, name string) (agentruntime.DeleteResult, int64, *pebblestore.EventEnvelope, error) {
	accountScopeID := r.agentAccountScopeID(scope)
	if accountScopeID != "" {
		return r.agents.DeleteForAccount(accountScopeID, name)
	}
	return r.agents.Delete(name)
}

func (r *Runtime) activatePrimaryForScope(scope WorkspaceScope, name string) (string, int64, *pebblestore.EventEnvelope, error) {
	accountScopeID := r.agentAccountScopeID(scope)
	if accountScopeID != "" {
		return r.agents.ActivatePrimaryForAccount(accountScopeID, name)
	}
	return r.agents.ActivatePrimary(name)
}

func (r *Runtime) setActiveSubagentForScope(scope WorkspaceScope, purpose, name string) (map[string]string, int64, *pebblestore.EventEnvelope, error) {
	accountScopeID := r.agentAccountScopeID(scope)
	if accountScopeID != "" {
		return r.agents.SetActiveSubagentForAccount(accountScopeID, purpose, name)
	}
	return r.agents.SetActiveSubagent(purpose, name)
}

func (r *Runtime) deleteActiveSubagentForScope(scope WorkspaceScope, purpose string) (map[string]string, int64, *pebblestore.EventEnvelope, error) {
	accountScopeID := r.agentAccountScopeID(scope)
	if accountScopeID != "" {
		return r.agents.DeleteActiveSubagentForAccount(accountScopeID, purpose)
	}
	return r.agents.DeleteActiveSubagent(purpose)
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
