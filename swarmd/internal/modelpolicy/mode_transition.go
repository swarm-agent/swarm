package modelpolicy

import (
	"encoding/json"
	"errors"
	"fmt"
	"strings"

	agentruntime "swarm/packages/swarmd/internal/agent"
	sessionruntime "swarm/packages/swarmd/internal/session"
	pebblestore "swarm/packages/swarmd/internal/store/pebble"
)

// AgentModelPolicy is the canonical model-policy payload committed with a mode
// transition. It intentionally matches the V3 API and realtime contract.
type AgentModelPolicy struct {
	AgentName       string                      `json:"agent_name"`
	ResolvedAgent   string                      `json:"resolved_agent_name"`
	Source          string                      `json:"source"`
	Locked          bool                        `json:"locked"`
	Reason          string                      `json:"reason,omitempty"`
	Preference      pebblestore.ModelPreference `json:"preference"`
	ContextWindow   int                         `json:"context_window"`
	MaxOutputTokens int                         `json:"max_output_tokens"`
	ProfileID       string                      `json:"profile_id,omitempty"`
	ProfileName     string                      `json:"profile_name,omitempty"`
	ProfileSource   string                      `json:"profile_source,omitempty"`
	ProfileMode     string                      `json:"profile_mode,omitempty"`
}

type ResolvedPreference struct {
	Preference      pebblestore.ModelPreference
	ContextWindow   int
	MaxOutputTokens int
}

type PreferenceResolver func(pebblestore.ModelPreference) (ResolvedPreference, error)

type ModeTransition struct {
	Mode             string
	ActiveProfile    pebblestore.AgentProfile
	Preference       pebblestore.ModelPreference
	ContextWindow    int
	MaxOutputTokens  int
	AgentModelPolicy AgentModelPolicy
}

// ResolveModeTransition resolves the complete effective model policy for the
// target mode. Callers must do this before committing a mode transition so a
// resolution error cannot leave the session in the new mode with the old model.
func ResolveModeTransition(session pebblestore.SessionSnapshot, activeProfile pebblestore.AgentProfile, targetMode string, resolve PreferenceResolver) (ModeTransition, error) {
	targetMode = sessionruntime.NormalizeMode(targetMode)
	if targetMode != sessionruntime.ModeAuto && targetMode != sessionruntime.ModePlan {
		return ModeTransition{}, fmt.Errorf("unsupported model-policy mode %q", targetMode)
	}
	if resolve == nil {
		return ModeTransition{}, errors.New("model preference resolver is not configured")
	}

	profile := activeProfile
	if strings.TrimSpace(profile.Name) == "" {
		decoded, err := agentProfileFromMetadata(session.Metadata)
		if err != nil {
			return ModeTransition{}, err
		}
		profile = decoded
	} else if strings.EqualFold(strings.TrimSpace(profile.Name), agentruntime.SwarmAgentID) {
		if configured, err := agentProfileFromMetadata(session.Metadata); err == nil && strings.EqualFold(strings.TrimSpace(configured.Name), agentruntime.SwarmAgentID) && agentProfileHasModePolicy(configured, targetMode) {
			profile = configured
		}
	}

	agentName := strings.TrimSpace(profile.Name)
	if agentName == "" {
		agentName = metadataString(session.Metadata, "agent_name")
	}
	resolvedAgent := metadataString(session.Metadata, "resolved_agent_name")
	if resolvedAgent == "" {
		resolvedAgent = agentName
	}
	policy := AgentModelPolicy{
		AgentName:     agentName,
		ResolvedAgent: resolvedAgent,
		Source:        "default",
	}

	preference := session.Preference
	if session.ModelProfile != nil {
		selected, err := modelProfilePreference(*session.ModelProfile, targetMode)
		if err != nil {
			return ModeTransition{}, err
		}
		preference = selected
		policy.Source = "model_profile"
		switch session.ModelProfile.Source {
		case pebblestore.SessionModelProfileSourceSaved:
			policy.Source = "saved_model_profile"
		case pebblestore.SessionModelProfileSourceTemporary:
			policy.Source = "temporary_model_profile"
		}
		policy.Locked = true
		policy.Reason = "Session model profile controls the model; clear or replace the session profile to change it."
		policy.ProfileSource = strings.TrimSpace(session.ModelProfile.Source)
		policy.ProfileMode = targetMode
		if targetMode == sessionruntime.ModePlan {
			policy.ProfileID = strings.TrimSpace(session.ModelProfile.PlanFavoriteID)
			policy.ProfileName = strings.TrimSpace(session.ModelProfile.PlanFavoriteName)
		} else {
			policy.ProfileID = strings.TrimSpace(session.ModelProfile.ActionFavoriteID)
			policy.ProfileName = strings.TrimSpace(session.ModelProfile.ActionFavoriteName)
		}
	} else if !strings.EqualFold(agentName, agentruntime.SwarmAgentID) || agentProfileHasModePolicy(profile, targetMode) {
		var ok bool
		preference, policy.Source, policy.Reason, ok = agentPreferenceForMode(preference, profile, targetMode)
		if ok {
			policy.Locked = true
		}
	}

	preference.Provider = strings.ToLower(strings.TrimSpace(preference.Provider))
	preference.Model = strings.TrimSpace(preference.Model)
	if preference.Provider == "" || preference.Model == "" {
		return ModeTransition{}, fmt.Errorf("configured %s model policy for agent %q has no provider/model", targetMode, agentName)
	}
	resolved, err := resolve(preference)
	if err != nil {
		return ModeTransition{}, fmt.Errorf("resolve configured %s model policy for agent %q: %w", targetMode, agentName, err)
	}
	resolved.Preference.Provider = strings.ToLower(strings.TrimSpace(resolved.Preference.Provider))
	resolved.Preference.Model = strings.TrimSpace(resolved.Preference.Model)
	if resolved.Preference.Provider == "" || resolved.Preference.Model == "" {
		return ModeTransition{}, fmt.Errorf("resolved %s model policy for agent %q has no provider/model", targetMode, agentName)
	}
	policy.Preference = resolved.Preference
	policy.ContextWindow = resolved.ContextWindow
	policy.MaxOutputTokens = resolved.MaxOutputTokens
	return ModeTransition{
		Mode:             targetMode,
		ActiveProfile:    profile,
		Preference:       resolved.Preference,
		ContextWindow:    resolved.ContextWindow,
		MaxOutputTokens:  resolved.MaxOutputTokens,
		AgentModelPolicy: policy,
	}, nil
}

func agentProfileHasModePolicy(profile pebblestore.AgentProfile, _ string) bool {
	return strings.TrimSpace(profile.Provider) != "" && strings.TrimSpace(profile.Model) != ""
}

func agentPreferenceForMode(base pebblestore.ModelPreference, profile pebblestore.AgentProfile, _ string) (pebblestore.ModelPreference, string, string, bool) {
	provider := strings.ToLower(strings.TrimSpace(profile.Provider))
	model := strings.TrimSpace(profile.Model)
	if provider == "" || model == "" {
		return base, "default", "", false
	}
	base.Provider, base.Model = provider, model
	if thinking := strings.TrimSpace(profile.Thinking); thinking != "" {
		base.Thinking = thinking
	}
	base.ServiceTier = strings.TrimSpace(profile.AutoServiceTier)
	if contextMode := strings.TrimSpace(profile.ContextMode); contextMode != "" {
		base.ContextMode = contextMode
	}
	base.UpdatedAt = profile.UpdatedAt
	return base, "agent_preset", "Agent model is set in agent settings; update the agent model in agent settings to choose a different model.", true
}

func modelProfilePreference(profile pebblestore.SessionModelProfileSnapshot, mode string) (pebblestore.ModelPreference, error) {
	selection := &profile.Action
	selectionName := strings.TrimSpace(profile.ActionFavoriteName)
	if mode == sessionruntime.ModePlan {
		selection = profile.Plan
		selectionName = strings.TrimSpace(profile.PlanFavoriteName)
		if selection == nil {
			return pebblestore.ModelPreference{}, errors.New("session model profile has Plan mode disabled")
		}
	}
	if selection == nil || strings.TrimSpace(selection.Provider) == "" || strings.TrimSpace(selection.Model) == "" {
		if selectionName == "" {
			selectionName = mode
		}
		return pebblestore.ModelPreference{}, fmt.Errorf("session model profile selection %q has no provider/model", selectionName)
	}
	return pebblestore.ModelPreference{
		Provider:    strings.ToLower(strings.TrimSpace(selection.Provider)),
		Model:       strings.TrimSpace(selection.Model),
		Thinking:    strings.TrimSpace(selection.Thinking),
		ServiceTier: strings.TrimSpace(selection.ServiceTier),
		ContextMode: strings.TrimSpace(selection.ContextMode),
		UpdatedAt:   profile.AppliedAt,
	}, nil
}

func agentProfileFromMetadata(metadata map[string]any) (pebblestore.AgentProfile, error) {
	raw, ok := metadata["agent_profile"]
	if !ok || raw == nil {
		return pebblestore.AgentProfile{}, errors.New("session metadata is missing agent_profile")
	}
	body, err := json.Marshal(raw)
	if err != nil {
		return pebblestore.AgentProfile{}, err
	}
	var profile pebblestore.AgentProfile
	if err := json.Unmarshal(body, &profile); err != nil {
		return pebblestore.AgentProfile{}, err
	}
	return profile, nil
}

func metadataString(metadata map[string]any, key string) string {
	if metadata == nil {
		return ""
	}
	return strings.TrimSpace(fmt.Sprint(metadata[key]))
}
