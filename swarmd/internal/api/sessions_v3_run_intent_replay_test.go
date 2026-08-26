package api

import (
	"bytes"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"testing"

	sessionruntime "swarm/packages/swarmd/internal/session"
	pebblestore "swarm/packages/swarmd/internal/store/pebble"
)

func TestSessionsV3PrimaryEventsReplayProjectsLatestRunIntentState(t *testing.T) {
	server, _, _, _, _ := newRoutedSessionTestServerWithSwarmStore(t)
	created := createSessionsV3PrimaryTestSessionWithWorkspace(t, server, "run-intent-replay-create", "Run intent replay", t.TempDir())

	transitions := []struct {
		status    string
		eventType string
	}{
		{status: sessionruntime.RunIntentPendingExecutor, eventType: "session.run_intent.recorded"},
		{status: sessionruntime.RunIntentRunning, eventType: "session.assistant.started"},
		{status: sessionruntime.RunIntentCompleted, eventType: "session.assistant.completed"},
	}
	for index, transition := range transitions {
		clientRequestID := "run-intent-replay-" + transition.status
		_, err := server.applySessionV3PrimaryMutation(sessionruntime.SessionMutationInput{
			SessionID:       created.ID,
			UserID:          testPrincipal().UserID,
			AccountScopeID:  testPrincipal().AccountScopeID,
			ClientRequestID: clientRequestID,
			IdempotencyKey:  clientRequestID,
			PayloadHash:     clientRequestID,
			RequestHash:     clientRequestID,
			Kind:            sessionruntime.SessionMutationRecordRunIntent,
			EventType:       transition.eventType,
			RunIntent: &pebblestore.V3SessionRunIntent{
				RunID:  "run-plan-auto-terminal",
				Status: transition.status,
			},
			NowUnixMs: int64(2000 + index),
		})
		if err != nil {
			t.Fatalf("record %s run transition: %v", transition.status, err)
		}
	}

	req := httptest.NewRequest(http.MethodGet, "/v3/sessions/"+created.ID+"/events?after_seq=0&limit=100", bytes.NewReader(nil))
	rec := httptest.NewRecorder()
	server.Handler().ServeHTTP(rec, withTestPrincipal(req))
	if rec.Code != http.StatusOK {
		t.Fatalf("events status = %d, want %d, body=%s", rec.Code, http.StatusOK, rec.Body.String())
	}
	var replay struct {
		RunIntents []sessionruntime.SessionRunIntent `json:"run_intents"`
	}
	if err := json.Unmarshal(rec.Body.Bytes(), &replay); err != nil {
		t.Fatalf("decode replay response: %v", err)
	}
	if len(replay.RunIntents) != 1 || replay.RunIntents[0].RunID != "run-plan-auto-terminal" || replay.RunIntents[0].Status != sessionruntime.RunIntentCompleted {
		t.Fatalf("replayed run intents = %+v, want one completed latest-state projection", replay.RunIntents)
	}
}
