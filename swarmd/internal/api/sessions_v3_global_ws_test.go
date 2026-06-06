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
	"swarm/packages/swarmd/internal/stream"
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

func TestSessionsV3RepresentativeMutationsPublishGlobalV3Envelopes(t *testing.T) {
	server, sessionSvc, _, _, _ := newRoutedSessionTestServerWithSwarmStore(t)
	created := createGlobalV3TestSession(t, sessionSvc, "session-global-representative", "create-global-representative")

	cases := []struct {
		name      string
		input     sessionruntime.SessionMutationInput
		wantType  string
		wantField string
	}{
		{
			name: "message",
			input: sessionruntime.SessionMutationInput{
				Kind:    sessionruntime.SessionMutationAppendMessage,
				Message: &pebblestore.MessageSnapshot{Role: "user", Content: "representative message"},
			},
			wantType:  "session.message.appended",
			wantField: "representative message",
		},
		{
			name: "lifecycle",
			input: sessionruntime.SessionMutationInput{
				Kind:      sessionruntime.SessionMutationUpsertLifecycle,
				Lifecycle: &pebblestore.SessionLifecycleSnapshot{RunID: "run-global-lifecycle", Active: true, Phase: "running", OwnerTransport: "global-ws-test"},
			},
			wantType:  "session.lifecycle.updated",
			wantField: "global-ws-test",
		},
		{
			name: "run-intent",
			input: sessionruntime.SessionMutationInput{
				Kind:      sessionruntime.SessionMutationRecordRunIntent,
				RunIntent: &pebblestore.V3SessionRunIntent{RunID: "run-global-intent", Status: pebblestore.V3RunIntentPendingExecutor},
			},
			wantType:  "session.run_intent.recorded",
			wantField: "run-global-intent",
		},
		{
			name: "assistant-delta",
			input: sessionruntime.SessionMutationInput{
				Kind:         sessionruntime.SessionMutationRecordDiagnostic,
				EventType:    "session.assistant.delta",
				EventPayload: json.RawMessage(`{"session_id":"session-global-representative","run_id":"run-global-assistant","delta":"hello from assistant"}`),
			},
			wantType:  "session.assistant.delta",
			wantField: "hello from assistant",
		},
		{
			name: "tool-started",
			input: sessionruntime.SessionMutationInput{
				Kind:         sessionruntime.SessionMutationRecordDiagnostic,
				EventType:    "session.tool.started",
				EventPayload: json.RawMessage(`{"session_id":"session-global-representative","run_id":"run-global-tool","tool_name":"shell","step_id":"step-1"}`),
			},
			wantType:  "session.tool.started",
			wantField: "step-1",
		},
	}

	for i, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			input := tc.input
			input.SessionID = created.ID
			input.UserID = testPrincipal().UserID
			input.AccountScopeID = testPrincipal().AccountScopeID
			input.ClientRequestID = "representative-" + tc.name
			input.IdempotencyKey = input.ClientRequestID
			input.PayloadHash = "hash-" + input.ClientRequestID
			input.NowUnixMs = int64(6000 + i)
			result, err := sessionSvc.ApplySessionMutation(input)
			if err != nil {
				t.Fatalf("apply %s mutation: %v", tc.name, err)
			}
			if err := server.publishCommittedSessionV3MutationResult(result); err != nil {
				t.Fatalf("publish %s mutation globally: %v", tc.name, err)
			}
			event := assertGlobalV3EnvelopeForResult(t, server, result)
			if event.EventType != tc.wantType {
				t.Fatalf("global event type = %q, want %q", event.EventType, tc.wantType)
			}
			if !strings.Contains(string(event.Payload), tc.wantField) {
				t.Fatalf("global payload for %s does not contain %q: %s", tc.name, tc.wantField, string(event.Payload))
			}
		})
	}
}

func TestSessionsV3GlobalEnvelopePreservesSourceAndCausation(t *testing.T) {
	server, sessionSvc, _, _, _ := newRoutedSessionTestServerWithSwarmStore(t)
	created := createGlobalV3TestSession(t, sessionSvc, "session-global-source", "create-global-source")
	result, err := sessionSvc.ApplySessionMutation(sessionruntime.SessionMutationInput{
		SessionID:       created.ID,
		UserID:          testPrincipal().UserID,
		AccountScopeID:  testPrincipal().AccountScopeID,
		ClientRequestID: "message-global-source",
		IdempotencyKey:  "message-global-source",
		PayloadHash:     "hash-message-global-source",
		Kind:            sessionruntime.SessionMutationAppendMessage,
		Message:         &pebblestore.MessageSnapshot{Role: "user", Content: "source and causation"},
		CausationID:     "cause-global-source",
		CorrelationID:   "corr-global-source",
		NowUnixMs:       7000,
	})
	if err != nil {
		t.Fatalf("apply source mutation: %v", err)
	}
	if err := server.publishCommittedSessionV3MutationResult(result); err != nil {
		t.Fatalf("publish source mutation globally: %v", err)
	}
	event := assertGlobalV3EnvelopeForResult(t, server, result)
	if event.Source != "v3" || event.CausationID != "cause-global-source" || event.CorrelationID != "corr-global-source" {
		t.Fatalf("global envelope metadata = %+v", event)
	}
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

func TestGlobalWebsocketSessionWildcardExcludesLegacyV2SessionEvents(t *testing.T) {
	server, _, _, _, _ := newRoutedSessionTestServerWithSwarmStore(t)
	legacyPayload, err := json.Marshal(map[string]any{"session_id": "legacy-session", "title": "legacy"})
	if err != nil {
		t.Fatalf("marshal legacy payload: %v", err)
	}
	legacyEvent, err := server.events.Append("session:legacy-session", "session.updated", "legacy-session", legacyPayload, "", "")
	if err != nil {
		t.Fatalf("append legacy global event: %v", err)
	}
	v3Payload, err := json.Marshal(map[string]any{"session_id": "v3-session", "title": "v3"})
	if err != nil {
		t.Fatalf("marshal v3 payload: %v", err)
	}
	v3Event, err := server.events.AppendWithSource("session:v3-session", "session.title.updated", "v3-session", v3Payload, "v3", "", "")
	if err != nil {
		t.Fatalf("append v3 global event: %v", err)
	}

	hub := stream.NewHub(server.events)
	httpServer := httptest.NewServer(hub)
	defer httpServer.Close()

	conn, _, err := gorillaws.DefaultDialer.Dial("ws"+strings.TrimPrefix(httpServer.URL, "http"), nil)
	if err != nil {
		t.Fatalf("dial global websocket: %v", err)
	}
	defer conn.Close()
	readGlobalWSFrame(t, conn, "connected")
	writeGlobalWSFrame(t, conn, map[string]any{"type": "subscribe", "channel": "session:*", "last_seen_seq": legacyEvent.GlobalSeq - 1})
	readGlobalWSFrame(t, conn, "subscribed")

	frame := readGlobalWSFrame(t, conn, "event")
	if frame.Event == nil {
		t.Fatalf("global websocket event frame missing event: %+v", frame)
	}
	if frame.Event.GlobalSeq != v3Event.GlobalSeq || frame.Event.Stream != "session:v3-session" || frame.Event.EventType != "session.title.updated" || frame.Event.Source != "v3" {
		t.Fatalf("replayed event = %+v, want only V3 source event %+v", *frame.Event, v3Event)
	}
	readGlobalWSFrame(t, conn, "resume-complete")

	liveLegacyPayload, err := json.Marshal(map[string]any{"session_id": "legacy-live", "title": "legacy live"})
	if err != nil {
		t.Fatalf("marshal live legacy payload: %v", err)
	}
	liveLegacy, err := server.events.Append("session:legacy-live", "session.updated", "legacy-live", liveLegacyPayload, "", "")
	if err != nil {
		t.Fatalf("append live legacy event: %v", err)
	}
	hub.Publish(liveLegacy)

	liveV3Payload, err := json.Marshal(map[string]any{"session_id": "v3-live", "title": "v3 live"})
	if err != nil {
		t.Fatalf("marshal live v3 payload: %v", err)
	}
	liveV3, err := server.events.AppendWithSource("session:v3-live", "session.title.updated", "v3-live", liveV3Payload, "v3", "", "")
	if err != nil {
		t.Fatalf("append live v3 event: %v", err)
	}
	hub.Publish(liveV3)

	frame = readGlobalWSFrame(t, conn, "event")
	if frame.Event == nil {
		t.Fatalf("global websocket live frame missing event: %+v", frame)
	}
	if frame.Event.GlobalSeq != liveV3.GlobalSeq || frame.Event.Stream != "session:v3-live" || frame.Event.Source != "v3" {
		t.Fatalf("live event = %+v, want V3 event %+v", *frame.Event, liveV3)
	}
}

func TestGlobalWebsocketSessionWildcardStillAllowsExactLegacySessionSubscription(t *testing.T) {
	server, _, _, _, _ := newRoutedSessionTestServerWithSwarmStore(t)
	payload, err := json.Marshal(map[string]any{"session_id": "legacy-exact", "title": "legacy exact"})
	if err != nil {
		t.Fatalf("marshal legacy exact payload: %v", err)
	}
	legacyEvent, err := server.events.Append("session:legacy-exact", "session.updated", "legacy-exact", payload, "", "")
	if err != nil {
		t.Fatalf("append legacy exact event: %v", err)
	}

	hub := stream.NewHub(server.events)
	httpServer := httptest.NewServer(hub)
	defer httpServer.Close()
	conn, _, err := gorillaws.DefaultDialer.Dial("ws"+strings.TrimPrefix(httpServer.URL, "http"), nil)
	if err != nil {
		t.Fatalf("dial global websocket: %v", err)
	}
	defer conn.Close()
	readGlobalWSFrame(t, conn, "connected")
	writeGlobalWSFrame(t, conn, map[string]any{"type": "subscribe", "channel": "session:legacy-exact", "last_seen_seq": legacyEvent.GlobalSeq - 1})
	readGlobalWSFrame(t, conn, "subscribed")

	frame := readGlobalWSFrame(t, conn, "event")
	if frame.Event == nil || frame.Event.GlobalSeq != legacyEvent.GlobalSeq || frame.Event.EventType != "session.updated" {
		t.Fatalf("exact legacy session event = %+v, want %+v", frame.Event, legacyEvent)
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

func assertGlobalV3EnvelopeForResult(t *testing.T, server *Server, result sessionruntime.SessionMutationResult) pebblestore.EventEnvelope {
	t.Helper()
	events, err := server.events.ReadFrom(1, 200)
	if err != nil {
		t.Fatalf("read global event log: %v", err)
	}
	for _, event := range events {
		if event.Stream == "session:"+result.Event.SessionID && event.EventType == result.Event.EventType && event.EntityID == result.Event.SessionID {
			assertJSONEqualRaw(t, event.Payload, result.Event.Payload)
			return event
		}
	}
	t.Fatalf("missing global V3 event envelope for %s/%s in %+v", result.Event.SessionID, result.Event.EventType, events)
	return pebblestore.EventEnvelope{}
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
