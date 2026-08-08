package api

import (
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	sessionruntime "swarm/packages/swarmd/internal/session"
)

func TestCheckpointBoundaryPayloadDoesNotSelectAnotherRunScope(t *testing.T) {
	payload := map[string]any{
		"action":             sessionruntime.CheckpointBoundaryTransitionAction,
		"next_action":        "continue_current_run",
		"checkpoint_id":      "followup-1",
		"next_checkpoint_id": "followup-1",
		"run_id":             "run-current",
		"context_preserved":  true,
	}
	original := sessionV3ProviderCheckpointScope{PlanID: "plan-old", CheckpointID: "cp-old", AttemptID: "attempt-old", ParentSessionID: "session-1"}
	scope := sessionV3ProviderCheckpointScopeFromPayload(original, payload)
	if scope.FreshContext || scope != original {
		t.Fatalf("checkpoint assignment changed current run scope: got %#v want %#v", scope, original)
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
