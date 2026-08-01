package api

import (
	"bytes"
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"path/filepath"
	"reflect"
	"testing"

	"swarm/packages/swarmd/internal/identity"
	"swarm/packages/swarmd/internal/modelprofile"
	"swarm/packages/swarmd/internal/permission"
	sessionruntime "swarm/packages/swarmd/internal/session"
	pebblestore "swarm/packages/swarmd/internal/store/pebble"
)

func TestSessionsV3PlanEntryPreservesLifecycleAuthorities(t *testing.T) {
	server, sessionSvc, permissionSvc, _, _ := newRoutedSessionTestServerWithSwarmStore(t)
	action, plan := installSessionsV3FlatSwarmModeSettings(t, server, true)
	created := createSessionsV3PrimaryTestSession(t, server, "plan-invariant-enter-create", "plan invariant enter")

	doc := &pebblestore.SessionPlanDocument{
		ID:                 "plan-invariant-active",
		Title:              "Active invariant plan",
		Status:             "approved",
		ExecutionPolicy:    pebblestore.SessionPlanExecutionPolicy{Mode: sessionruntime.PlanExecutionPolicyModeReviewEachCheckpoint, Shape: sessionruntime.PlanExecutionShapeCheckpointed},
		ActiveCheckpointID: "cp-1",
		Checkpoints: []pebblestore.SessionPlanCheckpoint{{
			ID: "cp-1", Title: "Preserve state", Objective: "Keep lifecycle authorities stable", Status: sessionruntime.PlanCheckpointStatusPending,
			Tasks: []string{"preserve the active plan"}, AcceptanceCriteria: []string{"the checkpoint remains pending"},
		}},
	}
	beforePlan := sessionsV3PlanModeSeedPlan(t, sessionSvc, created.ID, doc.ID, doc, "approved")
	pending := createSessionsV3PlanInvariantPermission(t, permissionSvc, created.ID)
	beforeSidechat := createSessionsV3PlanInvariantSidechat(t, server, sessionSvc, created.ID, pending.ID)
	beforePlan = sessionsV3PlanModeGetPlan(t, sessionSvc, created.ID, doc.ID)

	postSessionsV3PlanModeTestStatus(t, server, created.ID, "/plan-mode/enter", `{}`, http.StatusOK)
	updated, ok, err := sessionSvc.GetSession(created.ID)
	if err != nil || !ok {
		t.Fatalf("get entered session: ok=%t err=%v", ok, err)
	}
	if sessionruntime.NormalizeMode(updated.Mode) != sessionruntime.ModePlan {
		t.Fatalf("entered mode = %q, want plan", updated.Mode)
	}

	afterPlan := sessionsV3PlanModeGetPlan(t, sessionSvc, created.ID, beforePlan.ID)
	if !reflect.DeepEqual(afterPlan, beforePlan) {
		t.Fatalf("Plan entry mutated active plan/approval/checkpoint state:\nbefore=%#v\nafter=%#v", beforePlan, afterPlan)
	}
	permissions, err := permissionSvc.ListPending(created.ID, 10)
	if err != nil {
		t.Fatalf("list pending permissions: %v", err)
	}
	if len(permissions) != 1 || permissions[0].ID != pending.ID || permissions[0].Status != pebblestore.PermissionStatusPending || permissions[0].ProposalRevision != pending.ProposalRevision {
		t.Fatalf("Plan entry mutated pending approval authority: %#v", permissions)
	}
	afterSidechat, ok, err := sessionSvc.GetSession(beforeSidechat.ID)
	if err != nil || !ok {
		t.Fatalf("get Plan sidechat after entry: ok=%t err=%v", ok, err)
	}
	if !reflect.DeepEqual(afterSidechat, beforeSidechat) {
		t.Fatalf("Plan entry mutated routed Plan sidechat:\nbefore=%#v\nafter=%#v", beforeSidechat, afterSidechat)
	}

	// cp-3 replaces the temporary pre-change behavior (which leaves the Action
	// preference current) with the immutable account Plan selection.
	if updated.Preference.Provider != plan.Provider || updated.Preference.Model != plan.Model || updated.Preference.Thinking != plan.Thinking {
		t.Fatalf("entered Plan preference = %+v, want flat Plan favorite %+v (Action=%+v)", updated.Preference, plan, action)
	}
}

func TestSessionsV3PlanEntryRejectsFlatPlanDisabledWithoutMutation(t *testing.T) {
	server, sessionSvc, _, _, _ := newRoutedSessionTestServerWithSwarmStore(t)
	action, _ := installSessionsV3FlatSwarmModeSettings(t, server, false)
	created := createSessionsV3PrimaryTestSession(t, server, "plan-invariant-disabled-create", "plan invariant disabled")
	before, ok, err := sessionSvc.GetSession(created.ID)
	if err != nil || !ok {
		t.Fatalf("get session before rejected entry: ok=%t err=%v", ok, err)
	}

	rec := postSessionsV3PlanModeTestStatus(t, server, created.ID, "/plan-mode/enter", `{}`, http.StatusBadRequest)
	var payload map[string]any
	if err := json.Unmarshal(rec.Body.Bytes(), &payload); err != nil {
		t.Fatalf("decode disabled Plan response: %v", err)
	}
	if payload["error"] == "" {
		t.Fatalf("disabled Plan rejection missing error: %#v", payload)
	}
	after, ok, err := sessionSvc.GetSession(created.ID)
	if err != nil || !ok {
		t.Fatalf("get session after rejected entry: ok=%t err=%v", ok, err)
	}
	if !reflect.DeepEqual(after, before) {
		t.Fatalf("disabled Plan entry partially mutated session:\nbefore=%#v\nafter=%#v action=%+v", before, after, action)
	}
}

func TestSessionsV3CanonicalPlanAcceptanceRestoresFlatActionAtomically(t *testing.T) {
	server, sessionSvc, permissionSvc, _, _ := newRoutedSessionTestServerWithSwarmStore(t)
	action, plan := installSessionsV3FlatSwarmModeSettings(t, server, true)
	created := createSessionsV3PrimaryTestSession(t, server, "plan-invariant-exit-create", "plan invariant exit")

	if _, _, err := sessionSvc.SetMode(created.ID, sessionruntime.ModePlan); err != nil {
		t.Fatalf("seed Plan mode: %v", err)
	}
	planPreference := planInvariantPreference(plan)
	if _, _, err := sessionSvc.SetSessionPreference(created.ID, sessionruntime.SessionPreferenceUpdate{Provider: &planPreference.Provider, Model: &planPreference.Model, Thinking: &planPreference.Thinking}); err != nil {
		t.Fatalf("seed current Plan selection: %v", err)
	}
	pending := createSessionsV3PlanInvariantPermission(t, permissionSvc, created.ID)

	document := &pebblestore.SessionPlanDocument{
		ID: "plan-invariant-exit", Title: "Exit invariant plan",
		Info: pebblestore.SessionPlanInfo{Goal: "restore Action while approving the plan"},
		Checkpoints: []pebblestore.SessionPlanCheckpoint{{
			ID: "cp-1", Title: "Execute", Objective: "exercise canonical acceptance", Status: sessionruntime.PlanCheckpointStatusPending, Order: 1,
			Tasks: []string{"execute the checkpoint"}, AcceptanceCriteria: []string{"the checkpoint is executable"},
		}},
	}
	rawDocument, err := json.Marshal(document)
	if err != nil {
		t.Fatalf("marshal exit invariant document: %v", err)
	}
	postSessionsV3PlanModeTestStatus(t, server, created.ID, "/plan-mode/plans/plan-invariant-exit/submit", `{"title":"Exit invariant plan","document":`+string(rawDocument)+`}`, http.StatusOK)

	after, ok, err := sessionSvc.GetSession(created.ID)
	if err != nil || !ok {
		t.Fatalf("get accepted session: ok=%t err=%v", ok, err)
	}
	active, ok, err := sessionSvc.GetActivePlan(created.ID)
	if err != nil || !ok || active.Document == nil {
		t.Fatalf("get active accepted plan: ok=%t err=%v plan=%#v", ok, err, active)
	}
	if sessionruntime.NormalizeMode(after.Mode) != sessionruntime.ModeAuto || active.ID != document.ID || active.Status != "approved" || active.ApprovalState != "approved" || !active.Active || active.Document.ActiveCheckpointID != "cp-1" || active.Document.Checkpoints[0].Status != sessionruntime.PlanCheckpointStatusPending {
		t.Fatalf("canonical acceptance was not atomic: session=%#v plan=%#v", after, active)
	}
	permissions, err := permissionSvc.ListPending(created.ID, 10)
	if err != nil {
		t.Fatalf("list permissions after acceptance: %v", err)
	}
	if len(permissions) != 1 || permissions[0].ID != pending.ID || permissions[0].ProposalRevision != pending.ProposalRevision {
		t.Fatalf("canonical acceptance mutated unrelated pending permission: %#v", permissions)
	}

	// exit_plan_mode and the dedicated submit endpoint share this V3 plan
	// acceptance mutation boundary; cp-3 must restore the immutable Action
	// selection in the same commit that activates the approved plan.
	if after.Preference.Provider != action.Provider || after.Preference.Model != action.Model || after.Preference.Thinking != action.Thinking {
		t.Fatalf("accepted session preference = %+v, want flat Action favorite %+v", after.Preference, action)
	}
}

func installSessionsV3FlatSwarmModeSettings(t *testing.T, server *Server, planEnabled bool) (pebblestore.ModelProfileRecord, pebblestore.ModelProfileRecord) {
	t.Helper()
	store, err := pebblestore.Open(filepath.Join(t.TempDir(), "plan-invariants.pebble"))
	if err != nil {
		t.Fatalf("open flat model settings store: %v", err)
	}
	t.Cleanup(func() { _ = store.Close() })
	favorites := pebblestore.NewModelProfileStore(store)
	principal := testPrincipal()
	action := pebblestore.ModelProfileRecord{ProfileID: "favorite-action", AccountScopeID: principal.AccountScopeID, Name: "Action", Provider: "test-provider", Model: "action-model", Thinking: "medium"}
	plan := pebblestore.ModelProfileRecord{ProfileID: "favorite-plan", AccountScopeID: principal.AccountScopeID, Name: "Plan", Provider: "test-provider", Model: "plan-model", Thinking: "high"}
	for _, favorite := range []pebblestore.ModelProfileRecord{action, plan} {
		if _, err := favorites.PutForAccount(favorite); err != nil {
			t.Fatalf("put flat favorite %q: %v", favorite.ProfileID, err)
		}
	}
	swarmProfiles := modelprofile.NewSwarmService(pebblestore.NewSwarmModeSettingsStore(store), favorites)
	server.SetSwarmProfileService(swarmProfiles)
	ctx := identity.ContextWithPrincipal(context.Background(), principal)
	input := modelprofile.SwarmSettingsInput{ActionFavoriteID: action.ProfileID, PlanEnabled: planEnabled}
	if planEnabled {
		input.PlanFavoriteID = plan.ProfileID
	}
	if _, err := swarmProfiles.Put(ctx, input); err != nil {
		t.Fatalf("put flat Swarm mode settings: %v", err)
	}
	return action, plan
}

func createSessionsV3PlanInvariantPermission(t *testing.T, permissionSvc *permission.Service, sessionID string) pebblestore.PermissionRecord {
	t.Helper()
	arguments := `{"path_id":"permission.exit-plan-mode.v1","title":"Invariant proposal","document":{"title":"Invariant proposal","info":{"goal":"preserve proposal authority"},"checkpoints":[{"id":"cp-1","title":"Preserve","status":"pending","tasks":["preserve"],"acceptance_criteria":["preserved"]}]},"approved_arguments":{"title":"Invariant proposal","document":{"title":"Invariant proposal","info":{"goal":"preserve proposal authority"},"checkpoints":[{"id":"cp-1","title":"Preserve","status":"pending","tasks":["preserve"],"acceptance_criteria":["preserved"]}]}}}`
	record, err := permissionSvc.CreatePending(permission.CreateInput{SessionID: sessionID, RunID: "run-plan-invariant", CallID: "call-plan-invariant", ToolName: "exit_plan_mode", ToolArguments: arguments, ToolCallArguments: arguments, Requirement: "exit_plan_mode", Mode: sessionruntime.ModePlan})
	if err != nil {
		t.Fatalf("create pending Plan permission: %v", err)
	}
	return record
}

func createSessionsV3PlanInvariantSidechat(t *testing.T, server *Server, sessionSvc interface {
	GetSession(string) (pebblestore.SessionSnapshot, bool, error)
}, parentSessionID, permissionID string) pebblestore.SessionSnapshot {
	t.Helper()
	req := httptest.NewRequest(http.MethodPost, "/v3/sessions/"+parentSessionID+"/sidechats/plan", bytes.NewBufferString(`{"permission_id":"`+permissionID+`","plan_id":"placeholder","plan_revision":1}`))
	req.Header.Set("Content-Type", "application/json")
	rec := httptest.NewRecorder()
	server.Handler().ServeHTTP(rec, withTestPrincipal(req))
	if rec.Code != http.StatusOK {
		t.Fatalf("create Plan sidechat status=%d body=%s", rec.Code, rec.Body.String())
	}
	var payload struct {
		SessionID string `json:"session_id"`
	}
	if err := json.Unmarshal(rec.Body.Bytes(), &payload); err != nil {
		t.Fatalf("decode Plan sidechat response: %v", err)
	}
	sidechat, ok, err := sessionSvc.GetSession(payload.SessionID)
	if err != nil || !ok {
		t.Fatalf("get created Plan sidechat: ok=%t err=%v", ok, err)
	}
	return sidechat
}

func planInvariantPreference(profile pebblestore.ModelProfileRecord) pebblestore.ModelPreference {
	return pebblestore.ModelPreference{Provider: profile.Provider, Model: profile.Model, Thinking: profile.Thinking, ServiceTier: profile.ServiceTier, ContextMode: profile.ContextMode}
}
