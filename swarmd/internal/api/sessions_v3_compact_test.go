package api

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"swarm/packages/swarmd/internal/permission"
	runruntime "swarm/packages/swarmd/internal/run"
	sessionruntime "swarm/packages/swarmd/internal/session"
	pebblestore "swarm/packages/swarmd/internal/store/pebble"
	"swarm/packages/swarmd/internal/tool"
)

func TestSessionsV3CompactUsesDirectCompactionCheckpointCursor(t *testing.T) {
	server, sessionSvc, _, _, _ := newRoutedSessionTestServerWithSwarmStore(t)
	created := createSessionsV3PrimaryTestSession(t, server, "compact-direct", "Compact Direct")
	runner := &directCompactRunService{summary: "direct compact summary"}
	server.runner = runner

	resp := postSessionsV3CompactForTest(t, server, created.ID, "compact-direct-request", "run-compact")
	if runner.compactCalls.Load() != 1 {
		t.Fatalf("direct compact calls = %d, want 1", runner.compactCalls.Load())
	}
	if runner.streamingCalls.Load() != 0 {
		t.Fatalf("RunTurnStreaming calls = %d, want 0", runner.streamingCalls.Load())
	}
	if runner.includeAssistantAck.Load() {
		t.Fatal("compact endpoint requested duplicate assistant acknowledgement")
	}
	if resp.RunIntent == nil || resp.RunIntent.RunID != "run-compact" || resp.RunIntent.Status != sessionruntime.RunIntentCompleted {
		t.Fatalf("compact response run_intent = %+v", resp.RunIntent)
	}
	if resp.RealtimeOutbox == nil || resp.RealtimeOutbox.EndpointCursor == "" || resp.RealtimeOutbox.Event.EventType != "session.message.appended" {
		t.Fatalf("compact response missing checkpoint outbox: %+v", resp.RealtimeOutbox)
	}
	scope, scopeErr := sessionsV3SelectedSessionHydrateCursorScope(testPrincipal(), created.ID)
	if scopeErr != nil {
		t.Fatalf("selected-session hydrate cursor scope: %v", scopeErr)
	}
	if !strings.Contains(scope.ResourceSet, "active_plan") {
		t.Fatalf("selected-session hydrate cursor scope missing active_plan: %+v", scope)
	}
	endpointSeq, legacyCursor, err := server.parseV3SyncEndpointCursor(resp.RealtimeOutbox.EndpointCursor, scope)
	if err != nil || legacyCursor || endpointSeq != resp.RealtimeOutbox.EndpointSeq {
		t.Fatalf("compact response cursor does not match selected-session hydrate scope: seq=%d legacy=%v err=%v outbox=%+v", endpointSeq, legacyCursor, err, resp.RealtimeOutbox)
	}
	checkpointID, _ := resp.CompactCheckpoint["message_id"].(string)
	if checkpointID == "" || checkpointID != resp.EventPayloadMessageID(t) {
		t.Fatalf("checkpoint id=%q outbox=%+v", checkpointID, resp.RealtimeOutbox)
	}
	if resp.CompactCheckpoint["endpoint_cursor"] == "" || resp.CompactCheckpoint["endpoint_seq"] == float64(0) {
		t.Fatalf("checkpoint cursor fields missing: %+v", resp.CompactCheckpoint)
	}
	if resp.AssistantMutation != nil {
		t.Fatalf("compact response should not include duplicate assistant ack mutation: %+v", resp.AssistantMutation)
	}
	messages, err := sessionSvc.ListSessionMessages(created.ID, 0, 20)
	if err != nil {
		t.Fatalf("list v3 messages: %v", err)
	}
	foundCheckpoint := false
	for _, msg := range messages {
		if msg.ID == checkpointID && msg.Role == "system" && msg.Content != "" {
			foundCheckpoint = true
		}
		if msg.Role == "assistant" && strings.HasPrefix(strings.TrimSpace(msg.Content), "Manual context compact complete (Compact #") {
			t.Fatalf("manual compact appended duplicate assistant ack: %+v", msg)
		}
	}
	if !foundCheckpoint {
		t.Fatalf("committed checkpoint message %q not visible in v3 messages: %+v", checkpointID, messages)
	}
	if active, ok, err := sessionSvc.GetSessionActiveRunIntent(created.ID); err != nil {
		t.Fatalf("active intent lookup: %v", err)
	} else if ok {
		t.Fatalf("compact left active run intent: %+v", active)
	}
}

func TestSessionsV3CompactWaitsForDirectCompactionReturn(t *testing.T) {
	server, _, _, _, _ := newRoutedSessionTestServerWithSwarmStore(t)
	created := createSessionsV3PrimaryTestSession(t, server, "compact-wait", "Compact Wait")
	started := make(chan struct{})
	release := make(chan struct{})
	runner := &directCompactRunService{summary: "wait summary", started: started, release: release}
	server.runner = runner

	done := make(chan *httptest.ResponseRecorder, 1)
	go func() {
		req := httptest.NewRequest(http.MethodPost, "/v3/sessions/"+created.ID+"/compact", bytes.NewBufferString(`{"client_request_id":"compact-wait-request","run_id":"run-compact"}`))
		req.Header.Set("Content-Type", "application/json")
		rec := httptest.NewRecorder()
		server.Handler().ServeHTTP(rec, withTestPrincipal(req))
		done <- rec
	}()
	select {
	case <-started:
	case <-time.After(2 * time.Second):
		t.Fatal("direct compaction did not start")
	}
	select {
	case rec := <-done:
		t.Fatalf("compact returned before direct compaction completed: status=%d body=%s", rec.Code, rec.Body.String())
	case <-time.After(50 * time.Millisecond):
	}
	close(release)
	select {
	case rec := <-done:
		if rec.Code != http.StatusAccepted {
			t.Fatalf("compact status = %d, body=%s", rec.Code, rec.Body.String())
		}
	case <-time.After(2 * time.Second):
		t.Fatal("compact did not return after direct compaction release")
	}
}

func TestSessionsV3CompactDoesNotReturnFakeSuccessOnDirectFailure(t *testing.T) {
	server, _, _, _, _ := newRoutedSessionTestServerWithSwarmStore(t)
	created := createSessionsV3PrimaryTestSession(t, server, "compact-failure", "Compact Failure")
	server.runner = &directCompactRunService{err: errors.New("memory compact boom")}

	req := httptest.NewRequest(http.MethodPost, "/v3/sessions/"+created.ID+"/compact", bytes.NewBufferString(`{"client_request_id":"compact-failure-request","run_id":"run-compact"}`))
	req.Header.Set("Content-Type", "application/json")
	rec := httptest.NewRecorder()
	server.Handler().ServeHTTP(rec, withTestPrincipal(req))
	if rec.Code != http.StatusInternalServerError {
		t.Fatalf("compact failure status = %d, want %d, body=%s", rec.Code, http.StatusInternalServerError, rec.Body.String())
	}
	var body map[string]any
	if err := json.Unmarshal(rec.Body.Bytes(), &body); err != nil {
		t.Fatalf("decode failure body: %v", err)
	}
	if body["ok"] == true || body["status"] == sessionruntime.RunIntentCompleted {
		t.Fatalf("compact failure returned fake success: %+v", body)
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
	OK                bool                                `json:"ok"`
	SessionID         string                              `json:"session_id"`
	RunID             string                              `json:"run_id"`
	Status            string                              `json:"status"`
	RunIntent         *pebblestore.V3SessionRunIntent     `json:"run_intent"`
	Compaction        map[string]any                      `json:"compaction"`
	CompactCheckpoint map[string]any                      `json:"compact_checkpoint"`
	Terminal          map[string]any                      `json:"terminal"`
	Mutation          map[string]any                      `json:"mutation"`
	RealtimeOutbox    *pebblestore.V3RealtimeOutboxRecord `json:"realtime_outbox"`
	AssistantMutation map[string]any                      `json:"assistant_mutation"`
}

func (r sessionsV3CompactTestResponse) EventPayloadMessageID(t *testing.T) string {
	t.Helper()
	var payload struct {
		MessageID string `json:"message_id"`
		Message   *struct {
			ID string `json:"id"`
		} `json:"message"`
	}
	if err := json.Unmarshal(r.RealtimeOutbox.Event.Payload, &payload); err != nil {
		t.Fatalf("decode outbox payload: %v", err)
	}
	if payload.MessageID != "" {
		return payload.MessageID
	}
	if payload.Message != nil {
		return payload.Message.ID
	}
	return ""
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
		t.Fatalf("compact response missing checkpoint mutation: %+v", resp)
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

type directCompactRunService struct {
	summary             string
	err                 error
	started             chan struct{}
	release             chan struct{}
	compactCalls        atomic.Int32
	streamingCalls      atomic.Int32
	includeAssistantAck atomic.Bool
}

func (r *directCompactRunService) RunManualCompaction(_ context.Context, sessionID string, input runruntime.ManualCompactionInput) (runruntime.ManualCompactionResult, error) {
	r.compactCalls.Add(1)
	r.includeAssistantAck.Store(input.IncludeAssistantAck)
	if r.started != nil {
		close(r.started)
	}
	if r.release != nil {
		<-r.release
	}
	if r.err != nil {
		return runruntime.ManualCompactionResult{}, r.err
	}
	summary := r.summary
	if summary == "" {
		summary = "direct compact summary"
	}
	now := time.Now().UnixMilli()
	message := pebblestore.MessageSnapshot{ID: "", SessionID: sessionID, UserID: input.Principal.UserID, AccountScopeID: input.Principal.AccountScopeID, Role: "system", Content: "[context-compact] index=2 origin=manual\n\n" + summary, CreatedAt: now}
	clientRequestID := "test-direct-compact:" + input.RunID + ":checkpoint"
	mutation, err := input.ApplySessionMutation(sessionruntime.SessionMutationInput{SessionID: sessionID, UserID: input.Principal.UserID, AccountScopeID: input.Principal.AccountScopeID, ClientRequestID: clientRequestID, IdempotencyKey: clientRequestID, PayloadHash: "hash-" + clientRequestID, RequestHash: "hash-" + clientRequestID, Kind: sessionruntime.SessionMutationAppendMessage, EventType: "session.message.appended", Message: &message, NowUnixMs: now})
	if err != nil {
		return runruntime.ManualCompactionResult{}, err
	}
	if mutation.Message != nil {
		message = *mutation.Message
	}
	return runruntime.ManualCompactionResult{Summary: summary, CompactIndex: 2, CheckpointMessage: message, CheckpointMutation: mutation}, nil
}

func (r *directCompactRunService) RunTurn(context.Context, string, runruntime.RunRequest, runruntime.RunStartMeta) (runruntime.RunResult, error) {
	return runruntime.RunResult{}, errors.New("RunTurn should not be used by compact endpoint")
}

func (r *directCompactRunService) RunTurnStreaming(context.Context, string, runruntime.RunRequest, runruntime.RunStartMeta, runruntime.StreamHandler) (runruntime.RunResult, error) {
	r.streamingCalls.Add(1)
	return runruntime.RunResult{}, errors.New("RunTurnStreaming should not be used by compact endpoint")
}

func (r *directCompactRunService) StopSessionRun(string, string, string) error { return nil }

func (r *directCompactRunService) ExecuteToolForSessionScope(context.Context, string, tool.Call) (string, error) {
	return "", nil
}

func (r *directCompactRunService) ListAgentToolDefinitions() []tool.Definition { return nil }

func (r *directCompactRunService) ListAgentToolDefinitionsForAccount(string) []tool.Definition {
	return nil
}

func (r *directCompactRunService) ResolveAgentToolContract(pebblestore.AgentProfile) (runruntime.ResolvedAgentToolContract, *permission.Policy, map[string]bool, error) {
	return runruntime.ResolvedAgentToolContract{}, nil, nil, nil
}

func (r *directCompactRunService) ResolveAgentToolContractForAccount(string, pebblestore.AgentProfile) (runruntime.ResolvedAgentToolContract, *permission.Policy, map[string]bool, error) {
	return runruntime.ResolvedAgentToolContract{}, nil, nil, nil
}
