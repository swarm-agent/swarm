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
	createdResult := createV3RealtimeTestSessionResult(t, server, "session-realtime-a", "create-realtime-a")
	created := *createdResult.Session

	httpServer := newV3RealtimeHTTPTestServer(t, server)
	conn := dialV3RealtimeStream(t, httpServer.URL)
	defer conn.Close()

	writeV3RealtimeMessage(t, conn, V3RealtimeMessage{Protocol: V3RealtimeProtocol, ProtocolVersion: V3RealtimeProtocolVersion, Kind: V3RealtimeKindSubscribe, SessionID: created.ID, SubscriptionID: "sub-a", EndpointCursor: "cursor-0"})
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
	if live.Rev != appendResult.RealtimeOutbox.EndpointSeq || live.PrevRev != appendResult.RealtimeOutbox.EndpointSeq-1 {
		t.Fatalf("live realtime rev metadata = rev:%d prevRev:%d outbox=%+v", live.Rev, live.PrevRev, appendResult.RealtimeOutbox)
	}

	outboxRows, err := sessionSvc.ListRealtimeOutboxAfter(0, 10)
	if err != nil {
		t.Fatalf("list realtime outbox rows: %v", err)
	}
	if len(outboxRows) != 2 || outboxRows[0].Event.Seq != 1 || outboxRows[1].Event.Seq != 2 {
		t.Fatalf("outbox rows = %+v, want create and append", outboxRows)
	}

	replayConn := dialV3RealtimeStream(t, httpServer.URL)
	defer replayConn.Close()
	writeV3RealtimeMessage(t, replayConn, V3RealtimeMessage{Protocol: V3RealtimeProtocol, ProtocolVersion: V3RealtimeProtocolVersion, Kind: V3RealtimeKindSubscribe, SessionID: created.ID, SubscriptionID: "sub-a-replay", EndpointCursor: createdResult.RealtimeOutbox.EndpointCursor})
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
	createdResult := createV3RealtimeTestSessionResult(t, server, "session-realtime-crash", "create-realtime-crash")
	created := *createdResult.Session

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
	writeV3RealtimeMessage(t, conn, V3RealtimeMessage{Protocol: V3RealtimeProtocol, ProtocolVersion: V3RealtimeProtocolVersion, Kind: V3RealtimeKindSubscribe, SessionID: created.ID, SubscriptionID: "sub-crash", EndpointCursor: createdResult.RealtimeOutbox.EndpointCursor})
	assertV3RealtimeFrame(t, readV3RealtimeFrame(t, conn), V3RealtimeKindReplayStart, created.ID, 0)
	replayed := readV3RealtimeFrame(t, conn)
	assertV3RealtimeFrame(t, replayed, V3RealtimeKindEvent, created.ID, committed.Event.Seq)
	if replayed.EndpointCursor != committed.RealtimeOutbox.EndpointCursor || replayed.Event.EventType != "session.message.appended" {
		t.Fatalf("crash-window replay = %+v committed outbox=%+v", replayed, committed.RealtimeOutbox)
	}
	assertV3RealtimeFrame(t, readV3RealtimeFrame(t, conn), V3RealtimeKindReplayDone, created.ID, committed.Event.Seq)
}

func TestV3RealtimeHydrationSnapshotEndpointCursorHandoffReplaysOnlyRowsAfterCursor(t *testing.T) {
	server, _, _, _, _ := newRoutedSessionTestServerWithSwarmStore(t)
	created := createV3RealtimeTestSessionResult(t, server, "session-realtime-handoff", "create-realtime-handoff")
	if created.RealtimeOutbox == nil {
		t.Fatalf("created mutation missing realtime outbox: %+v", created)
	}

	snapshotCursor := hydrateV3RealtimeSnapshotEndpointCursor(t, server, created.SessionID)
	if snapshotCursor != created.RealtimeOutbox.EndpointCursor {
		t.Fatalf("snapshot_endpoint_cursor = %q, want create endpoint cursor %q", snapshotCursor, created.RealtimeOutbox.EndpointCursor)
	}
	firstMissed := appendV3RealtimeTestMessage(t, server, created.SessionID, "message-realtime-handoff-1", "handoff one")
	secondMissed := appendV3RealtimeTestMessage(t, server, created.SessionID, "message-realtime-handoff-2", "handoff two")

	httpServer := newV3RealtimeHTTPTestServer(t, server)
	conn := dialV3RealtimeStreamWithQuery(t, httpServer.URL, "endpoint_cursor="+snapshotCursor+"&sessions="+created.SessionID)
	defer conn.Close()

	started := readV3RealtimeFrame(t, conn)
	assertV3RealtimeFrame(t, started, V3RealtimeKindReplayStart, created.SessionID, 0)
	if started.EndpointCursor != snapshotCursor || started.AfterSeq != 0 || started.AfterRev != 0 {
		closeV3RealtimeConnBeforeFatal(conn)
		t.Fatalf("replay start cursor = endpoint:%q after_seq:%d afterRev:%d, want endpoint_cursor %q only", started.EndpointCursor, started.AfterSeq, started.AfterRev, snapshotCursor)
	}
	first := readV3RealtimeFrame(t, conn)
	assertV3RealtimeFrame(t, first, V3RealtimeKindEvent, created.SessionID, firstMissed.Event.Seq)
	second := readV3RealtimeFrame(t, conn)
	assertV3RealtimeFrame(t, second, V3RealtimeKindEvent, created.SessionID, secondMissed.Event.Seq)
	if first.EndpointCursor != firstMissed.RealtimeOutbox.EndpointCursor || second.EndpointCursor != secondMissed.RealtimeOutbox.EndpointCursor || first.Rev >= second.Rev {
		t.Fatalf("handoff replay order = first %+v second %+v", first, second)
	}
	completed := readV3RealtimeFrame(t, conn)
	assertV3RealtimeFrame(t, completed, V3RealtimeKindReplayDone, created.SessionID, secondMissed.Event.Seq)
	if completed.EndpointCursor != secondMissed.RealtimeOutbox.EndpointCursor || completed.AfterSeq != 0 || completed.AfterRev != 0 {
		t.Fatalf("replay complete cursor = endpoint:%q after_seq:%d afterRev:%d, want endpoint cursor %q only", completed.EndpointCursor, completed.AfterSeq, completed.AfterRev, secondMissed.RealtimeOutbox.EndpointCursor)
	}
}

func TestV3RealtimeReconnectWithEndpointCursorReplaysMissedRowsInEndpointOrder(t *testing.T) {
	server, _, _, _, _ := newRoutedSessionTestServerWithSwarmStore(t)
	created := createV3RealtimeTestSessionResult(t, server, "session-realtime-endpoint-reconnect", "create-realtime-endpoint-reconnect")
	checkpointCursor := created.RealtimeOutbox.EndpointCursor
	firstMissed := appendV3RealtimeTestMessage(t, server, created.SessionID, "message-realtime-endpoint-reconnect-1", "missed one")
	secondMissed := appendV3RealtimeTestMessage(t, server, created.SessionID, "message-realtime-endpoint-reconnect-2", "missed two")

	httpServer := newV3RealtimeHTTPTestServer(t, server)
	conn := dialV3RealtimeStreamWithQuery(t, httpServer.URL, "endpoint_cursor="+checkpointCursor+"&sessions="+created.SessionID)
	defer conn.Close()

	started := readV3RealtimeFrame(t, conn)
	assertV3RealtimeFrame(t, started, V3RealtimeKindReplayStart, created.SessionID, 0)
	if started.EndpointCursor != checkpointCursor || started.AfterSeq != 0 || started.AfterRev != 0 {
		closeV3RealtimeConnBeforeFatal(conn)
		t.Fatalf("reconnect replay start cursor = endpoint:%q after_seq:%d afterRev:%d, want endpoint_cursor %q only", started.EndpointCursor, started.AfterSeq, started.AfterRev, checkpointCursor)
	}
	for i, want := range []sessionruntime.SessionMutationResult{firstMissed, secondMissed} {
		frame := readV3RealtimeFrame(t, conn)
		assertV3RealtimeFrame(t, frame, V3RealtimeKindEvent, created.SessionID, want.Event.Seq)
		if frame.EndpointCursor != want.RealtimeOutbox.EndpointCursor || frame.Rev != want.RealtimeOutbox.EndpointSeq {
			t.Fatalf("replayed[%d] = %+v, want outbox %+v", i, frame, want.RealtimeOutbox)
		}
	}
	completed := readV3RealtimeFrame(t, conn)
	assertV3RealtimeFrame(t, completed, V3RealtimeKindReplayDone, created.SessionID, secondMissed.Event.Seq)
	if completed.EndpointCursor != secondMissed.RealtimeOutbox.EndpointCursor {
		t.Fatalf("reconnect replay complete endpoint_cursor = %q, want %q", completed.EndpointCursor, secondMissed.RealtimeOutbox.EndpointCursor)
	}
}

func TestV3RealtimeEndpointCursorReplaySurvivesLostHubWakeup(t *testing.T) {
	server, sessionSvc, _, _, _ := newRoutedSessionTestServerWithSwarmStore(t)
	created := createV3RealtimeTestSessionResult(t, server, "session-realtime-lost-wakeup", "create-realtime-lost-wakeup")
	checkpointCursor := created.RealtimeOutbox.EndpointCursor

	committed, err := sessionSvc.ApplySessionMutation(sessionruntime.SessionMutationInput{
		SessionID:       created.SessionID,
		UserID:          testPrincipal().UserID,
		AccountScopeID:  testPrincipal().AccountScopeID,
		ClientRequestID: "message-realtime-lost-wakeup",
		IdempotencyKey:  "message-realtime-lost-wakeup",
		PayloadHash:     "hash-message-realtime-lost-wakeup",
		Kind:            sessionruntime.SessionMutationAppendMessage,
		Message:         &pebblestore.MessageSnapshot{Role: "user", Content: "committed without hub wakeup"},
		NowUnixMs:       2000,
	})
	if err != nil {
		t.Fatalf("commit mutation without hub wakeup: %v", err)
	}
	if committed.RealtimeOutbox == nil {
		t.Fatalf("committed mutation missing realtime outbox: %+v", committed)
	}

	httpServer := newV3RealtimeHTTPTestServer(t, server)
	conn := dialV3RealtimeStreamWithQuery(t, httpServer.URL, "endpoint_cursor="+checkpointCursor+"&sessions="+created.SessionID)
	defer conn.Close()

	started := readV3RealtimeFrame(t, conn)
	assertV3RealtimeFrame(t, started, V3RealtimeKindReplayStart, created.SessionID, 0)
	if started.EndpointCursor != checkpointCursor || started.AfterSeq != 0 || started.AfterRev != 0 {
		closeV3RealtimeConnBeforeFatal(conn)
		t.Fatalf("lost-wakeup replay start cursor = endpoint:%q after_seq:%d afterRev:%d, want endpoint_cursor %q only", started.EndpointCursor, started.AfterSeq, started.AfterRev, checkpointCursor)
	}
	replayed := readV3RealtimeFrame(t, conn)
	assertV3RealtimeFrame(t, replayed, V3RealtimeKindEvent, created.SessionID, committed.Event.Seq)
	if replayed.EndpointCursor != committed.RealtimeOutbox.EndpointCursor || replayed.Event.EventType != "session.message.appended" {
		t.Fatalf("lost-wakeup replay = %+v committed outbox=%+v", replayed, committed.RealtimeOutbox)
	}
}

func TestV3RealtimeReplayAfterReconnectCarriesDurableTerminalRunIntent(t *testing.T) {
	server, sessionSvc, _, _, _ := newRoutedSessionTestServerWithSwarmStore(t)
	createdResult := createV3RealtimeTestSessionResult(t, server, "session-realtime-terminal", "create-realtime-terminal")
	created := *createdResult.Session

	pending, err := server.applySessionV3PrimaryMutation(sessionruntime.SessionMutationInput{
		SessionID:       created.ID,
		UserID:          testPrincipal().UserID,
		AccountScopeID:  testPrincipal().AccountScopeID,
		ClientRequestID: "message-realtime-terminal",
		IdempotencyKey:  "message-realtime-terminal",
		PayloadHash:     "hash-message-realtime-terminal",
		Kind:            sessionruntime.SessionMutationAppendMessage,
		Message:         &pebblestore.MessageSnapshot{Role: "user", Content: "cancel me durably"},
		RunIntent:       &pebblestore.V3SessionRunIntent{RunID: "run-realtime-terminal", Status: sessionruntime.RunIntentPendingExecutor},
		NowUnixMs:       2000,
	})
	if err != nil {
		t.Fatalf("record pending terminal replay run: %v", err)
	}
	running, err := server.applySessionV3PrimaryMutation(sessionruntime.SessionMutationInput{
		SessionID:       created.ID,
		UserID:          testPrincipal().UserID,
		AccountScopeID:  testPrincipal().AccountScopeID,
		ClientRequestID: "running-realtime-terminal",
		IdempotencyKey:  "running-realtime-terminal",
		PayloadHash:     "hash-running-realtime-terminal",
		Kind:            sessionruntime.SessionMutationRecordRunIntent,
		EventType:       "session.assistant.started",
		RunIntent:       &pebblestore.V3SessionRunIntent{RunID: "run-realtime-terminal", Status: sessionruntime.RunIntentRunning},
		NowUnixMs:       3000,
	})
	if err != nil {
		t.Fatalf("record running terminal replay run: %v", err)
	}
	cancelled, err := server.applySessionV3PrimaryMutation(sessionruntime.SessionMutationInput{
		SessionID:       created.ID,
		UserID:          testPrincipal().UserID,
		AccountScopeID:  testPrincipal().AccountScopeID,
		ClientRequestID: "cancelled-realtime-terminal",
		IdempotencyKey:  "cancelled-realtime-terminal",
		PayloadHash:     "hash-cancelled-realtime-terminal",
		Kind:            sessionruntime.SessionMutationRecordRunIntent,
		EventType:       "session.run.cancelled",
		RunIntent:       &pebblestore.V3SessionRunIntent{RunID: "run-realtime-terminal", Status: sessionruntime.RunIntentCancelled, BlockedReason: "stop from test"},
		NowUnixMs:       4000,
	})
	if err != nil {
		t.Fatalf("record cancelled terminal replay run: %v", err)
	}
	if _, ok, err := sessionSvc.GetSessionActiveRunIntent(created.ID); err != nil || ok {
		t.Fatalf("active run pointer after terminal = ok:%v err:%v", ok, err)
	}

	httpServer := newV3RealtimeHTTPTestServer(t, server)
	conn := dialV3RealtimeStream(t, httpServer.URL)
	defer conn.Close()
	writeV3RealtimeMessage(t, conn, V3RealtimeMessage{Protocol: V3RealtimeProtocol, ProtocolVersion: V3RealtimeProtocolVersion, Kind: V3RealtimeKindSubscribe, SessionID: created.ID, SubscriptionID: "sub-terminal", EndpointCursor: pending.RealtimeOutbox.EndpointCursor})
	assertV3RealtimeFrame(t, readV3RealtimeFrame(t, conn), V3RealtimeKindReplayStart, created.ID, 0)
	replayedRunning := readV3RealtimeFrame(t, conn)
	assertV3RealtimeFrame(t, replayedRunning, V3RealtimeKindEvent, created.ID, running.Event.Seq)
	replayedTerminal := readV3RealtimeFrame(t, conn)
	assertV3RealtimeFrame(t, replayedTerminal, V3RealtimeKindEvent, created.ID, cancelled.Event.Seq)
	assertV3RealtimeFrame(t, readV3RealtimeFrame(t, conn), V3RealtimeKindReplayDone, created.ID, cancelled.Event.Seq)

	var terminalPayload struct {
		Status    string                         `json:"status"`
		RunIntent pebblestore.V3SessionRunIntent `json:"run_intent"`
	}
	if err := json.Unmarshal(replayedTerminal.Event.Payload, &terminalPayload); err != nil {
		t.Fatalf("decode terminal replay payload: %v", err)
	}
	if replayedTerminal.Event.EventType != "session.run.cancelled" || terminalPayload.Status != sessionruntime.RunIntentCancelled || terminalPayload.RunIntent.Status != sessionruntime.RunIntentCancelled {
		t.Fatalf("terminal replay frame = %+v payload=%+v", replayedTerminal, terminalPayload)
	}
}

func TestV3RealtimeEndpointResumeDeliversAuthorizedSubscribedEventsAndSkipsOnlyServerFilteredRows(t *testing.T) {
	server, _, _, _, _ := newRoutedSessionTestServerWithSwarmStore(t)
	aResult := createV3RealtimeTestSessionResult(t, server, "session-realtime-resume-a", "create-realtime-resume-a")
	a := *aResult.Session
	bResult := createV3RealtimeTestSessionResult(t, server, "session-realtime-resume-b", "create-realtime-resume-b")
	b := *bResult.Session

	appendV3RealtimeTestMessage(t, server, a.ID, "message-realtime-resume-a-1", "a-one")
	bAppend := appendV3RealtimeTestMessage(t, server, b.ID, "message-realtime-resume-b-1", "b-one")
	aSecondAppend := appendV3RealtimeTestMessage(t, server, a.ID, "message-realtime-resume-a-2", "a-two")

	httpServer := newV3RealtimeHTTPTestServer(t, server)
	conn := dialV3RealtimeStream(t, httpServer.URL)
	defer conn.Close()

	writeV3RealtimeMessage(t, conn, V3RealtimeMessage{Protocol: V3RealtimeProtocol, ProtocolVersion: V3RealtimeProtocolVersion, Kind: V3RealtimeKindSubscribe, SessionID: b.ID, SubscriptionID: "sub-b", EndpointCursor: bResult.RealtimeOutbox.EndpointCursor})
	assertV3RealtimeFrame(t, readV3RealtimeFrame(t, conn), V3RealtimeKindReplayStart, b.ID, 0)
	assertV3RealtimeFrame(t, readV3RealtimeFrame(t, conn), V3RealtimeKindEvent, b.ID, bAppend.Event.Seq)
	assertV3RealtimeFrame(t, readV3RealtimeFrame(t, conn), V3RealtimeKindReplayDone, b.ID, bAppend.Event.Seq)

	writeV3RealtimeMessage(t, conn, V3RealtimeMessage{Protocol: V3RealtimeProtocol, ProtocolVersion: V3RealtimeProtocolVersion, Kind: V3RealtimeKindResume, EndpointCursor: aSecondAppend.RealtimeOutbox.EndpointCursor})
	appendAfterResume := appendV3RealtimeTestMessage(t, server, b.ID, "message-realtime-resume-b-2", "b-two")
	live := readV3RealtimeFrame(t, conn)
	assertV3RealtimeFrame(t, live, V3RealtimeKindEvent, b.ID, appendAfterResume.Event.Seq)
	if live.EndpointCursor != appendAfterResume.RealtimeOutbox.EndpointCursor {
		t.Fatalf("live endpoint cursor = %q, want %q", live.EndpointCursor, appendAfterResume.RealtimeOutbox.EndpointCursor)
	}
}

func TestV3RealtimeSessionGapDirtiesOnlyAffectedSession(t *testing.T) {
	server, _, _, _, _ := newRoutedSessionTestServerWithSwarmStore(t)
	aResult := createV3RealtimeTestSessionResult(t, server, "session-realtime-gap-a", "create-realtime-gap-a")
	a := *aResult.Session
	bResult := createV3RealtimeTestSessionResult(t, server, "session-realtime-gap-b", "create-realtime-gap-b")
	b := *bResult.Session

	httpServer := newV3RealtimeHTTPTestServer(t, server)
	conn := dialV3RealtimeStream(t, httpServer.URL)
	defer conn.Close()

	writeV3RealtimeMessage(t, conn, V3RealtimeMessage{Protocol: V3RealtimeProtocol, ProtocolVersion: V3RealtimeProtocolVersion, Kind: V3RealtimeKindSubscribe, SessionID: a.ID, SubscriptionID: "sub-a", EndpointCursor: aResult.RealtimeOutbox.EndpointCursor})
	assertV3RealtimeFrame(t, readV3RealtimeFrame(t, conn), V3RealtimeKindReplayStart, a.ID, 0)
	assertV3RealtimeFrame(t, readV3RealtimeFrame(t, conn), V3RealtimeKindReplayDone, a.ID, 1)
	writeV3RealtimeMessage(t, conn, V3RealtimeMessage{Protocol: V3RealtimeProtocol, ProtocolVersion: V3RealtimeProtocolVersion, Kind: V3RealtimeKindSubscribe, SessionID: b.ID, SubscriptionID: "sub-b", EndpointCursor: "cursor-0"})
	assertV3RealtimeFrame(t, readV3RealtimeFrame(t, conn), V3RealtimeKindReplayStart, b.ID, 0)
	assertV3RealtimeFrame(t, readV3RealtimeFrame(t, conn), V3RealtimeKindEvent, b.ID, 1)
	assertV3RealtimeFrame(t, readV3RealtimeFrame(t, conn), V3RealtimeKindReplayDone, b.ID, 1)

	bAppend := appendV3RealtimeTestMessage(t, server, b.ID, "message-realtime-gap-b", "b survives")

	first := readV3RealtimeFrame(t, conn)
	assertV3RealtimeFrame(t, first, V3RealtimeKindEvent, b.ID, bAppend.Event.Seq)
	server.v3RealtimeOutbox.publish(sessionruntime.RealtimeOutboxRecord{EndpointSeq: bAppend.RealtimeOutbox.EndpointSeq + 1, EndpointCursor: pebblestore.V3RealtimeOutboxCursor(bAppend.RealtimeOutbox.EndpointSeq + 1), SessionID: a.ID, UserID: testPrincipal().UserID, AccountScopeID: testPrincipal().AccountScopeID, Event: sessionruntime.SessionEvent{ID: "event-gap-a", SessionID: a.ID, Seq: 3, EventType: "session.message.appended", Payload: json.RawMessage(`{"kind":"message"}`)}, Projection: sessionruntime.SessionProjection{SessionID: a.ID, ProjectionHighWatermarkSeq: 3}})
	time.Sleep(150 * time.Millisecond)

	bSecondAppend := appendV3RealtimeTestMessage(t, server, b.ID, "message-realtime-gap-b-2", "b still survives")
	second := readV3RealtimeFrame(t, conn)
	assertV3RealtimeFrame(t, second, V3RealtimeKindEvent, b.ID, bSecondAppend.Event.Seq)
}

func TestV3RealtimeSubscribeWildcardLeaksZeroEvents(t *testing.T) {
	server, _, _, _, _ := newRoutedSessionTestServerWithSwarmStore(t)
	created := createV3RealtimeTestSession(t, server, "session-realtime-auth-a", "create-realtime-auth-a")

	httpServer := newV3RealtimeHTTPTestServer(t, server)
	conn := dialV3RealtimeStream(t, httpServer.URL)
	defer conn.Close()

	writeV3RealtimeMessage(t, conn, V3RealtimeMessage{Protocol: V3RealtimeProtocol, ProtocolVersion: V3RealtimeProtocolVersion, Kind: V3RealtimeKindSubscribe, SessionID: "v3/session:*", SubscriptionID: "sub-wildcard", EndpointCursor: "cursor-0"})
	frame := readV3RealtimeFrame(t, conn)
	assertV3RealtimeFrame(t, frame, V3RealtimeKindAuthDenied, "v3/session:*", 0)
	if frame.ErrorCode == "" {
		t.Fatalf("wildcard auth denial missing error code: %+v", frame)
	}

	appendV3RealtimeTestMessage(t, server, created.ID, "message-realtime-auth-a", "secret")
	assertNoV3RealtimeFrame(t, conn, 150*time.Millisecond)
}

func TestV3RealtimeSlowConsumerNoticeDoesNotBlockOtherSubscribers(t *testing.T) {
	hub := newV3RealtimeOutboxHub()
	slow := hub.subscribe()
	fast := hub.subscribe()
	defer hub.unsubscribe(fast)

	fastDeliveries := 0
	for i := uint64(1); i <= v3RealtimeSubscriberBufSize+1; i++ {
		hub.publish(sessionruntime.RealtimeOutboxRecord{EndpointSeq: i, EndpointCursor: pebblestore.V3RealtimeOutboxCursor(i), SessionID: "session-slow", Event: sessionruntime.SessionEvent{SessionID: "session-slow", Seq: i, EventType: "session.message.appended", Payload: json.RawMessage(`{"kind":"message"}`)}, Projection: sessionruntime.SessionProjection{SessionID: "session-slow", ProjectionHighWatermarkSeq: i}})
		select {
		case <-fast.send:
			fastDeliveries++
		case <-time.After(time.Second):
			t.Fatalf("fast subscriber starved at endpoint seq %d", i)
		}
	}

	select {
	case notice := <-slow.slow:
		if notice.EndpointSeq != v3RealtimeSubscriberBufSize+1 || !strings.Contains(notice.Reason, "reconnect required") {
			t.Fatalf("slow notice = %+v", notice)
		}
	case <-time.After(time.Second):
		t.Fatal("timed out waiting for slow-consumer notice")
	}
	if fastDeliveries != v3RealtimeSubscriberBufSize+1 {
		t.Fatalf("fast subscriber deliveries = %d, want %d", fastDeliveries, v3RealtimeSubscriberBufSize+1)
	}
	select {
	case notice := <-fast.slow:
		t.Fatalf("fast subscriber was incorrectly marked slow: %+v", notice)
	default:
	}
}

func TestV3RealtimeSingleConnectionInterleavesSessionsBySessionID(t *testing.T) {
	server, _, _, _, _ := newRoutedSessionTestServerWithSwarmStore(t)
	aResult := createV3RealtimeTestSessionResult(t, server, "session-realtime-a", "create-realtime-a")
	a := *aResult.Session
	bResult := createV3RealtimeTestSessionResult(t, server, "session-realtime-b", "create-realtime-b")
	b := *bResult.Session

	httpServer := newV3RealtimeHTTPTestServer(t, server)
	conn := dialV3RealtimeStream(t, httpServer.URL)
	defer conn.Close()

	writeV3RealtimeMessage(t, conn, V3RealtimeMessage{Protocol: V3RealtimeProtocol, ProtocolVersion: V3RealtimeProtocolVersion, Kind: V3RealtimeKindSubscribe, SessionID: a.ID, SubscriptionID: "sub-a", EndpointCursor: aResult.RealtimeOutbox.EndpointCursor})
	assertV3RealtimeFrame(t, readV3RealtimeFrame(t, conn), V3RealtimeKindReplayStart, a.ID, 0)
	assertV3RealtimeFrame(t, readV3RealtimeFrame(t, conn), V3RealtimeKindReplayDone, a.ID, 1)
	writeV3RealtimeMessage(t, conn, V3RealtimeMessage{Protocol: V3RealtimeProtocol, ProtocolVersion: V3RealtimeProtocolVersion, Kind: V3RealtimeKindSubscribe, SessionID: b.ID, SubscriptionID: "sub-b", EndpointCursor: bResult.RealtimeOutbox.EndpointCursor})
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
			for _, required := range []string{"V3RealtimeKindResume", "ListRealtimeOutboxAfter", "sendV3RealtimeOutboxEvent"} {
				if !strings.Contains(body, required) {
					t.Fatalf("%s missing V3-native realtime symbol %q", file, required)
				}
			}
			for _, forbidden := range []string{"ListRealtimeOutboxForSessionAfterSeq", "message.AfterSeq", "message.AfterRev", "firstNonZeroUint64(message.AfterRev"} {
				if strings.Contains(body, forbidden) {
					t.Fatalf("%s contains forbidden session-cursor replay dependency %q", file, forbidden)
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

func TestPublishCommittedSessionV3MutationResultWakesRealtimeOutboxAndGlobalDiscovery(t *testing.T) {
	body := readSourceFileForTest(t, "sessions_v3_outbox.go")
	publishBody := sourceBetweenForTest(t, body, "func (s *Server) publishCommittedSessionV3MutationResult", "func (s *Server) publishCommittedSessionV3GlobalEvent")
	for _, required := range []string{"publishCommittedV3RealtimeOutbox", "publishCommittedSessionV3GlobalEvent"} {
		if !strings.Contains(publishBody, required) {
			t.Fatalf("publishCommittedSessionV3MutationResult missing committed event wake %q", required)
		}
	}
	for _, forbidden := range []string{"s.registerSessionV3StreamLineageFromResult(", "s.publishCommittedSessionV3Event("} {
		if strings.Contains(publishBody, forbidden) {
			t.Fatalf("publishCommittedSessionV3MutationResult still fans out through retired session transport via %q", forbidden)
		}
	}
}

func TestPublishCommittedSessionV3MutationResultDoesNotRequireLegacyMirrors(t *testing.T) {
	server := &Server{v3RealtimeOutbox: newV3RealtimeOutboxHub()}
	event := sessionruntime.SessionEvent{SessionID: "session-outbox-only", Seq: 1, EventType: "session.message.appended", Payload: json.RawMessage(`{"ok":true}`)}
	result := sessionruntime.SessionMutationResult{
		SessionID: "session-outbox-only",
		Event:     event,
		RealtimeOutbox: &sessionruntime.RealtimeOutboxRecord{
			EndpointSeq: 1,
			SessionID:   "session-outbox-only",
			Event:       event,
		},
	}
	if err := server.publishCommittedSessionV3MutationResult(result); err != nil {
		t.Fatalf("publish committed mutation without legacy mirrors: %v", err)
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
	result := createV3RealtimeTestSessionResult(t, server, sessionID, requestID)
	if result.Session == nil {
		t.Fatalf("create realtime test session result missing session: %+v", result)
	}
	return *result.Session
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
	return result
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
	return dialV3RealtimeStreamWithQuery(t, baseURL, "")
}

func dialV3RealtimeStreamWithQuery(t *testing.T, baseURL, query string) *gorillaws.Conn {
	t.Helper()
	wsURL := "ws" + strings.TrimPrefix(baseURL, "http") + V3RealtimeStreamPath
	if strings.TrimSpace(query) != "" {
		wsURL += "?" + strings.TrimPrefix(query, "?")
	}
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
	for {
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
		if frame.Kind == V3RealtimeKindHello {
			continue
		}
		return frame
	}
}

func closeV3RealtimeConnBeforeFatal(conn *gorillaws.Conn) {
	_ = conn.Close()
	time.Sleep(25 * time.Millisecond)
}

func assertNoV3RealtimeFrame(t *testing.T, conn *gorillaws.Conn, wait time.Duration) {
	t.Helper()
	if err := conn.SetReadDeadline(time.Now().Add(wait)); err != nil {
		t.Fatalf("set v3 realtime read deadline: %v", err)
	}
	_, raw, err := conn.ReadMessage()
	if err == nil {
		t.Fatalf("unexpected v3 realtime frame: %s", string(raw))
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

func hydrateV3RealtimeSnapshotEndpointCursor(t *testing.T, server *Server, sessionID string) string {
	t.Helper()
	rec := httptest.NewRecorder()
	req := httptest.NewRequest(http.MethodGet, "/v3/sessions/"+sessionID, nil)
	server.Handler().ServeHTTP(rec, withTestPrincipal(req))
	if rec.Code != http.StatusOK {
		t.Fatalf("hydrate session status = %d body=%s", rec.Code, rec.Body.String())
	}
	var payload map[string]any
	if err := json.NewDecoder(rec.Body).Decode(&payload); err != nil {
		t.Fatalf("decode hydrate response: %v", err)
	}
	cursor, _ := payload["snapshot_endpoint_cursor"].(string)
	if strings.TrimSpace(cursor) == "" {
		t.Fatalf("hydrate response missing snapshot_endpoint_cursor: %+v", payload)
	}
	return cursor
}

func readSourceFileForTest(t *testing.T, path string) string {
	t.Helper()
	raw, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("read %s: %v", path, err)
	}
	return string(raw)
}

func sourceBetweenForTest(t *testing.T, source, start, end string) string {
	t.Helper()
	startIndex := strings.Index(source, start)
	if startIndex < 0 {
		t.Fatalf("source missing start marker %q", start)
	}
	endIndex := strings.Index(source[startIndex:], end)
	if endIndex < 0 {
		t.Fatalf("source missing end marker %q after %q", end, start)
	}
	return source[startIndex : startIndex+endIndex]
}
