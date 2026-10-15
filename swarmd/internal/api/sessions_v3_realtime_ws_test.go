package api

import (
	"encoding/json"

	"net/http"
	"net/http/httptest"
	"net/url"

	"strings"

	"testing"
	"time"

	gorillaws "github.com/gorilla/websocket"

	sessionruntime "swarm/packages/swarmd/internal/session"
	pebblestore "swarm/packages/swarmd/internal/store/pebble"
)

func TestV3RealtimeRejectsInboundServerOnlyFrame(t *testing.T) {
	server, _, _, _, _ := newRoutedSessionTestServerWithSwarmStore(t)
	created := createV3RealtimeTestSessionResult(t, server, "session-realtime-server-only-inbound", "create-realtime-server-only-inbound")
	httpServer := newV3RealtimeHTTPTestServer(t, server)

	conn := dialV3RealtimeStream(t, httpServer.URL)
	defer conn.Close()

	writeV3RealtimeMessage(t, conn, V3RealtimeMessage{Protocol: V3RealtimeProtocol, ProtocolVersion: V3RealtimeProtocolVersion, Kind: V3RealtimeKindHello, EndpointCursor: signedV3RealtimeCursorForTest(t, server, created.RealtimeOutbox.EndpointSeq), SessionID: created.SessionID, SubscriptionID: "echo-me-not", Event: &created.Event, Projection: &created.Projection})
	frame := readV3RealtimeFrame(t, conn)
	assertV3RealtimeFrame(t, frame, V3RealtimeKindAuthDenied, "", 0)
	if frame.ErrorCode != "invalid_message" || !strings.Contains(frame.Error, "not allowed") {
		t.Fatalf("server-only inbound frame = %+v", frame)
	}
	if frame.EndpointCursor != "" || frame.Event != nil || frame.Projection != nil || frame.SubscriptionID != "" || frame.WorksetID != "" || len(frame.Subscriptions) != 0 || len(frame.Worksets) != 0 {
		t.Fatalf("auth.denied echoed client-supplied fields: %+v", frame)
	}
}

func newV3RealtimeHTTPTestServer(t *testing.T, server *Server) *httptest.Server {
	t.Helper()
	httpServer := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		server.Handler().ServeHTTP(w, withTestPrincipal(r))
	}))
	t.Cleanup(httpServer.Close)
	return httpServer
}

func createV3RealtimeTestSessionResult(t *testing.T, server *Server, sessionID, requestID string) sessionruntime.SessionMutationResult {
	t.Helper()
	result, err := server.applySessionV3PrimaryMutation(sessionruntime.SessionMutationInput{
		SessionID:       sessionID,
		UserID:          testPrincipal().UserID,
		AccountScopeID:  testPrincipal().AccountScopeID,
		ClientRequestID: requestID,
		IdempotencyKey:  requestID,
		PayloadHash:     "hash-" + requestID,
		Kind:            sessionruntime.SessionMutationCreateSession,
		Session: &pebblestore.SessionSnapshot{
			ID:             sessionID,
			UserID:         testPrincipal().UserID,
			AccountScopeID: testPrincipal().AccountScopeID,
			WorkspacePath:  "/workspace/realtime",
			WorkspaceName:  "realtime",
			Title:          sessionID,
		},
		NowUnixMs: 1000,
	})
	if err != nil {
		t.Fatalf("create realtime test session %s: %v", sessionID, err)
	}
	if result.RealtimeOutbox == nil {
		t.Fatalf("create realtime test session missing realtime outbox: %+v", result)
	}
	result.RealtimeOutbox.EndpointCursor = signedV3RealtimeCursorForTest(t, server, result.RealtimeOutbox.EndpointSeq)
	return result
}

func signedV3RealtimeCursorForTest(t *testing.T, server *Server, endpointSeq uint64) string {
	t.Helper()
	cursor, err := server.signV3SyncEndpointCursor(v3SyncCursorScopeForRealtime(testPrincipal(), "desktop"), endpointSeq)
	if err != nil {
		t.Fatalf("sign realtime cursor for seq %d: %v", endpointSeq, err)
	}
	return cursor
}

func dialV3RealtimeStream(t *testing.T, baseURL string) *gorillaws.Conn {
	t.Helper()
	return dialV3RealtimeStreamWithQuery(t, baseURL, "")
}

func dialV3RealtimeStreamWithQuery(t *testing.T, baseURL, query string) *gorillaws.Conn {
	t.Helper()
	values, err := url.ParseQuery(strings.TrimPrefix(query, "?"))
	if err != nil {
		t.Fatalf("parse realtime query %q: %v", query, err)
	}
	endpointCursor := values.Get("endpoint_cursor")
	rawSessions := values.Get("sessions")
	values.Del("sessions")
	values.Del("session")
	values.Del("session_id")
	wsURL := "ws" + strings.TrimPrefix(baseURL, "http") + V3RealtimeStreamPath
	if encoded := values.Encode(); encoded != "" {
		wsURL += "?" + encoded
	}
	conn, resp, err := gorillaws.DefaultDialer.Dial(wsURL, nil)
	if err != nil {
		if resp != nil {
			t.Fatalf("dial v3 realtime stream: %v status=%d", err, resp.StatusCode)
		}
		t.Fatalf("dial v3 realtime stream: %v", err)
	}
	for _, sessionID := range strings.Split(rawSessions, ",") {
		sessionID = strings.TrimSpace(sessionID)
		if sessionID == "" {
			continue
		}
		writeV3RealtimeMessage(t, conn, V3RealtimeMessage{Protocol: V3RealtimeProtocol, ProtocolVersion: V3RealtimeProtocolVersion, Kind: V3RealtimeKindSubscribe, SessionID: sessionID, SubscriptionID: "sub-" + sessionID, EndpointCursor: endpointCursor})
	}
	return conn
}

func writeV3RealtimeMessage(t *testing.T, conn *gorillaws.Conn, message V3RealtimeMessage) {
	t.Helper()
	raw, err := json.Marshal(message)
	if err != nil {
		t.Fatalf("marshal v3 realtime message: %v", err)
	}
	if err := conn.WriteMessage(gorillaws.TextMessage, raw); err != nil {
		t.Fatalf("write v3 realtime message: %v", err)
	}
}

func readV3RealtimeFrame(t *testing.T, conn *gorillaws.Conn) V3RealtimeMessage {
	t.Helper()
	for {
		frame := readV3RealtimeFrameIncludingHello(t, conn)
		if frame.Kind == V3RealtimeKindHello {
			continue
		}
		return frame
	}
}

func readV3RealtimeFrameIncludingHello(t *testing.T, conn *gorillaws.Conn) V3RealtimeMessage {
	t.Helper()
	if err := conn.SetReadDeadline(time.Now().Add(2 * time.Second)); err != nil {
		t.Fatalf("set v3 realtime read deadline: %v", err)
	}
	_, raw, err := conn.ReadMessage()
	if err != nil {
		t.Fatalf("read v3 realtime frame: %v", err)
	}
	var frame V3RealtimeMessage
	if err := json.Unmarshal(raw, &frame); err != nil {
		t.Fatalf("decode v3 realtime frame %s: %v", string(raw), err)
	}
	if err := ValidateV3RealtimeOutboundServerMessage(frame); err != nil {
		t.Fatalf("invalid v3 realtime frame %s: %v", string(raw), err)
	}
	return frame
}

func assertV3RealtimeCursorError(t *testing.T, frame V3RealtimeMessage, code string) {
	t.Helper()
	if frame.Protocol != V3RealtimeProtocol || frame.ProtocolVersion != V3RealtimeProtocolVersion || frame.Kind != V3RealtimeKindCursorError {
		t.Fatalf("cursor error frame = %+v, want protocol=%s version=%d kind=%s", frame, V3RealtimeProtocol, V3RealtimeProtocolVersion, V3RealtimeKindCursorError)
	}
	if frame.ErrorCode != code {
		t.Fatalf("cursor error code = %q, want %q: %+v", frame.ErrorCode, code, frame)
	}
}

func assertV3RealtimeFrame(t *testing.T, frame V3RealtimeMessage, kind, sessionID string, lastSeq uint64) {
	t.Helper()
	if frame.Protocol != V3RealtimeProtocol || frame.ProtocolVersion != V3RealtimeProtocolVersion || frame.Kind != kind || frame.SessionID != sessionID {
		t.Fatalf("frame = %+v, want protocol=%s version=%d kind=%s session=%s", frame, V3RealtimeProtocol, V3RealtimeProtocolVersion, kind, sessionID)
	}
	if kind == V3RealtimeKindEvent {
		if frame.Event == nil || frame.Event.SessionID != sessionID || frame.Event.Seq != lastSeq || frame.LastSeq != lastSeq {
			t.Fatalf("event frame = %+v, want session=%s seq=%d", frame, sessionID, lastSeq)
		}
		return
	}
	if lastSeq != 0 && frame.LastSeq != lastSeq {
		t.Fatalf("frame last_seq = %d, want %d: %+v", frame.LastSeq, lastSeq, frame)
	}
}
