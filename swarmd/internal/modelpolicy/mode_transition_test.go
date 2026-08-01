package modelpolicy

import (
	"errors"
	"strings"
	"testing"

	sessionruntime "swarm/packages/swarmd/internal/session"
	pebblestore "swarm/packages/swarmd/internal/store/pebble"
)

func TestResolveModeTransitionUsesSnapshotActionOnRepeatedExit(t *testing.T) {
	snapshot := &pebblestore.SessionModelProfileSnapshot{
		Source:             pebblestore.SessionModelProfileSourceSaved,
		UseAccountDefault:  true,
		ActionFavoriteID:   "favorite-action",
		ActionFavoriteName: "Action Favorite",
		Action: pebblestore.ModelProfileSelection{
			Provider: "CODEX", Model: "action-snapshot", Thinking: "medium", ServiceTier: "fast", ContextMode: "compact",
		},
		PlanFavoriteID:   "favorite-plan",
		PlanFavoriteName: "Plan Favorite",
		Plan:             &pebblestore.ModelProfileSelection{Provider: "codex", Model: "plan-snapshot", Thinking: "high"},
		AppliedAt:        42,
	}
	profile := pebblestore.AgentProfile{Name: "custom", RuntimeMode: pebblestore.AgentRuntimeModePlanAuto, ExitPlanModeEnabled: pebblestore.BoolPtr(true), Provider: "codex", Model: "mutable-model-v1"}
	session := pebblestore.SessionSnapshot{
		Mode:         sessionruntime.ModePlan,
		Preference:   pebblestore.ModelPreference{Provider: "codex", Model: "plan-snapshot"},
		ModelProfile: snapshot,
		Metadata:     map[string]any{"agent_name": "custom", "resolved_agent_name": "custom", "agent_profile": profile},
	}
	resolve := func(preference pebblestore.ModelPreference) (ResolvedPreference, error) {
		return ResolvedPreference{Preference: preference, ContextWindow: 200000, MaxOutputTokens: 16000}, nil
	}

	first, err := ResolveModeTransition(session, profile, sessionruntime.ModeAuto, resolve)
	if err != nil {
		t.Fatal(err)
	}
	assertSnapshotActionTransition(t, first)

	// A repeated exit must remain bound to the immutable session snapshot even
	// if mutable agent/account policy has changed since session creation.
	profile.Model = "mutable-model-v2"
	profile.Thinking = "low"
	session.Preference = pebblestore.ModelPreference{Provider: "codex", Model: "stale-plan"}
	second, err := ResolveModeTransition(session, profile, sessionruntime.ModeAuto, resolve)
	if err != nil {
		t.Fatal(err)
	}
	assertSnapshotActionTransition(t, second)
	if second.Preference != first.Preference || second.AgentModelPolicy.Preference != first.AgentModelPolicy.Preference {
		t.Fatalf("repeated exit changed immutable selection: first=%#v second=%#v", first, second)
	}
}

func TestResolveModeTransitionUsesSnapshotPlan(t *testing.T) {
	session := pebblestore.SessionSnapshot{
		Mode:       sessionruntime.ModeAuto,
		Preference: pebblestore.ModelPreference{Provider: "codex", Model: "action-snapshot"},
		ModelProfile: &pebblestore.SessionModelProfileSnapshot{
			Source:             pebblestore.SessionModelProfileSourceSaved,
			ActionFavoriteID:   "favorite-action",
			ActionFavoriteName: "Action Favorite",
			Action:             pebblestore.ModelProfileSelection{Provider: "codex", Model: "action-snapshot"},
			PlanFavoriteID:     "favorite-plan",
			PlanFavoriteName:   "Plan Favorite",
			Plan:               &pebblestore.ModelProfileSelection{Provider: "OPENAI", Model: "plan-snapshot", Thinking: "high", ContextMode: "full"},
			AppliedAt:          77,
		},
		Metadata: map[string]any{"agent_name": "swarm", "resolved_agent_name": "swarm"},
	}

	transition, err := ResolveModeTransition(session, pebblestore.AgentProfile{Name: "swarm"}, sessionruntime.ModePlan, func(preference pebblestore.ModelPreference) (ResolvedPreference, error) {
		return ResolvedPreference{Preference: preference, ContextWindow: 180000, MaxOutputTokens: 12000}, nil
	})
	if err != nil {
		t.Fatal(err)
	}
	policy := transition.AgentModelPolicy
	if transition.Mode != sessionruntime.ModePlan || transition.Preference.Provider != "openai" || transition.Preference.Model != "plan-snapshot" || transition.Preference.Thinking != "high" || transition.Preference.ContextMode != "full" || transition.Preference.UpdatedAt != 77 {
		t.Fatalf("plan transition preference = %#v", transition)
	}
	if policy.Source != "saved_model_profile" || !policy.Locked || policy.ProfileID != "favorite-plan" || policy.ProfileName != "Plan Favorite" || policy.ProfileSource != pebblestore.SessionModelProfileSourceSaved || policy.ProfileMode != sessionruntime.ModePlan || policy.Preference != transition.Preference || policy.ContextWindow != 180000 || policy.MaxOutputTokens != 12000 {
		t.Fatalf("plan transition policy = %#v", policy)
	}
}

func TestResolveModeTransitionRejectsDisabledSnapshotPlan(t *testing.T) {
	resolverCalled := false
	session := pebblestore.SessionSnapshot{
		ModelProfile: &pebblestore.SessionModelProfileSnapshot{
			Source: pebblestore.SessionModelProfileSourceSaved,
			Action: pebblestore.ModelProfileSelection{Provider: "codex", Model: "action-snapshot"},
			Plan:   nil,
		},
		Metadata: map[string]any{"agent_name": "swarm"},
	}
	activeProfile := pebblestore.AgentProfile{Name: "swarm", RuntimeMode: pebblestore.AgentRuntimeModePlanAuto, ExitPlanModeEnabled: pebblestore.BoolPtr(true), Provider: "codex", Model: "mutable-fallback"}
	_, err := ResolveModeTransition(session, activeProfile, sessionruntime.ModePlan, func(preference pebblestore.ModelPreference) (ResolvedPreference, error) {
		resolverCalled = true
		return ResolvedPreference{Preference: preference}, nil
	})
	if err == nil || !strings.Contains(err.Error(), "Plan mode disabled") {
		t.Fatalf("disabled Plan error = %v", err)
	}
	if resolverCalled {
		t.Fatal("disabled Plan unexpectedly reached model resolver")
	}
}

func TestResolveModeTransitionPreservesFlatAgentFallback(t *testing.T) {
	profile := pebblestore.AgentProfile{
		Name: "custom", RuntimeMode: pebblestore.AgentRuntimeModePlanAuto, ExitPlanModeEnabled: pebblestore.BoolPtr(true),
		Provider: "codex", Model: "fallback-model", Thinking: "medium", AutoServiceTier: "fast", ContextMode: "compact", UpdatedAt: 10,
	}
	session := pebblestore.SessionSnapshot{Preference: pebblestore.ModelPreference{Provider: "codex", Model: "old"}, Metadata: map[string]any{"agent_name": "custom"}}
	transition, err := ResolveModeTransition(session, profile, sessionruntime.ModeAuto, func(preference pebblestore.ModelPreference) (ResolvedPreference, error) {
		return ResolvedPreference{Preference: preference}, nil
	})
	if err != nil {
		t.Fatal(err)
	}
	if transition.Preference.Model != "fallback-model" || transition.Preference.ContextMode != "compact" || transition.AgentModelPolicy.Source != "agent_preset" || !transition.AgentModelPolicy.Locked {
		t.Fatalf("flat agent fallback = %#v", transition)
	}
}

func TestResolveModeTransitionFailsClosed(t *testing.T) {
	session := pebblestore.SessionSnapshot{
		ModelProfile: &pebblestore.SessionModelProfileSnapshot{Action: pebblestore.ModelProfileSelection{}},
		Metadata:     map[string]any{"agent_name": "custom"},
	}
	if _, err := ResolveModeTransition(session, pebblestore.AgentProfile{Name: "custom"}, sessionruntime.ModeAuto, func(preference pebblestore.ModelPreference) (ResolvedPreference, error) {
		return ResolvedPreference{Preference: preference}, nil
	}); err == nil {
		t.Fatal("invalid snapshot Action unexpectedly resolved")
	}

	session.ModelProfile.Action = pebblestore.ModelProfileSelection{Provider: "codex", Model: "action-snapshot"}
	if _, err := ResolveModeTransition(session, pebblestore.AgentProfile{Name: "custom"}, sessionruntime.ModeAuto, func(pebblestore.ModelPreference) (ResolvedPreference, error) {
		return ResolvedPreference{}, errors.New("catalog unavailable")
	}); err == nil {
		t.Fatal("resolver failure unexpectedly resolved")
	}
}

func assertSnapshotActionTransition(t *testing.T, transition ModeTransition) {
	t.Helper()
	policy := transition.AgentModelPolicy
	if transition.Mode != sessionruntime.ModeAuto || transition.Preference.Provider != "codex" || transition.Preference.Model != "action-snapshot" || transition.Preference.Thinking != "medium" || transition.Preference.ServiceTier != "fast" || transition.Preference.ContextMode != "compact" || transition.Preference.UpdatedAt != 42 || transition.ContextWindow != 200000 || transition.MaxOutputTokens != 16000 {
		t.Fatalf("action transition preference = %#v", transition)
	}
	if policy.Source != "saved_model_profile" || !policy.Locked || policy.ProfileID != "favorite-action" || policy.ProfileName != "Action Favorite" || policy.ProfileSource != pebblestore.SessionModelProfileSourceSaved || policy.ProfileMode != sessionruntime.ModeAuto || policy.Preference != transition.Preference {
		t.Fatalf("action transition policy = %#v", policy)
	}
}
