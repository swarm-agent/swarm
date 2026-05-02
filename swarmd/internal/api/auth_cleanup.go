package api

import (
	"context"
	"fmt"
	"sort"
	"strings"

	agentruntime "swarm/packages/swarmd/internal/agent"
	sessionruntime "swarm/packages/swarmd/internal/session"
)

type authCredentialDeleteCleanup struct {
	ProviderUnavailable     bool     `json:"provider_unavailable"`
	ClearedGlobalPreference bool     `json:"cleared_global_preference"`
	ClearedSessionCount     int      `json:"cleared_session_count,omitempty"`
	ClearedSessionIDs       []string `json:"cleared_session_ids,omitempty"`
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

	if s.sessions != nil {
		sessions, err := s.sessions.ListSessions(10000)
		if err != nil {
			return cleanup, fmt.Errorf("list sessions after credential delete: %w", err)
		}
		for _, session := range sessions {
			if !strings.EqualFold(strings.TrimSpace(session.Preference.Provider), provider) {
				continue
			}
			pref, event, err := s.sessions.SetSessionPreference(strings.TrimSpace(session.ID), sessionruntime.SessionPreferenceUpdate{
				Provider:    stringPtr(""),
				Model:       stringPtr(""),
				Thinking:    stringPtr(""),
				ServiceTier: stringPtr(""),
				ContextMode: stringPtr(""),
			})
			if err != nil {
				return cleanup, fmt.Errorf("clear session preference for %s after credential delete: %w", session.ID, err)
			}
			if strings.TrimSpace(pref.Provider) == "" && strings.TrimSpace(pref.Model) == "" {
				cleanup.ClearedSessionIDs = append(cleanup.ClearedSessionIDs, strings.TrimSpace(session.ID))
			}
			if event != nil && s.hub != nil {
				s.hub.Publish(*event)
			}
		}
		sort.Strings(cleanup.ClearedSessionIDs)
		cleanup.ClearedSessionCount = len(cleanup.ClearedSessionIDs)
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
			_, _, _, err := s.agents.Upsert(agentruntime.UpsertInput{
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
		}
		sort.Strings(cleanup.ResetAgents)
	}

	return cleanup, nil
}

func stringPtr(value string) *string {
	return &value
}
