package compact

import (
	"errors"
	"fmt"
	"strings"

	agentruntime "swarm/packages/swarmd/internal/agent"
	"swarm/packages/swarmd/internal/model"
	pebblestore "swarm/packages/swarmd/internal/store/pebble"
	"swarm/packages/swarmd/internal/uisettings"
)

// ResolvePreference is the single model-selection path for every Compact case.
// Compact's identity and execution contract remain compiled, while its active
// provider/model/thinking/service-tier selection is account-owned UI state.
func ResolvePreference(modelService *model.Service, agentService *agentruntime.Service, settingsService *uisettings.Service, accountScopeID string, basePreference pebblestore.ModelPreference) (model.ResolvedPreference, pebblestore.AgentProfile, error) {
	if modelService == nil || agentService == nil || settingsService == nil {
		return model.ResolvedPreference{}, pebblestore.AgentProfile{}, errors.New("Compact model, agent, and settings services are not configured")
	}
	settings, err := settingsService.GetForAccount(strings.TrimSpace(accountScopeID))
	if err != nil {
		return model.ResolvedPreference{}, pebblestore.AgentProfile{}, fmt.Errorf("read active Compact system-agent settings: %w", err)
	}
	configured := settings.Agents.Compact
	providerID := strings.ToLower(strings.TrimSpace(configured.Provider))
	if providerID == "" {
		providerID = strings.ToLower(strings.TrimSpace(basePreference.Provider))
	}
	if providerID == "" {
		return model.ResolvedPreference{}, pebblestore.AgentProfile{}, errors.New("Compact system-agent provider is empty")
	}

	_, _, utility, ok, err := modelService.RecommendedCatalogDefaults(providerID)
	if err != nil {
		return model.ResolvedPreference{}, pebblestore.AgentProfile{}, fmt.Errorf("resolve Compact utility recommendation: %w", err)
	}
	if !ok || strings.TrimSpace(utility.Model) == "" {
		return model.ResolvedPreference{}, pebblestore.AgentProfile{}, fmt.Errorf("Compact utility recommendation for provider %q is unavailable", providerID)
	}
	modelName := strings.TrimSpace(configured.Model)
	if modelName == "" {
		modelName = strings.TrimSpace(utility.Model)
	}
	thinking := strings.TrimSpace(configured.Thinking)
	if thinking == "" {
		for _, recommendation := range utility.Recommendations {
			if strings.EqualFold(strings.TrimSpace(recommendation.Role), "utility") {
				thinking = strings.TrimSpace(recommendation.Thinking)
				break
			}
		}
	}
	resolved, err := modelService.ResolvePreference(pebblestore.ModelPreference{
		Provider:    providerID,
		Model:       modelName,
		Thinking:    thinking,
		ServiceTier: strings.TrimSpace(configured.ServiceTier),
		ContextMode: basePreference.ContextMode,
	})
	if err != nil {
		return model.ResolvedPreference{}, pebblestore.AgentProfile{}, fmt.Errorf("resolve active Compact system-agent preference: %w", err)
	}
	profile, err := agentService.ResolveSystemAgent(agentruntime.CompactAgentID, pebblestore.AgentProfile{
		Provider:        resolved.Preference.Provider,
		Model:           resolved.Preference.Model,
		Thinking:        resolved.Preference.Thinking,
		AutoServiceTier: resolved.Preference.ServiceTier,
	})
	if err != nil {
		return model.ResolvedPreference{}, pebblestore.AgentProfile{}, fmt.Errorf("resolve Compact system agent: %w", err)
	}
	return resolved, profile, nil
}
