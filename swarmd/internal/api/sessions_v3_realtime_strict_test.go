package api

import (
	"net/url"
	"strings"
	"testing"

	gorillaws "github.com/gorilla/websocket"
)

func TestV3RealtimeRejectsLegacyEndpointCursors(t *testing.T) {
	server, _, _, _, _ := newRoutedSessionTestServerWithSwarmStore(t)
	created := createV3RealtimeTestSessionResult(t, server, "session-realtime-legacy-cursor", "create-realtime-legacy-cursor")
	httpServer := newV3RealtimeHTTPTestServer(t, server)

	conn := dialV3RealtimeStreamWithQuery(t, httpServer.URL, "endpoint_cursor=cursor-1")
	defer conn.Close()
	assertV3RealtimeCursorError(t, readV3RealtimeFrame(t, conn), "endpoint_cursor_legacy_unsupported")

	subConn := dialV3RealtimeStream(t, httpServer.URL)
	defer subConn.Close()
	writeV3RealtimeMessage(t, subConn, V3RealtimeMessage{Protocol: V3RealtimeProtocol, ProtocolVersion: V3RealtimeProtocolVersion, Kind: V3RealtimeKindSubscribe, SessionID: created.SessionID, SubscriptionID: "sub-legacy", EndpointCursor: "cursor-1"})
	assertV3RealtimeCursorError(t, readV3RealtimeFrame(t, subConn), "endpoint_cursor_legacy_unsupported")
}

func TestV3RealtimeRejectsQueryStringSessionSubscriptions(t *testing.T) {
	server, _, _, _, _ := newRoutedSessionTestServerWithSwarmStore(t)
	created := createV3RealtimeTestSessionResult(t, server, "session-realtime-query-sub", "create-realtime-query-sub")
	httpServer := newV3RealtimeHTTPTestServer(t, server)
	wsURL := "ws" + strings.TrimPrefix(httpServer.URL, "http") + V3RealtimeStreamPath + "?endpoint_cursor=" + url.QueryEscape(signedV3RealtimeCursorForTest(t, server, created.RealtimeOutbox.EndpointSeq)) + "&sessions=" + url.QueryEscape(created.SessionID)
	conn, resp, err := gorillaws.DefaultDialer.Dial(wsURL, nil)
	if err != nil {
		if resp != nil {
			t.Fatalf("dial realtime query subscription: %v status=%d", err, resp.StatusCode)
		}
		t.Fatalf("dial realtime query subscription: %v", err)
	}
	defer conn.Close()
	frame := readV3RealtimeFrame(t, conn)
	assertV3RealtimeFrame(t, frame, V3RealtimeKindAuthDenied, "", 0)
	if frame.ErrorCode != "query_session_subscriptions_unsupported" {
		t.Fatalf("query subscription frame = %+v", frame)
	}
}

func TestV3RealtimeResumeRejectsUnknownSelectorKindWithoutLegacyWorkset(t *testing.T) {
	server, _, _, _, _ := newRoutedSessionTestServerWithSwarmStore(t)
	created := createV3RealtimeTestSessionResult(t, server, "session-realtime-bad-selector", "create-realtime-bad-selector")
	httpServer := newV3RealtimeHTTPTestServer(t, server)
	conn := dialV3RealtimeStream(t, httpServer.URL)
	defer conn.Close()
	writeV3RealtimeMessage(t, conn, V3RealtimeMessage{Protocol: V3RealtimeProtocol, ProtocolVersion: V3RealtimeProtocolVersion, Kind: V3RealtimeKindResume, EndpointCursor: signedV3RealtimeCursorForTest(t, server, created.RealtimeOutbox.EndpointSeq), Worksets: []V3RealtimeWorksetSubscriptionRequest{{WorksetID: "bad-kind", SubscriptionID: "bad-kind-sub", Selector: V3RealtimeWorksetSelector{Kind: "everything", Global: true}, AutoSubscribeSessions: true}}})
	frame := readV3RealtimeFrame(t, conn)
	assertV3RealtimeFrame(t, frame, V3RealtimeKindAuthDenied, "", 0)
	if frame.ErrorCode != "invalid_message" || !strings.Contains(frame.Error, `unsupported v3 realtime selector.kind "everything"`) {
		t.Fatalf("unknown selector frame = %+v", frame)
	}
}
