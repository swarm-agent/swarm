package api

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"net/http/httptest"
	"testing"

	"swarm/packages/swarmd/internal/permission"
	runruntime "swarm/packages/swarmd/internal/run"
	sessionruntime "swarm/packages/swarmd/internal/session"
	pebblestore "swarm/packages/swarmd/internal/store/pebble"
	"swarm/packages/swarmd/internal/tool"
)

func TestSessionsV3CompactWaitsForTerminalLifecycleCursor(t *testing.T) {
	server, sessionSvc, _, _, _ := newRoutedSessionTestServerWithSwarmStore(t)
	created := createSessionsV3PrimaryTestSession(t, server, "compact-terminal", "Compact Terminal")
	server.runner = compactTerminalRunService{events: []runruntime.StreamEvent{
		{Type: runruntime.StreamEventSessionLifecycle, Lifecycle: &pebblestore.SessionLifecycleSnapshot{SessionID: created.ID, RunID: "run-compact", Active: true, Phase: "running", OwnerTransport: sessionV3ManualCompactOwnerTransport}},
		{Type: runruntime.StreamEventSessionLifecycle, Lifecycle: &pebblestore.SessionLifecycleSnapshot{SessionID: created.ID, RunID: "run-compact", Active: false, Phase: "completed", OwnerTransport: sessionV3ManualCompactOwnerTransport}},
	}}

	resp := postSessionsV3CompactForTest(t, server, created.ID, "compact-terminal-request", "run-compact")
	if resp.RunIntent == nil || resp.RunIntent.RunID != "run-compact" || resp.RunIntent.Status != sessionruntime.RunIntentCompleted {
		t.Fatalf("compact response run_intent = %+v", resp.RunIntent)
	}
	if resp.Status != sessionruntime.RunIntentCompleted || resp.Compaction["status"] != sessionruntime.RunIntentCompleted {
		t.Fatalf("compact response status=%q compaction=%+v", resp.Status, resp.Compaction)
	}
	if resp.Terminal["event_type"] != "session.lifecycle.updated" || resp.Terminal["phase"] != "completed" {
		t.Fatalf("compact terminal = %+v", resp.Terminal)
	}
	if resp.RealtimeOutbox == nil || resp.RealtimeOutbox.EndpointCursor == "" || resp.RealtimeOutbox.Event.EventType != "session.lifecycle.updated" {
		t.Fatalf("compact response missing terminal outbox: %+v", resp.RealtimeOutbox)
	}
	active, ok, err := sessionSvc.GetSessionActiveRunIntent(created.ID)
	if err != nil {
		t.Fatalf("active intent lookup: %v", err)
	}
	if ok {
		t.Fatalf("compact left active run intent: %+v", active)
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
	OK             bool                                `json:"ok"`
	SessionID      string                              `json:"session_id"`
	RunID          string                              `json:"run_id"`
	Status         string                              `json:"status"`
	RunIntent      *pebblestore.V3SessionRunIntent     `json:"run_intent"`
	Compaction     map[string]any                      `json:"compaction"`
	Terminal       map[string]any                      `json:"terminal"`
	Mutation       map[string]any                      `json:"mutation"`
	RealtimeOutbox *pebblestore.V3RealtimeOutboxRecord `json:"realtime_outbox"`
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
	if resp.RealtimeOutbox == nil || resp.RealtimeOutbox.SessionID != sessionID || resp.RealtimeOutbox.EndpointCursor == "" {
		t.Fatalf("compact response missing realtime outbox: %+v", resp)
	}
	for _, forbidden := range []string{"session", "messages", "events", "workset_id", "worksets", "subscriptions"} {
		if _, exists := resp.Mutation[forbidden]; exists {
			t.Fatalf("compact mutation response included %s: %+v", forbidden, resp.Mutation)
		}
	}
	return resp
}

type compactTerminalRunService struct {
	events []runruntime.StreamEvent
	err    error
}

func (r compactTerminalRunService) RunTurn(context.Context, string, runruntime.RunRequest, runruntime.RunStartMeta) (runruntime.RunResult, error) {
	return runruntime.RunResult{}, errors.New("RunTurn should not be used by compact endpoint")
}

func (r compactTerminalRunService) RunTurnStreaming(_ context.Context, sessionID string, request runruntime.RunRequest, meta runruntime.RunStartMeta, onEvent runruntime.StreamHandler) (runruntime.RunResult, error) {
	for _, event := range r.events {
		if event.SessionID == "" {
			event.SessionID = sessionID
		}
		if event.RunID == "" {
			event.RunID = meta.RunID
		}
		if onEvent != nil {
			onEvent(event)
		}
	}
	if r.err != nil {
		return runruntime.RunResult{}, r.err
	}
	return runruntime.RunResult{SessionID: sessionID, Background: request.Background, TargetKind: request.TargetKind, TargetName: request.TargetName}, nil
}

func (r compactTerminalRunService) StopSessionRun(string, string, string) error { return nil }

func (r compactTerminalRunService) ExecuteToolForSessionScope(context.Context, string, tool.Call) (string, error) {
	return "", nil
}

func (r compactTerminalRunService) ListAgentToolDefinitions() []tool.Definition { return nil }

func (r compactTerminalRunService) ListAgentToolDefinitionsForAccount(string) []tool.Definition {
	return nil
}

func (r compactTerminalRunService) ResolveAgentToolContract(pebblestore.AgentProfile) (runruntime.ResolvedAgentToolContract, *permission.Policy, map[string]bool, error) {
	return runruntime.ResolvedAgentToolContract{}, nil, nil, nil
}

func (r compactTerminalRunService) ResolveAgentToolContractForAccount(string, pebblestore.AgentProfile) (runruntime.ResolvedAgentToolContract, *permission.Policy, map[string]bool, error) {
	return runruntime.ResolvedAgentToolContract{}, nil, nil, nil
}
