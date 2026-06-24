package api

import (
	"bytes"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
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
