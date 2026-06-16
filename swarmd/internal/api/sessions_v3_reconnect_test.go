package api

import (
	"bytes"
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"

	sessionruntime "swarm/packages/swarmd/internal/session"
	pebblestore "swarm/packages/swarmd/internal/store/pebble"
)

type sessionsV3ReconnectTestPayload struct {
	OK                        bool                                           `json:"ok"`
	Rev                       uint64                                         `json:"rev"`
	SnapshotEndpointCursor    string                                         `json:"snapshot_endpoint_cursor"`
	SessionsByID              map[string]pebblestore.SessionSnapshot         `json:"sessions_by_id"`
	ProjectionsBySession      map[string]pebblestore.V3SessionProjection     `json:"projections_by_session"`
	RunIntentsBySession       map[string][]pebblestore.V3SessionRunIntent    `json:"run_intents_by_session"`
	CurrentRunIntentBySession map[string]pebblestore.V3SessionRunIntent      `json:"current_run_intent_by_session"`
	Subscriptions             []sessionsV3ReconnectSubscription              `json:"subscriptions"`
	Worksets                  []V3RealtimeWorksetSubscriptionRequest         `json:"worksets"`
	SessionOrder              []string                                       `json:"session_order"`
	DiagnosticsBySession      map[string][]sessionsV3ReconnectDiagnostic     `json:"diagnostics_by_session"`
	ClientID                  string                                         `json:"client_id"`
	Surface                   string                                         `json:"surface"`
	WorksetID                 string                                         `json:"workset_id"`
	Realtime                  sessionsV3ReconnectRealtimeInstructionTestWire `json:"realtime"`
}

type sessionsV3ReconnectRealtimeInstructionTestWire struct {
	StreamPath string            `json:"stream_path"`
	Resume     V3RealtimeMessage `json:"resume"`
}

func TestSessionsV3ReconnectIncludesPendingExecutorFromDurableRunIntent(t *testing.T) {
	server, _, _, _, _ := newRoutedSessionTestServerWithSwarmStore(t)
	created := createSessionsV3PrimaryTestSession(t, server, "reconnect-pending", "Reconnect Pending")
	recordSessionsV3ReconnectRunIntent(t, server, created.ID, "run-pending", sessionruntime.RunIntentPendingExecutor, 1000)

	payload := postSessionsV3Reconnect(t, server)
	if !payload.OK {
		t.Fatalf("reconnect ok = false")
	}
	if payload.SessionsByID[created.ID].ID != created.ID {
		t.Fatalf("pending session missing from sessions_by_id: %+v", payload.SessionsByID)
	}
	current := payload.CurrentRunIntentBySession[created.ID]
	if current.RunID != "run-pending" || current.Status != sessionruntime.RunIntentPendingExecutor {
		t.Fatalf("current intent = %+v", current)
	}
	assertSessionsV3ReconnectSubscription(t, payload, created.ID)
}

func TestSessionsV3ReconnectIncludesRunningAndExcludesTerminal(t *testing.T) {
	server, _, _, _, _ := newRoutedSessionTestServerWithSwarmStore(t)
	runningSession := createSessionsV3PrimaryTestSession(t, server, "reconnect-running", "Reconnect Running")
	terminalSession := createSessionsV3PrimaryTestSession(t, server, "reconnect-terminal", "Reconnect Terminal")
	recordSessionsV3ReconnectRunIntent(t, server, runningSession.ID, "run-running", sessionruntime.RunIntentPendingExecutor, 1000)
	recordSessionsV3ReconnectRunIntent(t, server, runningSession.ID, "run-running", sessionruntime.RunIntentRunning, 2000)
	recordSessionsV3ReconnectRunIntent(t, server, terminalSession.ID, "run-terminal", sessionruntime.RunIntentPendingExecutor, 3000)
	recordSessionsV3ReconnectRunIntent(t, server, terminalSession.ID, "run-terminal", sessionruntime.RunIntentCompleted, 4000)

	payload := postSessionsV3Reconnect(t, server)
	if payload.SessionsByID[runningSession.ID].ID != runningSession.ID {
		t.Fatalf("running session missing: %+v", payload.SessionsByID)
	}
	if got := payload.CurrentRunIntentBySession[runningSession.ID]; got.RunID != "run-running" || got.Status != sessionruntime.RunIntentRunning {
		t.Fatalf("running current intent = %+v", got)
	}
	if _, ok := payload.SessionsByID[terminalSession.ID]; ok {
		t.Fatalf("terminal session must be inactive in reconnect response: %+v", payload.SessionsByID[terminalSession.ID])
	}
	assertSessionsV3ReconnectSubscription(t, payload, runningSession.ID)
}

func TestSessionsV3ReconnectExcludesLifecycleActiveOnlySession(t *testing.T) {
	server, _, _, _, _ := newRoutedSessionTestServerWithSwarmStore(t)
	created := createSessionsV3PrimaryTestSession(t, server, "reconnect-lifecycle", "Reconnect Lifecycle")
	recordSessionsV3ReconnectLifecycle(t, server, created.ID, true)

	payload := postSessionsV3Reconnect(t, server)
	if _, ok := payload.SessionsByID[created.ID]; ok {
		t.Fatalf("lifecycle.active-only session must not be active in reconnect response: %+v", payload.SessionsByID[created.ID])
	}
	if len(payload.Subscriptions) != 0 {
		t.Fatalf("subscriptions = %+v, want none", payload.Subscriptions)
	}
}

func TestSessionsV3ReconnectIncludesCanonicalActiveWithStaleInactiveLifecycle(t *testing.T) {
	server, _, _, _, _ := newRoutedSessionTestServerWithSwarmStore(t)
	created := createSessionsV3PrimaryTestSession(t, server, "reconnect-stale-lifecycle", "Reconnect Stale Lifecycle")
	recordSessionsV3ReconnectRunIntent(t, server, created.ID, "run-canonical", sessionruntime.RunIntentPendingExecutor, 1000)
	recordSessionsV3ReconnectRunIntent(t, server, created.ID, "run-canonical", sessionruntime.RunIntentRunning, 2000)
	recordSessionsV3ReconnectLifecycle(t, server, created.ID, false)

	payload := postSessionsV3Reconnect(t, server)
	if payload.SessionsByID[created.ID].ID != created.ID {
		t.Fatalf("canonical active session missing with stale inactive lifecycle: %+v", payload.SessionsByID)
	}
	current := payload.CurrentRunIntentBySession[created.ID]
	if current.RunID != "run-canonical" || current.Status != sessionruntime.RunIntentRunning {
		t.Fatalf("current canonical intent = %+v", current)
	}
	assertSessionsV3ReconnectSubscription(t, payload, created.ID)
}

func TestSessionsV3ReconnectOrdersSessionsDeterministically(t *testing.T) {
	server, _, _, _, _ := newRoutedSessionTestServerWithSwarmStore(t)
	older := createSessionsV3PrimaryTestSession(t, server, "reconnect-order-older", "Reconnect Order Older")
	newer := createSessionsV3PrimaryTestSession(t, server, "reconnect-order-newer", "Reconnect Order Newer")
	recordSessionsV3ReconnectRunIntent(t, server, older.ID, "run-older", sessionruntime.RunIntentPendingExecutor, 1000)
	recordSessionsV3ReconnectRunIntent(t, server, newer.ID, "run-newer", sessionruntime.RunIntentPendingExecutor, 2000)

	payload := postSessionsV3Reconnect(t, server)
	if len(payload.SessionOrder) < 2 || payload.SessionOrder[0] != newer.ID || payload.SessionOrder[1] != older.ID {
		t.Fatalf("session_order = %+v, want newest active run intent first", payload.SessionOrder)
	}
}

func TestSessionsV3ReconnectWorksetContractIncludesRealtimeResume(t *testing.T) {
	server, _, _, _, _ := newRoutedSessionTestServerWithSwarmStore(t)
	created := createSessionsV3PrimaryTestSessionWithWorkspace(t, server, "reconnect-workset-create", "Reconnect Workset", "/workspace/reconnect-workset")
	recordSessionsV3ReconnectRunIntent(t, server, created.ID, "run-workset", sessionruntime.RunIntentPendingExecutor, 2000)
	recordSessionsV3ReconnectRunIntent(t, server, created.ID, "run-workset", sessionruntime.RunIntentRunning, 3000)

	payload := postSessionsV3ReconnectBody(t, server, `{
		"surface":"desktop",
		"client_id":"desktop-client-1",
		"workset":{
			"workset_id":"desktop:global",
			"selector":{"kind":"global","global":true,"recent":{"limit":10}},
			"resources":{"messages":true,"events":true,"run_intents":true},
			"include_active":true,
			"auto_subscribe_sessions":true
		}
	}`)
	if payload.ClientID != "desktop-client-1" || payload.Surface != "desktop" || payload.WorksetID != "desktop:global" {
		t.Fatalf("client/workset identity = client %q surface %q workset %q", payload.ClientID, payload.Surface, payload.WorksetID)
	}
	if payload.SessionsByID[created.ID].ID != created.ID {
		t.Fatalf("workset reconnect missing created session: %+v", payload.SessionsByID)
	}
	if payload.SnapshotEndpointCursor == "" {
		t.Fatalf("workset reconnect missing snapshot_endpoint_cursor")
	}
	if len(payload.Worksets) != 1 {
		t.Fatalf("worksets = %+v, want one workset subscription", payload.Worksets)
	}
	workset := payload.Worksets[0]
	if workset.WorksetID != "desktop:global" || workset.SubscriptionID == "" || !workset.AutoSubscribeSessions || workset.Selector.Kind != "global" || !workset.Selector.Global {
		t.Fatalf("workset subscription = %+v", workset)
	}
	assertSessionsV3ReconnectSubscription(t, payload, created.ID)
	if payload.Realtime.StreamPath != V3RealtimeStreamPath {
		t.Fatalf("realtime stream_path = %q", payload.Realtime.StreamPath)
	}
	resume := payload.Realtime.Resume
	if resume.Protocol != V3RealtimeProtocol || resume.ProtocolVersion != V3RealtimeProtocolVersion || resume.Kind != V3RealtimeKindResume || resume.EndpointCursor != payload.SnapshotEndpointCursor {
		t.Fatalf("resume frame = %+v snapshot=%q", resume, payload.SnapshotEndpointCursor)
	}
	if len(resume.Worksets) != 1 || resume.Worksets[0].WorksetID != "desktop:global" || !resume.Worksets[0].AutoSubscribeSessions {
		t.Fatalf("resume worksets = %+v", resume.Worksets)
	}
	if len(resume.Subscriptions) == 0 || resume.Subscriptions[0].SessionID == "" || resume.Subscriptions[0].EndpointCursor != payload.SnapshotEndpointCursor {
		t.Fatalf("resume subscriptions = %+v snapshot=%q", resume.Subscriptions, payload.SnapshotEndpointCursor)
	}
	if err := ValidateV3RealtimeMessage(resume); err != nil {
		t.Fatalf("reconnect realtime resume rejected by contract: %v", err)
	}
}

func TestSessionsV3ReconnectWorksetRequiresClientID(t *testing.T) {
	server, _, _, _, _ := newRoutedSessionTestServerWithSwarmStore(t)
	req := httptest.NewRequest(http.MethodPost, "/v3/sessions:reconnect", bytes.NewBufferString(`{"workset":{"selector":{"kind":"global","global":true,"recent":{"limit":10}},"auto_subscribe_sessions":true}}`))
	req.Header.Set("Content-Type", "application/json")
	rec := httptest.NewRecorder()
	server.Handler().ServeHTTP(rec, withTestPrincipal(req))
	if rec.Code != http.StatusBadRequest {
		t.Fatalf("reconnect without client_id status = %d, want %d, body=%s", rec.Code, http.StatusBadRequest, rec.Body.String())
	}
}

func postSessionsV3Reconnect(t *testing.T, server *Server) sessionsV3ReconnectTestPayload {
	t.Helper()
	return postSessionsV3ReconnectBody(t, server, `{}`)
}

func postSessionsV3ReconnectBody(t *testing.T, server *Server, body string) sessionsV3ReconnectTestPayload {
	t.Helper()
	req := httptest.NewRequest(http.MethodPost, "/v3/sessions:reconnect", bytes.NewBufferString(body))
	req.Header.Set("Content-Type", "application/json")
	rec := httptest.NewRecorder()
	server.Handler().ServeHTTP(rec, withTestPrincipal(req))
	if rec.Code != http.StatusOK {
		t.Fatalf("reconnect status = %d, want %d, body=%s", rec.Code, http.StatusOK, rec.Body.String())
	}
	var payload sessionsV3ReconnectTestPayload
	if err := json.Unmarshal(rec.Body.Bytes(), &payload); err != nil {
		t.Fatalf("decode reconnect response: %v", err)
	}
	return payload
}

func recordSessionsV3ReconnectRunIntent(t *testing.T, server *Server, sessionID, runID, status string, updatedAt int64) {
	t.Helper()
	clientRequestID := fmt.Sprintf("reconnect-%s-%s-%d", runID, status, updatedAt)
	if _, err := server.applySessionV3PrimaryMutation(sessionruntime.SessionMutationInput{
		SessionID:       sessionID,
		UserID:          testPrincipal().UserID,
		AccountScopeID:  testPrincipal().AccountScopeID,
		ClientRequestID: clientRequestID,
		IdempotencyKey:  clientRequestID,
		PayloadHash:     clientRequestID + "-hash",
		RequestHash:     clientRequestID + "-hash",
		Kind:            sessionruntime.SessionMutationRecordRunIntent,
		EventType:       "session.run_intent.recorded",
		RunIntent: &pebblestore.V3SessionRunIntent{
			RunID:     runID,
			Status:    status,
			UpdatedAt: updatedAt,
		},
		NowUnixMs: updatedAt,
	}); err != nil {
		t.Fatalf("record run intent %s/%s: %v", runID, status, err)
	}
}

func recordSessionsV3ReconnectLifecycle(t *testing.T, server *Server, sessionID string, active bool) {
	t.Helper()
	now := time.Now().UnixMilli()
	clientRequestID := fmt.Sprintf("reconnect-lifecycle-%s-%t", sessionID, active)
	if _, err := server.applySessionV3PrimaryMutation(sessionruntime.SessionMutationInput{
		SessionID:       sessionID,
		UserID:          testPrincipal().UserID,
		AccountScopeID:  testPrincipal().AccountScopeID,
		ClientRequestID: clientRequestID,
		IdempotencyKey:  clientRequestID,
		PayloadHash:     clientRequestID + "-hash",
		RequestHash:     clientRequestID + "-hash",
		Kind:            sessionruntime.SessionMutationUpsertLifecycle,
		EventType:       "session.lifecycle.updated",
		Lifecycle: &pebblestore.SessionLifecycleSnapshot{
			RunID:     "run-lifecycle-only",
			Active:    active,
			Phase:     "running",
			UpdatedAt: now,
		},
		NowUnixMs: now,
	}); err != nil {
		t.Fatalf("record lifecycle: %v", err)
	}
}

func assertSessionsV3ReconnectSubscription(t *testing.T, payload sessionsV3ReconnectTestPayload, sessionID string) {
	t.Helper()
	for _, sub := range payload.Subscriptions {
		if sub.SessionID != sessionID {
			continue
		}
		if sub.Protocol != V3RealtimeProtocol || sub.ProtocolVersion != V3RealtimeProtocolVersion || sub.Kind != V3RealtimeKindSubscribe {
			t.Fatalf("subscription protocol fields = %+v", sub)
		}
		if sub.SubscriptionID == "" || sub.EndpointCursor == "" || sub.EndpointCursor != payload.SnapshotEndpointCursor {
			t.Fatalf("subscription cursor/id = %+v, snapshot=%q", sub, payload.SnapshotEndpointCursor)
		}
		return
	}
	t.Fatalf("subscription missing for %q: %+v", sessionID, payload.Subscriptions)
}
