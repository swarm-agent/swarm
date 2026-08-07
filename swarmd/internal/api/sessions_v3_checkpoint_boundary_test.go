package api

import (
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	sessionruntime "swarm/packages/swarmd/internal/session"
)

func TestCheckpointBoundaryPayloadSelectsFreshCommittedRunIdentity(t *testing.T) {
	payload := map[string]any{
		"action":             sessionruntime.CheckpointBoundaryTransitionAction,
		"next_action":        "run_checkpoint_with_fresh_context",
		"checkpoint_id":      "followup-1",
		"next_checkpoint_id": "followup-1",
		"run_request": map[string]any{"plan_checkpoint_context": map[string]any{
			"plan_id": "plan-1", "checkpoint_id": "followup-1", "attempt_id": "followup-1:attempt-1", "run_id": "run-next", "execution_epoch_id": "epoch-2", "parent_session_id": "session-1",
		}},
	}
	scope := sessionV3ProviderCheckpointScopeFromPayload(sessionV3ProviderCheckpointScope{PlanID: "plan-old", CheckpointID: "cp-old"}, payload)
	if !scope.FreshContext || scope.PlanID != "plan-1" || scope.CheckpointID != "followup-1" || scope.AttemptID != "followup-1:attempt-1" || scope.ParentSessionID != "session-1" {
		t.Fatalf("checkpoint boundary scope = %#v", scope)
	}
}

func TestLegacyFollowupCheckpointRouteReturnsMigrationError(t *testing.T) {
	server, sessions, _, _, _ := newRoutedSessionTestServerWithSwarmStore(t)
	before, err := sessions.ListSessionEvents("session", 0, 100)
	if err != nil {
		t.Fatalf("list events before legacy route: %v", err)
	}
	recorder := httptest.NewRecorder()
	request := httptest.NewRequest(http.MethodPost, "/v3/sessions/session/plan/request-followup-checkpoint", strings.NewReader(`{}`))
	server.handleSessionV3PrimaryPlanModeRequestFollowupCheckpoint(recorder, request, testPrincipal(), "session")
	if recorder.Code != http.StatusGone || !strings.Contains(recorder.Body.String(), "legacy_followup_checkpoint_disabled") || !strings.Contains(recorder.Body.String(), "transition_checkpoint_boundary") {
		t.Fatalf("legacy route response code=%d body=%s", recorder.Code, recorder.Body.String())
	}
	after, err := sessions.ListSessionEvents("session", 0, 100)
	if err != nil {
		t.Fatalf("list events after legacy route: %v", err)
	}
	if len(after) != len(before) {
		t.Fatalf("legacy route mutated durable state: before=%d after=%d", len(before), len(after))
	}
}
