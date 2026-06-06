package api

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	gorillaws "github.com/gorilla/websocket"

	sessionruntime "swarm/packages/swarmd/internal/session"
	pebblestore "swarm/packages/swarmd/internal/store/pebble"
)

func TestSessionsV3CommittedMutationPublishesGlobalEventEnvelope(t *testing.T) {
	server, sessionSvc, _, _, _ := newRoutedSessionTestServerWithSwarmStore(t)
	created := createGlobalV3TestSession(t, sessionSvc, "session-global-envelope", "create-global-envelope")

	result, err := sessionSvc.ApplySessionMutation(sessionruntime.SessionMutationInput{
		SessionID:       created.ID,
		UserID:          testPrincipal().UserID,
		AccountScopeID:  testPrincipal().AccountScopeID,
		ClientRequestID: "message-global-envelope",
		IdempotencyKey:  "message-global-envelope",
		PayloadHash:     "hash-message-global-envelope",
		Kind:            sessionruntime.SessionMutationAppendMessage,
		Message:         &pebblestore.MessageSnapshot{Role: "user", Content: "global v3 envelope"},
		NowUnixMs:       2000,
	})
	if err != nil {
		t.Fatalf("apply v3 mutation: %v", err)
	}
	if err := server.publishCommittedSessionV3MutationResult(result); err != nil {
		t.Fatalf("publish v3 mutation globally: %v", err)
	}

	events, err := server.events.ReadFrom(1, 20)
	if err != nil {
		t.Fatalf("read global event log: %v", err)
	}
	var got *pebblestore.EventEnvelope
	for i := range events {
		if events[i].Stream == "session:"+created.ID && events[i].EventType == result.Event.EventType {
			got = &events[i]
			break
		}
	}
	if got == nil {
		t.Fatalf("missing global V3 event envelope for %s in %+v", result.Event.EventType, events)
	}
	if got.EntityID != created.ID || got.EventType != "session.message.appended" {
		t.Fatalf("global envelope = %+v", *got)
	}
	assertJSONEqualRaw(t, got.Payload, result.Event.Payload)
}

func TestSessionsV3TitleMutationPublishesCanonicalGlobalV3Payload(t *testing.T) {
	server, sessionSvc, _, _, _ := newRoutedSessionTestServerWithSwarmStore(t)
	created := createGlobalV3TestSession(t, sessionSvc, "session-global-title", "create-global-title")
	created.Title = "Generated global title"
	created.UpdatedAt = 3000

	result, err := sessionSvc.ApplySessionMutation(sessionruntime.SessionMutationInput{
		SessionID:       created.ID,
		UserID:          testPrincipal().UserID,
		AccountScopeID:  testPrincipal().AccountScopeID,
		ClientRequestID: "title-global-title",
		IdempotencyKey:  "title-global-title",
		PayloadHash:     "hash-title-global-title",
		Kind:            sessionruntime.SessionMutationUpdateTitle,
		Session:         &created,
		NowUnixMs:       3000,
	})
	if err != nil {
		t.Fatalf("apply title mutation: %v", err)
	}
	if err := server.publishCommittedSessionV3MutationResult(result); err != nil {
		t.Fatalf("publish title mutation globally: %v", err)
	}

	events, err := server.events.ReadFrom(1, 20)
	if err != nil {
		t.Fatalf("read global event log: %v", err)
	}
	for _, event := range events {
		if event.Stream != "session:"+created.ID || event.EventType != "session.title.updated" {
			continue
		}
		assertJSONEqualRaw(t, event.Payload, result.Event.Payload)
		if !strings.Contains(string(event.Payload), "Generated global title") {
			t.Fatalf("title payload does not contain generated title: %s", string(event.Payload))
		}
		return
	}
	t.Fatalf("missing session.title.updated global envelope in %+v", events)
}

func TestSessionsV3ReplayedMutationDoesNotDuplicateGlobalEvent(t *testing.T) {
	server, sessionSvc, _, _, _ := newRoutedSessionTestServerWithSwarmStore(t)
	created := createGlobalV3TestSession(t, sessionSvc, "session-global-idempotent", "create-global-idempotent")
	input := sessionruntime.SessionMutationInput{
		SessionID:       created.ID,
		UserID:          testPrincipal().UserID,
		AccountScopeID:  testPrincipal().AccountScopeID,
		ClientRequestID: "message-global-idempotent",
		IdempotencyKey:  "message-global-idempotent",
		PayloadHash:     "hash-message-global-idempotent",
		Kind:            sessionruntime.SessionMutationAppendMessage,
		Message:         &pebblestore.MessageSnapshot{Role: "user", Content: "idempotent global v3"},
		NowUnixMs:       4000,
	}

	first, err := sessionSvc.ApplySessionMutation(input)
	if err != nil {
		t.Fatalf("apply first mutation: %v", err)
	}
	if err := server.publishCommittedSessionV3MutationResult(first); err != nil {
		t.Fatalf("publish first mutation: %v", err)
	}
	afterFirst := server.events.CurrentSequence()

	replayed, err := sessionSvc.ApplySessionMutation(input)
	if err != nil {
		t.Fatalf("replay mutation: %v", err)
	}
	if !replayed.Replayed {
		t.Fatalf("second mutation was not marked replayed: %+v", replayed)
	}
	if err := server.publishCommittedSessionV3MutationResult(replayed); err != nil {
		t.Fatalf("publish replayed mutation: %v", err)
	}
	if got := server.events.CurrentSequence(); got != afterFirst {
		t.Fatalf("global sequence after replay = %d, want unchanged %d", got, afterFirst)
	}
}

func TestSessionsV3CommittedMutationReachesGlobalWebsocketSessionWildcard(t *testing.T) {
	server, sessionSvc, _, _, _ := newRoutedSessionTestServerWithSwarmStore(t)
	created := createGlobalV3TestSession(t, sessionSvc, "session-global-ws", "create-global-ws")

	httpServer := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		server.Handler().ServeHTTP(w, withTestPrincipal(r))
	}))
	defer httpServer.Close()

	conn, _, err := gorillaws.DefaultDialer.Dial("ws"+strings.TrimPrefix(httpServer.URL, "http")+"/ws", nil)
	if err != nil {
		t.Fatalf("dial global websocket: %v", err)
	}
	defer conn.Close()
	readGlobalWSFrame(t, conn, "connected")
	writeGlobalWSFrame(t, conn, map[string]any{"type": "subscribe", "channel": "session:*"})
	readGlobalWSFrame(t, conn, "subscribed")

	result, err := sessionSvc.ApplySessionMutation(sessionruntime.SessionMutationInput{
		SessionID:       created.ID,
		UserID:          testPrincipal().UserID,
		AccountScopeID:  testPrincipal().AccountScopeID,
		ClientRequestID: "message-global-ws",
		IdempotencyKey:  "message-global-ws",
		PayloadHash:     "hash-message-global-ws",
		Kind:            sessionruntime.SessionMutationAppendMessage,
		Message:         &pebblestore.MessageSnapshot{Role: "user", Content: "live global websocket"},
		NowUnixMs:       5000,
	})
	if err != nil {
		t.Fatalf("apply websocket mutation: %v", err)
	}
	if err := server.publishCommittedSessionV3MutationResult(result); err != nil {
		t.Fatalf("publish websocket mutation: %v", err)
	}

	frame := readGlobalWSFrame(t, conn, "event")
	if frame.Event == nil {
		t.Fatalf("global websocket event frame missing event: %+v", frame)
	}
	if frame.Event.Stream != "session:"+created.ID || frame.Event.EventType != "session.message.appended" || frame.Event.EntityID != created.ID {
		t.Fatalf("global websocket event = %+v", *frame.Event)
	}
	assertJSONEqualRaw(t, frame.Event.Payload, result.Event.Payload)
}

func createGlobalV3TestSession(t *testing.T, sessionSvc *sessionruntime.Service, sessionID, requestID string) pebblestore.SessionSnapshot {
	t.Helper()
	result, err := sessionSvc.ApplySessionMutation(sessionruntime.SessionMutationInput{
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
			WorkspacePath:  "/workspace/global-v3",
			WorkspaceName:  "global-v3",
			Title:          sessionID,
		},
		NowUnixMs: 1000,
	})
	if err != nil {
		t.Fatalf("create global v3 test session %s: %v", sessionID, err)
	}
	if result.Session == nil {
		t.Fatalf("create global v3 result missing session: %+v", result)
	}
	return *result.Session
}

type globalWSFrame struct {
	Type  string                     `json:"type"`
	OK    bool                       `json:"ok,omitempty"`
	Event *pebblestore.EventEnvelope `json:"event,omitempty"`
}

func readGlobalWSFrame(t *testing.T, conn *gorillaws.Conn, wantType string) globalWSFrame {
	t.Helper()
	if err := conn.SetReadDeadline(time.Now().Add(2 * time.Second)); err != nil {
		t.Fatalf("set read deadline: %v", err)
	}
	_, raw, err := conn.ReadMessage()
	if err != nil {
		t.Fatalf("read global websocket frame %q: %v", wantType, err)
	}
	var frame globalWSFrame
	if err := json.Unmarshal(raw, &frame); err != nil {
		t.Fatalf("decode global websocket frame %q: %v raw=%s", wantType, err, string(raw))
	}
	if frame.Type != wantType {
		t.Fatalf("global websocket frame type = %q, want %q raw=%s", frame.Type, wantType, string(raw))
	}
	return frame
}

func writeGlobalWSFrame(t *testing.T, conn *gorillaws.Conn, frame map[string]any) {
	t.Helper()
	if err := conn.SetWriteDeadline(time.Now().Add(2 * time.Second)); err != nil {
		t.Fatalf("set write deadline: %v", err)
	}
	if err := conn.WriteJSON(frame); err != nil {
		t.Fatalf("write global websocket frame: %v", err)
	}
}

func assertJSONEqualRaw(t *testing.T, got, want json.RawMessage) {
	t.Helper()
	var gotValue any
	if err := json.Unmarshal(got, &gotValue); err != nil {
		t.Fatalf("decode got JSON %s: %v", string(got), err)
	}
	var wantValue any
	if err := json.Unmarshal(want, &wantValue); err != nil {
		t.Fatalf("decode want JSON %s: %v", string(want), err)
	}
	gotCanonical, _ := json.Marshal(gotValue)
	wantCanonical, _ := json.Marshal(wantValue)
	if string(gotCanonical) != string(wantCanonical) {
		t.Fatalf("JSON payload mismatch\ngot:  %s\nwant: %s", string(gotCanonical), string(wantCanonical))
	}
}
