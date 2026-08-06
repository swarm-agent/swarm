package agentmodel

import (
	"errors"
	"fmt"
	"strings"

	agentruntime "swarm/packages/swarmd/internal/agent"
	"swarm/packages/swarmd/internal/agentmodelsettings"
	"swarm/packages/swarmd/internal/model"
	pebblestore "swarm/packages/swarmd/internal/store/pebble"
)

// ResolveSystemAgent is the canonical model-selection path for compiled agents
// configured during onboarding. Agent identity and tools remain compiled, while
// provider/model/thinking/service-tier always come from canonical account-scoped
// agent-model settings.
func ResolveSystemAgent(
	modelService *model.Service,
	agentService *agentruntime.Service,
	settingsService *agentmodelsettings.Service,
	accountScopeID string,
	agentID string,
	contextMode string,
) (model.ResolvedPreference, pebblestore.AgentProfile, error) {
	if modelService == nil || agentService == nil || settingsService == nil {
		return model.ResolvedPreference{}, pebblestore.AgentProfile{}, errors.New("system-agent model, agent, and canonical agent-model settings services are not configured")
	}

	canonicalID, ok := agentruntime.CanonicalSystemAgentID(agentID)
	if !ok {
		return model.ResolvedPreference{}, pebblestore.AgentProfile{}, fmt.Errorf("unknown compiled system agent %q", strings.TrimSpace(agentID))
	}
	settings, err := settingsService.GetForAccount(accountScopeID)
	if err != nil {
		return model.ResolvedPreference{}, pebblestore.AgentProfile{}, fmt.Errorf("read %s model settings: %w", systemAgentLabel(canonicalID), err)
	}
	configured, ok := configuredAgentSettingsForID(settings, canonicalID)
	if !ok {
		return model.ResolvedPreference{}, pebblestore.AgentProfile{}, fmt.Errorf("system agent %q does not use onboarding model settings", canonicalID)
	}
	providerID := strings.ToLower(strings.TrimSpace(configured.Provider))
	modelID := strings.TrimSpace(configured.Model)
	if providerID == "" || modelID == "" {
		return model.ResolvedPreference{}, pebblestore.AgentProfile{}, fmt.Errorf("%s provider and model settings are required", systemAgentLabel(canonicalID))
	}

	resolved, err := modelService.ResolvePreference(pebblestore.ModelPreference{
		Provider:    providerID,
		Model:       modelID,
		Thinking:    strings.TrimSpace(configured.Thinking),
		ServiceTier: strings.TrimSpace(configured.ServiceTier),
		ContextMode: strings.TrimSpace(contextMode),
	})
	if err != nil {
		return model.ResolvedPreference{}, pebblestore.AgentProfile{}, fmt.Errorf("resolve configured %s preference: %w", systemAgentLabel(canonicalID), err)
	}
	profile, err := agentService.ResolveSystemAgent(canonicalID, pebblestore.AgentProfile{
		Provider:        resolved.Preference.Provider,
		Model:           resolved.Preference.Model,
		Thinking:        resolved.Preference.Thinking,
		AutoServiceTier: resolved.Preference.ServiceTier,
		ContextMode:     resolved.Preference.ContextMode,
	})
	if err != nil {
		return model.ResolvedPreference{}, pebblestore.AgentProfile{}, fmt.Errorf("resolve compiled %s: %w", systemAgentLabel(canonicalID), err)
	}
	return resolved, profile, nil
}

func configuredAgentSettingsForID(settings agentmodelsettings.Settings, agentID string) (agentmodelsettings.Assignment, bool) {
	switch agentID {
	case agentruntime.CompactAgentID:
		return settings.SystemAgents.Compact, true
	case agentruntime.FinderAgentID:
		return settings.SystemAgents.Finder, true
	case agentruntime.CoderAgentID:
		return settings.SystemAgents.Coder, true
	case agentruntime.DesignerAgentID:
		return settings.SystemAgents.Designer, true
	case agentruntime.RouterAgentID:
		return settings.SystemAgents.Router, true
	default:
		return agentmodelsettings.Assignment{}, false
	}
}

func systemAgentLabel(agentID string) string {
	switch agentID {
	case agentruntime.CompactAgentID:
		return agentruntime.CompactAgentName
	case agentruntime.FinderAgentID:
		return agentruntime.FinderAgentName
	case agentruntime.CoderAgentID:
		return agentruntime.CoderAgentName
	case agentruntime.DesignerAgentID:
		return agentruntime.DesignerAgentName
	case agentruntime.RouterAgentID:
		return agentruntime.RouterAgentName
	default:
		return agentID
	}
}
