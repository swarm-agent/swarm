package api

import (
	"bytes"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"reflect"
	"strings"
	"testing"

	sessionruntime "swarm/packages/swarmd/internal/session"
	pebblestore "swarm/packages/swarmd/internal/store/pebble"
)

func TestSessionsV3PlanSidechatUsesImmutableSnapshotAndPreservesParentState(t *testing.T) {
	server, sessionSvc, permissionSvc, _, _ := newRoutedSessionTestServerWithSwarmStore(t)
	created := createSessionsV3PrimaryTestSession(t, server, "plan-state-create", "plan state")

	before, ok, err := sessionSvc.GetSession(created.ID)
	if err != nil || !ok {
		t.Fatalf("get parent session: ok=%t err=%v", ok, err)
	}
	before.Mode = sessionruntime.ModePlan
	before.Preference = pebblestore.ModelPreference{Provider: "mutable-provider", Model: "mutable-model", Thinking: "low", ServiceTier: "standard", ContextMode: "compact", UpdatedAt: 444}
	before.UpdatedAt = 444
	before.ModelProfile = &pebblestore.SessionModelProfileSnapshot{
		Source:             pebblestore.SessionModelProfileSourceSaved,
		UseAccountDefault:  true,
		ActionFavoriteID:   "favorite-action",
		ActionFavoriteName: "Action",
		Action:             pebblestore.ModelProfileSelection{Provider: "action-provider", Model: "action-model", Thinking: "medium"},
		PlanFavoriteID:     "favorite-plan",
		PlanFavoriteName:   "Plan",
		Plan:               &pebblestore.ModelProfileSelection{Provider: "PLAN-PROVIDER", Model: "plan-model", Thinking: "high", ServiceTier: "fast", ContextMode: "full"},
		AppliedAt:          333,
	}
	before.Metadata["model_profile"] = *before.ModelProfile
	if err := sessionSvc.Store().UpdateSession(before); err != nil {
		t.Fatalf("seed immutable model snapshot: %v", err)
	}
	planDoc := &pebblestore.SessionPlanDocument{
		ID:                 "plan-state-active",
		Title:              "Preserved Plan state",
		Status:             "approved",
		ActiveCheckpointID: "cp-1",
		Checkpoints: []pebblestore.SessionPlanCheckpoint{{
			ID: "cp-1", Title: "Preserve", Status: sessionruntime.PlanCheckpointStatusPending,
			Tasks: []string{"preserve parent authority"}, AcceptanceCriteria: []string{"state remains unchanged"},
		}},
	}
	beforePlan := sessionsV3PlanModeSeedPlan(t, sessionSvc, created.ID, planDoc.ID, planDoc, "approved")
	pending := createSessionsV3PlanInvariantPermission(t, permissionSvc, created.ID)
	before, ok, err = sessionSvc.GetSession(created.ID)
	if err != nil || !ok {
		t.Fatalf("reload parent before sidechat: ok=%t err=%v", ok, err)
	}
	beforeProjection, ok, err := sessionSvc.GetSessionProjection(created.ID)
	if err != nil || !ok {
		t.Fatalf("get parent projection before sidechat: ok=%t err=%v", ok, err)
	}

	rec := postSessionsV3PlanStateSidechat(t, server, created.ID, pending.ID)
	if rec.Code != http.StatusOK {
		t.Fatalf("create Plan sidechat status=%d body=%s", rec.Code, rec.Body.String())
	}
	var response struct {
		SessionID string `json:"session_id"`
		Provider  string `json:"provider"`
		Model     string `json:"model"`
	}
	if err := json.Unmarshal(rec.Body.Bytes(), &response); err != nil {
		t.Fatalf("decode Plan sidechat response: %v", err)
	}
	if response.Provider != "plan-provider" || response.Model != "plan-model" {
		t.Fatalf("Plan sidechat response model = %s/%s, want immutable snapshot Plan", response.Provider, response.Model)
	}

	sidechat, ok, err := sessionSvc.GetSession(response.SessionID)
	if err != nil || !ok {
		t.Fatalf("get Plan sidechat: ok=%t err=%v", ok, err)
	}
	wantPreference := pebblestore.ModelPreference{Provider: "plan-provider", Model: "plan-model", Thinking: "high", ServiceTier: "fast", ContextMode: "full", UpdatedAt: 333}
	if !reflect.DeepEqual(sidechat.Preference, wantPreference) {
		t.Fatalf("Plan sidechat preference = %+v, want %+v", sidechat.Preference, wantPreference)
	}
	if sidechat.ModelProfile == nil || sidechat.ModelProfile.ActionFavoriteID != "favorite-plan" || sidechat.ModelProfile.ActionFavoriteName != "Plan" || sidechat.ModelProfile.Action.Provider != "PLAN-PROVIDER" || sidechat.ModelProfile.Action.Model != "plan-model" {
		t.Fatalf("Plan sidechat auto slot is not bound to parent Plan selection: %+v", sidechat.ModelProfile)
	}
	effective, err := resolveSessionV3EffectivePreference(sidechat, pebblestore.AgentProfile{})
	if err != nil || effective.Provider != "plan-provider" || effective.Model != "plan-model" || effective.Thinking != "high" {
		t.Fatalf("Plan sidechat executor-visible model = %+v err=%v", effective, err)
	}
	if sidechat.Mode != sessionruntime.ModeAuto || !sessionsV3SystemSidechat(sidechat) || sessionsV3MetadataString(sidechat.Metadata, "parent_session_id") != created.ID || sidechat.Metadata["navigation_hidden"] != true || sidechat.Metadata["settings_locked"] != true {
		t.Fatalf("Plan sidechat lost hidden parent-owned binding: %#v", sidechat)
	}
	lockedMutation := httptest.NewRequest(http.MethodDelete, "/v3/sessions/"+sidechat.ID+"/model-profile", bytes.NewBufferString(`{"client_request_id":"locked-plan-sidechat-profile"}`))
	lockedMutation.Header.Set("Content-Type", "application/json")
	lockedRec := httptest.NewRecorder()
	server.Handler().ServeHTTP(lockedRec, withTestPrincipal(lockedMutation))
	if lockedRec.Code != http.StatusConflict || !strings.Contains(lockedRec.Body.String(), "parent-owned and locked") {
		t.Fatalf("Plan sidechat model-profile lock status=%d body=%s", lockedRec.Code, lockedRec.Body.String())
	}
	lockedSidechat, ok, err := sessionSvc.GetSession(sidechat.ID)
	if err != nil || !ok || !reflect.DeepEqual(lockedSidechat, sidechat) {
		t.Fatalf("locked Plan sidechat mutated: ok=%t err=%v before=%#v after=%#v", ok, err, sidechat, lockedSidechat)
	}

	after, ok, err := sessionSvc.GetSession(created.ID)
	if err != nil || !ok {
		t.Fatalf("get parent after sidechat: ok=%t err=%v", ok, err)
	}
	if !reflect.DeepEqual(after, before) {
		t.Fatalf("Plan sidechat creation mutated parent state:\nbefore=%#v\nafter=%#v", before, after)
	}
	afterProjection, ok, err := sessionSvc.GetSessionProjection(created.ID)
	if err != nil || !ok || !reflect.DeepEqual(afterProjection, beforeProjection) {
		t.Fatalf("Plan sidechat creation mutated parent projection: ok=%t err=%v before=%#v after=%#v", ok, err, beforeProjection, afterProjection)
	}
	afterPlan := sessionsV3PlanModeGetPlan(t, sessionSvc, created.ID, beforePlan.ID)
	if !reflect.DeepEqual(afterPlan, beforePlan) {
		t.Fatalf("Plan sidechat creation mutated active plan/checkpoint state:\nbefore=%#v\nafter=%#v", beforePlan, afterPlan)
	}
	permissions, err := permissionSvc.ListPending(created.ID, 10)
	if err != nil {
		t.Fatalf("list pending permissions: %v", err)
	}
	if len(permissions) != 1 || permissions[0].ID != pending.ID || permissions[0].Status != pebblestore.PermissionStatusPending || permissions[0].ProposalRevision != pending.ProposalRevision {
		t.Fatalf("Plan sidechat mutated permission authority: %#v", permissions)
	}
}

func TestSessionsV3PlanSidechatRejectsDisabledSnapshotWithoutMutation(t *testing.T) {
	server, sessionSvc, permissionSvc, _, _ := newRoutedSessionTestServerWithSwarmStore(t)
	created := createSessionsV3PrimaryTestSession(t, server, "plan-state-disabled-create", "plan state disabled")
	parent, ok, err := sessionSvc.GetSession(created.ID)
	if err != nil || !ok {
		t.Fatalf("get parent session: ok=%t err=%v", ok, err)
	}
	parent.UpdatedAt = 555
	parent.ModelProfile = &pebblestore.SessionModelProfileSnapshot{
		Source:             pebblestore.SessionModelProfileSourceSaved,
		ActionFavoriteID:   "favorite-action",
		ActionFavoriteName: "Action",
		Action:             pebblestore.ModelProfileSelection{Provider: "action-provider", Model: "action-model", Thinking: "medium"},
		AppliedAt:          555,
	}
	if err := sessionSvc.Store().UpdateSession(parent); err != nil {
		t.Fatalf("seed Plan-disabled snapshot: %v", err)
	}
	pending := createSessionsV3PlanInvariantPermission(t, permissionSvc, created.ID)
	before, ok, err := sessionSvc.GetSession(created.ID)
	if err != nil || !ok {
		t.Fatalf("reload parent before rejected sidechat: ok=%t err=%v", ok, err)
	}

	rec := postSessionsV3PlanStateSidechat(t, server, created.ID, pending.ID)
	if rec.Code != http.StatusConflict || !strings.Contains(rec.Body.String(), "Plan is disabled") {
		t.Fatalf("Plan-disabled sidechat status=%d body=%s", rec.Code, rec.Body.String())
	}
	after, ok, err := sessionSvc.GetSession(created.ID)
	if err != nil || !ok || !reflect.DeepEqual(after, before) {
		t.Fatalf("Plan-disabled rejection mutated parent: ok=%t err=%v before=%#v after=%#v", ok, err, before, after)
	}
	sidechatID, _ := sessionsV3SystemSidechatID(created.ID, "plan")
	if _, exists, err := sessionSvc.GetSession(sidechatID); err != nil || exists {
		t.Fatalf("Plan-disabled rejection created sidechat: exists=%t err=%v", exists, err)
	}
}

func postSessionsV3PlanStateSidechat(t *testing.T, server *Server, parentSessionID, permissionID string) *httptest.ResponseRecorder {
	t.Helper()
	req := httptest.NewRequest(http.MethodPost, "/v3/sessions/"+parentSessionID+"/sidechats/plan", bytes.NewBufferString(`{"permission_id":"`+permissionID+`","plan_id":"client-placeholder","plan_revision":1}`))
	req.Header.Set("Content-Type", "application/json")
	rec := httptest.NewRecorder()
	server.Handler().ServeHTTP(rec, withTestPrincipal(req))
	return rec
}
