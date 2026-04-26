package api

import (
	"strings"

	agentruntime "swarm/packages/swarmd/internal/agent"
	"swarm/packages/swarmd/internal/provider/defaults"
	pebblestore "swarm/packages/swarmd/internal/store/pebble"
)

type providerDefaultsPreviewResponse struct {
	Provider              string   `json:"provider,omitempty"`
	PrimaryAgent          string   `json:"primary_agent,omitempty"`
	PrimaryModel          string   `json:"primary_model,omitempty"`
	PrimaryThinking       string   `json:"primary_thinking,omitempty"`
	UtilityProvider       string   `json:"utility_provider,omitempty"`
	UtilityModel          string   `json:"utility_model,omitempty"`
	UtilityThinking       string   `json:"utility_thinking,omitempty"`
	UtilityAgents         []string `json:"utility_agents,omitempty"`
	AffectedAgents        []string `json:"affected_agents,omitempty"`
	OutOfSyncAgents       []string `json:"out_of_sync_agents,omitempty"`
	InheritingAgents      []string `json:"inheriting_agents,omitempty"`
	StaleInheritedAgents  []string `json:"stale_inherited_agents,omitempty"`
	CustomUtilityAgents   []string `json:"custom_utility_agents,omitempty"`
	UtilityBaselineAgents []string `json:"utility_baseline_agents,omitempty"`
}

func (s *Server) providerDefaultsPreviewForState(state agentruntime.State) *providerDefaultsPreviewResponse {
	providerID, providerDefaults, ok := s.resolveAgentProviderDefaults()
	if !ok {
		return nil
	}
	utilityAgents := utilityAgentNames(providerDefaults)
	preview := &providerDefaultsPreviewResponse{
		Provider:              providerID,
		PrimaryAgent:          "swarm",
		PrimaryModel:          strings.TrimSpace(providerDefaults.PrimaryModel),
		PrimaryThinking:       strings.TrimSpace(providerDefaults.PrimaryThinking),
		UtilityProvider:       providerID,
		UtilityModel:          strings.TrimSpace(providerDefaults.UtilityModel),
		UtilityThinking:       strings.TrimSpace(providerDefaults.UtilityThinking),
		UtilityAgents:         append([]string(nil), utilityAgents...),
		AffectedAgents:        append([]string{"swarm"}, utilityAgents...),
		OutOfSyncAgents:       nil,
		InheritingAgents:      nil,
		StaleInheritedAgents:  nil,
		CustomUtilityAgents:   nil,
		UtilityBaselineAgents: nil,
	}
	profilesByName := agentProfilesByName(state.Profiles)
	for _, name := range utilityAgents {
		profile, found := profilesByName[strings.ToLower(strings.TrimSpace(name))]
		if !found {
			preview.StaleInheritedAgents = append(preview.StaleInheritedAgents, name)
			preview.OutOfSyncAgents = append(preview.OutOfSyncAgents, name)
			continue
		}
		if agentProfileInheritsModel(profile) {
			preview.InheritingAgents = append(preview.InheritingAgents, name)
			preview.StaleInheritedAgents = append(preview.StaleInheritedAgents, name)
			preview.OutOfSyncAgents = append(preview.OutOfSyncAgents, name)
			continue
		}
	}
	preview.OutOfSyncAgents = uniqueNamesInOrder(preview.OutOfSyncAgents)
	preview.InheritingAgents = uniqueNamesInOrder(preview.InheritingAgents)
	preview.StaleInheritedAgents = uniqueNamesInOrder(preview.StaleInheritedAgents)
	preview.UtilityBaselineAgents, preview.CustomUtilityAgents = classifyUtilityAgents(utilityAgents, profilesByName)
	return preview
}

func (s *Server) applyProviderDefaultsToBuiltIns(state agentruntime.State) (agentruntime.State, error) {
	if s == nil {
		return state, nil
	}
	providerID, providerDefaults, ok := s.resolveAgentProviderDefaults()
	if !ok {
		return state, nil
	}
	return s.applyUtilityAIToBuiltIns(state, providerID, providerDefaults.UtilityModel, providerDefaults.UtilityThinking, false)
}

func (s *Server) applyUtilityAIToBuiltIns(state agentruntime.State, utilityProvider, utilityModel, utilityThinking string, overwriteExplicit bool) (agentruntime.State, error) {
	if s == nil || s.agents == nil {
		return state, nil
	}
	utilityProvider = strings.ToLower(strings.TrimSpace(utilityProvider))
	utilityModel = strings.TrimSpace(utilityModel)
	utilityThinking = strings.TrimSpace(utilityThinking)
	if strings.EqualFold(utilityThinking, "off") {
		utilityThinking = ""
	}
	if utilityProvider == "" || utilityModel == "" {
		return state, nil
	}
	profilesByName := agentProfilesByName(state.Profiles)
	baselineAgents, _ := classifyUtilityAgents(builtinUtilityAgentNames(), profilesByName)
	baselineKeys := make(map[string]struct{}, len(baselineAgents))
	for _, name := range baselineAgents {
		key := strings.ToLower(strings.TrimSpace(name))
		if key != "" {
			baselineKeys[key] = struct{}{}
		}
	}
	updated := false
	for _, name := range builtinUtilityAgentNames() {
		key := strings.ToLower(strings.TrimSpace(name))
		if !overwriteExplicit {
			if _, ok := baselineKeys[key]; !ok {
				continue
			}
		}
		profile, found := profilesByName[key]
		if !found {
			defaultProfile, ok := agentruntime.DefaultProfileByName(name)
			if !ok {
				continue
			}
			profile = defaultProfile
		}
		enabled := profile.Enabled
		_, _, event, err := s.agents.Upsert(agentruntime.UpsertInput{
			Name:                profile.Name,
			Mode:                profile.Mode,
			Description:         profile.Description,
			Provider:            utilityProvider,
			ProviderSet:         true,
			Model:               utilityModel,
			ModelSet:            true,
			Thinking:            utilityThinking,
			ThinkingSet:         true,
			Prompt:              profile.Prompt,
			ExecutionSetting:    profile.ExecutionSetting,
			ExitPlanModeEnabled: pebblestore.CloneBoolPtr(profile.ExitPlanModeEnabled),
			ToolScope:           pebblestore.CloneAgentToolScope(profile.ToolScope),
			ToolContract:        pebblestore.CloneAgentToolContract(profile.ToolContract),
			Enabled:             &enabled,
		})
		if err != nil {
			return state, err
		}
		if event != nil && s.hub != nil {
			s.hub.Publish(*event)
		}
		updated = true
		if strings.EqualFold(strings.TrimSpace(profile.Mode), agentruntime.ModeSubagent) && !strings.EqualFold(strings.TrimSpace(state.ActiveSubagent[name]), profile.Name) {
			_, _, event, err = s.agents.SetActiveSubagent(name, profile.Name)
			if err != nil {
				return state, err
			}
			if event != nil && s.hub != nil {
				s.hub.Publish(*event)
			}
			updated = true
		}
	}
	if !updated {
		return state, nil
	}
	return s.agents.ListState(2000)
}

func (s *Server) resolveAgentProviderDefaults() (string, defaults.ProviderDefaults, bool) {
	if s == nil {
		return "", defaults.ProviderDefaults{}, false
	}
	if s.model != nil {
		pref, err := s.model.GetGlobalPreference()
		if err == nil {
			providerID := strings.ToLower(strings.TrimSpace(pref.Provider))
			if providerID != "" {
				providerDefaults, ok := defaults.Lookup(providerID)
				if ok {
					return providerID, providerDefaults, true
				}
			}
		}
	}
	if s.providers != nil {
		providerID, providerDefaults, ok, err := s.resolveUtilityModelProvider("")
		if err == nil && ok {
			return providerID, providerDefaults, true
		}
	}
	return "", defaults.ProviderDefaults{}, false
}

func agentProfilesByName(profiles []pebblestore.AgentProfile) map[string]pebblestore.AgentProfile {
	profilesByName := make(map[string]pebblestore.AgentProfile, len(profiles))
	for _, profile := range profiles {
		name := strings.ToLower(strings.TrimSpace(profile.Name))
		if name != "" {
			profilesByName[name] = profile
		}
	}
	return profilesByName
}

func builtinUtilityAgentNames() []string {
	return []string{"explorer", "memory", "parallel"}
}

func utilityAgentNames(providerDefaults defaults.ProviderDefaults) []string {
	return uniqueNamesInOrder(providerDefaults.UtilitySubagents)
}

func agentProfileInheritsModel(profile pebblestore.AgentProfile) bool {
	return strings.TrimSpace(profile.Provider) == "" || strings.TrimSpace(profile.Model) == ""
}

func classifyUtilityAgents(utilityAgents []string, profilesByName map[string]pebblestore.AgentProfile) ([]string, []string) {
	type utilityGroup struct {
		Key   string
		Names []string
	}
	inherited := make([]string, 0, len(utilityAgents))
	groupsByKey := make(map[string]int, len(utilityAgents))
	groups := make([]utilityGroup, 0, len(utilityAgents))
	for _, name := range utilityAgents {
		trimmed := strings.TrimSpace(name)
		if trimmed == "" {
			continue
		}
		profile, ok := profilesByName[strings.ToLower(trimmed)]
		if !ok || agentProfileInheritsModel(profile) {
			inherited = append(inherited, trimmed)
			continue
		}
		key := utilityProfileModelKey(profile)
		idx, ok := groupsByKey[key]
		if !ok {
			idx = len(groups)
			groupsByKey[key] = idx
			groups = append(groups, utilityGroup{Key: key})
		}
		groups[idx].Names = append(groups[idx].Names, trimmed)
	}
	if len(groups) == 0 {
		return uniqueNamesInOrder(inherited), nil
	}
	if len(inherited) > 0 {
		explicit := make([]string, 0, len(utilityAgents)-len(inherited))
		for _, group := range groups {
			explicit = append(explicit, group.Names...)
		}
		return uniqueNamesInOrder(inherited), uniqueNamesInOrder(explicit)
	}
	baseline := groups[0]
	for _, group := range groups[1:] {
		if len(group.Names) > len(baseline.Names) {
			baseline = group
		}
	}
	if len(baseline.Names) <= 1 && len(groups) > 1 {
		return nil, uniqueNamesInOrder(utilityAgents)
	}
	baselineNames := append([]string(nil), inherited...)
	baselineNames = append(baselineNames, baseline.Names...)
	custom := make([]string, 0, len(utilityAgents)-len(baseline.Names))
	baselineKeys := make(map[string]struct{}, len(baselineNames))
	for _, name := range baselineNames {
		baselineKeys[strings.ToLower(strings.TrimSpace(name))] = struct{}{}
	}
	for _, name := range utilityAgents {
		trimmed := strings.TrimSpace(name)
		if trimmed == "" {
			continue
		}
		if _, ok := baselineKeys[strings.ToLower(trimmed)]; ok {
			continue
		}
		custom = append(custom, trimmed)
	}
	return uniqueNamesInOrder(baselineNames), uniqueNamesInOrder(custom)
}

func utilityProfileModelKey(profile pebblestore.AgentProfile) string {
	return strings.ToLower(strings.TrimSpace(profile.Provider)) + "\x00" + strings.TrimSpace(profile.Model) + "\x00" + strings.ToLower(strings.TrimSpace(profile.Thinking))
}

func uniqueNamesInOrder(values []string) []string {
	if len(values) == 0 {
		return nil
	}
	out := make([]string, 0, len(values))
	seen := make(map[string]struct{}, len(values))
	for _, value := range values {
		trimmed := strings.TrimSpace(value)
		key := strings.ToLower(trimmed)
		if key == "" {
			continue
		}
		if _, ok := seen[key]; ok {
			continue
		}
		seen[key] = struct{}{}
		out = append(out, trimmed)
	}
	return out
}
