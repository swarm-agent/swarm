package api

import (
	"context"
	"path/filepath"
	"testing"

	"swarm/packages/swarmd/internal/identity"
	"swarm/packages/swarmd/internal/modelprofile"
	pebblestore "swarm/packages/swarmd/internal/store/pebble"
)

func TestSessionsV3ModelProfileChoiceSnapshotsSavedAndTemporaryProfiles(t *testing.T) {
	store, err := pebblestore.Open(filepath.Join(t.TempDir(), "profiles.pebble"))
	if err != nil {
		t.Fatalf("open store: %v", err)
	}
	defer store.Close()
	service := modelprofile.NewService(pebblestore.NewModelProfileStore(store))
	server := &Server{modelProfiles: service}
	principal := identity.Principal{Type: identity.PrincipalTypeUser, UserID: "user", AccountScopeID: "account"}
	ctx := identity.ContextWithPrincipal(context.Background(), principal)
	selection := pebblestore.ModelProfileSelection{Provider: "openai", Model: "saved-model", Thinking: "high", ContextMode: "full"}
	created, err := service.Create(ctx, modelprofile.Input{Name: "Saved", ModelMode: pebblestore.ModelProfileModeSingle, Single: &selection})
	if err != nil {
		t.Fatalf("create profile: %v", err)
	}

	saved, err := server.resolveSessionsV3ModelProfileChoice(ctx, nil, true, 10)
	if err != nil {
		t.Fatalf("resolve default: %v", err)
	}
	if saved == nil || saved.Source != pebblestore.SessionModelProfileSourceSaved || saved.SavedProfileID != created.ProfileID || saved.Single.Model != "saved-model" || saved.Single.ContextMode != "full" {
		t.Fatalf("saved snapshot = %+v", saved)
	}
	selection.Model = "edited-model"
	if saved.Single.Model != "saved-model" {
		t.Fatalf("saved snapshot mutated with source selection: %+v", saved)
	}
	updatedSelection := pebblestore.ModelProfileSelection{Provider: "openai", Model: "updated-model", Thinking: "medium", ContextMode: "compact"}
	if _, err := service.Update(ctx, created.ProfileID, modelprofile.Input{Name: "Renamed", ModelMode: pebblestore.ModelProfileModeSingle, Single: &updatedSelection}); err != nil {
		t.Fatalf("update saved profile: %v", err)
	}
	if deleted, err := service.Delete(ctx, created.ProfileID); err != nil || !deleted {
		t.Fatalf("delete saved profile: deleted=%t err=%v", deleted, err)
	}
	if saved.Name != "Saved" || saved.Single.Model != "saved-model" || saved.Single.ContextMode != "full" {
		t.Fatalf("session snapshot changed after saved profile update/delete: %+v", saved)
	}

	temporary, err := server.resolveSessionsV3ModelProfileChoice(ctx, &sessionsV3ModelProfileChoice{Temporary: &sessionsV3ModelProfileInline{Name: "Scratch", ModelMode: pebblestore.ModelProfileModeSplit, Plan: &pebblestore.ModelProfileSelection{Provider: "openai", Model: "plan-model", Thinking: "high"}, Auto: &pebblestore.ModelProfileSelection{Provider: "openai", Model: "action-model", Thinking: "medium"}}}, false, 20)
	if err != nil {
		t.Fatalf("resolve temporary: %v", err)
	}
	if temporary == nil || temporary.Source != pebblestore.SessionModelProfileSourceTemporary || temporary.SavedProfileID != "" || temporary.Plan.Model != "plan-model" || temporary.Auto.Model != "action-model" {
		t.Fatalf("temporary snapshot = %+v", temporary)
	}
	state, err := service.ListState(ctx)
	if err != nil || len(state.Profiles) != 0 {
		t.Fatalf("temporary profile was saved: profiles=%d err=%v", len(state.Profiles), err)
	}
}

func TestSessionsV3ModelProfileMetadataPersistsExactSavedProfileIdentity(t *testing.T) {
	profile := &pebblestore.SessionModelProfileSnapshot{
		Source:         pebblestore.SessionModelProfileSourceSaved,
		SavedProfileID: "mp_exact",
		Name:           "Exact",
		ModelMode:      pebblestore.ModelProfileModeSingle,
		Single:         &pebblestore.ModelProfileSelection{Provider: "openai", Model: "same-model"},
	}
	metadata := sessionsV3ModelProfileMetadata(map[string]any{"agent_name": "swarm"}, profile)
	stored, ok := metadata["model_profile"].(pebblestore.SessionModelProfileSnapshot)
	if !ok || stored.SavedProfileID != "mp_exact" || stored.Name != "Exact" {
		t.Fatalf("model profile metadata = %#v", metadata["model_profile"])
	}
	profile.SavedProfileID = "mutated"
	profile.Single.Model = "mutated"
	if stored.SavedProfileID != "mp_exact" || stored.Single.Model != "same-model" {
		t.Fatalf("metadata did not snapshot profile identity: %+v", stored)
	}
	cleared := sessionsV3ModelProfileMetadata(metadata, nil)
	if _, ok := cleared["model_profile"]; ok {
		t.Fatalf("cleared metadata retained model_profile: %+v", cleared)
	}
	if cleared["agent_name"] != "swarm" {
		t.Fatalf("clearing model profile removed unrelated metadata: %+v", cleared)
	}
}

func TestSessionsV3ProfilePreferenceUsesCurrentMode(t *testing.T) {
	session := pebblestore.SessionSnapshot{Mode: "plan", ModelProfile: &pebblestore.SessionModelProfileSnapshot{ModelMode: pebblestore.ModelProfileModeSplit, AppliedAt: 7, Plan: &pebblestore.ModelProfileSelection{Provider: "openai", Model: "plan", Thinking: "high", ContextMode: "full"}, Auto: &pebblestore.ModelProfileSelection{Provider: "openai", Model: "action", Thinking: "medium", ServiceTier: "fast"}}}
	plan, ok := sessionsV3ProfilePreference(session)
	if !ok || plan.Model != "plan" || plan.ContextMode != "full" {
		t.Fatalf("plan preference = %+v ok=%t", plan, ok)
	}
	session.Mode = "auto"
	action, ok := sessionsV3ProfilePreference(session)
	if !ok || action.Model != "action" || action.ServiceTier != "fast" {
		t.Fatalf("action preference = %+v ok=%t", action, ok)
	}
}
