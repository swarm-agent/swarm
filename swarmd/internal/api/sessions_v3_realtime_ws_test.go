package api

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"os"
	"strings"
	"testing"
	"time"

	gorillaws "github.com/gorilla/websocket"

	sessionruntime "swarm/packages/swarmd/internal/session"
	pebblestore "swarm/packages/swarmd/internal/store/pebble"
)

func TestV3RealtimePublishesCommittedOutboxEventAndReplaysAfterReconnect(t *testing.T) {
	server, sessionSvc, _, _, _ := newRoutedSessionTestServerWithSwarmStore(t)
	created := createV3RealtimeTestSession(t, server, "session-realtime-a", "create-realtime-a")

	httpServer := newV3RealtimeHTTPTestServer(t, server)
	conn := dialV3RealtimeStream(t, httpServer.URL)
	defer conn.Close()

	writeV3RealtimeMessage(t, conn, V3RealtimeMessage{Protocol: V3RealtimeProtocol, ProtocolVersion: V3RealtimeProtocolVersion, Kind: V3RealtimeKindSubscribe, SessionID: created.ID, SubscriptionID: "sub-a", AfterSeq: 0})
	assertV3RealtimeFrame(t, readV3RealtimeFrame(t, conn), V3RealtimeKindReplayStart, created.ID, 0)
	createdEvent := readV3RealtimeFrame(t, conn)
	assertV3RealtimeFrame(t, createdEvent, V3RealtimeKindEvent, created.ID, 1)
	if createdEvent.EndpointCursor != "cursor-1" || createdEvent.Event.EventType != "session.created" {
		t.Fatalf("created realtime event = %+v", createdEvent)
	}
	assertV3RealtimeFrame(t, readV3RealtimeFrame(t, conn), V3RealtimeKindReplayDone, created.ID, 1)

	appendResult := appendV3RealtimeTestMessage(t, server, created.ID, "message-realtime-a", "hello realtime")
	live := readV3RealtimeFrame(t, conn)
	assertV3RealtimeFrame(t, live, V3RealtimeKindEvent, created.ID, appendResult.Event.Seq)
	if live.EndpointCursor != appendResult.RealtimeOutbox.EndpointCursor || live.Event.EventType != "session.message.appended" {
		t.Fatalf("live realtime event = %+v outbox=%+v", live, appendResult.RealtimeOutbox)
	}

	outboxRows, err := sessionSvc.ListRealtimeOutboxForSessionAfterSeq(created.ID, 0, 10)
	if err != nil {
		t.Fatalf("list realtime outbox rows: %v", err)
	}
	if len(outboxRows) != 2 || outboxRows[0].Event.Seq != 1 || outboxRows[1].Event.Seq != 2 {
		t.Fatalf("outbox rows = %+v, want create and append", outboxRows)
	}

	replayConn := dialV3RealtimeStream(t, httpServer.URL)
	defer replayConn.Close()
	writeV3RealtimeMessage(t, replayConn, V3RealtimeMessage{Protocol: V3RealtimeProtocol, ProtocolVersion: V3RealtimeProtocolVersion, Kind: V3RealtimeKindSubscribe, SessionID: created.ID, SubscriptionID: "sub-a-replay", AfterSeq: 1})
	assertV3RealtimeFrame(t, readV3RealtimeFrame(t, replayConn), V3RealtimeKindReplayStart, created.ID, 0)
	replayedAppend := readV3RealtimeFrame(t, replayConn)
	assertV3RealtimeFrame(t, replayedAppend, V3RealtimeKindEvent, created.ID, appendResult.Event.Seq)
	if replayedAppend.EndpointCursor != appendResult.RealtimeOutbox.EndpointCursor {
		t.Fatalf("replayed endpoint cursor = %q, want %q", replayedAppend.EndpointCursor, appendResult.RealtimeOutbox.EndpointCursor)
	}
	assertV3RealtimeFrame(t, readV3RealtimeFrame(t, replayConn), V3RealtimeKindReplayDone, created.ID, appendResult.Event.Seq)
}

func TestV3RealtimeReplaysCommittedOutboxRowAfterPublishCrashWindow(t *testing.T) {
	server, sessionSvc, _, _, _ := newRoutedSessionTestServerWithSwarmStore(t)
	created := createV3RealtimeTestSession(t, server, "session-realtime-crash", "create-realtime-crash")

	committed, err := sessionSvc.ApplySessionMutation(sessionruntime.SessionMutationInput{
		SessionID:       created.ID,
		UserID:          testPrincipal().UserID,
		AccountScopeID:  testPrincipal().AccountScopeID,
		ClientRequestID: "message-realtime-crash",
		IdempotencyKey:  "message-realtime-crash",
		PayloadHash:     "hash-message-realtime-crash",
		Kind:            sessionruntime.SessionMutationAppendMessage,
		Message:         &pebblestore.MessageSnapshot{Role: "user", Content: "committed before publish"},
		NowUnixMs:       2000,
	})
	if err != nil {
		t.Fatalf("commit mutation before publish: %v", err)
	}
	if committed.RealtimeOutbox == nil || committed.Event.Seq != 2 {
		t.Fatalf("committed mutation missing realtime outbox or seq: %+v", committed)
	}

	httpServer := newV3RealtimeHTTPTestServer(t, server)
	conn := dialV3RealtimeStream(t, httpServer.URL)
	defer conn.Close()
	writeV3RealtimeMessage(t, conn, V3RealtimeMessage{Protocol: V3RealtimeProtocol, ProtocolVersion: V3RealtimeProtocolVersion, Kind: V3RealtimeKindSubscribe, SessionID: created.ID, SubscriptionID: "sub-crash", AfterSeq: 1})
	assertV3RealtimeFrame(t, readV3RealtimeFrame(t, conn), V3RealtimeKindReplayStart, created.ID, 0)
	replayed := readV3RealtimeFrame(t, conn)
	assertV3RealtimeFrame(t, replayed, V3RealtimeKindEvent, created.ID, committed.Event.Seq)
	if replayed.EndpointCursor != committed.RealtimeOutbox.EndpointCursor || replayed.Event.EventType != "session.message.appended" {
		t.Fatalf("crash-window replay = %+v committed outbox=%+v", replayed, committed.RealtimeOutbox)
	}
	assertV3RealtimeFrame(t, readV3RealtimeFrame(t, conn), V3RealtimeKindReplayDone, created.ID, committed.Event.Seq)
}

func TestV3RealtimeSingleConnectionInterleavesSessionsBySessionID(t *testing.T) {
	server, _, _, _, _ := newRoutedSessionTestServerWithSwarmStore(t)
	a := createV3RealtimeTestSession(t, server, "session-realtime-a", "create-realtime-a")
	b := createV3RealtimeTestSession(t, server, "session-realtime-b", "create-realtime-b")

	httpServer := newV3RealtimeHTTPTestServer(t, server)
	conn := dialV3RealtimeStream(t, httpServer.URL)
	defer conn.Close()

	writeV3RealtimeMessage(t, conn, V3RealtimeMessage{Protocol: V3RealtimeProtocol, ProtocolVersion: V3RealtimeProtocolVersion, Kind: V3RealtimeKindSubscribe, SessionID: a.ID, SubscriptionID: "sub-a", AfterSeq: 1})
	assertV3RealtimeFrame(t, readV3RealtimeFrame(t, conn), V3RealtimeKindReplayStart, a.ID, 0)
	assertV3RealtimeFrame(t, readV3RealtimeFrame(t, conn), V3RealtimeKindReplayDone, a.ID, 1)
	writeV3RealtimeMessage(t, conn, V3RealtimeMessage{Protocol: V3RealtimeProtocol, ProtocolVersion: V3RealtimeProtocolVersion, Kind: V3RealtimeKindSubscribe, SessionID: b.ID, SubscriptionID: "sub-b", AfterSeq: 1})
	assertV3RealtimeFrame(t, readV3RealtimeFrame(t, conn), V3RealtimeKindReplayStart, b.ID, 0)
	assertV3RealtimeFrame(t, readV3RealtimeFrame(t, conn), V3RealtimeKindReplayDone, b.ID, 1)

	bAppend := appendV3RealtimeTestMessage(t, server, b.ID, "message-realtime-b", "hello b")
	aAppend := appendV3RealtimeTestMessage(t, server, a.ID, "message-realtime-a", "hello a")

	first := readV3RealtimeFrame(t, conn)
	second := readV3RealtimeFrame(t, conn)
	assertV3RealtimeFrame(t, first, V3RealtimeKindEvent, b.ID, bAppend.Event.Seq)
	assertV3RealtimeFrame(t, second, V3RealtimeKindEvent, a.ID, aAppend.Event.Seq)
	if first.Event.SessionID == second.Event.SessionID || first.Event.Seq != 2 || second.Event.Seq != 2 {
		t.Fatalf("interleaved realtime events = %+v then %+v", first, second)
	}
}

func TestV3RealtimeSourceGuardRequiresNativeOutboxAndRejectsOldTransport(t *testing.T) {
	for _, file := range []string{"sessions_v3_realtime_ws.go", "sessions_v3_realtime_hub.go", "sessions_v3_outbox.go"} {
		body := readSourceFileForTest(t, file)
		if file == "sessions_v3_realtime_ws.go" {
			for _, required := range []string{"V3RealtimeKindSubscribe", "ListRealtimeOutboxForSessionAfterSeq", "sendV3RealtimeOutboxEvent"} {
				if !strings.Contains(body, required) {
					t.Fatalf("%s missing V3-native realtime symbol %q", file, required)
				}
			}
		}
		for _, forbidden := range []string{"EventEnvelope", "sessionV3StreamFrame", "SessionV3StreamFrame", "runStreamManager", "handleRunStream", "handleSessionV3PrimaryStream", "streamSessionV3PrimaryEvents"} {
			if strings.Contains(body, forbidden) {
				t.Fatalf("%s contains forbidden old realtime dependency %q", file, forbidden)
			}
		}
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

func createV3RealtimeTestSession(t *testing.T, server *Server, sessionID, requestID string) pebblestore.SessionSnapshot {
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
	if result.Session == nil {
		t.Fatalf("create realtime test session result missing session: %+v", result)
	}
	return *result.Session
}

func appendV3RealtimeTestMessage(t *testing.T, server *Server, sessionID, requestID, content string) sessionruntime.SessionMutationResult {
	t.Helper()
	result, err := server.applySessionV3PrimaryMutation(sessionruntime.SessionMutationInput{
		SessionID:       sessionID,
		UserID:          testPrincipal().UserID,
		AccountScopeID:  testPrincipal().AccountScopeID,
		ClientRequestID: requestID,
		IdempotencyKey:  requestID,
		PayloadHash:     "hash-" + requestID,
		Kind:            sessionruntime.SessionMutationAppendMessage,
		Message:         &pebblestore.MessageSnapshot{Role: "user", Content: content},
		NowUnixMs:       time.Now().UnixMilli(),
	})
	if err != nil {
		t.Fatalf("append realtime test message %s: %v", requestID, err)
	}
	if result.RealtimeOutbox == nil {
		t.Fatalf("append realtime test message missing realtime outbox: %+v", result)
	}
	return result
}

func dialV3RealtimeStream(t *testing.T, baseURL string) *gorillaws.Conn {
	t.Helper()
	wsURL := "ws" + strings.TrimPrefix(baseURL, "http") + V3RealtimeStreamPath
	conn, resp, err := gorillaws.DefaultDialer.Dial(wsURL, nil)
	if err != nil {
		if resp != nil {
			t.Fatalf("dial v3 realtime stream: %v status=%d", err, resp.StatusCode)
		}
		t.Fatalf("dial v3 realtime stream: %v", err)
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
	if err := ValidateV3RealtimeMessage(frame); err != nil {
		t.Fatalf("invalid v3 realtime frame %s: %v", string(raw), err)
	}
	return frame
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

func readSourceFileForTest(t *testing.T, path string) string {
	t.Helper()
	raw, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("read %s: %v", path, err)
	}
	return string(raw)
}
