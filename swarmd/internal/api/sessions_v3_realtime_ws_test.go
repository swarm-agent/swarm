package api

import (
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"net/http/httptest"
	"net/url"
	"os"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	gorillaws "github.com/gorilla/websocket"

	"swarm/packages/swarmd/internal/identity"
	sessionruntime "swarm/packages/swarmd/internal/session"
	pebblestore "swarm/packages/swarmd/internal/store/pebble"
	transportws "swarm/packages/swarmd/internal/transport/ws"
)

func TestV3RealtimeWorksetsExcludeNavigationHiddenSessions(t *testing.T) {
	principal := identity.Principal{AccountScopeID: "account-1", UserID: "user-1"}
	selector := V3RealtimeWorksetSelector{Kind: "global", Global: true}
	for name, metadata := range map[string]map[string]any{
		"navigation_hidden": {"navigation_hidden": true},
		"system_session":    {"system_session": true},
		"system_sidechat":   {"system_sidechat": true},
		"lineage_kind":      {"lineage_kind": "system_sidechat"},
	} {
		t.Run(name, func(t *testing.T) {
			session := pebblestore.SessionSnapshot{ID: "hidden", AccountScopeID: principal.AccountScopeID, UserID: principal.UserID, Metadata: metadata}
			if v3RealtimeSessionMatchesWorksetSelector(principal, session, selector) {
				t.Fatal("navigation-hidden session matched Desktop workset")
			}
		})
	}
	visible := pebblestore.SessionSnapshot{ID: "visible", AccountScopeID: principal.AccountScopeID, UserID: principal.UserID}
	if !v3RealtimeSessionMatchesWorksetSelector(principal, visible, selector) {
		t.Fatal("visible session did not match global workset")
	}
}

func TestV3RealtimeArchivedSessionSubscriptionAllowsReactivationSendPath(t *testing.T) {
	server, _, _, _, _ := newRoutedSessionTestServerWithSwarmStore(t)
	createdResult := createV3RealtimeTestSessionResult(t, server, "session-realtime-archived-open", "create-realtime-archived-open")
	created := *createdResult.Session

	archiveReq := httptest.NewRequest(http.MethodPost, "/v3/sessions:archive", strings.NewReader(`{"session_ids":["`+created.ID+`"]}`))
	archiveReq.Header.Set("Content-Type", "application/json")
	archiveRec := httptest.NewRecorder()
	server.Handler().ServeHTTP(archiveRec, withTestPrincipal(archiveReq))
	if archiveRec.Code != http.StatusOK {
		t.Fatalf("archive status = %d body=%s", archiveRec.Code, archiveRec.Body.String())
	}

	httpServer := newV3RealtimeHTTPTestServer(t, server)
	conn := dialV3RealtimeStream(t, httpServer.URL)
	defer conn.Close()

	writeV3RealtimeMessage(t, conn, V3RealtimeMessage{Protocol: V3RealtimeProtocol, ProtocolVersion: V3RealtimeProtocolVersion, Kind: V3RealtimeKindSubscribe, SessionID: created.ID, SubscriptionID: "sub-archived-open", EndpointCursor: signedV3RealtimeCursorForTest(t, server, createdResult.RealtimeOutbox.EndpointSeq)})
	assertV3RealtimeFrame(t, readV3RealtimeFrame(t, conn), V3RealtimeKindReplayStart, created.ID, 0)
	archiveEvent := readV3RealtimeFrame(t, conn)
	assertV3RealtimeFrame(t, archiveEvent, V3RealtimeKindEvent, created.ID, createdResult.Event.Seq+1)
	if archiveEvent.EventType != "session.archived" {
		t.Fatalf("archived replay event = %+v", archiveEvent)
	}
	assertV3RealtimeFrame(t, readV3RealtimeFrame(t, conn), V3RealtimeKindReplayDone, created.ID, createdResult.Event.Seq+1)

	messagesReq := httptest.NewRequest(http.MethodGet, "/v3/sessions/"+created.ID+"/messages?tail=true&limit=200", nil)
	messagesRec := httptest.NewRecorder()
	server.Handler().ServeHTTP(messagesRec, withTestPrincipal(messagesReq))
	if messagesRec.Code != http.StatusOK {
		t.Fatalf("list archived messages status = %d body=%s", messagesRec.Code, messagesRec.Body.String())
	}

	messageReq := httptest.NewRequest(http.MethodPost, "/v3/sessions/"+created.ID+"/messages", strings.NewReader(`{"client_request_id":"reactivate-realtime-archived","message_id":"reactivate-realtime-archived-message","run_id":"reactivate-realtime-archived-run","role":"user","content":"reactivate archived realtime session"}`))
	messageReq.Header.Set("Content-Type", "application/json")
	messageRec := httptest.NewRecorder()
	server.Handler().ServeHTTP(messageRec, withTestPrincipal(messageReq))
	if messageRec.Code != http.StatusOK {
		t.Fatalf("append archived message status = %d body=%s", messageRec.Code, messageRec.Body.String())
	}
}

func TestV3RealtimePublishesCommittedOutboxEventAndReplaysAfterReconnect(t *testing.T) {
	server, sessionSvc, _, _, _ := newRoutedSessionTestServerWithSwarmStore(t)
	createdResult := createV3RealtimeTestSessionResult(t, server, "session-realtime-a", "create-realtime-a")
	created := *createdResult.Session

	httpServer := newV3RealtimeHTTPTestServer(t, server)
	conn := dialV3RealtimeStream(t, httpServer.URL)
	defer conn.Close()

	writeV3RealtimeMessage(t, conn, V3RealtimeMessage{Protocol: V3RealtimeProtocol, ProtocolVersion: V3RealtimeProtocolVersion, Kind: V3RealtimeKindSubscribe, SessionID: created.ID, SubscriptionID: "sub-a", EndpointCursor: signedV3RealtimeCursorForTest(t, server, 0)})
	assertV3RealtimeFrame(t, readV3RealtimeFrame(t, conn), V3RealtimeKindReplayStart, created.ID, 0)
	createdEvent := readV3RealtimeFrame(t, conn)
	assertV3RealtimeFrame(t, createdEvent, V3RealtimeKindEvent, created.ID, 1)
	assertV3RealtimeSignedCursorSeq(t, server, createdEvent.EndpointCursor, createdResult.RealtimeOutbox.EndpointSeq)
	if createdEvent.Event.EventType != "session.created" {
		t.Fatalf("created realtime event = %+v", createdEvent)
	}
	assertV3RealtimeFrame(t, readV3RealtimeFrame(t, conn), V3RealtimeKindReplayDone, created.ID, 1)

	appendResult := appendV3RealtimeTestMessage(t, server, created.ID, "message-realtime-a", "hello realtime")
	live := readV3RealtimeFrame(t, conn)
	assertV3RealtimeFrame(t, live, V3RealtimeKindEvent, created.ID, appendResult.Event.Seq)
	assertV3RealtimeSignedCursorSeq(t, server, live.EndpointCursor, appendResult.RealtimeOutbox.EndpointSeq)
	if live.Event.EventType != "session.message.appended" {
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
	writeV3RealtimeMessage(t, replayConn, V3RealtimeMessage{Protocol: V3RealtimeProtocol, ProtocolVersion: V3RealtimeProtocolVersion, Kind: V3RealtimeKindSubscribe, SessionID: created.ID, SubscriptionID: "sub-a-replay", EndpointCursor: signedV3RealtimeCursorForTest(t, server, createdResult.RealtimeOutbox.EndpointSeq)})
	assertV3RealtimeFrame(t, readV3RealtimeFrame(t, replayConn), V3RealtimeKindReplayStart, created.ID, 0)
	replayedAppend := readV3RealtimeFrame(t, replayConn)
	assertV3RealtimeFrame(t, replayedAppend, V3RealtimeKindEvent, created.ID, appendResult.Event.Seq)
	assertV3RealtimeSignedCursorSeq(t, server, replayedAppend.EndpointCursor, appendResult.RealtimeOutbox.EndpointSeq)
	assertV3RealtimeFrame(t, readV3RealtimeFrame(t, replayConn), V3RealtimeKindReplayDone, created.ID, appendResult.Event.Seq)
}

func TestV3RealtimePlanSavedOutboxRowReachesWebsocket(t *testing.T) {
	server, sessionSvc, _, _, _ := newRoutedSessionTestServerWithSwarmStore(t)
	createdResult := createV3RealtimeTestSessionResult(t, server, "session-realtime-plan", "create-realtime-plan")
	created := *createdResult.Session
	plan, event, err := sessionSvc.SavePlanWithMetadata(created.ID, "plan-realtime", "Realtime Plan", "# Realtime", "approved", "approved", true, sessionruntime.PlanSaveMetadata{Document: &pebblestore.SessionPlanDocument{
		Info:           pebblestore.SessionPlanInfo{Goal: "realtime"},
		Checkpoints:    []pebblestore.SessionPlanCheckpoint{{ID: "cp-1", Title: "Checkpoint", Status: sessionruntime.PlanCheckpointStatusInProgress}},
		ExecutionState: &pebblestore.SessionPlanExecutionState{Status: sessionruntime.PlanExecutionStateInProgress, LastCheckpointID: "cp-1"},
	}})
	if err != nil {
		t.Fatalf("save plan: %v", err)
	}
	if err := server.publishCommittedPlanSaved(plan, event); err != nil {
		t.Fatalf("publish plan saved: %v", err)
	}

	httpServer := newV3RealtimeHTTPTestServer(t, server)
	conn := dialV3RealtimeStream(t, httpServer.URL)
	defer conn.Close()
	writeV3RealtimeMessage(t, conn, V3RealtimeMessage{Protocol: V3RealtimeProtocol, ProtocolVersion: V3RealtimeProtocolVersion, Kind: V3RealtimeKindSubscribe, SessionID: created.ID, SubscriptionID: "sub-plan", EndpointCursor: signedV3RealtimeCursorForTest(t, server, createdResult.RealtimeOutbox.EndpointSeq)})
	assertV3RealtimeFrame(t, readV3RealtimeFrame(t, conn), V3RealtimeKindReplayStart, created.ID, 0)
	planFrame := readV3RealtimeFrame(t, conn)
	assertV3RealtimeFrame(t, planFrame, V3RealtimeKindEvent, created.ID, 2)
	if planFrame.Event.EventType != "session.plan.saved" {
		t.Fatalf("plan realtime event = %+v", planFrame)
	}
	var payload struct {
		HasActivePlan bool `json:"has_active_plan"`
		ActivePlan    *struct {
			Document *struct {
				ExecutionState *struct {
					Status string `json:"status"`
				} `json:"execution_state"`
			} `json:"document"`
		} `json:"active_plan"`
	}
	if err := json.Unmarshal(planFrame.Event.Payload, &payload); err != nil {
		t.Fatalf("decode plan realtime payload: %v", err)
	}
	if !payload.HasActivePlan || payload.ActivePlan == nil || payload.ActivePlan.Document == nil || payload.ActivePlan.Document.ExecutionState == nil || payload.ActivePlan.Document.ExecutionState.Status != sessionruntime.PlanExecutionStateInProgress {
		t.Fatalf("plan realtime payload missing active plan sidebar state: %+v", planFrame.Event)
	}
	assertV3RealtimeFrame(t, readV3RealtimeFrame(t, conn), V3RealtimeKindReplayDone, created.ID, 2)
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
	writeV3RealtimeMessage(t, conn, V3RealtimeMessage{Protocol: V3RealtimeProtocol, ProtocolVersion: V3RealtimeProtocolVersion, Kind: V3RealtimeKindSubscribe, SessionID: created.ID, SubscriptionID: "sub-crash", EndpointCursor: signedV3RealtimeCursorForTest(t, server, createdResult.RealtimeOutbox.EndpointSeq)})
	assertV3RealtimeFrame(t, readV3RealtimeFrame(t, conn), V3RealtimeKindReplayStart, created.ID, 0)
	replayed := readV3RealtimeFrame(t, conn)
	assertV3RealtimeFrame(t, replayed, V3RealtimeKindEvent, created.ID, committed.Event.Seq)
	assertV3RealtimeSignedCursorSeq(t, server, replayed.EndpointCursor, committed.RealtimeOutbox.EndpointSeq)
	if replayed.Event.EventType != "session.message.appended" {
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
	assertV3RealtimeSignedCursorSeq(t, server, snapshotCursor, created.RealtimeOutbox.EndpointSeq)
	firstMissed := appendV3RealtimeTestMessage(t, server, created.SessionID, "message-realtime-handoff-1", "handoff one")
	secondMissed := appendV3RealtimeTestMessage(t, server, created.SessionID, "message-realtime-handoff-2", "handoff two")

	httpServer := newV3RealtimeHTTPTestServer(t, server)
	conn := dialV3RealtimeStreamWithQuery(t, httpServer.URL, "endpoint_cursor="+snapshotCursor+"&sessions="+created.SessionID)
	defer conn.Close()

	started := readV3RealtimeFrame(t, conn)
	assertV3RealtimeFrame(t, started, V3RealtimeKindReplayStart, created.SessionID, 0)
	assertV3RealtimeSignedCursorSeq(t, server, started.EndpointCursor, created.RealtimeOutbox.EndpointSeq)
	if started.AfterSeq != 0 || started.AfterRev != 0 {
		closeV3RealtimeConnBeforeFatal(conn)
		t.Fatalf("replay start cursor = endpoint:%q after_seq:%d afterRev:%d", started.EndpointCursor, started.AfterSeq, started.AfterRev)
	}
	first := readV3RealtimeFrame(t, conn)
	assertV3RealtimeFrame(t, first, V3RealtimeKindEvent, created.SessionID, firstMissed.Event.Seq)
	second := readV3RealtimeFrame(t, conn)
	assertV3RealtimeFrame(t, second, V3RealtimeKindEvent, created.SessionID, secondMissed.Event.Seq)
	assertV3RealtimeSignedCursorSeq(t, server, first.EndpointCursor, firstMissed.RealtimeOutbox.EndpointSeq)
	assertV3RealtimeSignedCursorSeq(t, server, second.EndpointCursor, secondMissed.RealtimeOutbox.EndpointSeq)
	if first.Rev >= second.Rev {
		t.Fatalf("handoff replay order = first %+v second %+v", first, second)
	}
	completed := readV3RealtimeFrame(t, conn)
	assertV3RealtimeFrame(t, completed, V3RealtimeKindReplayDone, created.SessionID, secondMissed.Event.Seq)
	assertV3RealtimeSignedCursorSeq(t, server, completed.EndpointCursor, secondMissed.RealtimeOutbox.EndpointSeq)
	if completed.AfterSeq != 0 || completed.AfterRev != 0 {
		t.Fatalf("replay complete cursor = endpoint:%q after_seq:%d afterRev:%d", completed.EndpointCursor, completed.AfterSeq, completed.AfterRev)
	}
}

func TestV3RealtimeSubscribeAcceptsCanonicalSyncSnapshotCursorHandoff(t *testing.T) {
	server, _, _, _, _ := newRoutedSessionTestServerWithSwarmStore(t)
	created := createV3RealtimeTestSessionResult(t, server, "session-realtime-sync-snapshot-handoff", "create-realtime-sync-snapshot-handoff")
	if created.RealtimeOutbox == nil {
		t.Fatalf("created mutation missing realtime outbox: %+v", created)
	}
	snapshotCursor := hydrateV3SyncSnapshotEndpointCursor(t, server, created.SessionID)
	if _, _, err := server.parseV3SyncEndpointCursor(snapshotCursor, v3SyncCursorScopeForRealtime(testPrincipal(), "desktop")); err == nil || !strings.Contains(err.Error(), "stream_kind") {
		t.Fatalf("test setup expected canonical sync snapshot cursor to mismatch strict realtime scope, err=%v", err)
	}
	firstMissed := appendV3RealtimeTestMessage(t, server, created.SessionID, "message-realtime-sync-snapshot-handoff-1", "snapshot handoff one")
	secondMissed := appendV3RealtimeTestMessage(t, server, created.SessionID, "message-realtime-sync-snapshot-handoff-2", "snapshot handoff two")

	httpServer := newV3RealtimeHTTPTestServer(t, server)
	conn := dialV3RealtimeStream(t, httpServer.URL)
	defer conn.Close()

	writeV3RealtimeMessage(t, conn, V3RealtimeMessage{Protocol: V3RealtimeProtocol, ProtocolVersion: V3RealtimeProtocolVersion, Kind: V3RealtimeKindSubscribe, SessionID: created.SessionID, SubscriptionID: "sub-sync-snapshot-handoff", EndpointCursor: snapshotCursor})
	started := readV3RealtimeFrame(t, conn)
	assertV3RealtimeFrame(t, started, V3RealtimeKindReplayStart, created.SessionID, 0)
	first := readV3RealtimeFrame(t, conn)
	assertV3RealtimeFrame(t, first, V3RealtimeKindEvent, created.SessionID, firstMissed.Event.Seq)
	second := readV3RealtimeFrame(t, conn)
	assertV3RealtimeFrame(t, second, V3RealtimeKindEvent, created.SessionID, secondMissed.Event.Seq)
	completed := readV3RealtimeFrame(t, conn)
	assertV3RealtimeFrame(t, completed, V3RealtimeKindReplayDone, created.SessionID, secondMissed.Event.Seq)
	assertV3RealtimeSignedCursorSeq(t, server, completed.EndpointCursor, secondMissed.RealtimeOutbox.EndpointSeq)
}

func TestV3RealtimeReconnectWithEndpointCursorReplaysMissedRowsInEndpointOrder(t *testing.T) {
	server, _, _, _, _ := newRoutedSessionTestServerWithSwarmStore(t)
	created := createV3RealtimeTestSessionResult(t, server, "session-realtime-endpoint-reconnect", "create-realtime-endpoint-reconnect")
	checkpointCursor := signedV3RealtimeCursorForTest(t, server, created.RealtimeOutbox.EndpointSeq)
	firstMissed := appendV3RealtimeTestMessage(t, server, created.SessionID, "message-realtime-endpoint-reconnect-1", "missed one")
	secondMissed := appendV3RealtimeTestMessage(t, server, created.SessionID, "message-realtime-endpoint-reconnect-2", "missed two")

	httpServer := newV3RealtimeHTTPTestServer(t, server)
	conn := dialV3RealtimeStreamWithQuery(t, httpServer.URL, "endpoint_cursor="+checkpointCursor+"&sessions="+created.SessionID)
	defer conn.Close()

	started := readV3RealtimeFrame(t, conn)
	assertV3RealtimeFrame(t, started, V3RealtimeKindReplayStart, created.SessionID, 0)
	assertV3RealtimeSignedCursorSeq(t, server, started.EndpointCursor, created.RealtimeOutbox.EndpointSeq)
	if started.AfterSeq != 0 || started.AfterRev != 0 {
		closeV3RealtimeConnBeforeFatal(conn)
		t.Fatalf("reconnect replay start cursor = endpoint:%q after_seq:%d afterRev:%d", started.EndpointCursor, started.AfterSeq, started.AfterRev)
	}
	for i, want := range []sessionruntime.SessionMutationResult{firstMissed, secondMissed} {
		frame := readV3RealtimeFrame(t, conn)
		assertV3RealtimeFrame(t, frame, V3RealtimeKindEvent, created.SessionID, want.Event.Seq)
		assertV3RealtimeSignedCursorSeq(t, server, frame.EndpointCursor, want.RealtimeOutbox.EndpointSeq)
		if frame.Rev != want.RealtimeOutbox.EndpointSeq {
			t.Fatalf("replayed[%d] = %+v, want outbox %+v", i, frame, want.RealtimeOutbox)
		}
	}
	completed := readV3RealtimeFrame(t, conn)
	assertV3RealtimeFrame(t, completed, V3RealtimeKindReplayDone, created.SessionID, secondMissed.Event.Seq)
	assertV3RealtimeSignedCursorSeq(t, server, completed.EndpointCursor, secondMissed.RealtimeOutbox.EndpointSeq)
}

func TestV3RealtimeCatchUpRejectsMissingDurableTailPrefix(t *testing.T) {
	server, sessionSvc, _, _, _ := newRoutedSessionTestServerWithSwarmStore(t)
	created := createV3RealtimeTestSessionResult(t, server, "session-realtime-tail-prefix", "create-realtime-tail-prefix")
	first := appendV3RealtimeTestMessage(t, server, created.SessionID, "message-realtime-tail-prefix-1", "one")
	second := appendV3RealtimeTestMessage(t, server, created.SessionID, "message-realtime-tail-prefix-2", "two")
	third := appendV3RealtimeTestMessage(t, server, created.SessionID, "message-realtime-tail-prefix-3", "three")
	previousListOutbox := v3RealtimeListRealtimeOutboxAfter
	v3RealtimeListRealtimeOutboxAfter = func(s *Server, afterEndpointSeq uint64, limit int) ([]sessionruntime.RealtimeOutboxRecord, error) {
		records, err := sessionSvc.ListRealtimeOutboxAfter(afterEndpointSeq, limit)
		if err != nil || len(records) <= 1 {
			return records, err
		}
		return records[:1], nil
	}
	t.Cleanup(func() { v3RealtimeListRealtimeOutboxAfter = previousListOutbox })

	httpServer := newV3RealtimeHTTPTestServer(t, server)
	conn := dialV3RealtimeStreamWithQuery(t, httpServer.URL, "endpoint_cursor="+url.QueryEscape(signedV3RealtimeCursorForTest(t, server, created.RealtimeOutbox.EndpointSeq))+"&sessions="+created.SessionID)
	defer conn.Close()

	assertV3RealtimeFrame(t, readV3RealtimeFrame(t, conn), V3RealtimeKindReplayStart, created.SessionID, 0)
	replayed := readV3RealtimeFrame(t, conn)
	assertV3RealtimeFrame(t, replayed, V3RealtimeKindEvent, created.SessionID, first.Event.Seq)
	frame := readV3RealtimeFrame(t, conn)
	assertV3RealtimeCursorError(t, frame, "endpoint_cursor_gap")
	if !frame.BootstrapRequired || frame.LatestEndpointSeq != third.RealtimeOutbox.EndpointSeq || frame.MissingEndpointSeq != second.RealtimeOutbox.EndpointSeq || frame.OldestAvailableEndpointSeq != 0 {
		t.Fatalf("missing tail prefix cursor error = %+v", frame)
	}
}

func TestV3RealtimeCatchUpRejectsEmptyScanBeforeDurableHead(t *testing.T) {
	server, _, _, _, _ := newRoutedSessionTestServerWithSwarmStore(t)
	created := createV3RealtimeTestSessionResult(t, server, "session-realtime-tail-empty", "create-realtime-tail-empty")
	first := appendV3RealtimeTestMessage(t, server, created.SessionID, "message-realtime-tail-empty-1", "one")
	second := appendV3RealtimeTestMessage(t, server, created.SessionID, "message-realtime-tail-empty-2", "two")
	third := appendV3RealtimeTestMessage(t, server, created.SessionID, "message-realtime-tail-empty-3", "three")
	previousListOutbox := v3RealtimeListRealtimeOutboxAfter
	v3RealtimeListRealtimeOutboxAfter = func(_ *Server, _ uint64, _ int) ([]sessionruntime.RealtimeOutboxRecord, error) {
		return nil, nil
	}
	t.Cleanup(func() { v3RealtimeListRealtimeOutboxAfter = previousListOutbox })

	httpServer := newV3RealtimeHTTPTestServer(t, server)
	conn := dialV3RealtimeStreamWithQuery(t, httpServer.URL, "endpoint_cursor="+url.QueryEscape(signedV3RealtimeCursorForTest(t, server, created.RealtimeOutbox.EndpointSeq))+"&sessions="+created.SessionID)
	defer conn.Close()

	assertV3RealtimeFrame(t, readV3RealtimeFrame(t, conn), V3RealtimeKindReplayStart, created.SessionID, 0)
	frame := readV3RealtimeFrame(t, conn)
	assertV3RealtimeCursorError(t, frame, "endpoint_cursor_gap")
	if !frame.BootstrapRequired || frame.LatestEndpointSeq != third.RealtimeOutbox.EndpointSeq || frame.MissingEndpointSeq != first.RealtimeOutbox.EndpointSeq || frame.OldestAvailableEndpointSeq != 0 {
		t.Fatalf("empty scan cursor error = %+v first=%d second=%d", frame, first.RealtimeOutbox.EndpointSeq, second.RealtimeOutbox.EndpointSeq)
	}
}

func TestV3RealtimeEndpointCursorAheadFailsClosed(t *testing.T) {
	server, _, _, _, _ := newRoutedSessionTestServerWithSwarmStore(t)
	created := createV3RealtimeTestSessionResult(t, server, "session-realtime-ahead", "create-realtime-ahead")
	currentHead := created.RealtimeOutbox.EndpointSeq
	scope := v3SyncCursorScopeForRealtime(testPrincipal(), "desktop")
	aheadCursor, err := server.signV3SyncEndpointCursor(scope, currentHead+1)
	if err != nil {
		t.Fatalf("sign ahead cursor: %v", err)
	}

	httpServer := newV3RealtimeHTTPTestServer(t, server)
	conn := dialV3RealtimeStreamWithQuery(t, httpServer.URL, "endpoint_cursor="+url.QueryEscape(aheadCursor))
	defer conn.Close()

	frame := readV3RealtimeFrame(t, conn)
	assertV3RealtimeCursorError(t, frame, "endpoint_cursor_ahead")
	if frame.LatestEndpointSeq != currentHead {
		t.Fatalf("ahead cursor frame latest_endpoint_seq = %d, want %d: %+v", frame.LatestEndpointSeq, currentHead, frame)
	}
}

func TestV3RealtimeNoCursorHelloStartsAtCurrentDurableHead(t *testing.T) {
	server, sessionSvc, _, _, _ := newRoutedSessionTestServerWithSwarmStore(t)
	created := createV3RealtimeTestSessionResult(t, server, "session-realtime-hello-head", "create-realtime-hello-head")
	httpServer := newV3RealtimeHTTPTestServer(t, server)
	if err := sessionSvc.PutSessionMaintenanceState(pebblestore.V3SessionMaintenanceState{OldestRetainedRealtimeEndpointSeq: created.RealtimeOutbox.EndpointSeq + 10, RealtimePrunedThroughEndpointSeq: created.RealtimeOutbox.EndpointSeq + 9, UpdatedAtUnixMs: time.Now().UnixMilli()}); err != nil {
		t.Fatal(err)
	}

	conn := dialV3RealtimeStream(t, httpServer.URL)
	defer conn.Close()

	hello := readV3RealtimeFrameIncludingHello(t, conn)
	assertV3RealtimeFrame(t, hello, V3RealtimeKindHello, "", 0)
	assertV3RealtimeSignedCursorSeq(t, server, hello.EndpointCursor, created.RealtimeOutbox.EndpointSeq)
	assertNoV3RealtimeFrame(t, conn, 50*time.Millisecond)
}

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

func TestV3RealtimeSendMessageRejectsOutboundClientOnlyFrame(t *testing.T) {
	server := &Server{}
	err := server.sendV3RealtimeMessage(nil, V3RealtimeMessage{Protocol: V3RealtimeProtocol, ProtocolVersion: V3RealtimeProtocolVersion, Kind: V3RealtimeKindSubscribe, SessionID: "session-a", SubscriptionID: "sub-a"})
	if err == nil || !strings.Contains(err.Error(), "not allowed") {
		t.Fatalf("sendV3RealtimeMessage client-only outbound err = %v", err)
	}
}

func TestV3RealtimeReadsDurableOldestRetainedBoundary(t *testing.T) {
	server, sessionSvc, _, _, _ := newRoutedSessionTestServerWithSwarmStore(t)
	created := createV3RealtimeTestSessionResult(t, server, "session-realtime-durable-boundary", "create-realtime-durable-boundary")
	boundary := created.RealtimeOutbox.EndpointSeq + 2
	if err := sessionSvc.PutSessionMaintenanceState(pebblestore.V3SessionMaintenanceState{
		OldestRetainedRealtimeEndpointSeq: boundary,
		RealtimePrunedThroughEndpointSeq:  boundary - 1,
		UpdatedAtUnixMs:                   time.Now().UnixMilli(),
	}); err != nil {
		t.Fatal(err)
	}
	got, err := server.v3RealtimeOldestAvailableEndpointSeq()
	if err != nil || got != boundary {
		t.Fatalf("durable oldest retained boundary=%d err=%v, want %d", got, err, boundary)
	}
}

func TestV3RealtimeEndpointCursorTooOldRequiresBootstrap(t *testing.T) {
	server, sessionSvc, _, _, _ := newRoutedSessionTestServerWithSwarmStore(t)
	created := createV3RealtimeTestSessionResult(t, server, "session-realtime-too-old", "create-realtime-too-old")
	currentHead := created.RealtimeOutbox.EndpointSeq
	scope := v3SyncCursorScopeForRealtime(testPrincipal(), "desktop")
	httpServer := newV3RealtimeHTTPTestServer(t, server)

	if err := sessionSvc.PutSessionMaintenanceState(pebblestore.V3SessionMaintenanceState{OldestRetainedRealtimeEndpointSeq: currentHead + 1, RealtimePrunedThroughEndpointSeq: currentHead, UpdatedAtUnixMs: time.Now().UnixMilli()}); err != nil {
		t.Fatal(err)
	}
	if !server.v3RealtimeValidateEndpointCursor(nil, currentHead) {
		t.Fatalf("boundary cursor oldest_available-1 should be replayable")
	}

	retentionBoundary := currentHead + 2
	if err := sessionSvc.PutSessionMaintenanceState(pebblestore.V3SessionMaintenanceState{OldestRetainedRealtimeEndpointSeq: retentionBoundary, RealtimePrunedThroughEndpointSeq: retentionBoundary - 1, UpdatedAtUnixMs: time.Now().UnixMilli()}); err != nil {
		t.Fatal(err)
	}
	oldCursor, err := server.signV3SyncEndpointCursor(scope, currentHead)
	if err != nil {
		t.Fatalf("sign old cursor: %v", err)
	}
	conn := dialV3RealtimeStreamWithQuery(t, httpServer.URL, "endpoint_cursor="+url.QueryEscape(oldCursor))
	defer conn.Close()

	frame := readV3RealtimeFrame(t, conn)
	assertV3RealtimeCursorError(t, frame, "endpoint_cursor_too_old")
	if !frame.BootstrapRequired || frame.OldestAvailableEndpointSeq != retentionBoundary || frame.LatestEndpointSeq != currentHead {
		t.Fatalf("too-old cursor frame = %+v, want bootstrap boundary=%d head=%d", frame, retentionBoundary, currentHead)
	}

	zeroCursor, err := server.signV3SyncEndpointCursor(scope, 0)
	if err != nil {
		t.Fatalf("sign zero cursor: %v", err)
	}
	zeroConn := dialV3RealtimeStreamWithQuery(t, httpServer.URL, "endpoint_cursor="+url.QueryEscape(zeroCursor))
	defer zeroConn.Close()
	zeroFrame := readV3RealtimeFrame(t, zeroConn)
	assertV3RealtimeCursorError(t, zeroFrame, "endpoint_cursor_too_old")
	if !zeroFrame.BootstrapRequired || zeroFrame.OldestAvailableEndpointSeq != retentionBoundary || zeroFrame.LatestEndpointSeq != currentHead {
		t.Fatalf("signed zero cursor frame = %+v, want bootstrap boundary=%d head=%d", zeroFrame, retentionBoundary, currentHead)
	}
}

func TestV3RealtimeIdleScanSendsZeroEventEndpointWatermark(t *testing.T) {
	server, sessionSvc, _, _, _ := newRoutedSessionTestServerWithSwarmStore(t)
	createdA := createV3RealtimeTestSessionResult(t, server, "session-realtime-watermark-a", "create-realtime-watermark-a")
	previousKeepaliveInterval := v3RealtimeKeepaliveInterval
	v3RealtimeKeepaliveInterval = 25 * time.Millisecond
	t.Cleanup(func() { v3RealtimeKeepaliveInterval = previousKeepaliveInterval })

	httpServer := newV3RealtimeHTTPTestServer(t, server)
	conn := dialV3RealtimeStreamWithQuery(t, httpServer.URL, "endpoint_cursor="+url.QueryEscape(signedV3RealtimeCursorForTest(t, server, createdA.RealtimeOutbox.EndpointSeq))+"&sessions="+createdA.SessionID)
	defer conn.Close()

	assertV3RealtimeFrame(t, readV3RealtimeFrame(t, conn), V3RealtimeKindReplayStart, createdA.SessionID, 0)
	assertV3RealtimeFrame(t, readV3RealtimeFrame(t, conn), V3RealtimeKindReplayDone, createdA.SessionID, 1)

	committedB, err := sessionSvc.ApplySessionMutation(sessionruntime.SessionMutationInput{
		SessionID:       "session-realtime-watermark-b",
		UserID:          testPrincipal().UserID,
		AccountScopeID:  testPrincipal().AccountScopeID,
		ClientRequestID: "create-realtime-watermark-b",
		IdempotencyKey:  "create-realtime-watermark-b",
		PayloadHash:     "hash-create-realtime-watermark-b",
		Kind:            sessionruntime.SessionMutationCreateSession,
		Session: &pebblestore.SessionSnapshot{
			ID:             "session-realtime-watermark-b",
			UserID:         testPrincipal().UserID,
			AccountScopeID: testPrincipal().AccountScopeID,
			WorkspacePath:  "/workspace/realtime",
			WorkspaceName:  "realtime",
			Title:          "session-realtime-watermark-b",
		},
		NowUnixMs: 2000,
	})
	if err != nil {
		t.Fatalf("commit filtered session without hub wakeup: %v", err)
	}
	if committedB.RealtimeOutbox == nil {
		t.Fatalf("filtered commit missing realtime outbox: %+v", committedB)
	}

	var watermark V3RealtimeMessage
	for i := 0; i < 5; i++ {
		frame := readV3RealtimeFrame(t, conn)
		if frame.Kind == V3RealtimeKindEndpointWatermark {
			watermark = frame
			break
		}
		if frame.Kind != V3RealtimeKindKeepalive {
			t.Fatalf("unexpected frame while waiting for zero-event watermark: %+v", frame)
		}
	}
	if watermark.Kind == "" {
		t.Fatal("timed out waiting for zero-event watermark")
	}
	assertV3RealtimeFrame(t, watermark, V3RealtimeKindEndpointWatermark, "", 0)
	assertV3RealtimeSignedCursorSeq(t, server, watermark.EndpointCursor, committedB.RealtimeOutbox.EndpointSeq)
	if watermark.HighWatermarkSeq != committedB.RealtimeOutbox.EndpointSeq || watermark.Rev != committedB.RealtimeOutbox.EndpointSeq || watermark.PrevRev != createdA.RealtimeOutbox.EndpointSeq {
		t.Fatalf("zero-event watermark = %+v, want endpoint seq %d prev %d", watermark, committedB.RealtimeOutbox.EndpointSeq, createdA.RealtimeOutbox.EndpointSeq)
	}
}

func TestV3RealtimeMixedScanSendsEndpointWatermarkAfterDeliveredEvent(t *testing.T) {
	server, sessionSvc, _, _, _ := newRoutedSessionTestServerWithSwarmStore(t)
	createdA := createV3RealtimeTestSessionResult(t, server, "session-realtime-mixed-watermark-a", "create-realtime-mixed-watermark-a")
	previousKeepaliveInterval := v3RealtimeKeepaliveInterval
	v3RealtimeKeepaliveInterval = 25 * time.Millisecond
	t.Cleanup(func() { v3RealtimeKeepaliveInterval = previousKeepaliveInterval })

	httpServer := newV3RealtimeHTTPTestServer(t, server)
	conn := dialV3RealtimeStreamWithQuery(t, httpServer.URL, "endpoint_cursor="+url.QueryEscape(signedV3RealtimeCursorForTest(t, server, createdA.RealtimeOutbox.EndpointSeq))+"&sessions="+createdA.SessionID)
	defer conn.Close()

	assertV3RealtimeFrame(t, readV3RealtimeFrame(t, conn), V3RealtimeKindReplayStart, createdA.SessionID, 0)
	assertV3RealtimeFrame(t, readV3RealtimeFrame(t, conn), V3RealtimeKindReplayDone, createdA.SessionID, 1)

	committedA, err := sessionSvc.ApplySessionMutation(sessionruntime.SessionMutationInput{
		SessionID:       createdA.SessionID,
		UserID:          testPrincipal().UserID,
		AccountScopeID:  testPrincipal().AccountScopeID,
		ClientRequestID: "message-realtime-mixed-watermark-a",
		IdempotencyKey:  "message-realtime-mixed-watermark-a",
		PayloadHash:     "hash-message-realtime-mixed-watermark-a",
		Kind:            sessionruntime.SessionMutationAppendMessage,
		Message:         &pebblestore.MessageSnapshot{Role: "user", Content: "delivered before filtered"},
		NowUnixMs:       2000,
	})
	if err != nil {
		t.Fatalf("commit subscribed event without hub wakeup: %v", err)
	}
	committedB, err := sessionSvc.ApplySessionMutation(sessionruntime.SessionMutationInput{
		SessionID:       "session-realtime-mixed-watermark-b",
		UserID:          testPrincipal().UserID,
		AccountScopeID:  testPrincipal().AccountScopeID,
		ClientRequestID: "create-realtime-mixed-watermark-b",
		IdempotencyKey:  "create-realtime-mixed-watermark-b",
		PayloadHash:     "hash-create-realtime-mixed-watermark-b",
		Kind:            sessionruntime.SessionMutationCreateSession,
		Session: &pebblestore.SessionSnapshot{
			ID:             "session-realtime-mixed-watermark-b",
			UserID:         testPrincipal().UserID,
			AccountScopeID: testPrincipal().AccountScopeID,
			WorkspacePath:  "/workspace/realtime",
			WorkspaceName:  "realtime",
			Title:          "session-realtime-mixed-watermark-b",
		},
		NowUnixMs: 3000,
	})
	if err != nil {
		t.Fatalf("commit filtered session without hub wakeup: %v", err)
	}

	var delivered, watermark V3RealtimeMessage
	for i := 0; i < 8 && (delivered.Kind == "" || watermark.Kind == ""); i++ {
		frame := readV3RealtimeFrame(t, conn)
		switch frame.Kind {
		case V3RealtimeKindEvent:
			if frame.SessionID != createdA.SessionID {
				t.Fatalf("unexpected event while waiting for mixed watermark: %+v", frame)
			}
			delivered = frame
		case V3RealtimeKindEndpointWatermark:
			watermark = frame
		case V3RealtimeKindKeepalive:
			// Keep waiting for the catch-up tick that scans both committed rows.
		default:
			t.Fatalf("unexpected frame while waiting for mixed watermark: %+v", frame)
		}
	}
	assertV3RealtimeFrame(t, delivered, V3RealtimeKindEvent, createdA.SessionID, committedA.Event.Seq)
	assertV3RealtimeSignedCursorSeq(t, server, delivered.EndpointCursor, committedA.RealtimeOutbox.EndpointSeq)
	assertV3RealtimeFrame(t, watermark, V3RealtimeKindEndpointWatermark, "", 0)
	assertV3RealtimeSignedCursorSeq(t, server, watermark.EndpointCursor, committedB.RealtimeOutbox.EndpointSeq)
	if watermark.HighWatermarkSeq != committedB.RealtimeOutbox.EndpointSeq || watermark.Rev != committedB.RealtimeOutbox.EndpointSeq || watermark.PrevRev != committedA.RealtimeOutbox.EndpointSeq {
		t.Fatalf("mixed-scan watermark = %+v, want endpoint seq %d prev %d", watermark, committedB.RealtimeOutbox.EndpointSeq, committedA.RealtimeOutbox.EndpointSeq)
	}
}

func TestV3RealtimeReconnectFromEndpointWatermarkDoesNotRescanFilteredRow(t *testing.T) {
	server, sessionSvc, _, _, _ := newRoutedSessionTestServerWithSwarmStore(t)
	createdA := createV3RealtimeTestSessionResult(t, server, "session-realtime-watermark-reconnect-a", "create-realtime-watermark-reconnect-a")
	previousKeepaliveInterval := v3RealtimeKeepaliveInterval
	v3RealtimeKeepaliveInterval = 25 * time.Millisecond
	t.Cleanup(func() { v3RealtimeKeepaliveInterval = previousKeepaliveInterval })

	httpServer := newV3RealtimeHTTPTestServer(t, server)
	conn := dialV3RealtimeStreamWithQuery(t, httpServer.URL, "endpoint_cursor="+url.QueryEscape(signedV3RealtimeCursorForTest(t, server, createdA.RealtimeOutbox.EndpointSeq))+"&sessions="+createdA.SessionID)
	assertV3RealtimeFrame(t, readV3RealtimeFrame(t, conn), V3RealtimeKindReplayStart, createdA.SessionID, 0)
	assertV3RealtimeFrame(t, readV3RealtimeFrame(t, conn), V3RealtimeKindReplayDone, createdA.SessionID, 1)

	committedB, err := sessionSvc.ApplySessionMutation(sessionruntime.SessionMutationInput{
		SessionID:       "session-realtime-watermark-reconnect-b",
		UserID:          testPrincipal().UserID,
		AccountScopeID:  testPrincipal().AccountScopeID,
		ClientRequestID: "create-realtime-watermark-reconnect-b",
		IdempotencyKey:  "create-realtime-watermark-reconnect-b",
		PayloadHash:     "hash-create-realtime-watermark-reconnect-b",
		Kind:            sessionruntime.SessionMutationCreateSession,
		Session: &pebblestore.SessionSnapshot{
			ID:             "session-realtime-watermark-reconnect-b",
			UserID:         testPrincipal().UserID,
			AccountScopeID: testPrincipal().AccountScopeID,
			WorkspacePath:  "/workspace/realtime",
			WorkspaceName:  "realtime",
			Title:          "session-realtime-watermark-reconnect-b",
		},
		NowUnixMs: 2000,
	})
	if err != nil {
		t.Fatalf("commit filtered session without hub wakeup: %v", err)
	}

	var watermark V3RealtimeMessage
	for i := 0; i < 5; i++ {
		frame := readV3RealtimeFrame(t, conn)
		if frame.Kind == V3RealtimeKindEndpointWatermark {
			watermark = frame
			break
		}
		if frame.Kind != V3RealtimeKindKeepalive {
			t.Fatalf("unexpected frame while waiting for reconnect watermark: %+v", frame)
		}
	}
	if watermark.Kind == "" {
		t.Fatal("timed out waiting for reconnect watermark")
	}
	assertV3RealtimeSignedCursorSeq(t, server, watermark.EndpointCursor, committedB.RealtimeOutbox.EndpointSeq)
	_ = conn.Close()
	v3RealtimeKeepaliveInterval = time.Hour

	replayConn := dialV3RealtimeStreamWithQuery(t, httpServer.URL, "endpoint_cursor="+url.QueryEscape(watermark.EndpointCursor)+"&sessions="+createdA.SessionID)
	defer replayConn.Close()
	assertV3RealtimeFrame(t, readV3RealtimeFrame(t, replayConn), V3RealtimeKindReplayStart, createdA.SessionID, 0)
	done := readV3RealtimeFrame(t, replayConn)
	assertV3RealtimeFrame(t, done, V3RealtimeKindReplayDone, createdA.SessionID, 1)
	assertV3RealtimeSignedCursorSeq(t, server, done.EndpointCursor, committedB.RealtimeOutbox.EndpointSeq)
	assertNoV3RealtimeFrame(t, replayConn, 75*time.Millisecond)
}

func TestV3RealtimeEndpointCursorReplaySurvivesLostHubWakeup(t *testing.T) {
	server, sessionSvc, _, _, _ := newRoutedSessionTestServerWithSwarmStore(t)
	created := createV3RealtimeTestSessionResult(t, server, "session-realtime-lost-wakeup", "create-realtime-lost-wakeup")
	checkpointCursor := signedV3RealtimeCursorForTest(t, server, created.RealtimeOutbox.EndpointSeq)

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
	assertV3RealtimeSignedCursorSeq(t, server, started.EndpointCursor, created.RealtimeOutbox.EndpointSeq)
	if started.AfterSeq != 0 || started.AfterRev != 0 {
		closeV3RealtimeConnBeforeFatal(conn)
		t.Fatalf("lost-wakeup replay start cursor = endpoint:%q after_seq:%d afterRev:%d", started.EndpointCursor, started.AfterSeq, started.AfterRev)
	}
	replayed := readV3RealtimeFrame(t, conn)
	assertV3RealtimeFrame(t, replayed, V3RealtimeKindEvent, created.SessionID, committed.Event.Seq)
	assertV3RealtimeSignedCursorSeq(t, server, replayed.EndpointCursor, committed.RealtimeOutbox.EndpointSeq)
	if replayed.Event.EventType != "session.message.appended" {
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
	writeV3RealtimeMessage(t, conn, V3RealtimeMessage{Protocol: V3RealtimeProtocol, ProtocolVersion: V3RealtimeProtocolVersion, Kind: V3RealtimeKindSubscribe, SessionID: created.ID, SubscriptionID: "sub-terminal", EndpointCursor: signedV3RealtimeCursorForTest(t, server, pending.RealtimeOutbox.EndpointSeq)})
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

func TestV3RealtimeEndpointResumeAtomicallyReplacesSubscriptions(t *testing.T) {
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

	writeV3RealtimeMessage(t, conn, V3RealtimeMessage{Protocol: V3RealtimeProtocol, ProtocolVersion: V3RealtimeProtocolVersion, Kind: V3RealtimeKindSubscribe, SessionID: b.ID, SubscriptionID: "sub-b", EndpointCursor: signedV3RealtimeCursorForTest(t, server, bResult.RealtimeOutbox.EndpointSeq)})
	assertV3RealtimeFrame(t, readV3RealtimeFrame(t, conn), V3RealtimeKindReplayStart, b.ID, 0)
	assertV3RealtimeFrame(t, readV3RealtimeFrame(t, conn), V3RealtimeKindEvent, b.ID, bAppend.Event.Seq)
	assertV3RealtimeFrame(t, readV3RealtimeFrame(t, conn), V3RealtimeKindReplayDone, b.ID, bAppend.Event.Seq)

	writeV3RealtimeMessage(t, conn, V3RealtimeMessage{Protocol: V3RealtimeProtocol, ProtocolVersion: V3RealtimeProtocolVersion, Kind: V3RealtimeKindResume, EndpointCursor: signedV3RealtimeCursorForTest(t, server, aSecondAppend.RealtimeOutbox.EndpointSeq)})
	appendV3RealtimeTestMessage(t, server, b.ID, "message-realtime-resume-b-2", "b-two")
	assertNoV3RealtimeEventFrame(t, conn, 150*time.Millisecond)
}

func TestV3RealtimeResumeInstallsWorksetAndDiscoversNewSession(t *testing.T) {
	server, _, _, _, _ := newRoutedSessionTestServerWithSwarmStore(t)
	httpServer := newV3RealtimeHTTPTestServer(t, server)
	conn := dialV3RealtimeStream(t, httpServer.URL)
	defer conn.Close()

	writeV3RealtimeMessage(t, conn, V3RealtimeMessage{Protocol: V3RealtimeProtocol, ProtocolVersion: V3RealtimeProtocolVersion, Kind: V3RealtimeKindResume, EndpointCursor: signedV3RealtimeCursorForTest(t, server, 0), Worksets: []V3RealtimeWorksetSubscriptionRequest{v3RealtimeGlobalWorksetRequestForTest()}})
	created := createV3RealtimeTestSessionResult(t, server, "session-realtime-workset-discover", "create-realtime-workset-discover")

	discovered := readV3RealtimeFrame(t, conn)
	assertV3RealtimeFrame(t, discovered, V3RealtimeKindWorksetSessionDiscovered, created.SessionID, 0)
	if discovered.WorksetID != "desktop:global" || discovered.WorksetSubscriptionID != "desktop-client:desktop:global" || !discovered.AutoSubscribed || discovered.SubscriptionID == "" {
		t.Fatalf("discovery frame = %+v", discovered)
	}
	assertV3RealtimeSignedCursorSeq(t, server, discovered.EndpointCursor, created.RealtimeOutbox.EndpointSeq)
	event := readV3RealtimeFrame(t, conn)
	assertV3RealtimeFrame(t, event, V3RealtimeKindEvent, created.SessionID, created.Event.Seq)
	if event.EventType != "session.created" {
		t.Fatalf("discovered event frame = %+v", event)
	}
}

func TestV3RealtimeWorksetReplayDiscoversSessionCommittedAfterSnapshotCursor(t *testing.T) {
	server, _, _, _, _ := newRoutedSessionTestServerWithSwarmStore(t)
	seed := createV3RealtimeTestSessionResult(t, server, "session-realtime-workset-replay-seed", "create-realtime-workset-replay-seed")
	snapshotCursor := signedV3RealtimeCursorForTest(t, server, seed.RealtimeOutbox.EndpointSeq)
	created := createV3RealtimeTestSessionResult(t, server, "session-realtime-workset-replay", "create-realtime-workset-replay")

	httpServer := newV3RealtimeHTTPTestServer(t, server)
	conn := dialV3RealtimeStream(t, httpServer.URL)
	defer conn.Close()
	writeV3RealtimeMessage(t, conn, V3RealtimeMessage{Protocol: V3RealtimeProtocol, ProtocolVersion: V3RealtimeProtocolVersion, Kind: V3RealtimeKindResume, EndpointCursor: snapshotCursor, Worksets: []V3RealtimeWorksetSubscriptionRequest{v3RealtimeGlobalWorksetRequestForTest()}})

	discovered := readV3RealtimeFrame(t, conn)
	assertV3RealtimeFrame(t, discovered, V3RealtimeKindWorksetSessionDiscovered, created.SessionID, 0)
	event := readV3RealtimeFrame(t, conn)
	assertV3RealtimeFrame(t, event, V3RealtimeKindEvent, created.SessionID, created.Event.Seq)
}

func TestV3RealtimeDoesNotAdvancePastUndiscoveredMatchingWorksetSession(t *testing.T) {
	server, _, _, _, _ := newRoutedSessionTestServerWithSwarmStore(t)
	created := createV3RealtimeTestSessionResult(t, server, "session-realtime-workset-no-skip", "create-realtime-workset-no-skip")
	appendResult := appendV3RealtimeTestMessage(t, server, created.SessionID, "message-realtime-workset-no-skip", "must not skip")

	httpServer := newV3RealtimeHTTPTestServer(t, server)
	conn := dialV3RealtimeStream(t, httpServer.URL)
	defer conn.Close()
	writeV3RealtimeMessage(t, conn, V3RealtimeMessage{Protocol: V3RealtimeProtocol, ProtocolVersion: V3RealtimeProtocolVersion, Kind: V3RealtimeKindResume, EndpointCursor: signedV3RealtimeCursorForTest(t, server, 0), Worksets: []V3RealtimeWorksetSubscriptionRequest{v3RealtimeGlobalWorksetRequestForTest()}})

	discovered := readV3RealtimeFrame(t, conn)
	assertV3RealtimeFrame(t, discovered, V3RealtimeKindWorksetSessionDiscovered, created.SessionID, 0)
	firstEvent := readV3RealtimeFrame(t, conn)
	assertV3RealtimeFrame(t, firstEvent, V3RealtimeKindEvent, created.SessionID, created.Event.Seq)
	secondEvent := readV3RealtimeFrame(t, conn)
	assertV3RealtimeFrame(t, secondEvent, V3RealtimeKindEvent, created.SessionID, appendResult.Event.Seq)
	assertV3RealtimeSignedCursorSeq(t, server, secondEvent.EndpointCursor, appendResult.RealtimeOutbox.EndpointSeq)
}

func TestV3RealtimeWorksetAutoSubscribeDeliversSubsequentEvents(t *testing.T) {
	server, _, _, _, _ := newRoutedSessionTestServerWithSwarmStore(t)
	httpServer := newV3RealtimeHTTPTestServer(t, server)
	conn := dialV3RealtimeStream(t, httpServer.URL)
	defer conn.Close()
	writeV3RealtimeMessage(t, conn, V3RealtimeMessage{Protocol: V3RealtimeProtocol, ProtocolVersion: V3RealtimeProtocolVersion, Kind: V3RealtimeKindResume, EndpointCursor: signedV3RealtimeCursorForTest(t, server, 0), Worksets: []V3RealtimeWorksetSubscriptionRequest{v3RealtimeGlobalWorksetRequestForTest()}})

	created := createV3RealtimeTestSessionResult(t, server, "session-realtime-workset-auto", "create-realtime-workset-auto")
	assertV3RealtimeFrame(t, readV3RealtimeFrame(t, conn), V3RealtimeKindWorksetSessionDiscovered, created.SessionID, 0)
	assertV3RealtimeFrame(t, readV3RealtimeFrame(t, conn), V3RealtimeKindEvent, created.SessionID, created.Event.Seq)
	appendResult := appendV3RealtimeTestMessage(t, server, created.SessionID, "message-realtime-workset-auto", "auto subscribed")
	assertV3RealtimeFrame(t, readV3RealtimeFrame(t, conn), V3RealtimeKindEvent, created.SessionID, appendResult.Event.Seq)
}

func TestV3RealtimeWorksetArchiveSendsRemovalWithTombstone(t *testing.T) {
	server, _, _, _, _ := newRoutedSessionTestServerWithSwarmStore(t)
	created := createV3RealtimeTestSessionResult(t, server, "session-realtime-workset-archive", "create-realtime-workset-archive")

	httpServer := newV3RealtimeHTTPTestServer(t, server)
	conn := dialV3RealtimeStream(t, httpServer.URL)
	defer conn.Close()
	writeV3RealtimeMessage(t, conn, V3RealtimeMessage{Protocol: V3RealtimeProtocol, ProtocolVersion: V3RealtimeProtocolVersion, Kind: V3RealtimeKindResume, EndpointCursor: signedV3RealtimeCursorForTest(t, server, 0), Worksets: []V3RealtimeWorksetSubscriptionRequest{v3RealtimeGlobalWorksetRequestForTest()}})
	assertV3RealtimeFrame(t, readV3RealtimeFrame(t, conn), V3RealtimeKindWorksetSessionDiscovered, created.SessionID, 0)
	assertV3RealtimeFrame(t, readV3RealtimeFrame(t, conn), V3RealtimeKindEvent, created.SessionID, created.Event.Seq)

	archiveReq := httptest.NewRequest(http.MethodPost, "/v3/sessions:archive", strings.NewReader(`{"session_ids":["`+created.SessionID+`"]}`))
	archiveReq.Header.Set("Content-Type", "application/json")
	archiveRec := httptest.NewRecorder()
	server.Handler().ServeHTTP(archiveRec, withTestPrincipal(archiveReq))
	if archiveRec.Code != http.StatusOK {
		t.Fatalf("archive status = %d body=%s", archiveRec.Code, archiveRec.Body.String())
	}

	removed := readV3RealtimeFrame(t, conn)
	assertV3RealtimeFrame(t, removed, V3RealtimeKindWorksetSessionRemoved, created.SessionID, 0)
	if removed.EventType != "session.archived" || removed.Tombstone == nil || removed.Tombstone.Kind != "archived" || !removed.Tombstone.Archived || removed.Tombstone.Deleted {
		t.Fatalf("archive removal frame = %+v", removed)
	}
	event := readV3RealtimeFrame(t, conn)
	assertV3RealtimeFrame(t, event, V3RealtimeKindEvent, created.SessionID, created.Event.Seq+1)
	if event.EventType != "session.archived" {
		t.Fatalf("archive event frame = %+v", event)
	}
}

func TestV3RealtimeWorksetArchiveWithoutExplicitSessionSubscriptionSendsRemoval(t *testing.T) {
	server, _, _, _, _ := newRoutedSessionTestServerWithSwarmStore(t)
	created := createV3RealtimeTestSessionResult(t, server, "session-realtime-workset-archive-no-sub", "create-realtime-workset-archive-no-sub")
	httpServer := newV3RealtimeHTTPTestServer(t, server)
	conn := dialV3RealtimeStream(t, httpServer.URL)
	defer conn.Close()
	workset := v3RealtimeGlobalWorksetRequestForTest()
	workset.AutoSubscribeSessions = false
	writeV3RealtimeMessage(t, conn, V3RealtimeMessage{Protocol: V3RealtimeProtocol, ProtocolVersion: V3RealtimeProtocolVersion, Kind: V3RealtimeKindResume, EndpointCursor: signedV3RealtimeCursorForTest(t, server, created.RealtimeOutbox.EndpointSeq), Worksets: []V3RealtimeWorksetSubscriptionRequest{workset}})
	time.Sleep(10 * time.Millisecond)

	archiveReq := httptest.NewRequest(http.MethodPost, "/v3/sessions:archive", strings.NewReader(`{"session_ids":["`+created.SessionID+`"]}`))
	archiveReq.Header.Set("Content-Type", "application/json")
	archiveRec := httptest.NewRecorder()
	server.Handler().ServeHTTP(archiveRec, withTestPrincipal(archiveReq))
	if archiveRec.Code != http.StatusOK {
		t.Fatalf("archive status = %d body=%s", archiveRec.Code, archiveRec.Body.String())
	}
	removed := readV3RealtimeFrame(t, conn)
	assertV3RealtimeFrame(t, removed, V3RealtimeKindWorksetSessionRemoved, created.SessionID, 0)
	if removed.Tombstone == nil || !removed.Tombstone.Archived {
		t.Fatalf("archive removal frame = %+v", removed)
	}
	event := readV3RealtimeFrame(t, conn)
	assertV3RealtimeFrame(t, event, V3RealtimeKindEvent, created.SessionID, created.Event.Seq+1)
	if event.EventType != "session.archived" {
		t.Fatalf("archive event frame = %+v", event)
	}
}

func TestV3RealtimeWorksetVisibilityChangedInDiscoversAndAutoSubscribes(t *testing.T) {
	server, _, _, _, _ := newRoutedSessionTestServerWithSwarmStore(t)
	created := createV3RealtimeTestSessionResult(t, server, "session-realtime-workset-visibility-in", "create-realtime-workset-visibility-in")
	moved := *created.Session
	moved.WorkspacePath = "/workspace/visible"
	moved.WorkspaceName = "visible"

	httpServer := newV3RealtimeHTTPTestServer(t, server)
	conn := dialV3RealtimeStream(t, httpServer.URL)
	defer conn.Close()
	writeV3RealtimeMessage(t, conn, V3RealtimeMessage{Protocol: V3RealtimeProtocol, ProtocolVersion: V3RealtimeProtocolVersion, Kind: V3RealtimeKindResume, EndpointCursor: signedV3RealtimeCursorForTest(t, server, created.RealtimeOutbox.EndpointSeq), Worksets: []V3RealtimeWorksetSubscriptionRequest{v3RealtimeWorkspaceWorksetRequestForTest("/workspace/visible")}})

	visibilityChanged, err := server.applySessionV3PrimaryMutation(sessionruntime.SessionMutationInput{
		SessionID:       moved.ID,
		UserID:          testPrincipal().UserID,
		AccountScopeID:  testPrincipal().AccountScopeID,
		ClientRequestID: "visibility-in-realtime-workset",
		IdempotencyKey:  "visibility-in-realtime-workset",
		PayloadHash:     "hash-visibility-in-realtime-workset",
		Kind:            sessionruntime.SessionMutationUpdateMetadata,
		EventType:       "session.visibility.changed",
		Session:         &moved,
		NowUnixMs:       2000,
	})
	if err != nil {
		t.Fatalf("change session visibility into workset: %v", err)
	}
	if visibilityChanged.RealtimeOutbox == nil {
		t.Fatalf("visibility changed mutation missing realtime outbox: %+v", visibilityChanged)
	}

	discovered := readV3RealtimeFrame(t, conn)
	assertV3RealtimeFrame(t, discovered, V3RealtimeKindWorksetSessionDiscovered, created.SessionID, 0)
	if discovered.WorksetID != "desktop:workspace:/workspace/visible" || discovered.WorksetSubscriptionID != "desktop-client:desktop:workspace:/workspace/visible" || !discovered.AutoSubscribed || discovered.SubscriptionID == "" || discovered.EventType != "session.visibility.changed" {
		t.Fatalf("visibility-in discovery frame = %+v", discovered)
	}
	assertV3RealtimeSignedCursorSeq(t, server, discovered.EndpointCursor, visibilityChanged.RealtimeOutbox.EndpointSeq)

	event := readV3RealtimeFrame(t, conn)
	assertV3RealtimeFrame(t, event, V3RealtimeKindEvent, created.SessionID, visibilityChanged.Event.Seq)
	if event.EventType != "session.visibility.changed" {
		t.Fatalf("visibility-in event frame = %+v", event)
	}

	appendResult := appendV3RealtimeTestMessage(t, server, created.SessionID, "message-realtime-workset-visibility-in", "visibility-in auto subscribed")
	assertV3RealtimeFrame(t, readV3RealtimeFrame(t, conn), V3RealtimeKindEvent, created.SessionID, appendResult.Event.Seq)
}

func TestV3RealtimeWorksetVisibilityChangedStillInDoesNotRemove(t *testing.T) {
	server, _, _, _, _ := newRoutedSessionTestServerWithSwarmStore(t)
	created := createV3RealtimeTestSessionResult(t, server, "session-realtime-workset-visibility-still-in", "create-realtime-workset-visibility-still-in")
	httpServer := newV3RealtimeHTTPTestServer(t, server)
	conn := dialV3RealtimeStream(t, httpServer.URL)
	defer conn.Close()
	writeV3RealtimeMessage(t, conn, V3RealtimeMessage{Protocol: V3RealtimeProtocol, ProtocolVersion: V3RealtimeProtocolVersion, Kind: V3RealtimeKindResume, EndpointCursor: signedV3RealtimeCursorForTest(t, server, 0), Worksets: []V3RealtimeWorksetSubscriptionRequest{v3RealtimeWorkspaceWorksetRequestForTest("/workspace/realtime")}})
	assertV3RealtimeFrame(t, readV3RealtimeFrame(t, conn), V3RealtimeKindWorksetSessionDiscovered, created.SessionID, 0)
	assertV3RealtimeFrame(t, readV3RealtimeFrame(t, conn), V3RealtimeKindEvent, created.SessionID, created.Event.Seq)

	stillVisible := *created.Session
	stillVisible.WorkspaceName = "still-visible"
	visibilityChanged, err := server.applySessionV3PrimaryMutation(sessionruntime.SessionMutationInput{
		SessionID:       stillVisible.ID,
		UserID:          testPrincipal().UserID,
		AccountScopeID:  testPrincipal().AccountScopeID,
		ClientRequestID: "visibility-still-in-realtime-workset",
		IdempotencyKey:  "visibility-still-in-realtime-workset",
		PayloadHash:     "hash-visibility-still-in-realtime-workset",
		Kind:            sessionruntime.SessionMutationUpdateMetadata,
		EventType:       "session.visibility.changed",
		Session:         &stillVisible,
		NowUnixMs:       2000,
	})
	if err != nil {
		t.Fatalf("change session visibility within workset: %v", err)
	}
	if visibilityChanged.RealtimeOutbox == nil {
		t.Fatalf("visibility still-in mutation missing realtime outbox: %+v", visibilityChanged)
	}

	event := readV3RealtimeFrame(t, conn)
	assertV3RealtimeFrame(t, event, V3RealtimeKindEvent, created.SessionID, visibilityChanged.Event.Seq)
	if event.EventType != "session.visibility.changed" {
		t.Fatalf("visibility still-in event frame = %+v", event)
	}
	assertV3RealtimeSignedCursorSeq(t, server, event.EndpointCursor, visibilityChanged.RealtimeOutbox.EndpointSeq)

	appendResult := appendV3RealtimeTestMessage(t, server, created.SessionID, "message-realtime-workset-visibility-still-in", "still-in remains subscribed")
	assertV3RealtimeFrame(t, readV3RealtimeFrame(t, conn), V3RealtimeKindEvent, created.SessionID, appendResult.Event.Seq)
}

func TestV3RealtimeWorksetVisibilityChangedOutRemovesAndUnsubscribes(t *testing.T) {
	server, _, _, _, _ := newRoutedSessionTestServerWithSwarmStore(t)
	created := createV3RealtimeTestSessionResult(t, server, "session-realtime-workset-visibility-out", "create-realtime-workset-visibility-out")
	httpServer := newV3RealtimeHTTPTestServer(t, server)
	conn := dialV3RealtimeStream(t, httpServer.URL)
	defer conn.Close()
	writeV3RealtimeMessage(t, conn, V3RealtimeMessage{Protocol: V3RealtimeProtocol, ProtocolVersion: V3RealtimeProtocolVersion, Kind: V3RealtimeKindResume, EndpointCursor: signedV3RealtimeCursorForTest(t, server, 0), Worksets: []V3RealtimeWorksetSubscriptionRequest{v3RealtimeWorkspaceWorksetRequestForTest("/workspace/realtime")}})
	assertV3RealtimeFrame(t, readV3RealtimeFrame(t, conn), V3RealtimeKindWorksetSessionDiscovered, created.SessionID, 0)
	assertV3RealtimeFrame(t, readV3RealtimeFrame(t, conn), V3RealtimeKindEvent, created.SessionID, created.Event.Seq)

	movedOut := *created.Session
	movedOut.WorkspacePath = "/workspace/other"
	movedOut.WorkspaceName = "other"
	visibilityChanged, err := server.applySessionV3PrimaryMutation(sessionruntime.SessionMutationInput{
		SessionID:       movedOut.ID,
		UserID:          testPrincipal().UserID,
		AccountScopeID:  testPrincipal().AccountScopeID,
		ClientRequestID: "visibility-out-realtime-workset",
		IdempotencyKey:  "visibility-out-realtime-workset",
		PayloadHash:     "hash-visibility-out-realtime-workset",
		Kind:            sessionruntime.SessionMutationUpdateMetadata,
		EventType:       "session.visibility.changed",
		Session:         &movedOut,
		NowUnixMs:       2000,
	})
	if err != nil {
		t.Fatalf("change session visibility out of workset: %v", err)
	}
	if visibilityChanged.RealtimeOutbox == nil {
		t.Fatalf("visibility out mutation missing realtime outbox: %+v", visibilityChanged)
	}

	removed := readV3RealtimeFrame(t, conn)
	assertV3RealtimeFrame(t, removed, V3RealtimeKindWorksetSessionRemoved, created.SessionID, 0)
	if removed.WorksetID != "desktop:workspace:/workspace/realtime" || removed.WorksetSubscriptionID != "desktop-client:desktop:workspace:/workspace/realtime" || !removed.AutoSubscribed || removed.SubscriptionID == "" || removed.EventType != "session.visibility.changed" {
		t.Fatalf("visibility-out removed frame = %+v", removed)
	}
	assertV3RealtimeSignedCursorSeq(t, server, removed.EndpointCursor, visibilityChanged.RealtimeOutbox.EndpointSeq)

	event := readV3RealtimeFrame(t, conn)
	assertV3RealtimeFrame(t, event, V3RealtimeKindEvent, created.SessionID, visibilityChanged.Event.Seq)
	if event.EventType != "session.visibility.changed" {
		t.Fatalf("visibility-out event frame = %+v", event)
	}
	assertV3RealtimeSignedCursorSeq(t, server, event.EndpointCursor, visibilityChanged.RealtimeOutbox.EndpointSeq)

	appendV3RealtimeTestMessage(t, server, created.SessionID, "message-realtime-workset-visibility-out", "out is unsubscribed")
	assertNoV3RealtimeEventFrame(t, conn, 150*time.Millisecond)
}

func TestV3RealtimeWorksetRemovalUnsubscribesOrDeactivatesSession(t *testing.T) {
	server, sessionSvc, _, _, _ := newRoutedSessionTestServerWithSwarmStore(t)
	created := createV3RealtimeTestSessionResult(t, server, "session-realtime-workset-remove", "create-realtime-workset-remove")
	httpServer := newV3RealtimeHTTPTestServer(t, server)
	conn := dialV3RealtimeStream(t, httpServer.URL)
	defer conn.Close()
	writeV3RealtimeMessage(t, conn, V3RealtimeMessage{Protocol: V3RealtimeProtocol, ProtocolVersion: V3RealtimeProtocolVersion, Kind: V3RealtimeKindResume, EndpointCursor: signedV3RealtimeCursorForTest(t, server, created.RealtimeOutbox.EndpointSeq), Worksets: []V3RealtimeWorksetSubscriptionRequest{v3RealtimeGlobalWorksetRequestForTest()}, Subscriptions: []V3RealtimeSubscriptionRequest{{SessionID: created.SessionID, SubscriptionID: "desktop-client:session:" + created.SessionID, EndpointCursor: signedV3RealtimeCursorForTest(t, server, created.RealtimeOutbox.EndpointSeq)}}})
	assertV3RealtimeFrame(t, readV3RealtimeFrame(t, conn), V3RealtimeKindReplayStart, created.SessionID, 0)
	assertV3RealtimeFrame(t, readV3RealtimeFrame(t, conn), V3RealtimeKindReplayDone, created.SessionID, created.Event.Seq)

	if err := sessionSvc.DeleteSession(created.SessionID); err != nil {
		t.Fatalf("delete session: %v", err)
	}
	rows, err := sessionSvc.ListRealtimeOutboxAfter(created.RealtimeOutbox.EndpointSeq, 10)
	if err != nil || len(rows) == 0 {
		t.Fatalf("list delete outbox: rows=%+v err=%v", rows, err)
	}
	if err := server.publishCommittedV3RealtimeOutbox(rows[len(rows)-1]); err != nil {
		t.Fatalf("publish delete outbox: %v", err)
	}
	removed := readV3RealtimeFrame(t, conn)
	assertV3RealtimeFrame(t, removed, V3RealtimeKindWorksetSessionRemoved, created.SessionID, 0)
	event := readV3RealtimeFrame(t, conn)
	assertV3RealtimeFrame(t, event, V3RealtimeKindEvent, created.SessionID, created.Event.Seq+1)
}

func TestV3RealtimeResumeRejectsInvalidWorksetAtomically(t *testing.T) {
	server, _, _, _, _ := newRoutedSessionTestServerWithSwarmStore(t)
	created := createV3RealtimeTestSessionResult(t, server, "session-realtime-invalid-workset", "create-realtime-invalid-workset")
	httpServer := newV3RealtimeHTTPTestServer(t, server)
	conn := dialV3RealtimeStream(t, httpServer.URL)
	defer conn.Close()
	writeV3RealtimeMessage(t, conn, V3RealtimeMessage{Protocol: V3RealtimeProtocol, ProtocolVersion: V3RealtimeProtocolVersion, Kind: V3RealtimeKindResume, EndpointCursor: signedV3RealtimeCursorForTest(t, server, created.RealtimeOutbox.EndpointSeq), Subscriptions: []V3RealtimeSubscriptionRequest{{SessionID: created.SessionID, SubscriptionID: "sub-valid", EndpointCursor: signedV3RealtimeCursorForTest(t, server, created.RealtimeOutbox.EndpointSeq)}}, Worksets: []V3RealtimeWorksetSubscriptionRequest{{WorksetID: "bad", SubscriptionID: "bad-sub", Selector: V3RealtimeWorksetSelector{Kind: "workspace", WorkspacePath: "relative"}, AutoSubscribeSessions: true}}})
	frame := readV3RealtimeFrame(t, conn)
	assertV3RealtimeFrame(t, frame, V3RealtimeKindAuthDenied, "", 0)
	if frame.ErrorCode != "invalid_workset_selector" {
		t.Fatalf("invalid workset frame = %+v", frame)
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

	writeV3RealtimeMessage(t, conn, V3RealtimeMessage{Protocol: V3RealtimeProtocol, ProtocolVersion: V3RealtimeProtocolVersion, Kind: V3RealtimeKindSubscribe, SessionID: a.ID, SubscriptionID: "sub-a", EndpointCursor: signedV3RealtimeCursorForTest(t, server, aResult.RealtimeOutbox.EndpointSeq)})
	assertV3RealtimeFrame(t, readV3RealtimeFrame(t, conn), V3RealtimeKindReplayStart, a.ID, 0)
	assertV3RealtimeFrame(t, readV3RealtimeFrame(t, conn), V3RealtimeKindReplayDone, a.ID, 1)
	writeV3RealtimeMessage(t, conn, V3RealtimeMessage{Protocol: V3RealtimeProtocol, ProtocolVersion: V3RealtimeProtocolVersion, Kind: V3RealtimeKindSubscribe, SessionID: b.ID, SubscriptionID: "sub-b", EndpointCursor: signedV3RealtimeCursorForTest(t, server, 0)})
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

	writeV3RealtimeMessage(t, conn, V3RealtimeMessage{Protocol: V3RealtimeProtocol, ProtocolVersion: V3RealtimeProtocolVersion, Kind: V3RealtimeKindSubscribe, SessionID: "v3/session:*", SubscriptionID: "sub-wildcard", EndpointCursor: signedV3RealtimeCursorForTest(t, server, 0)})
	frame := readV3RealtimeFrame(t, conn)
	assertV3RealtimeFrame(t, frame, V3RealtimeKindAuthDenied, "v3/session:*", 0)
	if frame.ErrorCode == "" {
		t.Fatalf("wildcard auth denial missing error code: %+v", frame)
	}

	appendV3RealtimeTestMessage(t, server, created.ID, "message-realtime-auth-a", "secret")
	assertNoV3RealtimeEventFrame(t, conn, 150*time.Millisecond)
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

	writeV3RealtimeMessage(t, conn, V3RealtimeMessage{Protocol: V3RealtimeProtocol, ProtocolVersion: V3RealtimeProtocolVersion, Kind: V3RealtimeKindSubscribe, SessionID: a.ID, SubscriptionID: "sub-a", EndpointCursor: signedV3RealtimeCursorForTest(t, server, aResult.RealtimeOutbox.EndpointSeq)})
	assertV3RealtimeFrame(t, readV3RealtimeFrame(t, conn), V3RealtimeKindReplayStart, a.ID, 0)
	assertV3RealtimeFrame(t, readV3RealtimeFrame(t, conn), V3RealtimeKindReplayDone, a.ID, 1)
	writeV3RealtimeMessage(t, conn, V3RealtimeMessage{Protocol: V3RealtimeProtocol, ProtocolVersion: V3RealtimeProtocolVersion, Kind: V3RealtimeKindSubscribe, SessionID: b.ID, SubscriptionID: "sub-b", EndpointCursor: signedV3RealtimeCursorForTest(t, server, bResult.RealtimeOutbox.EndpointSeq)})
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

func TestV3RealtimeFiveSessionsInterleaveByEndpointSeq(t *testing.T) {
	server, sessionSvc, _, _, _ := newRoutedSessionTestServerWithSwarmStore(t)
	httpServer := newV3RealtimeHTTPTestServer(t, server)
	conn := dialV3RealtimeStream(t, httpServer.URL)
	defer conn.Close()

	writeV3RealtimeMessage(t, conn, V3RealtimeMessage{Protocol: V3RealtimeProtocol, ProtocolVersion: V3RealtimeProtocolVersion, Kind: V3RealtimeKindResume, EndpointCursor: signedV3RealtimeCursorForTest(t, server, 0), Worksets: []V3RealtimeWorksetSubscriptionRequest{v3RealtimeGlobalWorksetRequestForTest()}})

	sessionIDs := []string{"session-realtime-five-a", "session-realtime-five-b", "session-realtime-five-c", "session-realtime-five-d", "session-realtime-five-e"}
	allEventFrames := make([]V3RealtimeMessage, 0, len(sessionIDs)*3)
	createdBySession := make(map[string]sessionruntime.SessionMutationResult, len(sessionIDs))
	for index, sessionID := range sessionIDs {
		created := createV3RealtimeTestSessionResult(t, server, sessionID, fmt.Sprintf("create-realtime-five-%d", index))
		createdBySession[sessionID] = created

		discovered := readV3RealtimeFrame(t, conn)
		assertV3RealtimeFrame(t, discovered, V3RealtimeKindWorksetSessionDiscovered, sessionID, 0)
		if discovered.WorksetID != "desktop:global" || !discovered.AutoSubscribed || discovered.SubscriptionID == "" {
			t.Fatalf("five-session discovery frame = %+v", discovered)
		}
		assertV3RealtimeSignedCursorSeq(t, server, discovered.EndpointCursor, created.RealtimeOutbox.EndpointSeq)

		event := readV3RealtimeFrame(t, conn)
		assertV3RealtimeFrame(t, event, V3RealtimeKindEvent, sessionID, created.Event.Seq)
		assertV3RealtimeSignedCursorSeq(t, server, event.EndpointCursor, created.RealtimeOutbox.EndpointSeq)
		allEventFrames = append(allEventFrames, event)
	}

	appendOrder := []string{
		sessionIDs[0], sessionIDs[1], sessionIDs[2], sessionIDs[3], sessionIDs[4],
		sessionIDs[0], sessionIDs[2], sessionIDs[4], sessionIDs[1], sessionIDs[3],
	}
	appendResults := make([]sessionruntime.SessionMutationResult, 0, len(appendOrder))
	for index, sessionID := range appendOrder {
		appendResults = append(appendResults, appendV3RealtimeTestMessage(t, server, sessionID, fmt.Sprintf("message-realtime-five-%02d", index), fmt.Sprintf("five-session content %02d", index)))
	}

	for index, want := range appendResults {
		frame := readV3RealtimeFrame(t, conn)
		assertV3RealtimeFrame(t, frame, V3RealtimeKindEvent, appendOrder[index], want.Event.Seq)
		assertV3RealtimeSignedCursorSeq(t, server, frame.EndpointCursor, want.RealtimeOutbox.EndpointSeq)
		allEventFrames = append(allEventFrames, frame)
	}

	outboxRows, err := sessionSvc.ListRealtimeOutboxAfter(0, len(allEventFrames)+5)
	if err != nil {
		t.Fatalf("list five-session realtime outbox: %v", err)
	}
	if len(outboxRows) != len(allEventFrames) {
		t.Fatalf("outbox row count = %d, websocket event frames = %d", len(outboxRows), len(allEventFrames))
	}

	seenEndpointSeq := make(map[uint64]bool, len(allEventFrames))
	perSessionSeqs := make(map[string][]uint64, len(sessionIDs))
	var previousEndpointSeq uint64
	for index, frame := range allEventFrames {
		if frame.Rev <= previousEndpointSeq {
			t.Fatalf("endpoint_seq regressed at frame %d: prev=%d frame=%+v", index, previousEndpointSeq, frame)
		}
		previousEndpointSeq = frame.Rev
		seenEndpointSeq[frame.Rev] = true
		perSessionSeqs[frame.SessionID] = append(perSessionSeqs[frame.SessionID], frame.Event.Seq)
	}

	for _, row := range outboxRows {
		if !seenEndpointSeq[row.EndpointSeq] {
			t.Fatalf("durable outbox row %+v did not produce exactly one websocket event; seen=%v", row, seenEndpointSeq)
		}
		seenEndpointSeq[row.EndpointSeq] = false
	}
	for endpointSeq, stillTrue := range seenEndpointSeq {
		if stillTrue {
			t.Fatalf("websocket event endpoint_seq %d has no durable outbox row", endpointSeq)
		}
	}

	for _, sessionID := range sessionIDs {
		seqs := perSessionSeqs[sessionID]
		if len(seqs) != 3 {
			t.Fatalf("session %s event seqs = %v, want create plus two appends", sessionID, seqs)
		}
		for index, seq := range seqs {
			want := uint64(index + 1)
			if seq != want {
				t.Fatalf("session %s event seqs = %v, want contiguous starting at 1", sessionID, seqs)
			}
		}
		if createdBySession[sessionID].Session == nil {
			t.Fatalf("session %s was not created for global workset subscription", sessionID)
		}
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
			for _, forbidden := range []string{"ListRealtimeOutboxForSessionAfterSeq", "message.AfterSeq", "message.AfterRev", "firstNonZeroUint64(message.AfterRev", "BuildSessionWorkset", "sessionsV3WorksetRequest"} {
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

func TestApplySessionV3PrimaryMutationAcceptsDurableCommitWhenRealtimeWakeFails(t *testing.T) {
	server, sessionSvc, _, _, _ := newRoutedSessionTestServerWithSwarmStore(t)
	created := createV3RealtimeTestSessionResult(t, server, "session-realtime-wake-fail", "create-realtime-wake-fail")
	checkpointCursor := signedV3RealtimeCursorForTest(t, server, created.RealtimeOutbox.EndpointSeq)

	previousWake := publishCommittedV3RealtimeOutboxWake
	publishCommittedV3RealtimeOutboxWake = func(*v3RealtimeOutboxHub, sessionruntime.RealtimeOutboxRecord) error {
		return errors.New("simulated realtime wake failure after durable commit")
	}
	t.Cleanup(func() { publishCommittedV3RealtimeOutboxWake = previousWake })

	input := sessionruntime.SessionMutationInput{
		SessionID:       created.SessionID,
		UserID:          testPrincipal().UserID,
		AccountScopeID:  testPrincipal().AccountScopeID,
		ClientRequestID: "message-realtime-wake-fail",
		IdempotencyKey:  "message-realtime-wake-fail",
		PayloadHash:     "hash-message-realtime-wake-fail",
		Kind:            sessionruntime.SessionMutationAppendMessage,
		Message:         &pebblestore.MessageSnapshot{Role: "user", Content: "durable despite realtime wake failure"},
		NowUnixMs:       2000,
	}
	committed, err := server.applySessionV3PrimaryMutation(input)
	if err != nil {
		t.Fatalf("apply mutation should accept durable commit despite post-commit realtime wake failure: %v", err)
	}
	if committed.RealtimeOutbox == nil || committed.Event.Seq != 2 {
		t.Fatalf("committed mutation missing realtime outbox or seq: %+v", committed)
	}
	outboxRows, err := sessionSvc.ListRealtimeOutboxAfter(created.RealtimeOutbox.EndpointSeq, 10)
	if err != nil {
		t.Fatalf("list realtime outbox after failed wake: %v", err)
	}
	if len(outboxRows) != 1 || outboxRows[0].EndpointSeq != committed.RealtimeOutbox.EndpointSeq {
		t.Fatalf("durable outbox rows after failed wake = %+v, want committed %+v", outboxRows, committed.RealtimeOutbox)
	}

	replayed, replayErr := server.applySessionV3PrimaryMutation(input)
	if replayErr != nil {
		t.Fatalf("idempotent retry after accepted durable commit: %v", replayErr)
	}
	if !replayed.Replayed || replayed.RealtimeOutbox != nil || replayed.Event.Seq != committed.Event.Seq {
		t.Fatalf("idempotent retry = %+v, want replayed without duplicate outbox for committed seq %d", replayed, committed.Event.Seq)
	}
	outboxRowsAfterRetry, err := sessionSvc.ListRealtimeOutboxAfter(created.RealtimeOutbox.EndpointSeq, 10)
	if err != nil {
		t.Fatalf("list realtime outbox after idempotent retry: %v", err)
	}
	if len(outboxRowsAfterRetry) != 1 || outboxRowsAfterRetry[0].EndpointSeq != committed.RealtimeOutbox.EndpointSeq {
		t.Fatalf("outbox rows after idempotent retry = %+v, want single committed row", outboxRowsAfterRetry)
	}

	httpServer := newV3RealtimeHTTPTestServer(t, server)
	conn := dialV3RealtimeStreamWithQuery(t, httpServer.URL, "endpoint_cursor="+url.QueryEscape(checkpointCursor)+"&sessions="+created.SessionID)
	defer conn.Close()
	assertV3RealtimeFrame(t, readV3RealtimeFrame(t, conn), V3RealtimeKindReplayStart, created.SessionID, 0)
	replayedFrame := readV3RealtimeFrame(t, conn)
	assertV3RealtimeFrame(t, replayedFrame, V3RealtimeKindEvent, created.SessionID, committed.Event.Seq)
	assertV3RealtimeSignedCursorSeq(t, server, replayedFrame.EndpointCursor, committed.RealtimeOutbox.EndpointSeq)
	assertV3RealtimeFrame(t, readV3RealtimeFrame(t, conn), V3RealtimeKindReplayDone, created.SessionID, committed.Event.Seq)
}

func TestApplySessionV3PrimaryMutationUsesNativeRealtimeWithoutGlobalMirror(t *testing.T) {
	server, sessionSvc, _, _, _ := newRoutedSessionTestServerWithSwarmStore(t)
	created := createV3RealtimeTestSessionResult(t, server, "session-realtime-no-global-mirror", "create-realtime-no-global-mirror")
	checkpointCursor := signedV3RealtimeCursorForTest(t, server, created.RealtimeOutbox.EndpointSeq)

	input := sessionruntime.SessionMutationInput{
		SessionID:       created.SessionID,
		UserID:          testPrincipal().UserID,
		AccountScopeID:  testPrincipal().AccountScopeID,
		ClientRequestID: "message-realtime-global-fail",
		IdempotencyKey:  "message-realtime-global-fail",
		PayloadHash:     "hash-message-realtime-global-fail",
		Kind:            sessionruntime.SessionMutationAppendMessage,
		Message:         &pebblestore.MessageSnapshot{Role: "user", Content: "durable despite global mirror failure"},
		NowUnixMs:       2000,
	}
	committed, err := server.applySessionV3PrimaryMutation(input)
	if err != nil {
		t.Fatalf("apply mutation should accept durable commit despite post-commit global mirror failure: %v", err)
	}
	if committed.RealtimeOutbox == nil || committed.Event.Seq != 2 {
		t.Fatalf("committed mutation missing realtime outbox or seq: %+v", committed)
	}
	outboxRows, err := sessionSvc.ListRealtimeOutboxAfter(created.RealtimeOutbox.EndpointSeq, 10)
	if err != nil {
		t.Fatalf("list realtime outbox after failed global mirror: %v", err)
	}
	if len(outboxRows) != 1 || outboxRows[0].EndpointSeq != committed.RealtimeOutbox.EndpointSeq {
		t.Fatalf("durable outbox rows after failed global mirror = %+v, want committed %+v", outboxRows, committed.RealtimeOutbox)
	}

	replayed, replayErr := server.applySessionV3PrimaryMutation(input)
	if replayErr != nil {
		t.Fatalf("idempotent retry after accepted durable commit: %v", replayErr)
	}
	if !replayed.Replayed || replayed.RealtimeOutbox != nil || replayed.Event.Seq != committed.Event.Seq {
		t.Fatalf("idempotent retry = %+v, want replayed without duplicate outbox for committed seq %d", replayed, committed.Event.Seq)
	}
	outboxRowsAfterRetry, err := sessionSvc.ListRealtimeOutboxAfter(created.RealtimeOutbox.EndpointSeq, 10)
	if err != nil {
		t.Fatalf("list realtime outbox after idempotent retry: %v", err)
	}
	if len(outboxRowsAfterRetry) != 1 || outboxRowsAfterRetry[0].EndpointSeq != committed.RealtimeOutbox.EndpointSeq {
		t.Fatalf("outbox rows after idempotent retry = %+v, want single committed row", outboxRowsAfterRetry)
	}

	httpServer := newV3RealtimeHTTPTestServer(t, server)
	conn := dialV3RealtimeStreamWithQuery(t, httpServer.URL, "endpoint_cursor="+url.QueryEscape(checkpointCursor)+"&sessions="+created.SessionID)
	defer conn.Close()
	assertV3RealtimeFrame(t, readV3RealtimeFrame(t, conn), V3RealtimeKindReplayStart, created.SessionID, 0)
	replayedFrame := readV3RealtimeFrame(t, conn)
	assertV3RealtimeFrame(t, replayedFrame, V3RealtimeKindEvent, created.SessionID, committed.Event.Seq)
	assertV3RealtimeSignedCursorSeq(t, server, replayedFrame.EndpointCursor, committed.RealtimeOutbox.EndpointSeq)
	assertV3RealtimeFrame(t, readV3RealtimeFrame(t, conn), V3RealtimeKindReplayDone, created.SessionID, committed.Event.Seq)
}

func TestV3RealtimeSlowConsumerDropsAndReconnectCatchesUpFromDurableOutbox(t *testing.T) {
	server, sessionSvc, _, _, _ := newRoutedSessionTestServerWithSwarmStore(t)
	created := createV3RealtimeTestSessionResult(t, server, "session-realtime-slow-recover", "create-realtime-slow-recover")
	slow := server.v3RealtimeOutbox.subscribe()
	if slow == nil {
		t.Fatal("slow test subscriber was nil")
	}
	defer server.v3RealtimeOutbox.unsubscribe(slow)

	committed := make([]sessionruntime.SessionMutationResult, 0, v3RealtimeSubscriberBufSize+1)
	for i := 0; i < v3RealtimeSubscriberBufSize+1; i++ {
		committed = append(committed, appendV3RealtimeTestMessage(t, server, created.SessionID, fmt.Sprintf("message-realtime-slow-recover-%03d", i), fmt.Sprintf("slow durable %03d", i)))
	}
	lastCommitted := committed[len(committed)-1]
	select {
	case notice := <-slow.slow:
		if notice.EndpointSeq != lastCommitted.RealtimeOutbox.EndpointSeq || !strings.Contains(notice.Reason, "reconnect required") {
			t.Fatalf("slow notice = %+v, want endpoint seq %d reconnect", notice, lastCommitted.RealtimeOutbox.EndpointSeq)
		}
	case <-time.After(time.Second):
		t.Fatal("timed out waiting for durable slow-consumer drop notice")
	}

	outboxRows, err := sessionSvc.ListRealtimeOutboxAfter(created.RealtimeOutbox.EndpointSeq, v3RealtimeSubscriberBufSize+2)
	if err != nil {
		t.Fatalf("list durable outbox after slow-consumer drop: %v", err)
	}
	if len(outboxRows) != len(committed) || outboxRows[len(outboxRows)-1].EndpointSeq != lastCommitted.RealtimeOutbox.EndpointSeq {
		t.Fatalf("durable outbox rows after slow-consumer drop = len:%d last:%+v, want len:%d last endpoint:%d", len(outboxRows), outboxRows[len(outboxRows)-1], len(committed), lastCommitted.RealtimeOutbox.EndpointSeq)
	}

	httpServer := newV3RealtimeHTTPTestServer(t, server)
	conn := dialV3RealtimeStreamWithQuery(t, httpServer.URL, "endpoint_cursor="+url.QueryEscape(signedV3RealtimeCursorForTest(t, server, created.RealtimeOutbox.EndpointSeq))+"&sessions="+created.SessionID)
	defer conn.Close()
	assertV3RealtimeFrame(t, readV3RealtimeFrame(t, conn), V3RealtimeKindReplayStart, created.SessionID, 0)
	seenSeqs := map[uint64]bool{}
	for _, want := range committed {
		frame := readV3RealtimeFrame(t, conn)
		assertV3RealtimeFrame(t, frame, V3RealtimeKindEvent, created.SessionID, want.Event.Seq)
		assertV3RealtimeSignedCursorSeq(t, server, frame.EndpointCursor, want.RealtimeOutbox.EndpointSeq)
		if seenSeqs[frame.Event.Seq] {
			t.Fatalf("duplicate event seq after slow-consumer reconnect: %d", frame.Event.Seq)
		}
		seenSeqs[frame.Event.Seq] = true
	}
	assertV3RealtimeFrame(t, readV3RealtimeFrame(t, conn), V3RealtimeKindReplayDone, created.SessionID, lastCommitted.Event.Seq)
}

func TestV3RealtimeConnectedClientRepairsLostHubPublishFromDurableOutbox(t *testing.T) {
	server, sessionSvc, _, _, _ := newRoutedSessionTestServerWithSwarmStore(t)
	created := createV3RealtimeTestSessionResult(t, server, "session-realtime-connected-repair", "create-realtime-connected-repair")
	previousKeepaliveInterval := v3RealtimeKeepaliveInterval
	v3RealtimeKeepaliveInterval = 25 * time.Millisecond
	t.Cleanup(func() { v3RealtimeKeepaliveInterval = previousKeepaliveInterval })

	httpServer := newV3RealtimeHTTPTestServer(t, server)
	conn := dialV3RealtimeStreamWithQuery(t, httpServer.URL, "endpoint_cursor="+url.QueryEscape(signedV3RealtimeCursorForTest(t, server, created.RealtimeOutbox.EndpointSeq))+"&sessions="+created.SessionID)
	defer conn.Close()
	assertV3RealtimeFrame(t, readV3RealtimeFrame(t, conn), V3RealtimeKindReplayStart, created.SessionID, 0)
	assertV3RealtimeFrame(t, readV3RealtimeFrame(t, conn), V3RealtimeKindReplayDone, created.SessionID, 1)

	committed, err := sessionSvc.ApplySessionMutation(sessionruntime.SessionMutationInput{
		SessionID:       created.SessionID,
		UserID:          testPrincipal().UserID,
		AccountScopeID:  testPrincipal().AccountScopeID,
		ClientRequestID: "message-realtime-connected-repair",
		IdempotencyKey:  "message-realtime-connected-repair",
		PayloadHash:     "hash-message-realtime-connected-repair",
		Kind:            sessionruntime.SessionMutationAppendMessage,
		Message:         &pebblestore.MessageSnapshot{Role: "user", Content: "durable row without hub publish"},
		NowUnixMs:       2000,
	})
	if err != nil {
		t.Fatalf("commit mutation without hub publish: %v", err)
	}
	if committed.RealtimeOutbox == nil {
		t.Fatalf("committed mutation missing realtime outbox: %+v", committed)
	}

	var repaired V3RealtimeMessage
	for i := 0; i < 5; i++ {
		frame := readV3RealtimeFrame(t, conn)
		if frame.Kind == V3RealtimeKindEvent {
			repaired = frame
			break
		}
		if frame.Kind != V3RealtimeKindKeepalive {
			t.Fatalf("unexpected frame while waiting for durable catch-up repair: %+v", frame)
		}
	}
	assertV3RealtimeFrame(t, repaired, V3RealtimeKindEvent, created.SessionID, committed.Event.Seq)
	assertV3RealtimeSignedCursorSeq(t, server, repaired.EndpointCursor, committed.RealtimeOutbox.EndpointSeq)
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
	if result.RealtimeOutbox == nil {
		t.Fatalf("create realtime test session missing realtime outbox: %+v", result)
	}
	result.RealtimeOutbox.EndpointCursor = signedV3RealtimeCursorForTest(t, server, result.RealtimeOutbox.EndpointSeq)
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

func assertNoV3RealtimeEventFrame(t *testing.T, conn *gorillaws.Conn, wait time.Duration) {
	t.Helper()
	deadline := time.Now().Add(wait)
	for time.Now().Before(deadline) {
		if err := conn.SetReadDeadline(deadline); err != nil {
			t.Fatalf("set v3 realtime read deadline: %v", err)
		}
		_, raw, err := conn.ReadMessage()
		if err != nil {
			return
		}
		var frame V3RealtimeMessage
		if err := json.Unmarshal(raw, &frame); err != nil {
			t.Fatalf("decode v3 realtime frame %s: %v", string(raw), err)
		}
		if frame.Kind == V3RealtimeKindEvent {
			t.Fatalf("unexpected v3 realtime event frame: %s", string(raw))
		}
	}
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

func v3RealtimeGlobalWorksetRequestForTest() V3RealtimeWorksetSubscriptionRequest {
	return V3RealtimeWorksetSubscriptionRequest{WorksetID: "desktop:global", SubscriptionID: "desktop-client:desktop:global", Surface: "desktop", Selector: V3RealtimeWorksetSelector{Kind: "global", Global: true, Recent: sessionsV3WorksetRecent{Limit: 100}}, AutoSubscribeSessions: true}
}

func v3RealtimeWorkspaceWorksetRequestForTest(workspacePath string) V3RealtimeWorksetSubscriptionRequest {
	worksetID := "desktop:workspace:" + workspacePath
	return V3RealtimeWorksetSubscriptionRequest{WorksetID: worksetID, SubscriptionID: "desktop-client:" + worksetID, Surface: "desktop", Selector: V3RealtimeWorksetSelector{Kind: "workspace", WorkspacePath: workspacePath}, AutoSubscribeSessions: true}
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

func assertV3RealtimeSignedCursorSeq(t *testing.T, server *Server, raw string, want uint64) {
	t.Helper()
	seq, legacy, err := server.parseV3SyncEndpointCursor(raw, v3SyncCursorScopeForRealtime(testPrincipal(), "desktop"))
	if err != nil {
		t.Fatalf("parse signed endpoint cursor %q: %v", raw, err)
	}
	if legacy {
		t.Fatalf("endpoint cursor %q is legacy numeric; want signed opaque cursor", raw)
	}
	if seq != want {
		t.Fatalf("signed endpoint cursor seq = %d, want %d (cursor %q)", seq, want, raw)
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

func hydrateV3SyncSnapshotEndpointCursor(t *testing.T, server *Server, sessionID string) string {
	t.Helper()
	body := `{"surface":"desktop","selector":{"kind":"session_ids","session_ids":["` + sessionID + `"]},"session_ids":["` + sessionID + `"],"history":{"mode":"none"},"resources":{"events":true}}`
	rec := httptest.NewRecorder()
	req := httptest.NewRequest(http.MethodPost, V3SyncHydratePath, strings.NewReader(body))
	req.Header.Set("Content-Type", "application/json")
	server.Handler().ServeHTTP(rec, withTestPrincipal(req))
	if rec.Code != http.StatusOK {
		t.Fatalf("sync hydrate status = %d body=%s", rec.Code, rec.Body.String())
	}
	var payload map[string]any
	if err := json.NewDecoder(rec.Body).Decode(&payload); err != nil {
		t.Fatalf("decode sync hydrate response: %v", err)
	}
	cursor, _ := payload["snapshot_endpoint_cursor"].(string)
	if strings.TrimSpace(cursor) == "" {
		t.Fatalf("sync hydrate response missing snapshot_endpoint_cursor: %+v", payload)
	}
	return cursor
}

func TestV3RealtimeWebsocketSourceGuardNoLegacyWorksetValidation(t *testing.T) {
	source := readSourceFileForTest(t, "sessions_v3_realtime_ws.go")
	validationSource := sourceBetweenForTest(t, source, "func (s *Server) v3RealtimeValidateResumeWorksets", "func (s *Server) v3RealtimeValidateEndpointCursor")
	for _, forbidden := range []string{"BuildSessionWorkset", "sessionsV3WorksetRequest", "sessionsV3WorksetOptionsFromRequest"} {
		if strings.Contains(validationSource, forbidden) {
			t.Fatalf("realtime resume validation contains forbidden legacy workset dependency %q", forbidden)
		}
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

func TestV3RealtimeLegacyResumeReceivesNoLivePatch(t *testing.T) {
	server, _, _, _, _ := newRoutedSessionTestServerWithSwarmStore(t)
	server.v3LivePatchEnabled = true
	created := createV3RealtimeTestSessionResult(t, server, "session-live-legacy", "create-live-legacy")
	httpServer := newV3RealtimeHTTPTestServer(t, server)
	conn := dialV3RealtimeStream(t, httpServer.URL)
	defer conn.Close()

	writeV3RealtimeMessage(t, conn, V3RealtimeMessage{Protocol: V3RealtimeProtocol, ProtocolVersion: V3RealtimeProtocolVersion, Kind: V3RealtimeKindResume, EndpointCursor: signedV3RealtimeCursorForTest(t, server, created.RealtimeOutbox.EndpointSeq), Subscriptions: []V3RealtimeSubscriptionRequest{{SessionID: created.SessionID, SubscriptionID: "sub-live-legacy"}}})
	assertV3RealtimeFrame(t, readV3RealtimeFrame(t, conn), V3RealtimeKindReplayStart, created.SessionID, 0)
	assertV3RealtimeFrame(t, readV3RealtimeFrame(t, conn), V3RealtimeKindReplayDone, created.SessionID, created.Event.Seq)

	server.v3LiveHub.publish(testPrincipal().AccountScopeID, v3RealtimeLivePatchForTest(created.SessionID, "run-1", "stream-1", 1, "x"))
	assertNoV3RealtimeFrame(t, conn, 100*time.Millisecond)
}

func TestV3RealtimeCapabilityResumeReceivesLivePatchAfterReplayComplete(t *testing.T) {
	server, _, _, _, _ := newRoutedSessionTestServerWithSwarmStore(t)
	server.v3LivePatchEnabled = true
	created := createV3RealtimeTestSessionResult(t, server, "session-live-capability", "create-live-capability")
	httpServer := newV3RealtimeHTTPTestServer(t, server)
	conn := dialV3RealtimeStream(t, httpServer.URL)
	defer conn.Close()

	hello := readV3RealtimeFrameIncludingHello(t, conn)
	assertV3RealtimeFrame(t, hello, V3RealtimeKindHello, "", 0)
	if !containsV3RealtimeCapability(hello.Capabilities, V3RealtimeCapabilityLivePatchV1) {
		t.Fatalf("hello capabilities = %+v, want live_patch_v1", hello.Capabilities)
	}
	writeV3RealtimeMessage(t, conn, V3RealtimeMessage{Protocol: V3RealtimeProtocol, ProtocolVersion: V3RealtimeProtocolVersion, Kind: V3RealtimeKindResume, EndpointCursor: signedV3RealtimeCursorForTest(t, server, created.RealtimeOutbox.EndpointSeq), Capabilities: []string{V3RealtimeCapabilityLivePatchV1}, Subscriptions: []V3RealtimeSubscriptionRequest{{SessionID: created.SessionID, SubscriptionID: "sub-live-capability"}}})
	assertV3RealtimeFrame(t, readV3RealtimeFrame(t, conn), V3RealtimeKindReplayStart, created.SessionID, 0)
	done := readV3RealtimeFrame(t, conn)
	assertV3RealtimeFrame(t, done, V3RealtimeKindReplayDone, created.SessionID, created.Event.Seq)

	server.v3LiveHub.publish(testPrincipal().AccountScopeID, v3RealtimeLivePatchForTest(created.SessionID, "run-1", "stream-1", 1, "x"))
	live := readV3RealtimeLiveFrame(t, conn)
	if live.Kind != V3RealtimeKindLivePatch || live.Live.Text != "x" || live.Live.SessionID != created.SessionID {
		t.Fatalf("live frame = %+v", live)
	}
}

func TestV3RealtimeCapabilitySubscribeReceivesLivePatchAfterReplayComplete(t *testing.T) {
	server, _, _, _, _ := newRoutedSessionTestServerWithSwarmStore(t)
	server.v3LivePatchEnabled = true
	created := createV3RealtimeTestSessionResult(t, server, "session-live-subscribe", "create-live-subscribe")
	httpServer := newV3RealtimeHTTPTestServer(t, server)
	conn := dialV3RealtimeStream(t, httpServer.URL)
	defer conn.Close()

	hello := readV3RealtimeFrameIncludingHello(t, conn)
	assertV3RealtimeFrame(t, hello, V3RealtimeKindHello, "", 0)
	if !containsV3RealtimeCapability(hello.Capabilities, V3RealtimeCapabilityLivePatchV1) {
		t.Fatalf("hello capabilities = %+v, want live_patch_v1", hello.Capabilities)
	}
	writeV3RealtimeMessage(t, conn, V3RealtimeMessage{Protocol: V3RealtimeProtocol, ProtocolVersion: V3RealtimeProtocolVersion, Kind: V3RealtimeKindResume, EndpointCursor: signedV3RealtimeCursorForTest(t, server, created.RealtimeOutbox.EndpointSeq), Capabilities: []string{V3RealtimeCapabilityLivePatchV1}})
	writeV3RealtimeMessage(t, conn, V3RealtimeMessage{Protocol: V3RealtimeProtocol, ProtocolVersion: V3RealtimeProtocolVersion, Kind: V3RealtimeKindSubscribe, EndpointCursor: signedV3RealtimeCursorForTest(t, server, created.RealtimeOutbox.EndpointSeq), SessionID: created.SessionID, SubscriptionID: "sub-live-subscribe"})
	assertV3RealtimeFrame(t, readV3RealtimeFrame(t, conn), V3RealtimeKindReplayStart, created.SessionID, 0)
	done := readV3RealtimeFrame(t, conn)
	assertV3RealtimeFrame(t, done, V3RealtimeKindReplayDone, created.SessionID, created.Event.Seq)

	server.v3LiveHub.publish(testPrincipal().AccountScopeID, v3RealtimeLivePatchForTest(created.SessionID, "run-1", "stream-1", 1, "x"))
	live := readV3RealtimeLiveFrame(t, conn)
	if live.Kind != V3RealtimeKindLivePatch || live.Live.Text != "x" || live.Live.SessionID != created.SessionID {
		t.Fatalf("live frame = %+v", live)
	}
}

func TestV3RealtimeLiveAndDurableUseOneSocketWriter(t *testing.T) {
	server, _, _, _, _ := newRoutedSessionTestServerWithSwarmStore(t)
	server.v3LivePatchEnabled = true
	created := createV3RealtimeTestSessionResult(t, server, "session-live-single-writer", "create-live-single-writer")
	httpServer := newV3RealtimeHTTPTestServer(t, server)
	conn := dialV3RealtimeStream(t, httpServer.URL)
	defer conn.Close()
	writeV3RealtimeMessage(t, conn, V3RealtimeMessage{Protocol: V3RealtimeProtocol, ProtocolVersion: V3RealtimeProtocolVersion, Kind: V3RealtimeKindResume, EndpointCursor: signedV3RealtimeCursorForTest(t, server, created.RealtimeOutbox.EndpointSeq), Capabilities: []string{V3RealtimeCapabilityLivePatchV1}, Subscriptions: []V3RealtimeSubscriptionRequest{{SessionID: created.SessionID, SubscriptionID: "sub-live-single-writer"}}})
	assertV3RealtimeFrame(t, readV3RealtimeFrame(t, conn), V3RealtimeKindReplayStart, created.SessionID, 0)
	assertV3RealtimeFrame(t, readV3RealtimeFrame(t, conn), V3RealtimeKindReplayDone, created.SessionID, created.Event.Seq)

	var active, maxActive, completed atomic.Int64
	previous := v3RealtimeWriteObserver
	v3RealtimeWriteObserver = func(delta int) {
		if delta > 0 {
			current := active.Add(1)
			for {
				old := maxActive.Load()
				if current <= old || maxActive.CompareAndSwap(old, current) {
					break
				}
			}
		} else {
			active.Add(-1)
			completed.Add(1)
		}
	}
	t.Cleanup(func() { v3RealtimeWriteObserver = previous })

	server.v3LiveHub.publish(testPrincipal().AccountScopeID, v3RealtimeLivePatchForTest(created.SessionID, "run-1", "stream-1", 1, "x"))
	appendV3RealtimeTestMessage(t, server, created.SessionID, "message-live-single-writer", "durable")
	_ = readV3RealtimeAnyFrame(t, conn)
	_ = readV3RealtimeAnyFrame(t, conn)
	if completed.Load() < 2 {
		t.Fatalf("completed writes = %d, want at least 2", completed.Load())
	}
	if maxActive.Load() != 1 {
		t.Fatalf("max active writes = %d, want 1", maxActive.Load())
	}
}

func TestV3RealtimeLivePatchServerGateDefaultsOn(t *testing.T) {
	server, _, _, _, _ := newRoutedSessionTestServerWithSwarmStore(t)
	created := createV3RealtimeTestSessionResult(t, server, "session-live-gate-on", "create-live-gate-on")
	httpServer := newV3RealtimeHTTPTestServer(t, server)
	conn := dialV3RealtimeStream(t, httpServer.URL)
	defer conn.Close()
	hello := readV3RealtimeFrameIncludingHello(t, conn)
	assertV3RealtimeFrame(t, hello, V3RealtimeKindHello, "", 0)
	if !containsV3RealtimeCapability(hello.Capabilities, V3RealtimeCapabilityLivePatchV1) {
		t.Fatalf("hello capabilities = %+v, want live_patch_v1 when gate defaults on", hello.Capabilities)
	}
	writeV3RealtimeMessage(t, conn, V3RealtimeMessage{Protocol: V3RealtimeProtocol, ProtocolVersion: V3RealtimeProtocolVersion, Kind: V3RealtimeKindResume, EndpointCursor: signedV3RealtimeCursorForTest(t, server, created.RealtimeOutbox.EndpointSeq), Capabilities: []string{V3RealtimeCapabilityLivePatchV1}, Subscriptions: []V3RealtimeSubscriptionRequest{{SessionID: created.SessionID, SubscriptionID: "sub-live-gate-on"}}})
	assertV3RealtimeFrame(t, readV3RealtimeFrame(t, conn), V3RealtimeKindReplayStart, created.SessionID, 0)
	assertV3RealtimeFrame(t, readV3RealtimeFrame(t, conn), V3RealtimeKindReplayDone, created.SessionID, created.Event.Seq)
	server.v3LiveHub.publish(testPrincipal().AccountScopeID, v3RealtimeLivePatchForTest(created.SessionID, "run-1", "stream-1", 1, "x"))
	live := readV3RealtimeLiveFrame(t, conn)
	if live.Kind != V3RealtimeKindLivePatch || live.Live.Text != "x" || live.Live.SessionID != created.SessionID {
		t.Fatalf("live frame = %+v", live)
	}
}

func TestV3RealtimeWriteDeadlineReleasesStalledSocket(t *testing.T) {
	previous := v3RealtimeWriteTimeout
	v3RealtimeWriteTimeout = 25 * time.Millisecond
	t.Cleanup(func() { v3RealtimeWriteTimeout = previous })

	accepted := make(chan *transportws.Conn, 1)
	releaseHandler := make(chan struct{})
	httpServer := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		conn, err := transportws.Accept(w, r)
		if err != nil {
			return
		}
		accepted <- conn
		<-releaseHandler
		_ = conn.Close()
	}))
	t.Cleanup(func() {
		close(releaseHandler)
		httpServer.Close()
	})

	wsURL := "ws" + strings.TrimPrefix(httpServer.URL, "http")
	client, _, err := gorillaws.DefaultDialer.Dial(wsURL, nil)
	if err != nil {
		t.Fatalf("dial stopped-reader websocket: %v", err)
	}
	defer client.Close()

	var conn *transportws.Conn
	select {
	case conn = <-accepted:
	case <-time.After(time.Second):
		t.Fatalf("server did not accept stopped-reader websocket")
	}

	writeErr := make(chan error, 1)
	go func() {
		raw := []byte(strings.Repeat("x", 256<<10))
		server := &Server{}
		for i := 0; i < 1024; i++ {
			if err := server.writeV3RealtimePayload(conn, raw); err != nil {
				writeErr <- err
				return
			}
		}
		writeErr <- nil
	}()

	select {
	case err := <-writeErr:
		if err == nil {
			t.Fatalf("stopped-reader websocket accepted all writes without deadline error")
		}
	case <-time.After(time.Second):
		t.Fatalf("stalled socket write did not exit through deadline")
	}
}

func readV3RealtimeLiveFrame(t *testing.T, conn *gorillaws.Conn) V3RealtimeLiveMessage {
	t.Helper()
	for {
		if err := conn.SetReadDeadline(time.Now().Add(2 * time.Second)); err != nil {
			t.Fatalf("set live read deadline: %v", err)
		}
		_, raw, err := conn.ReadMessage()
		if err != nil {
			t.Fatalf("read live frame: %v", err)
		}
		var base struct {
			Kind string `json:"kind"`
		}
		if err := json.Unmarshal(raw, &base); err != nil {
			t.Fatalf("decode live base %s: %v", string(raw), err)
		}
		if base.Kind == V3RealtimeKindHello {
			continue
		}
		if base.Kind != V3RealtimeKindLivePatch {
			t.Fatalf("frame kind = %q, want live.patch raw=%s", base.Kind, string(raw))
		}
		var frame V3RealtimeLiveMessage
		if err := json.Unmarshal(raw, &frame); err != nil {
			t.Fatalf("decode live frame %s: %v", string(raw), err)
		}
		if err := ValidateV3RealtimeLiveMessage(frame); err != nil {
			t.Fatalf("invalid live frame %s: %v", string(raw), err)
		}
		return frame
	}
}

func readV3RealtimeAnyFrame(t *testing.T, conn *gorillaws.Conn) string {
	t.Helper()
	if err := conn.SetReadDeadline(time.Now().Add(2 * time.Second)); err != nil {
		t.Fatalf("set any read deadline: %v", err)
	}
	_, raw, err := conn.ReadMessage()
	if err != nil {
		t.Fatalf("read any frame: %v", err)
	}
	return string(raw)
}
