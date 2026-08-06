package api

import (
	"testing"

	sessionruntime "swarm/packages/swarmd/internal/session"
	pebblestore "swarm/packages/swarmd/internal/store/pebble"
)

func TestSessionsV3PlanSidechatModelProjectionConsistentAcrossSyncViewAndRealtime(t *testing.T) {
	server, sessionSvc, _, _, _ := newRoutedSessionTestServerWithSwarmStore(t)
	principal := testPrincipal()
	planSelection := pebblestore.ModelProfileSelection{
		Provider:    "codex",
		Model:       "plan-bound-model",
		Thinking:    "xhigh",
		ServiceTier: "fast",
		ContextMode: "compact",
	}
	planSidechat := pebblestore.SessionSnapshot{
		ID:             "plan-sidechat-model-projection",
		UserID:         principal.UserID,
		AccountScopeID: principal.AccountScopeID,
		WorkspacePath:  "/workspace/plan-sidechat-model-projection",
		WorkspaceName:  "plan-sidechat-model-projection",
		Title:          "Plan Sidechat",
		Mode:           sessionruntime.ModeAuto,
		Preference:     pebblestore.ModelPreference{Provider: "codex", Model: "stale-parent-action-model"},
		ModelProfile: &pebblestore.SessionModelProfileSnapshot{
			Source:             pebblestore.SessionModelProfileSourceSaved,
			ActionFavoriteID:   "favorite-plan",
			ActionFavoriteName: "Plan Favorite",
			Action:             planSelection,
			PlanFavoriteID:     "favorite-plan",
			PlanFavoriteName:   "Plan Favorite",
			Plan:               pebblestore.CloneModelProfileSelection(&planSelection),
			AppliedAt:          1700,
		},
		Metadata: map[string]any{
			"agent_name":           "plan",
			"resolved_agent_name":  "plan",
			"parent_session_id":    "parent-session",
			"system_sidechat_kind": "plan",
			"presentation_kind":    "system_sidechat",
		},
		CreatedAt: 1700,
		UpdatedAt: 1700,
	}
	created, err := server.applySessionV3PrimaryMutation(sessionruntime.SessionMutationInput{
		SessionID:       planSidechat.ID,
		UserID:          principal.UserID,
		AccountScopeID:  principal.AccountScopeID,
		ClientRequestID: "create-plan-sidechat-model-projection",
		IdempotencyKey:  "create-plan-sidechat-model-projection",
		PayloadHash:     "hash-create-plan-sidechat-model-projection",
		Kind:            sessionruntime.SessionMutationCreateSession,
		Session:         &planSidechat,
		NowUnixMs:       planSidechat.CreatedAt,
	})
	if err != nil {
		t.Fatalf("create durable Plan sidechat fixture: %v", err)
	}
	if created.RealtimeOutbox == nil {
		t.Fatalf("create durable Plan sidechat fixture missing realtime outbox: %+v", created)
	}

	durable, ok, err := sessionSvc.GetSession(planSidechat.ID)
	if err != nil || !ok {
		t.Fatalf("load durable Plan sidechat fixture: ok=%t err=%v", ok, err)
	}
	shell, err := sessionsV3SyncSessionShell(durable)
	if err != nil {
		t.Fatalf("project sync shell: %v", err)
	}
	assertPlanSidechatModelPreference(t, "sync shell", shell.Preference)

	projection, ok, err := sessionSvc.GetSessionProjection(planSidechat.ID)
	if err != nil || !ok {
		t.Fatalf("load durable Plan sidechat projection: ok=%t err=%v", ok, err)
	}
	view, err := server.buildSessionsV3SessionView(principal, durable, projection, nil, false)
	if err != nil {
		t.Fatalf("build session view: %v", err)
	}
	assertPlanSidechatModelPreference(t, "session view stored preference", view.AgenticSettings.StoredPreference)
	assertPlanSidechatModelPreference(t, "session view effective preference", view.AgenticSettings.EffectivePreference)
	assertPlanSidechatModelPreference(t, "session view model policy", view.AgenticSettings.AgentModelPolicy.Preference)
	if !view.AgenticSettings.AgentModelPolicy.Locked || view.AgenticSettings.AgentModelPolicy.ProfileID != "favorite-plan" || view.AgenticSettings.AgentModelPolicy.ProfileMode != sessionruntime.ModeAuto {
		t.Fatalf("session view model policy attribution = %+v", view.AgenticSettings.AgentModelPolicy)
	}

	realtimeShell, err := sessionsV3SyncSessionShell(durable)
	if err != nil {
		t.Fatalf("project realtime workset shell: %v", err)
	}
	message := V3RealtimeMessage{
		Protocol:              V3RealtimeProtocol,
		ProtocolVersion:       V3RealtimeProtocolVersion,
		Kind:                  V3RealtimeKindWorksetSessionUpdated,
		SessionID:             durable.ID,
		WorksetID:             "desktop:global",
		WorksetSubscriptionID: "desktop-client:desktop:global",
		EndpointCursor:        "v3c1.opaque",
		Rev:                   1,
		EventType:             "session.model_profile.updated",
		Session:               &realtimeShell,
	}
	if err := ValidateV3RealtimeOutboundServerMessage(message); err != nil {
		t.Fatalf("validate realtime workset payload: %v", err)
	}
	if message.Session == nil {
		t.Fatal("realtime workset payload missing session shell")
	}
	assertPlanSidechatModelPreference(t, "realtime workset", message.Session.Preference)
	if message.Session.ModelProfile == nil || message.Session.ModelProfile.Action.Model != "plan-bound-model" || message.Session.ModelProfile.ActionFavoriteID != "favorite-plan" {
		t.Fatalf("realtime workset model profile = %+v", message.Session.ModelProfile)
	}
}

func assertPlanSidechatModelPreference(t *testing.T, surface string, preference pebblestore.ModelPreference) {
	t.Helper()
	if preference.Provider != "codex" || preference.Model != "plan-bound-model" || preference.Thinking != "xhigh" || preference.ServiceTier != "fast" || preference.ContextMode != "compact" || preference.UpdatedAt != 1700 {
		t.Fatalf("%s preference = %+v", surface, preference)
	}
}
