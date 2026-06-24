package api

import (
	"bytes"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	gorillaws "github.com/gorilla/websocket"
)

func TestSessionV3ConnectReturnsSnapshotAndConnectionContract(t *testing.T) {
	server, _, closeStore := newSessionsV3PrimaryAPITestServer(t, t.TempDir())
	defer func() { _ = closeStore() }()
	server.SetDataDir(t.TempDir())
	created := createSessionsV3PrimaryTestSession(t, server, "session-connect-create", "connect contract")
	appendSessionsV3PrimaryTestUserMessage(t, server, created.ID, "session-connect-message", "hello connect")

	body := `{"client_id":"desktop:test","request_id":"request-connect-1","resume_token":null}`
	req := httptest.NewRequest(http.MethodPost, "/v3/sessions/"+created.ID+":connect", bytes.NewBufferString(body))
	req.Header.Set("Content-Type", "application/json")
	rec := httptest.NewRecorder()
	server.Handler().ServeHTTP(rec, withTestPrincipal(req))
	if rec.Code != http.StatusOK {
		t.Fatalf("connect status = %d body=%s", rec.Code, rec.Body.String())
	}
	var resp SessionConnectResponse
	if err := json.Unmarshal(rec.Body.Bytes(), &resp); err != nil {
		t.Fatalf("decode connect response: %v", err)
	}
	if !resp.Ok || resp.ContractVersion != SessionConnectionContractVersion || resp.SessionId != created.ID {
		t.Fatalf("connect response identity = %+v", resp)
	}
	if resp.Snapshot.EventSeq < 2 || len(resp.Snapshot.Messages) != 1 || resp.Connection.Protocol != SessionConnectionProtocol || resp.Connection.Transport != "websocket" || resp.Connection.ResumeToken == "" {
		t.Fatalf("connect response contract = %+v", resp)
	}
	if !strings.HasPrefix(resp.Connection.StreamUrl, sessionConnectionStreamPrefix) || strings.Contains(resp.Connection.StreamUrl, "/v3/realtime/stream") {
		t.Fatalf("stream_url = %q", resp.Connection.StreamUrl)
	}
}

func TestSessionConnectionStreamReplaysThenReadyAndLiveEvents(t *testing.T) {
	server, _, closeStore := newSessionsV3PrimaryAPITestServer(t, t.TempDir())
	defer func() { _ = closeStore() }()
	server.SetDataDir(t.TempDir())
	created := createSessionsV3PrimaryTestSession(t, server, "session-stream-create", "stream contract")
	connectResp := postSessionV3ConnectForTest(t, server, created.ID, "desktop:stream", "request-stream-1", nil)
	appendSessionsV3PrimaryTestUserMessage(t, server, created.ID, "session-stream-replay", "replayed")

	httpServer := httptest.NewServer(server.Handler())
	defer httpServer.Close()
	conn := dialSessionConnectionStreamForTest(t, httpServer.URL, connectResp.Connection.StreamUrl)
	defer conn.Close()

	first := readSessionConnectionFrameForTest(t, conn)
	if first["type"] != "session.event" || first["session_id"] != created.ID {
		t.Fatalf("first frame = %+v", first)
	}
	ready := readSessionConnectionFrameForTest(t, conn)
	if ready["type"] != "session.ready" || ready["session_id"] != created.ID || strings.TrimSpace(asStringMapValue(ready, "resume_token")) == "" {
		t.Fatalf("ready frame = %+v", ready)
	}

	appendSessionsV3PrimaryTestUserMessage(t, server, created.ID, "session-stream-live", "live")
	live := readSessionConnectionFrameForTest(t, conn)
	if live["type"] != "session.event" || live["session_id"] != created.ID {
		t.Fatalf("live frame = %+v", live)
	}
}

func postSessionV3ConnectForTest(t *testing.T, server *Server, sessionID, clientID, requestID string, resumeToken *string) SessionConnectResponse {
	t.Helper()
	payload := SessionConnectRequest{ClientId: clientID, RequestId: requestID, ResumeToken: resumeToken}
	raw, err := json.Marshal(payload)
	if err != nil {
		t.Fatalf("marshal connect request: %v", err)
	}
	req := httptest.NewRequest(http.MethodPost, "/v3/sessions/"+sessionID+":connect", bytes.NewReader(raw))
	req.Header.Set("Content-Type", "application/json")
	rec := httptest.NewRecorder()
	server.Handler().ServeHTTP(rec, withTestPrincipal(req))
	if rec.Code != http.StatusOK {
		t.Fatalf("connect status=%d body=%s", rec.Code, rec.Body.String())
	}
	var resp SessionConnectResponse
	if err := json.Unmarshal(rec.Body.Bytes(), &resp); err != nil {
		t.Fatalf("decode connect response: %v", err)
	}
	return resp
}

func dialSessionConnectionStreamForTest(t *testing.T, baseURL, streamURL string) *gorillaws.Conn {
	t.Helper()
	wsURL := "ws" + strings.TrimPrefix(baseURL, "http") + streamURL
	conn, resp, err := gorillaws.DefaultDialer.Dial(wsURL, nil)
	if err != nil {
		if resp != nil {
			t.Fatalf("dial session connection stream: %v status=%d", err, resp.StatusCode)
		}
		t.Fatalf("dial session connection stream: %v", err)
	}
	return conn
}

func readSessionConnectionFrameForTest(t *testing.T, conn *gorillaws.Conn) map[string]any {
	t.Helper()
	_, raw, err := conn.ReadMessage()
	if err != nil {
		t.Fatalf("read session connection frame: %v", err)
	}
	var frame map[string]any
	if err := json.Unmarshal(raw, &frame); err != nil {
		t.Fatalf("decode session connection frame %s: %v", string(raw), err)
	}
	return frame
}

func asStringMapValue(values map[string]any, key string) string {
	text, _ := values[key].(string)
	return text
}
