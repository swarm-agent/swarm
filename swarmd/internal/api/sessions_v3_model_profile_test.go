package api

import (
	"context"
	"encoding/json"
	"fmt"
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
	swarmProfiles := modelprofile.NewSwarmService(pebblestore.NewSwarmModeSettingsStore(store))
	server := &Server{modelProfiles: service, swarmModelSettings: swarmProfiles}
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
	if _, err := swarmProfiles.Put(ctx, modelprofile.SwarmSettingsInput{Action: pebblestore.ModelProfileSelection{Provider: created.Provider, Model: created.Model, Thinking: created.Thinking, ServiceTier: created.ServiceTier, ContextMode: created.ContextMode}, Plan: pebblestore.ModelProfileSelection{Provider: planFavorite.Provider, Model: planFavorite.Model, Thinking: planFavorite.Thinking, ServiceTier: planFavorite.ServiceTier, ContextMode: planFavorite.ContextMode}}); err != nil {
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
	if saved == nil || saved.Source != pebblestore.SessionModelProfileSourceSwarmSettings || saved.ActionFavoriteID != "" || saved.ActionFavoriteName != "" || !saved.UseAccountDefault || saved.Action.Model != "saved-model" || saved.Action.ServiceTier != "priority" || saved.Action.ContextMode != "full" || saved.PlanFavoriteID != "" || saved.PlanFavoriteName != "" || saved.Plan == nil || saved.Plan.Model != "plan-model" || saved.Plan.Thinking != "xhigh" || saved.AppliedAt != 10 {
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
		for index, body := range []string{
			`{"client_request_id":"request","choice":{"` + field + `":{}}}`,
			`{"client_request_id":"request","choice":{"temporary":{"name":"Temporary","provider":"openai","model":"gpt-test","thinking":"high","` + field + `":{}}}}`,
		} {
			t.Run(fmt.Sprintf("%s_%d", field, index), func(t *testing.T) {
				var request sessionsV3ModelProfileApplyRequest
				err := decodeJSONBytes([]byte(body), &request)
				if err == nil || !strings.Contains(err.Error(), "unknown field") {
					t.Fatalf("decode removed field %q error = %v, want unknown field", field, err)
				}
			})
		}
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

func TestSessionsV3PlanSidechatModelProfileClonesImmutableSnapshot(t *testing.T) {
	parent := pebblestore.SessionSnapshot{ModelProfile: &pebblestore.SessionModelProfileSnapshot{
		Source:             pebblestore.SessionModelProfileSourceSaved,
		UseAccountDefault:  true,
		ActionFavoriteID:   "action-id",
		ActionFavoriteName: "Action",
		Action:             pebblestore.ModelProfileSelection{Provider: "test-provider", Model: "action-model"},
		PlanFavoriteID:     "plan-id",
		PlanFavoriteName:   "Plan",
		Plan:               &pebblestore.ModelProfileSelection{Provider: "test-provider", Model: "plan-model", Thinking: "high"},
		AppliedAt:          9,
	}}
	profile := sessionsV3PlanSidechatModelProfile(parent)
	if profile == nil || profile.ActionFavoriteID != "plan-id" || profile.ActionFavoriteName != "Plan" || profile.Action.Model != "plan-model" || profile.Action.Thinking != "high" || profile.PlanFavoriteID != "plan-id" || profile.Plan == nil || profile.Plan.Model != "plan-model" || profile.Plan.Thinking != "high" || profile.AppliedAt != 9 {
		t.Fatalf("Plan sidechat model profile = %+v", profile)
	}
	sidechat := pebblestore.SessionSnapshot{Mode: sessionruntime.ModeAuto, ModelProfile: profile}
	effective, err := resolveSessionV3EffectivePreference(sidechat, pebblestore.AgentProfile{})
	if err != nil || effective.Model != "plan-model" || effective.Thinking != "high" {
		t.Fatalf("Plan sidechat executor preference = %+v err=%v", effective, err)
	}
	parent.ModelProfile.Plan.Model = "mutated"
	if profile.Action.Model != "plan-model" || profile.Plan == nil || profile.Plan.Model != "plan-model" {
		t.Fatalf("Plan sidechat model profile shares mutable selection: %+v", profile)
	}
}

func TestSessionsV3ModelProfileMutationCommitsCurrentSlotAndRealtimeOutbox(t *testing.T) {
	server, sessionSvc, closeStore := newSessionsV3PrimaryAPITestServer(t, filepath.Join(t.TempDir(), "model-profile-mutation.pebble"))
	defer func() { _ = closeStore() }()
	principal := testPrincipal()
	ctx := identity.ContextWithPrincipal(context.Background(), principal)
	favorite, err := server.modelProfiles.Create(ctx, modelprofile.Input{Name: "Action New", Provider: "test-provider", Model: "action-new", Thinking: "high"})
	if err != nil {
		t.Fatalf("create favorite: %v", err)
	}
	created := createSessionsV3PrimaryTestSessionWithPreference(t, server, "model-profile-mutation-create", "model profile mutation", pebblestore.ModelPreference{Provider: "test-provider", Model: "action-old", Thinking: "medium"})
	created.ModelProfile = &pebblestore.SessionModelProfileSnapshot{Source: pebblestore.SessionModelProfileSourceSaved, ActionFavoriteID: "action-old", ActionFavoriteName: "Action Old", Action: pebblestore.ModelProfileSelection{Provider: "test-provider", Model: "action-old"}, PlanFavoriteID: "plan-old", PlanFavoriteName: "Plan Old", Plan: &pebblestore.ModelProfileSelection{Provider: "test-provider", Model: "plan-old"}, AppliedAt: 1}
	if _, err := server.applySessionV3PrimaryMutation(sessionruntime.SessionMutationInput{SessionID: created.ID, UserID: created.UserID, AccountScopeID: created.AccountScopeID, ClientRequestID: "model-profile-mutation-seed", IdempotencyKey: "model-profile-mutation-seed", PayloadHash: "model-profile-mutation-seed", RequestHash: "model-profile-mutation-seed", Kind: sessionruntime.SessionMutationUpdateModelProfile, Session: &created, NowUnixMs: 2}); err != nil {
		t.Fatalf("seed model profile: %v", err)
	}

	body := `{"client_request_id":"model-profile-mutation-update","choice":{"saved_profile_id":"` + favorite.ProfileID + `"}}`
	req := httptest.NewRequest(http.MethodPut, "/v3/sessions/"+created.ID+"/model-profile", strings.NewReader(body))
	req.Header.Set("Content-Type", "application/json")
	rec := httptest.NewRecorder()
	server.Handler().ServeHTTP(rec, withTestPrincipal(req))
	if rec.Code != http.StatusOK {
		t.Fatalf("model mutation status=%d body=%s", rec.Code, rec.Body.String())
	}
	var response struct {
		Preference     pebblestore.ModelPreference            `json:"preference"`
		ModelProfile   pebblestore.SessionModelProfileSnapshot `json:"model_profile"`
		Mutation       sessionruntime.SessionMutationResult    `json:"mutation"`
		RealtimeOutbox *pebblestore.V3RealtimeOutboxRecord     `json:"realtime_outbox"`
	}
	if err := json.Unmarshal(rec.Body.Bytes(), &response); err != nil {
		t.Fatalf("decode model mutation: %v", err)
	}
	if response.Mutation.Event.EventType != "session.model_profile.updated" || response.RealtimeOutbox == nil {
		t.Fatalf("model mutation durability = %+v outbox=%+v", response.Mutation, response.RealtimeOutbox)
	}
	var eventPayload struct {
		ModelProfile     pebblestore.SessionModelProfileSnapshot `json:"model_profile"`
		Preference       pebblestore.ModelPreference             `json:"preference"`
		AgentModelPolicy sessionsV3AgentModelPolicy               `json:"agent_model_policy"`
	}
	if err := json.Unmarshal(response.Mutation.Event.Payload, &eventPayload); err != nil {
		t.Fatalf("decode model mutation event: %v", err)
	}
	if eventPayload.ModelProfile.ActionFavoriteID != favorite.ProfileID || eventPayload.Preference.Model != "action-new" || eventPayload.AgentModelPolicy.Preference.Model != "action-new" {
		t.Fatalf("model mutation policy event = %+v", eventPayload)
	}
	if response.Preference.Model != "action-new" || response.ModelProfile.ActionFavoriteID != favorite.ProfileID || response.ModelProfile.PlanFavoriteID != "plan-old" || response.ModelProfile.Plan == nil || response.ModelProfile.Plan.Model != "plan-old" || response.ModelProfile.AppliedAt <= 2 {
		t.Fatalf("model mutation response = %+v preference=%+v", response.ModelProfile, response.Preference)
	}
	stored, ok, err := sessionSvc.GetSession(created.ID)
	if err != nil || !ok || stored.Mode != sessionruntime.ModeAuto || stored.Preference.Model != "action-new" || stored.ModelProfile == nil || stored.ModelProfile.ActionFavoriteID != favorite.ProfileID || stored.ModelProfile.PlanFavoriteID != "plan-old" {
		t.Fatalf("stored model mutation = %+v ok=%t err=%v", stored, ok, err)
	}
}

func TestSessionsV3ModelProfileMutationRejectsClearWithoutChangingAuthorities(t *testing.T) {
	server, sessionSvc, closeStore := newSessionsV3PrimaryAPITestServer(t, filepath.Join(t.TempDir(), "model-profile-clear.pebble"))
	defer func() { _ = closeStore() }()
	created := createSessionsV3PrimaryTestSessionWithPreference(t, server, "model-profile-clear-create", "model profile clear", pebblestore.ModelPreference{Provider: "test-provider", Model: "action-model", Thinking: "medium"})
	created.ModelProfile = &pebblestore.SessionModelProfileSnapshot{Source: pebblestore.SessionModelProfileSourceSaved, ActionFavoriteID: "action-old", ActionFavoriteName: "Action Old", Action: pebblestore.ModelProfileSelection{Provider: "test-provider", Model: "action-model", Thinking: "medium"}, PlanFavoriteID: "plan-old", PlanFavoriteName: "Plan Old", Plan: &pebblestore.ModelProfileSelection{Provider: "test-provider", Model: "plan-model"}, AppliedAt: 1}
	created.Metadata = sessionsV3ModelProfileMetadata(created.Metadata, created.ModelProfile)
	if _, err := server.applySessionV3PrimaryMutation(sessionruntime.SessionMutationInput{SessionID: created.ID, UserID: created.UserID, AccountScopeID: created.AccountScopeID, ClientRequestID: "model-profile-clear-seed", IdempotencyKey: "model-profile-clear-seed", PayloadHash: "model-profile-clear-seed", RequestHash: "model-profile-clear-seed", Kind: sessionruntime.SessionMutationUpdateModelProfile, Session: &created, NowUnixMs: 2}); err != nil {
		t.Fatalf("seed model profile: %v", err)
	}

	req := httptest.NewRequest(http.MethodDelete, "/v3/sessions/"+created.ID+"/model-profile", strings.NewReader(`{"client_request_id":"model-profile-clear"}`))
	req.Header.Set("Content-Type", "application/json")
	rec := httptest.NewRecorder()
	server.Handler().ServeHTTP(rec, withTestPrincipal(req))
	if rec.Code != http.StatusConflict || !strings.Contains(rec.Body.String(), "requires immutable model authority") {
		t.Fatalf("clear model profile status=%d body=%s", rec.Code, rec.Body.String())
	}
	stored, ok, err := sessionSvc.GetSession(created.ID)
	if err != nil || !ok || stored.ModelProfile == nil || stored.ModelProfile.ActionFavoriteID != "action-old" || stored.Preference.Model != "action-model" {
		t.Fatalf("clear changed durable authorities: session=%+v ok=%t err=%v", stored, ok, err)
	}
	metadataProfile, ok := stored.Metadata["model_profile"].(pebblestore.SessionModelProfileSnapshot)
	if !ok || metadataProfile.ActionFavoriteID != "action-old" {
		t.Fatalf("clear changed profile metadata: %#v", stored.Metadata["model_profile"])
	}
}

func TestSessionsV3ModelProfileMutationRejectsActiveRunWithoutChangingSnapshot(t *testing.T) {
	server, sessionSvc, closeStore := newSessionsV3PrimaryAPITestServer(t, filepath.Join(t.TempDir(), "model-profile-active-run.pebble"))
	defer func() { _ = closeStore() }()
	created := createSessionsV3PrimaryTestSessionWithPreference(t, server, "model-profile-active-run-create", "model profile active run", pebblestore.ModelPreference{Provider: "test-provider", Model: "action-model", Thinking: "medium"})
	created.ModelProfile = &pebblestore.SessionModelProfileSnapshot{Source: pebblestore.SessionModelProfileSourceSaved, ActionFavoriteID: "action-old", ActionFavoriteName: "Action Old", Action: pebblestore.ModelProfileSelection{Provider: "test-provider", Model: "action-model"}, AppliedAt: 1}
	if _, err := server.applySessionV3PrimaryMutation(sessionruntime.SessionMutationInput{SessionID: created.ID, UserID: created.UserID, AccountScopeID: created.AccountScopeID, ClientRequestID: "model-profile-seed", IdempotencyKey: "model-profile-seed", PayloadHash: "model-profile-seed", RequestHash: "model-profile-seed", Kind: sessionruntime.SessionMutationUpdateModelProfile, Session: &created, NowUnixMs: 2}); err != nil {
		t.Fatalf("seed model profile: %v", err)
	}
	if _, err := server.applySessionV3PrimaryMutation(sessionruntime.SessionMutationInput{SessionID: created.ID, UserID: created.UserID, AccountScopeID: created.AccountScopeID, ClientRequestID: "model-profile-active-run", IdempotencyKey: "model-profile-active-run", PayloadHash: "model-profile-active-run", RequestHash: "model-profile-active-run", Kind: sessionruntime.SessionMutationRecordRunIntent, RunIntent: &pebblestore.V3SessionRunIntent{RunID: "run-active", Status: sessionruntime.RunIntentPendingExecutor}, NowUnixMs: 3}); err != nil {
		t.Fatalf("record active run: %v", err)
	}

	req := httptest.NewRequest(http.MethodDelete, "/v3/sessions/"+created.ID+"/model-profile", strings.NewReader(`{"client_request_id":"model-profile-clear-active"}`))
	req.Header.Set("Content-Type", "application/json")
	rec := httptest.NewRecorder()
	server.Handler().ServeHTTP(rec, withTestPrincipal(req))
	if rec.Code != http.StatusConflict || !strings.Contains(rec.Body.String(), "active run") {
		t.Fatalf("active-run model mutation status=%d body=%s", rec.Code, rec.Body.String())
	}
	stored, ok, err := sessionSvc.GetSession(created.ID)
	if err != nil || !ok || stored.ModelProfile == nil || stored.ModelProfile.ActionFavoriteID != "action-old" || stored.Mode != sessionruntime.ModeAuto {
		t.Fatalf("model snapshot changed during active run: session=%+v ok=%t err=%v", stored, ok, err)
	}
}

func TestSessionsV3ModelProfileChoiceUpdatesCurrentModeSlotAndPreservesOtherSlot(t *testing.T) {
	original := &pebblestore.SessionModelProfileSnapshot{
		Source:             pebblestore.SessionModelProfileSourceSaved,
		ActionFavoriteID:   "action-old",
		ActionFavoriteName: "Action Old",
		Action:             pebblestore.ModelProfileSelection{Provider: "test-provider", Model: "action-old"},
		PlanFavoriteID:     "plan-old",
		PlanFavoriteName:   "Plan Old",
		Plan:               &pebblestore.ModelProfileSelection{Provider: "test-provider", Model: "plan-old"},
		AppliedAt:          1,
	}
	choice := &pebblestore.SessionModelProfileSnapshot{
		Source:             pebblestore.SessionModelProfileSourceSaved,
		ActionFavoriteID:   "favorite-new",
		ActionFavoriteName: "Favorite New",
		Action:             pebblestore.ModelProfileSelection{Provider: "test-provider", Model: "new-model", Thinking: "high"},
		AppliedAt:          2,
	}

	auto, err := mergeSessionsV3ModelProfileChoice(pebblestore.SessionSnapshot{Mode: sessionruntime.ModeAuto, ModelProfile: original}, choice)
	if err != nil {
		t.Fatalf("merge auto slot: %v", err)
	}
	if auto.Source != pebblestore.SessionModelProfileSourceSaved || auto.AppliedAt != 2 || auto.ActionFavoriteID != "favorite-new" || auto.Action.Model != "new-model" || auto.PlanFavoriteID != "plan-old" || auto.Plan == nil || auto.Plan.Model != "plan-old" {
		t.Fatalf("auto slot merge = %+v", auto)
	}
	plan, err := mergeSessionsV3ModelProfileChoice(pebblestore.SessionSnapshot{Mode: sessionruntime.ModePlan, ModelProfile: original}, choice)
	if err != nil {
		t.Fatalf("merge plan slot: %v", err)
	}
	if plan.Source != pebblestore.SessionModelProfileSourceSaved || plan.AppliedAt != 2 || plan.ActionFavoriteID != "action-old" || plan.Action.Model != "action-old" || plan.PlanFavoriteID != "favorite-new" || plan.Plan == nil || plan.Plan.Model != "new-model" {
		t.Fatalf("plan slot merge = %+v", plan)
	}
	if original.Action.Model != "action-old" || original.Plan == nil || original.Plan.Model != "plan-old" {
		t.Fatalf("merge mutated original snapshot: %+v", original)
	}
}

func TestSessionsV3ModelProfileChoiceRejectsInitialPlanSlotWithoutAction(t *testing.T) {
	_, err := mergeSessionsV3ModelProfileChoice(pebblestore.SessionSnapshot{Mode: sessionruntime.ModePlan}, &pebblestore.SessionModelProfileSnapshot{Action: pebblestore.ModelProfileSelection{Provider: "test-provider", Model: "plan-only"}})
	if err == nil || !strings.Contains(err.Error(), "Action model slot") {
		t.Fatalf("initial Plan slot error = %v", err)
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
