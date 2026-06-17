package api

import (
	"bytes"
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"testing"

	sessionruntime "swarm/packages/swarmd/internal/session"
	pebblestore "swarm/packages/swarmd/internal/store/pebble"
)

func TestSessionsV3CompactIgnoresLifecycleActiveOnly(t *testing.T) {
	server, sessionSvc, _, _, _ := newRoutedSessionTestServerWithSwarmStore(t)
	created := createSessionsV3PrimaryTestSession(t, server, "compact-lifecycle-only", "Compact Lifecycle Only")
	recordSessionsV3ReconnectLifecycle(t, server, created.ID, true)

	resp := postSessionsV3CompactForTest(t, server, created.ID, "compact-lifecycle-only-request", "run-compact")
	if resp.RunIntent == nil || resp.RunIntent.RunID != "run-compact" || resp.RunIntent.Status != sessionruntime.RunIntentPendingExecutor {
		t.Fatalf("compact response run_intent = %+v", resp.RunIntent)
	}
	if resp.Compaction["run_id"] != "run-compact" || resp.Compaction["status"] != "accepted" {
		t.Fatalf("compact response compaction = %+v", resp.Compaction)
	}
	active, ok, err := sessionSvc.GetSessionActiveRunIntent(created.ID)
	if err != nil || !ok || active.RunID != "run-compact" || active.Status != sessionruntime.RunIntentRunning {
		t.Fatalf("canonical active after compact = %+v ok=%v err=%v", active, ok, err)
	}
}

func TestSessionsV3CompactRejectsDifferentCanonicalActiveRun(t *testing.T) {
	server, _, _, _, _ := newRoutedSessionTestServerWithSwarmStore(t)
	created := createSessionsV3PrimaryTestSession(t, server, "compact-canonical-active", "Compact Canonical Active")
	recordSessionsV3ReconnectRunIntent(t, server, created.ID, "run-active", sessionruntime.RunIntentPendingExecutor, 1000)
	recordSessionsV3ReconnectRunIntent(t, server, created.ID, "run-active", sessionruntime.RunIntentRunning, 2000)
	recordSessionsV3ReconnectLifecycle(t, server, created.ID, false)

	req := httptest.NewRequest(http.MethodPost, "/v3/sessions/"+created.ID+"/compact", bytes.NewBufferString(`{"client_request_id":"compact-conflict","run_id":"run-other"}`))
	req.Header.Set("Content-Type", "application/json")
	rec := httptest.NewRecorder()
	server.Handler().ServeHTTP(rec, withTestPrincipal(req))
	if rec.Code != http.StatusConflict {
		t.Fatalf("compact conflict status = %d, want %d, body=%s", rec.Code, http.StatusConflict, rec.Body.String())
	}
}

type sessionsV3CompactTestResponse struct {
	OK         bool                            `json:"ok"`
	SessionID  string                          `json:"session_id"`
	RunIntent  *pebblestore.V3SessionRunIntent `json:"run_intent"`
	Compaction map[string]any                  `json:"compaction"`
	Mutation   map[string]any                  `json:"mutation"`
}

func postSessionsV3CompactForTest(t *testing.T, server *Server, sessionID, clientRequestID, runID string) sessionsV3CompactTestResponse {
	t.Helper()
	body := fmt.Sprintf(`{"client_request_id":%q,"run_id":%q}`, clientRequestID, runID)
	req := httptest.NewRequest(http.MethodPost, "/v3/sessions/"+sessionID+"/compact", bytes.NewBufferString(body))
	req.Header.Set("Content-Type", "application/json")
	rec := httptest.NewRecorder()
	server.Handler().ServeHTTP(rec, withTestPrincipal(req))
	if rec.Code != http.StatusAccepted {
		t.Fatalf("compact status = %d, want %d, body=%s", rec.Code, http.StatusAccepted, rec.Body.String())
	}
	var resp sessionsV3CompactTestResponse
	if err := json.Unmarshal(rec.Body.Bytes(), &resp); err != nil {
		t.Fatalf("decode compact response: %v", err)
	}
	if !resp.OK || resp.SessionID != sessionID || resp.RunIntent == nil || resp.RunIntent.RunID != runID {
		t.Fatalf("compact response = %+v", resp)
	}
	if resp.Mutation == nil || resp.Mutation["session_id"] != sessionID {
		t.Fatalf("compact response missing mutation: %+v", resp)
	}
	for _, forbidden := range []string{"session", "messages", "events", "workset_id", "worksets", "subscriptions"} {
		if _, exists := resp.Mutation[forbidden]; exists {
			t.Fatalf("compact mutation response included %s: %+v", forbidden, resp.Mutation)
		}
	}
	return resp
}
