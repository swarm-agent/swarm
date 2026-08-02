package compact

import (
	agentruntime "swarm/packages/swarmd/internal/agent"
	"swarm/packages/swarmd/internal/agentmodel"
	"swarm/packages/swarmd/internal/model"
	pebblestore "swarm/packages/swarmd/internal/store/pebble"
	"swarm/packages/swarmd/internal/uisettings"
)

// ResolvePreference delegates Compact model selection to the canonical
// account-scoped system-agent settings resolver.
func ResolvePreference(modelService *model.Service, agentService *agentruntime.Service, settingsService *uisettings.Service, accountScopeID string, basePreference pebblestore.ModelPreference) (model.ResolvedPreference, pebblestore.AgentProfile, error) {
	return agentmodel.ResolveSystemAgent(modelService, agentService, settingsService, accountScopeID, agentruntime.CompactAgentID, basePreference.ContextMode)
}
