package api

import (
	"strings"

	agentruntime "swarm/packages/swarmd/internal/agent"
	"swarm/packages/swarmd/internal/provider/defaults"
	pebblestore "swarm/packages/swarmd/internal/store/pebble"
)

type providerDefaultsPreviewResponse struct {
	Provider         string   `json:"provider,omitempty"`
	PrimaryAgent     string   `json:"primary_agent,omitempty"`
	PrimaryModel     string   `json:"primary_model,omitempty"`
	PrimaryThinking  string   `json:"primary_thinking,omitempty"`
	UtilityModel     string   `json:"utility_model,omitempty"`
	UtilityThinking  string   `json:"utility_thinking,omitempty"`
	UtilityAgents    []string `json:"utility_agents,omitempty"`
	AffectedAgents   []string `json:"affected_agents,omitempty"`
	OutOfSyncAgents  []string `json:"out_of_sync_agents,omitempty"`
	InheritingAgents []string `json:"inheriting_agents,omitempty"`
}

func (s *Server) providerDefaultsPreviewForState(state agentruntime.State) *providerDefaultsPreviewResponse {
	providerID, providerDefaults, ok := s.resolveAgentProviderDefaults()
	if !ok {
		return nil
	}
	preview := &providerDefaultsPreviewResponse{
		Provider:         providerID,
		PrimaryAgent:     "swarm",
		PrimaryModel:     strings.TrimSpace(providerDefaults.PrimaryModel),
		PrimaryThinking:  strings.TrimSpace(providerDefaults.PrimaryThinking),
		UtilityModel:     strings.TrimSpace(providerDefaults.UtilityModel),
		UtilityThinking:  strings.TrimSpace(providerDefaults.UtilityThinking),
		UtilityAgents:    append([]string(nil), providerDefaults.UtilitySubagents...),
		AffectedAgents:   append([]string{"swarm"}, providerDefaults.UtilitySubagents...),
		OutOfSyncAgents:  nil,
		InheritingAgents: nil,
	}
	profilesByName := make(map[string]pebblestore.AgentProfile, len(state.Profiles))
	for _, profile := range state.Profiles {
		name := strings.ToLower(strings.TrimSpace(profile.Name))
		if name != "" {
			profilesByName[name] = profile
		}
	}
	for _, name := range providerDefaults.UtilitySubagents {
		profile, found := profilesByName[strings.ToLower(strings.TrimSpace(name))]
		if !found {
			preview.OutOfSyncAgents = append(preview.OutOfSyncAgents, name)
			continue
		}
		inherits := strings.TrimSpace(profile.Provider) == "" || strings.TrimSpace(profile.Model) == ""
		if inherits {
			preview.InheritingAgents = append(preview.InheritingAgents, name)
		}
		matches := strings.EqualFold(strings.TrimSpace(profile.Mode), agentruntime.ModeSubagent) &&
			profile.Enabled &&
			strings.EqualFold(strings.TrimSpace(profile.Provider), providerID) &&
			strings.EqualFold(strings.TrimSpace(profile.Model), strings.TrimSpace(providerDefaults.UtilityModel))
		if strings.TrimSpace(providerDefaults.UtilityThinking) != "" {
			matches = matches && strings.EqualFold(strings.TrimSpace(profile.Thinking), strings.TrimSpace(providerDefaults.UtilityThinking))
		}
		assigned := strings.EqualFold(strings.TrimSpace(state.ActiveSubagent[name]), name)
		if !matches || !assigned {
			preview.OutOfSyncAgents = append(preview.OutOfSyncAgents, name)
		}
	}
	preview.OutOfSyncAgents = uniqueNamesInOrder(preview.OutOfSyncAgents)
	preview.InheritingAgents = uniqueNamesInOrder(preview.InheritingAgents)
	return preview
}

func (s *Server) applyProviderDefaultsToBuiltIns(state agentruntime.State) (agentruntime.State, error) {
	if s == nil || s.agents == nil {
		return state, nil
	}
	providerID, providerDefaults, ok := s.resolveAgentProviderDefaults()
	if !ok {
		return state, nil
	}
	profilesByName := make(map[string]pebblestore.AgentProfile, len(state.Profiles))
	for _, profile := range state.Profiles {
		name := strings.ToLower(strings.TrimSpace(profile.Name))
		if name != "" {
			profilesByName[name] = profile
		}
	}
	updated := false
	for _, name := range providerDefaults.UtilitySubagents {
		profile, found := profilesByName[strings.ToLower(strings.TrimSpace(name))]
		if !found {
			continue
		}
		enabled := profile.Enabled
		_, _, event, err := s.agents.Upsert(agentruntime.UpsertInput{
			Name:                profile.Name,
			Mode:                profile.Mode,
			Description:         profile.Description,
			Provider:            providerID,
			ProviderSet:         true,
			Model:               providerDefaults.UtilityModel,
			ModelSet:            true,
			Thinking:            providerDefaults.UtilityThinking,
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
		if !strings.EqualFold(strings.TrimSpace(state.ActiveSubagent[name]), profile.Name) {
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
