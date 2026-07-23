package modelpolicy

import (
	"errors"
	"testing"

	sessionruntime "swarm/packages/swarmd/internal/session"
	pebblestore "swarm/packages/swarmd/internal/store/pebble"
)

func TestResolveModeTransitionUsesCurrentSplitAgentPolicyOnRepeatedExit(t *testing.T) {
	profile := pebblestore.AgentProfile{
		Name: "custom", RuntimeMode: pebblestore.AgentRuntimeModePlanAuto, ExitPlanModeEnabled: pebblestore.BoolPtr(true), ModelMode: "split",
		PlanProvider: "codex", PlanModel: "plan-v1", PlanThinking: "high",
		AutoProvider: "codex", AutoModel: "auto-v1", AutoThinking: "medium", AutoServiceTier: "fast", UpdatedAt: 10,
	}
	session := pebblestore.SessionSnapshot{Mode: sessionruntime.ModePlan, Preference: pebblestore.ModelPreference{Provider: "codex", Model: "plan-v1"}, Metadata: map[string]any{"agent_name": "custom", "resolved_agent_name": "custom", "agent_profile": profile}}
	resolve := func(preference pebblestore.ModelPreference) (ResolvedPreference, error) {
		return ResolvedPreference{Preference: preference, ContextWindow: 200000, MaxOutputTokens: 16000}, nil
	}

	first, err := ResolveModeTransition(session, profile, sessionruntime.ModeAuto, resolve)
	if err != nil {
		t.Fatal(err)
	}
	if first.Preference.Model != "auto-v1" || first.AgentModelPolicy.Source != "agent_auto_preset" || !first.AgentModelPolicy.Locked || first.AgentModelPolicy.Preference != first.Preference || first.ContextWindow != 200000 || first.MaxOutputTokens != 16000 {
		t.Fatalf("first auto transition = %#v", first)
	}

	profile.AutoModel, profile.AutoThinking, profile.AutoServiceTier, profile.UpdatedAt = "auto-v2", "low", "flex", 20
	session.Mode = sessionruntime.ModePlan
	session.Preference = pebblestore.ModelPreference{Provider: "codex", Model: "plan-v1"}
	second, err := ResolveModeTransition(session, profile, sessionruntime.ModeAuto, resolve)
	if err != nil {
		t.Fatal(err)
	}
	if second.Preference.Model != "auto-v2" || second.Preference.Thinking != "low" || second.Preference.ServiceTier != "flex" || second.Preference.UpdatedAt != 20 || second.ActiveProfile.AutoModel != "auto-v2" {
		t.Fatalf("second auto transition did not use current profile = %#v", second)
	}
}

func TestResolveModeTransitionUsesStoredSwarmPlanAutoPolicyWhenCompiledIdentityIsModelNeutral(t *testing.T) {
	configured := pebblestore.AgentProfile{
		Name: "swarm", RuntimeMode: pebblestore.AgentRuntimeModePlanAuto, ExitPlanModeEnabled: pebblestore.BoolPtr(true), ModelMode: "split",
		PlanProvider: "codex", PlanModel: "plan-model", AutoProvider: "codex", AutoModel: "action-model", AutoThinking: "high", UpdatedAt: 11,
	}
	session := pebblestore.SessionSnapshot{
		Mode: sessionruntime.ModePlan, Preference: pebblestore.ModelPreference{Provider: "codex", Model: "plan-model"},
		Metadata: map[string]any{"agent_name": "swarm", "resolved_agent_name": "swarm", "agent_profile": configured},
	}
	compiled := pebblestore.AgentProfile{Name: "swarm", RuntimeMode: pebblestore.AgentRuntimeModePlanAuto, ExitPlanModeEnabled: pebblestore.BoolPtr(true)}
	transition, err := ResolveModeTransition(session, compiled, sessionruntime.ModeAuto, func(preference pebblestore.ModelPreference) (ResolvedPreference, error) {
		return ResolvedPreference{Preference: preference, ContextWindow: 180000}, nil
	})
	if err != nil {
		t.Fatal(err)
	}
	if transition.Preference.Model != "action-model" || transition.AgentModelPolicy.Source != "agent_auto_preset" || !transition.AgentModelPolicy.Locked || transition.ActiveProfile.AutoModel != "action-model" {
		t.Fatalf("stored Swarm action policy = %#v", transition)
	}
}

func TestResolveModeTransitionUsesSplitSessionModelProfile(t *testing.T) {
	session := pebblestore.SessionSnapshot{
		Mode: sessionruntime.ModePlan,
		ModelProfile: &pebblestore.SessionModelProfileSnapshot{
			Source: pebblestore.SessionModelProfileSourceSaved, SavedProfileID: "profile-1", Name: "Focused work", ModelMode: pebblestore.ModelProfileModeSplit, AppliedAt: 42,
			Plan: &pebblestore.ModelProfileSelection{Provider: "codex", Model: "plan-model", Thinking: "high"},
			Auto: &pebblestore.ModelProfileSelection{Provider: "codex", Model: "action-model", Thinking: "medium", ServiceTier: "fast"},
		},
		Metadata: map[string]any{"agent_name": "swarm", "resolved_agent_name": "swarm", "agent_profile": pebblestore.AgentProfile{Name: "swarm"}},
	}
	transition, err := ResolveModeTransition(session, pebblestore.AgentProfile{Name: "swarm"}, sessionruntime.ModeAuto, func(preference pebblestore.ModelPreference) (ResolvedPreference, error) {
		return ResolvedPreference{Preference: preference, ContextWindow: 180000, MaxOutputTokens: 12000}, nil
	})
	if err != nil {
		t.Fatal(err)
	}
	policy := transition.AgentModelPolicy
	if transition.Preference.Model != "action-model" || policy.Source != "saved_model_profile" || !policy.Locked || policy.ProfileID != "profile-1" || policy.ProfileName != "Focused work" || policy.ProfileSource != "saved" || policy.ProfileMode != "split" || policy.Preference != transition.Preference || policy.ContextWindow != 180000 || policy.MaxOutputTokens != 12000 {
		t.Fatalf("session profile transition = %#v", transition)
	}
}

func TestResolveModeTransitionFailsClosed(t *testing.T) {
	session := pebblestore.SessionSnapshot{Metadata: map[string]any{"agent_name": "custom"}}
	profile := pebblestore.AgentProfile{Name: "custom", RuntimeMode: pebblestore.AgentRuntimeModePlanAuto, ExitPlanModeEnabled: pebblestore.BoolPtr(true), ModelMode: "split"}
	if _, err := ResolveModeTransition(session, profile, sessionruntime.ModeAuto, func(pebblestore.ModelPreference) (ResolvedPreference, error) { return ResolvedPreference{}, nil }); err == nil {
		t.Fatal("missing auto provider/model unexpectedly resolved")
	}
	profile.AutoProvider, profile.AutoModel = "codex", "action-model"
	if _, err := ResolveModeTransition(session, profile, sessionruntime.ModeAuto, func(pebblestore.ModelPreference) (ResolvedPreference, error) {
		return ResolvedPreference{}, errors.New("catalog unavailable")
	}); err == nil {
		t.Fatal("resolver failure unexpectedly resolved")
	}
}
