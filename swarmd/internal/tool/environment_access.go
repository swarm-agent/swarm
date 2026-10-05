package tool

import (
	"encoding/json"
	"errors"
	"strings"

	agentruntime "swarm/packages/swarmd/internal/agent"
	pebblestore "swarm/packages/swarmd/internal/store/pebble"
)

// authorizeEnvironmentAccess deliberately does not accept role or profile arguments.
// Reload the durable session on every call, including calls from resumed children.
func (r *Runtime) authorizeEnvironmentAccess(scope WorkspaceScope, toolName string) (pebblestore.SessionSnapshot, error) {
	deny := errors.New("environment access denied: authenticated Swarm or Orchestrator session required")
	if r == nil || r.sessions == nil || !scope.Principal.Valid() || scope.SessionID == "" || (scope.Principal.SessionID != "" && scope.Principal.SessionID != scope.SessionID) {
		return pebblestore.SessionSnapshot{}, deny
	}
	snapshot, found, err := r.sessions.GetSession(scope.SessionID)
	if err != nil || !found || snapshot.ID != scope.SessionID || snapshot.AccountScopeID != scope.Principal.AccountScopeID || snapshot.UserID != scope.Principal.UserID {
		return pebblestore.SessionSnapshot{}, deny
	}
	if !EnvironmentToolAllowed(snapshot.Metadata, toolName) {
		return pebblestore.SessionSnapshot{}, deny
	}
	return snapshot, nil
}

// EnvironmentToolAllowed is also used by prompt assembly to avoid advertising
// execution to excluded agents. It grants no authority without session admission.
func EnvironmentToolAllowed(metadata map[string]any, toolName string) bool {
	var profile pebblestore.AgentProfile
	raw, err := json.Marshal(metadata["agent_profile"])
	if err != nil || json.Unmarshal(raw, &profile) != nil || strings.TrimSpace(profile.Name) == "" {
		return false
	}
	for _, name := range []string{profile.Name, asString(metadata["agent_name"]), asString(metadata["subagent"])} {
		if agentruntime.IsCoderAgentName(name) {
			return false
		}
	}
	if profile.ToolContract != nil {
		if cfg, ok := profile.ToolContract.Tools[toolName]; ok && cfg.Enabled != nil && !*cfg.Enabled {
			return false
		}
	}
	return strings.EqualFold(strings.TrimSpace(profile.Name), agentruntime.SwarmAgentID) || agentruntime.IsOrchestratorAgentName(profile.Name)
}
