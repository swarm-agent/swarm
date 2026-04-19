package api

import (
	"context"
	"fmt"
	"sort"
	"strings"

	agentruntime "swarm/packages/swarmd/internal/agent"
)

type authCredentialDeleteCleanup struct {
	ProviderUnavailable     bool     `json:"provider_unavailable"`
	ClearedGlobalPreference bool     `json:"cleared_global_preference"`
	ResetAgents             []string `json:"reset_agents,omitempty"`
}

func (s *Server) cleanupProviderAfterCredentialDeletion(ctx context.Context, provider string) (authCredentialDeleteCleanup, error) {
	provider = strings.ToLower(strings.TrimSpace(provider))
	cleanup := authCredentialDeleteCleanup{}
	if provider == "" {
		return cleanup, nil
	}
	if s.providers == nil {
		return cleanup, nil
	}

	statuses, err := s.providers.ListStatuses(ctx)
	if err != nil {
		return cleanup, fmt.Errorf("list provider statuses after credential delete: %w", err)
	}

	providerUnavailable := true
	for _, status := range statuses {
		if !strings.EqualFold(strings.TrimSpace(status.ID), provider) {
			continue
		}
		providerUnavailable = !status.Ready
		break
	}
	cleanup.ProviderUnavailable = providerUnavailable
	if !providerUnavailable {
		return cleanup, nil
	}

	if s.model != nil {
		pref, err := s.model.GetGlobalPreference()
		if err != nil {
			return cleanup, fmt.Errorf("read model preference after credential delete: %w", err)
		}
		if strings.EqualFold(strings.TrimSpace(pref.Provider), provider) {
			_, event, err := s.model.ClearGlobalPreference()
			if err != nil {
				return cleanup, fmt.Errorf("clear model preference for provider %s: %w", provider, err)
			}
			cleanup.ClearedGlobalPreference = true
			if event != nil && s.hub != nil {
				s.hub.Publish(*event)
			}
		}
	}

	if s.agents != nil {
		state, err := s.agents.ListState(2000)
		if err != nil {
			return cleanup, fmt.Errorf("list agents after credential delete: %w", err)
		}
		for _, profile := range state.Profiles {
			if !strings.EqualFold(strings.TrimSpace(profile.Provider), provider) {
				continue
			}
			_, _, event, err := s.agents.Upsert(agentruntime.UpsertInput{
				Name:        profile.Name,
				Provider:    "",
				Model:       "",
				Thinking:    "",
				ProviderSet: true,
				ModelSet:    true,
				ThinkingSet: true,
			})
			if err != nil {
				return cleanup, fmt.Errorf("reset agent %s to inherit after credential delete: %w", profile.Name, err)
			}
			cleanup.ResetAgents = append(cleanup.ResetAgents, profile.Name)
			if event != nil && s.hub != nil {
				s.hub.Publish(*event)
			}
		}
		sort.Strings(cleanup.ResetAgents)
	}

	return cleanup, nil
}
