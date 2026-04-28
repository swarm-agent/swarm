package api

import (
	"context"
	"fmt"
	"sort"
	"strings"

	agentruntime "swarm/packages/swarmd/internal/agent"
	"swarm/packages/swarmd/internal/auth"
	"swarm/packages/swarmd/internal/provider/defaults"
)

func (s *Server) applyUtilityModelDefaults(preferredProvider string) (*auth.AutoDefaultsStatus, error) {
	if s == nil || s.model == nil || s.agents == nil || s.providers == nil {
		return nil, nil
	}

	providerID, providerDefaults, ok, err := s.resolveUtilityModelProvider(preferredProvider)
	if err != nil {
		return nil, err
	}
	if !ok {
		return nil, nil
	}

	out := &auth.AutoDefaultsStatus{
		Provider:        providerID,
		Model:           providerDefaults.PrimaryModel,
		Thinking:        providerDefaults.PrimaryThinking,
		UtilityProvider: providerID,
		UtilityModel:    providerDefaults.UtilityModel,
		UtilityThinking: providerDefaults.UtilityThinking,
	}

	pref, err := s.model.GetGlobalPreference()
	if err != nil {
		return nil, fmt.Errorf("read model preference: %w", err)
	}
	firstProviderOnboarding := strings.TrimSpace(pref.Provider) == ""
	if firstProviderOnboarding {
		_, event, err := s.model.SetGlobalPreference(providerID, providerDefaults.PrimaryModel, providerDefaults.PrimaryThinking)
		if err != nil {
			return nil, fmt.Errorf("set global model default: %w", err)
		}
		if event != nil && s.hub != nil {
			s.hub.Publish(*event)
		}
		out.Applied = true
		out.GlobalModel = true
	}

	state, err := s.agents.ListState(2000)
	if err != nil {
		return nil, fmt.Errorf("list agent state: %w", err)
	}

	assignments := make(map[string]struct{}, len(builtinUtilityAgentNames()))
	for _, name := range builtinUtilityAgentNames() {
		if normalized := strings.ToLower(strings.TrimSpace(name)); normalized != "" {
			assignments[normalized] = struct{}{}
		}
	}
	if len(assignments) == 0 {
		return nil, nil
	}
	agentsSeen := make(map[string]struct{}, len(assignments))
	subagentsSeen := make(map[string]struct{}, len(assignments))
	if firstProviderOnboarding {
		state, err = s.applyUtilityAIToBuiltIns(state, providerID, providerDefaults.UtilityModel, providerDefaults.UtilityThinking, false)
		if err != nil {
			return nil, fmt.Errorf("set utility AI defaults: %w", err)
		}
		for _, profile := range state.Profiles {
			name := strings.ToLower(strings.TrimSpace(profile.Name))
			if name == "" {
				continue
			}
			if _, target := assignments[name]; !target {
				continue
			}
			agentsSeen[name] = struct{}{}
			if strings.EqualFold(strings.TrimSpace(profile.Mode), agentruntime.ModeSubagent) {
				subagentsSeen[name] = struct{}{}
			}
		}
		if len(agentsSeen) > 0 {
			out.Applied = true
		}
	}

	if !out.Applied {
		return nil, nil
	}
	out.Agents = sortedKeys(agentsSeen)
	out.Subagents = sortedKeys(subagentsSeen)
	return out, nil
}

func (s *Server) resolveUtilityModelProvider(preferredProvider string) (providerID string, providerDefaults defaults.ProviderDefaults, ok bool, err error) {
	statuses, err := s.providers.ListStatuses(context.Background())
	if err != nil {
		return "", defaults.ProviderDefaults{}, false, fmt.Errorf("list provider statuses: %w", err)
	}

	preferredProvider = strings.ToLower(strings.TrimSpace(preferredProvider))
	if preferredProvider != "" {
		for _, status := range statuses {
			id := strings.ToLower(strings.TrimSpace(status.ID))
			if id != preferredProvider || !status.Runnable {
				continue
			}
			providerDefaults, ok := defaults.Lookup(id)
			if !ok || strings.TrimSpace(providerDefaults.PrimaryModel) == "" || strings.TrimSpace(providerDefaults.UtilityModel) == "" {
				continue
			}
			return id, providerDefaults, true, nil
		}
	}

	for _, status := range statuses {
		id := strings.ToLower(strings.TrimSpace(status.ID))
		if id == "" || !status.Runnable {
			continue
		}
		providerDefaults, ok := defaults.Lookup(id)
		if !ok || strings.TrimSpace(providerDefaults.PrimaryModel) == "" || strings.TrimSpace(providerDefaults.UtilityModel) == "" {
			continue
		}
		return id, providerDefaults, true, nil
	}
	return "", defaults.ProviderDefaults{}, false, nil
}

func sortedKeys(values map[string]struct{}) []string {
	if len(values) == 0 {
		return nil
	}
	out := make([]string, 0, len(values))
	for value := range values {
		if strings.TrimSpace(value) != "" {
			out = append(out, strings.TrimSpace(value))
		}
	}
	sort.Strings(out)
	return out
}
