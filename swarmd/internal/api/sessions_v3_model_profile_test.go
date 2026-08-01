package api

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"path/filepath"
	"strings"
	"testing"

	"swarm/packages/swarmd/internal/identity"
	"swarm/packages/swarmd/internal/modelprofile"
	sessionruntime "swarm/packages/swarmd/internal/session"
	pebblestore "swarm/packages/swarmd/internal/store/pebble"
)

func TestSessionsV3ModelProfileChoiceSnapshotsSavedAndTemporaryProfiles(t *testing.T) {
	store, err := pebblestore.Open(filepath.Join(t.TempDir(), "profiles.pebble"))
	if err != nil {
		t.Fatalf("open store: %v", err)
	}
	defer store.Close()
	favorites := pebblestore.NewModelProfileStore(store)
	service := modelprofile.NewService(favorites)
	swarmProfiles := modelprofile.NewSwarmService(pebblestore.NewSwarmModeSettingsStore(store), favorites)
	server := &Server{modelProfiles: service, swarmProfiles: swarmProfiles}
	principal := identity.Principal{Type: identity.PrincipalTypeUser, UserID: "user", AccountScopeID: "account"}
	ctx := identity.ContextWithPrincipal(context.Background(), principal)
	created, err := service.Create(ctx, modelprofile.Input{Name: "Saved", Provider: "openai", Model: "saved-model", Thinking: "high", ServiceTier: "priority", ContextMode: "full"})
	if err != nil {
		t.Fatalf("create action profile: %v", err)
	}
	planFavorite, err := service.Create(ctx, modelprofile.Input{Name: "Plan", Provider: "codex", Model: "plan-model", Thinking: "xhigh", ContextMode: "compact"})
	if err != nil {
		t.Fatalf("create plan profile: %v", err)
	}
	if _, err := swarmProfiles.Put(ctx, modelprofile.SwarmSettingsInput{ActionFavoriteID: created.ProfileID, PlanEnabled: true, PlanFavoriteID: planFavorite.ProfileID}); err != nil {
		t.Fatalf("configure account defaults: %v", err)
	}

	omitted, err := server.resolveSessionsV3ModelProfileChoice(ctx, nil, 10)
	if err != nil || omitted != nil {
		t.Fatalf("omitted model profile = %+v err=%v, want nil", omitted, err)
	}
	saved, err := server.resolveSessionsV3ModelProfileChoice(ctx, &sessionsV3ModelProfileChoice{UseAccountDefault: boolPtr(true)}, 10)
	if err != nil {
		t.Fatalf("resolve explicit default: %v", err)
	}
	if saved == nil || saved.Source != pebblestore.SessionModelProfileSourceSaved || saved.ActionFavoriteID != created.ProfileID || saved.ActionFavoriteName != "Saved" || !saved.UseAccountDefault || saved.Action.Model != "saved-model" || saved.Action.ServiceTier != "priority" || saved.Action.ContextMode != "full" || saved.PlanFavoriteID != planFavorite.ProfileID || saved.PlanFavoriteName != "Plan" || saved.Plan == nil || saved.Plan.Model != "plan-model" || saved.Plan.Thinking != "xhigh" || saved.AppliedAt != 10 {
		t.Fatalf("saved default snapshot = %+v", saved)
	}
	savedByID, err := server.resolveSessionsV3ModelProfileChoice(ctx, &sessionsV3ModelProfileChoice{SavedProfileID: created.ProfileID}, 11)
	if err != nil {
		t.Fatalf("resolve saved profile: %v", err)
	}
	if savedByID == nil || savedByID.ActionFavoriteID != created.ProfileID || savedByID.ActionFavoriteName != "Saved" || savedByID.UseAccountDefault || savedByID.Action.Model != "saved-model" || savedByID.Plan != nil || savedByID.AppliedAt != 11 {
		t.Fatalf("saved profile snapshot = %+v", savedByID)
	}
	if _, err := service.Update(ctx, created.ProfileID, modelprofile.Input{Name: "Renamed", Provider: "openai", Model: "updated-model", Thinking: "medium", ContextMode: "compact"}); err != nil {
		t.Fatalf("update saved profile: %v", err)
	}
	if deleted, err := service.Delete(ctx, created.ProfileID); err != nil || !deleted {
		t.Fatalf("delete saved profile: deleted=%t err=%v", deleted, err)
	}
	if saved.ActionFavoriteName != "Saved" || saved.Action.Model != "saved-model" || saved.Action.ContextMode != "full" || saved.PlanFavoriteName != "Plan" || saved.Plan.Model != "plan-model" {
		t.Fatalf("session snapshot changed after saved profile update/delete: %+v", saved)
	}

	temporary, err := server.resolveSessionsV3ModelProfileChoice(ctx, &sessionsV3ModelProfileChoice{Temporary: &sessionsV3ModelProfileInline{Name: "Scratch", Provider: "openai", Model: "temporary-model", Thinking: "high", ServiceTier: "fast", ContextMode: "compact"}}, 20)
	if err != nil {
		t.Fatalf("resolve temporary: %v", err)
	}
	if temporary == nil || temporary.Source != pebblestore.SessionModelProfileSourceTemporary || temporary.ActionFavoriteID != "" || temporary.ActionFavoriteName != "" || temporary.UseAccountDefault || temporary.Action.Provider != "openai" || temporary.Action.Model != "temporary-model" || temporary.Action.Thinking != "high" || temporary.Action.ServiceTier != "fast" || temporary.Action.ContextMode != "compact" || temporary.Plan != nil || temporary.AppliedAt != 20 {
		t.Fatalf("temporary snapshot = %+v", temporary)
	}
	state, err := service.ListState(ctx)
	if err != nil || len(state.Profiles) != 1 {
		t.Fatalf("temporary profile was saved or account favorites changed unexpectedly: profiles=%d err=%v", len(state.Profiles), err)
	}
}

func TestSessionsV3ModelProfileChoiceRejectsRemovedBundleFields(t *testing.T) {
	for _, field := range []string{"model_mode", "single", "plan", "auto"} {
		t.Run(field, func(t *testing.T) {
			body := `{"client_request_id":"request","choice":{"temporary":{"name":"Temporary","provider":"openai","model":"gpt-test","thinking":"high","` + field + `":{}}}}`
			var request sessionsV3ModelProfileApplyRequest
			err := decodeJSONBytes([]byte(body), &request)
			if err == nil || !strings.Contains(err.Error(), "unknown field") {
				t.Fatalf("decode removed field %q error = %v, want unknown field", field, err)
			}
		})
	}
}

func TestSessionsV3ModelProfileMetadataPersistsExactSavedProfileIdentity(t *testing.T) {
	profile := &pebblestore.SessionModelProfileSnapshot{
		Source:             pebblestore.SessionModelProfileSourceSaved,
		ActionFavoriteID:   "mp_exact",
		ActionFavoriteName: "Exact",
		Action:             pebblestore.ModelProfileSelection{Provider: "openai", Model: "same-model"},
	}
	metadata := sessionsV3ModelProfileMetadata(map[string]any{"agent_name": "swarm"}, profile)
	stored, ok := metadata["model_profile"].(pebblestore.SessionModelProfileSnapshot)
	if !ok || stored.ActionFavoriteID != "mp_exact" || stored.ActionFavoriteName != "Exact" {
		t.Fatalf("model profile metadata = %#v", metadata["model_profile"])
	}
	profile.ActionFavoriteID = "mutated"
	profile.Action.Model = "mutated"
	if stored.ActionFavoriteID != "mp_exact" || stored.Action.Model != "same-model" {
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

func TestSessionsV3ExplicitModelProfilePreferenceMatchesDurableSessionPreference(t *testing.T) {
	server, _, closeStore := newSessionsV3PrimaryAPITestServer(t, filepath.Join(t.TempDir(), "explicit-profile-create.pebble"))
	defer func() { _ = closeStore() }()
	principal := testPrincipal()
	ctx := identity.ContextWithPrincipal(context.Background(), principal)
	profile, err := server.modelProfiles.Create(ctx, modelprofile.Input{Name: "Explicit", Provider: "test-provider", Model: "profile-model", Thinking: "high"})
	if err != nil {
		t.Fatalf("create model profile: %v", err)
	}
	body := `{"client_request_id":"explicit-profile-create","workspace_path":"/workspace/v3","workspace_binding_id":"workspace-binding","swarm_id":"local-swarm","agent_name":"swarm","preference":{"provider":"test-provider","model":"stale-model","thinking":"low"},"model_profile":{"saved_profile_id":"` + profile.ProfileID + `"}}`
	req := httptest.NewRequest(http.MethodPost, "/v3/sessions", strings.NewReader(body))
	req.Header.Set("Content-Type", "application/json")
	rec := httptest.NewRecorder()
	server.Handler().ServeHTTP(rec, withTestPrincipal(req))
	if rec.Code != http.StatusOK {
		t.Fatalf("create session status=%d body=%s", rec.Code, rec.Body.String())
	}
	var response struct {
		Session pebblestore.SessionSnapshot `json:"session"`
	}
	if err := json.Unmarshal(rec.Body.Bytes(), &response); err != nil {
		t.Fatalf("decode create response: %v", err)
	}
	if response.Session.ModelProfile == nil || response.Session.ModelProfile.ActionFavoriteID != profile.ProfileID || response.Session.ModelProfile.ActionFavoriteName != "Explicit" || response.Session.Preference.Provider != "test-provider" || response.Session.Preference.Model != "profile-model" || response.Session.Preference.Thinking != "high" {
		t.Fatalf("created session authorities diverged: %+v", response.Session)
	}
}

func TestSessionsV3PlanSidechatPreferencePassesThroughParentModelSetup(t *testing.T) {
	parentPreference := pebblestore.ModelPreference{Provider: "codex", Model: "parent-model", Thinking: "xhigh", ServiceTier: "priority", ContextMode: "full", UpdatedAt: 42}
	withoutProfile := pebblestore.SessionSnapshot{Preference: parentPreference}
	if got := sessionsV3PlanSidechatPreference(withoutProfile); got != (pebblestore.ModelPreference{}) {
		t.Fatalf("sidechat inherited non-snapshotted parent preference: got %+v", got)
	}

	profilePreference := pebblestore.ModelPreference{Provider: "openrouter", Model: "profile-parent-model", Thinking: "high", ServiceTier: "fast", ContextMode: "session", UpdatedAt: 77}
	parent := pebblestore.SessionSnapshot{
		Mode:       sessionruntime.ModePlan,
		Preference: profilePreference,
		ModelProfile: &pebblestore.SessionModelProfileSnapshot{
			AppliedAt: 77,
			Action:    pebblestore.ModelProfileSelection{Provider: "codex", Model: "action-model", Thinking: "medium"},
			Plan: &pebblestore.ModelProfileSelection{
				Provider: "openrouter", Model: "profile-parent-model", Thinking: "high", ServiceTier: "fast", ContextMode: "session",
			},
		},
	}
	if got := sessionsV3PlanSidechatPreference(parent); got != profilePreference {
		t.Fatalf("durable model-profile preference changed: got %+v want %+v", got, profilePreference)
	}
}

func TestSessionsV3ProfilePreferenceUsesCurrentMode(t *testing.T) {
	session := pebblestore.SessionSnapshot{Mode: "plan", ModelProfile: &pebblestore.SessionModelProfileSnapshot{AppliedAt: 7, Plan: &pebblestore.ModelProfileSelection{Provider: "openai", Model: "plan", Thinking: "high", ContextMode: "full"}, Action: pebblestore.ModelProfileSelection{Provider: "openai", Model: "action", Thinking: "medium", ServiceTier: "fast"}}}
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
