package api

import (
	"bytes"
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"
)

func TestSessionV3StartCreatesFirstMessageAndConnectionContract(t *testing.T) {
	server, _, closeStore := newSessionsV3PrimaryAPITestServer(t, t.TempDir())
	defer func() { _ = closeStore() }()
	server.SetDataDir(t.TempDir())

	bindingID := seedSessionsV3PrimaryAuthority(t, server, "/workspace/start")
	body := `{"client_id":"desktop:start","request_id":"start-request-1","session_id":"start-session-1","title":"Started Session","workspace_path":"/workspace/start","swarm_id":"host-swarm-id","workspace_binding_id":"` + bindingID + `","target_kind":"host","target_relationship":"self","agent_name":"swarm","mode":"auto","first_message":{"message_id":"msg-start-1","run_id":"run-start-1","role":"user","content":"hello start"}}`
	req := httptest.NewRequest(http.MethodPost, "/v3/sessions:start", bytes.NewBufferString(body))
	req.Header.Set("Content-Type", "application/json")
	rec := httptest.NewRecorder()
	server.Handler().ServeHTTP(rec, withTestPrincipal(req))
	if rec.Code != http.StatusOK {
		t.Fatalf("start status = %d body=%s", rec.Code, rec.Body.String())
	}
	var resp SessionStartResponse
	if err := json.Unmarshal(rec.Body.Bytes(), &resp); err != nil {
		t.Fatalf("decode start response: %v", err)
	}
	if !resp.Ok || resp.ContractVersion != SessionConnectionContractVersion || resp.SessionId != "start-session-1" {
		t.Fatalf("start response identity = %+v", resp)
	}
	if resp.Snapshot.EventSeq < 2 || len(resp.Snapshot.Messages) != 1 {
		t.Fatalf("start snapshot = %+v", resp.Snapshot)
	}
	if resp.Connection.Protocol != SessionConnectionProtocol || resp.Connection.ResumeToken == "" || strings.Contains(resp.Connection.StreamUrl, "/v3/realtime/stream") {
		t.Fatalf("start connection = %+v", resp.Connection)
	}
	if resp.Run.RunId != "run-start-1" || resp.Run.Phase != RunPhaseAccepted || resp.AcceptedEventSeq == 0 {
		t.Fatalf("start accepted run = %+v accepted_seq=%d", resp.Run, resp.AcceptedEventSeq)
	}
	if len(resp.Message) == 0 {
		t.Fatalf("start response missing message")
	}
}

func TestSessionV3StartRetriesCreateAndMessageWithoutDuplicateExecutorEnqueue(t *testing.T) {
	server, sessionSvc, closeStore := newSessionsV3PrimaryAPITestServer(t, t.TempDir())
	defer func() { _ = closeStore() }()
	server.SetDataDir(t.TempDir())
	workspace := t.TempDir()
	bindingID := seedSessionsV3PrimaryAuthority(t, server, workspace)
	body := sessionV3StartTestBody(bindingID, workspace, "start-retry-request-1", "start-retry-session-1", "msg-start-retry-1", "run-start-retry-1", "hello retry", nil)

	rec := postSessionV3StartForTest(t, server, body)
	if rec.Code != http.StatusOK {
		t.Fatalf("first start status = %d body=%s", rec.Code, rec.Body.String())
	}
	rec = postSessionV3StartForTest(t, server, body)
	if rec.Code != http.StatusOK {
		t.Fatalf("replayed start status = %d body=%s", rec.Code, rec.Body.String())
	}
	messages, err := sessionSvc.ListSessionMessages("start-retry-session-1", 0, 10)
	if err != nil {
		t.Fatalf("list messages after replay: %v", err)
	}
	if len(messages) != 1 {
		t.Fatalf("messages after replay = %+v", messages)
	}

	mismatch := sessionV3StartTestBody(bindingID, workspace, "start-retry-request-1", "start-retry-session-1", "msg-start-retry-1", "run-start-retry-1", "changed retry", nil)
	rec = postSessionV3StartForTest(t, server, mismatch)
	if rec.Code != http.StatusConflict {
		t.Fatalf("conflicting replay status = %d body=%s", rec.Code, rec.Body.String())
	}
	if strings.Contains(rec.Body.String(), "\"conflict\"") || strings.Contains(rec.Body.String(), "\"result\"") {
		t.Fatalf("start conflict leaked mutation internals: %s", rec.Body.String())
	}
}

func TestSessionV3StartRetriesAfterCreateSucceededMessageFailed(t *testing.T) {
	server, sessionSvc, closeStore := newSessionsV3PrimaryAPITestServer(t, t.TempDir())
	defer func() { _ = closeStore() }()
	server.SetDataDir(t.TempDir())
	workspace := t.TempDir()
	bindingID := seedSessionsV3PrimaryAuthority(t, server, workspace)
	invalid := sessionV3StartTestBody(bindingID, workspace, "start-partial-request-1", "start-partial-session-1", "msg-start-partial-1", "run-start-partial-1", "hello partial", map[string]any{"metadata": map[string]any{"agent_name": "reserved"}})

	rec := postSessionV3StartForTest(t, server, invalid)
	if rec.Code != http.StatusBadRequest {
		t.Fatalf("partial first status = %d body=%s", rec.Code, rec.Body.String())
	}
	if _, ok, err := sessionSvc.GetSession("start-partial-session-1"); err != nil {
		t.Fatalf("get partial session: %v", err)
	} else if !ok {
		t.Fatalf("create mutation did not persist before message failure")
	}
	messages, err := sessionSvc.ListSessionMessages("start-partial-session-1", 0, 10)
	if err != nil {
		t.Fatalf("list messages after partial failure: %v", err)
	}
	if len(messages) != 0 {
		t.Fatalf("messages after partial failure = %+v", messages)
	}
	session, ok, err := sessionSvc.GetSession("start-partial-session-1")
	if err != nil || !ok {
		t.Fatalf("get session after partial failure ok=%v err=%v", ok, err)
	}
	if session.Preference.Provider != "test-provider" || session.Preference.Model != "test-model" {
		t.Fatalf("session preference after partial failure = %+v", session.Preference)
	}

	server.v3SessionExecutor = newSessionV3Executor(server)
	server.v3SessionExecutor.startDelay = 0
	valid := sessionV3StartTestBody(bindingID, workspace, "start-partial-request-1", "start-partial-session-1", "msg-start-partial-1", "run-start-partial-1", "hello partial", nil)
	rec = postSessionV3StartForTest(t, server, valid)
	if rec.Code != http.StatusOK {
		t.Fatalf("partial retry status = %d body=%s", rec.Code, rec.Body.String())
	}
	waitForSessionsV3MessageCount(t, sessionSvc, "start-partial-session-1", 2)
	if !server.WaitForInFlightRuns(2 * time.Second) {
		t.Fatalf("executor did not drain")
	}
	messages, err = sessionSvc.ListSessionMessages("start-partial-session-1", 0, 10)
	if err != nil {
		t.Fatalf("list messages after partial retry: %v", err)
	}
	if len(messages) != 2 {
		t.Fatalf("messages after partial retry = %+v", messages)
	}

	rec = postSessionV3StartForTest(t, server, valid)
	if rec.Code != http.StatusOK {
		t.Fatalf("partial replay status = %d body=%s", rec.Code, rec.Body.String())
	}
	messages, err = sessionSvc.ListSessionMessages("start-partial-session-1", 0, 10)
	if err != nil {
		t.Fatalf("list messages after partial replay: %v", err)
	}
	if len(messages) != 2 {
		t.Fatalf("messages after partial replay = %+v", messages)
	}
	events, err := sessionSvc.ListSessionEvents("start-partial-session-1", 0, 50)
	if err != nil {
		t.Fatalf("list events after partial replay: %v", err)
	}
	var completed int
	for _, event := range events {
		if event.EventType == "session.assistant.completed" {
			completed++
		}
	}
	if completed != 1 {
		t.Fatalf("assistant completed events after partial replay = %d events=%+v", completed, events)
	}
}

func TestSessionV3MessageConflictReturnsTypedContractError(t *testing.T) {
	server, _, closeStore := newSessionsV3PrimaryAPITestServer(t, t.TempDir())
	defer func() { _ = closeStore() }()
	created := createSessionsV3PrimaryTestSession(t, server, "message-conflict-create", "message conflict")

	body := `{"client_request_id":"message-conflict-1","message_id":"msg-conflict-1","run_id":"run-conflict-1","role":"user","content":"hello conflict"}`
	rec := postSessionV3MessageForTest(t, server, created.ID, body)
	if rec.Code != http.StatusOK {
		t.Fatalf("message status = %d body=%s", rec.Code, rec.Body.String())
	}
	conflict := `{"client_request_id":"message-conflict-1","message_id":"msg-conflict-1","run_id":"run-conflict-1","role":"user","content":"changed conflict"}`
	rec = postSessionV3MessageForTest(t, server, created.ID, conflict)
	if rec.Code != http.StatusConflict {
		t.Fatalf("message conflict status = %d body=%s", rec.Code, rec.Body.String())
	}
	var contractErr SessionConnectionError
	if err := json.Unmarshal(rec.Body.Bytes(), &contractErr); err != nil {
		t.Fatalf("decode conflict error: %v", err)
	}
	if contractErr.Code != "idempotency_conflict" || contractErr.Message == "" || contractErr.Retryable || contractErr.Action.Method != http.MethodPost {
		t.Fatalf("conflict error = %+v", contractErr)
	}
	if strings.Contains(rec.Body.String(), "\"conflict\"") || strings.Contains(rec.Body.String(), "\"result\"") {
		t.Fatalf("message conflict leaked mutation internals: %s", rec.Body.String())
	}
}

func TestSessionV3MessageReturnsTypedAcceptedResponse(t *testing.T) {
	server, _, closeStore := newSessionsV3PrimaryAPITestServer(t, t.TempDir())
	defer func() { _ = closeStore() }()
	created := createSessionsV3PrimaryTestSession(t, server, "message-accepted-create", "message accepted")

	body := `{"client_request_id":"message-accepted-1","message_id":"msg-accepted-1","run_id":"run-accepted-1","role":"user","content":"hello accepted"}`
	req := httptest.NewRequest(http.MethodPost, "/v3/sessions/"+created.ID+"/messages", bytes.NewBufferString(body))
	req.Header.Set("Content-Type", "application/json")
	rec := httptest.NewRecorder()
	server.Handler().ServeHTTP(rec, withTestPrincipal(req))
	if rec.Code != http.StatusOK {
		t.Fatalf("message status = %d body=%s", rec.Code, rec.Body.String())
	}
	var resp SessionMessageAcceptedResponse
	if err := json.Unmarshal(rec.Body.Bytes(), &resp); err != nil {
		t.Fatalf("decode message response: %v", err)
	}
	if !resp.Ok || resp.SessionId != created.ID || resp.Run.RunId != "run-accepted-1" || resp.Run.Phase != RunPhaseAccepted || resp.AcceptedEventSeq == 0 {
		t.Fatalf("message accepted response = %+v", resp)
	}
	if strings.Contains(rec.Body.String(), "realtime_outbox") || strings.Contains(rec.Body.String(), "run_intent") {
		t.Fatalf("message response leaked legacy fields: %s", rec.Body.String())
	}
}

func postSessionV3StartForTest(t *testing.T, server *Server, body string) *httptest.ResponseRecorder {
	t.Helper()
	req := httptest.NewRequest(http.MethodPost, "/v3/sessions:start", bytes.NewBufferString(body))
	req.Header.Set("Content-Type", "application/json")
	rec := httptest.NewRecorder()
	server.Handler().ServeHTTP(rec, withTestPrincipal(req))
	return rec
}

func postSessionV3MessageForTest(t *testing.T, server *Server, sessionID, body string) *httptest.ResponseRecorder {
	t.Helper()
	req := httptest.NewRequest(http.MethodPost, "/v3/sessions/"+sessionID+"/messages", bytes.NewBufferString(body))
	req.Header.Set("Content-Type", "application/json")
	rec := httptest.NewRecorder()
	server.Handler().ServeHTTP(rec, withTestPrincipal(req))
	return rec
}

func sessionV3StartTestBody(bindingID, workspacePath, requestID, sessionID, messageID, runID, content string, firstMessageOverrides map[string]any) string {
	firstMessage := map[string]any{
		"message_id": messageID,
		"run_id":     runID,
		"role":       "user",
		"content":    content,
	}
	for key, value := range firstMessageOverrides {
		firstMessage[key] = value
	}
	rawFirstMessage, _ := json.Marshal(firstMessage)
	return fmt.Sprintf(`{"client_id":"desktop:start-test","request_id":%q,"session_id":%q,"title":"Start Test","workspace_path":%q,"swarm_id":"host-swarm-id","workspace_binding_id":%q,"target_kind":"host","target_relationship":"self","agent_name":"swarm","mode":"auto","preference":{"provider":"test-provider","model":"test-model","thinking":"medium"},"first_message":%s}`, requestID, sessionID, workspacePath, bindingID, string(rawFirstMessage))
}
