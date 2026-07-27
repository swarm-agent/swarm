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
		policy.ProfileID = strings.TrimSpace(session.ModelProfile.SavedProfileID)
		policy.ProfileName = strings.TrimSpace(session.ModelProfile.Name)
		policy.ProfileSource = strings.TrimSpace(session.ModelProfile.Source)
		policy.ProfileMode = strings.TrimSpace(session.ModelProfile.ModelMode)
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

func agentProfileHasModePolicy(profile pebblestore.AgentProfile, mode string) bool {
	if pebblestore.AgentModelMode(profile) == "split" && pebblestore.AgentSupportsSplitModel(profile) {
		if mode == sessionruntime.ModePlan {
			return strings.TrimSpace(profile.PlanProvider) != "" && strings.TrimSpace(profile.PlanModel) != ""
		}
		return strings.TrimSpace(profile.AutoProvider) != "" && strings.TrimSpace(profile.AutoModel) != ""
	}
	return strings.TrimSpace(profile.Provider) != "" && strings.TrimSpace(profile.Model) != ""
}

func agentPreferenceForMode(base pebblestore.ModelPreference, profile pebblestore.AgentProfile, mode string) (pebblestore.ModelPreference, string, string, bool) {
	provider := strings.ToLower(strings.TrimSpace(profile.Provider))
	model := strings.TrimSpace(profile.Model)
	thinking := strings.TrimSpace(profile.Thinking)
	serviceTier := strings.TrimSpace(profile.AutoServiceTier)
	source := "agent_preset"
	reason := "Agent model is set in agent settings; update the agent model in agent settings to choose a different model."
	if pebblestore.AgentModelMode(profile) == "split" && pebblestore.AgentSupportsSplitModel(profile) {
		if mode == sessionruntime.ModePlan {
			provider, model, thinking, serviceTier = strings.ToLower(strings.TrimSpace(profile.PlanProvider)), strings.TrimSpace(profile.PlanModel), strings.TrimSpace(profile.PlanThinking), strings.TrimSpace(profile.PlanServiceTier)
			source = "agent_plan_preset"
			reason = "Agent plan model is set in agent settings; exit plan mode uses the configured auto model."
		} else {
			provider, model, thinking, serviceTier = strings.ToLower(strings.TrimSpace(profile.AutoProvider)), strings.TrimSpace(profile.AutoModel), strings.TrimSpace(profile.AutoThinking), strings.TrimSpace(profile.AutoServiceTier)
			source = "agent_auto_preset"
			reason = "Agent auto model is set in agent settings; enter plan mode uses the configured plan model."
		}
	}
	if provider == "" || model == "" {
		return base, "default", "", false
	}
	base.Provider, base.Model = provider, model
	if thinking != "" {
		base.Thinking = thinking
	}
	base.ServiceTier = serviceTier
	base.UpdatedAt = profile.UpdatedAt
	return base, source, reason, true
}

func modelProfilePreference(profile pebblestore.SessionModelProfileSnapshot, mode string) (pebblestore.ModelPreference, error) {
	selection := profile.Single
	if profile.ModelMode == pebblestore.ModelProfileModeSplit {
		if mode == sessionruntime.ModePlan {
			selection = profile.Plan
		} else {
			selection = profile.Auto
		}
	}
	if selection == nil || strings.TrimSpace(selection.Provider) == "" || strings.TrimSpace(selection.Model) == "" {
		return pebblestore.ModelPreference{}, fmt.Errorf("session model profile %q has no %s provider/model", strings.TrimSpace(profile.Name), mode)
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
