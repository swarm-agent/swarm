package api

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
	"strconv"
	"strings"
	"testing"
	"time"
	"unicode/utf8"

	agentruntime "swarm/packages/swarmd/internal/agent"
	"swarm/packages/swarmd/internal/identity"
	sessionruntime "swarm/packages/swarmd/internal/session"
	pebblestore "swarm/packages/swarmd/internal/store/pebble"
)

// failingPendingCountPerm fails the pending-call check for one session.
type failingPendingCountPerm struct {
	permissionService
	failFor string
}

func (p failingPendingCountPerm) PendingCount(sessionID string) (int, error) {
	if sessionID == p.failFor {
		return 0, errors.New("injected pending count failure")
	}
	return p.permissionService.PendingCount(sessionID)
}

func seedAgentTestSession(t *testing.T, server *Server, principal identity.Principal, id, agent string, at int64) {
	t.Helper()
	snapshot := pebblestore.SessionSnapshot{ID: id, UserID: principal.UserID, AccountScopeID: principal.AccountScopeID, Title: id, Mode: "auto", Metadata: map[string]any{"agent_name": agent}, CreatedAt: at, UpdatedAt: at}
	if _, err := server.applySessionV3PrimaryMutation(sessionruntime.SessionMutationInput{Kind: sessionruntime.SessionMutationCreateSession, SessionID: id, UserID: principal.UserID, AccountScopeID: principal.AccountScopeID, Session: &snapshot, IdempotencyKey: id, ClientRequestID: id, PayloadHash: id, NowUnixMs: at}); err != nil {
		t.Fatal(err)
	}
}

func appendAgentTestMessage(t *testing.T, server *Server, principal identity.Principal, sessionID, requestID, role, content string, metadata map[string]any) {
	t.Helper()
	now := time.Now().UnixMilli()
	message := pebblestore.MessageSnapshot{ID: "msg-" + requestID, Role: role, Content: content, CreatedAt: now, Metadata: metadata}
	if _, err := server.applySessionV3PrimaryMutation(sessionruntime.SessionMutationInput{Kind: sessionruntime.SessionMutationAppendMessage, SessionID: sessionID, UserID: principal.UserID, AccountScopeID: principal.AccountScopeID, Message: &message, IdempotencyKey: requestID, ClientRequestID: requestID, PayloadHash: requestID, RequestHash: requestID, NowUnixMs: now}); err != nil {
		t.Fatal(err)
	}
}

func readAgentFeed(t *testing.T, server *Server, query string) agentEventsPage {
	t.Helper()
	w := httptest.NewRecorder()
	server.handleSessionsV3AgentEvents(w, withTestPrincipal(httptest.NewRequest(http.MethodGet, "/v3/sessions:agent-events?agent=frontdesk&"+query, nil)))
	if w.Code != http.StatusOK {
		t.Fatalf("feed %s: %d %s", query, w.Code, w.Body.String())
	}
	var page agentEventsPage
	if err := json.Unmarshal(w.Body.Bytes(), &page); err != nil {
		t.Fatal(err)
	}
	return page
}

// Purpose: the agent event feed lists one agent's conversation events in
// commit order with a cursor: paging by limit and next_after neither skips nor
// repeats an entry, a kind filter narrows it, a cursor ahead of the feed is a
// gap, long message text is cut with truncated set, another agent's sessions
// never appear, and a long-poll wakes on the agent's next entry (not on
// another agent's) or ends empty when the wait is over. Threat: consoles
// missing or double-counting events, polling, or seeing other agents'
// conversations. Boundary: listAgentEvents, agentEventFromSessionEvent,
// handleSessionsV3AgentEvents. Narrowest layer: the handler over real V3
// sessions whose writes go through applySessionV3PrimaryMutation.
func TestAgentEventFeedOrderCursorsAndWakeups(t *testing.T) {
	server, _, _, _, _ := newRoutedSessionTestServerWithSwarmStore(t)
	principal := testPrincipal()
	seedAgentTestSession(t, server, principal, "f-1", "frontdesk", 1_000)
	seedAgentTestSession(t, server, principal, "o-1", "swarm", 1_100)
	seedAgentTestSession(t, server, principal, "f-2", "frontdesk", 1_200)
	appendAgentTestMessage(t, server, principal, "f-1", "m1", "user", "hi", nil)
	appendAgentTestMessage(t, server, principal, "o-1", "m2", "user", "not for this feed", nil)
	appendAgentTestMessage(t, server, principal, "f-2", "m3", "user", "hello", nil)
	appendAgentTestMessage(t, server, principal, "f-1", "m4", "assistant", strings.Repeat("é", agentEventTextMaxRunes+500), map[string]any{"source": "operator"})

	full := readAgentFeed(t, server, "")
	var kinds []string
	var last uint64
	for _, event := range full.Events {
		if event.Seq <= last {
			t.Fatalf("feed out of order: %d after %d", event.Seq, last)
		}
		last = event.Seq
		if event.SessionID == "o-1" {
			t.Fatal("another agent's session is in the feed")
		}
		kinds = append(kinds, event.SessionID+":"+event.Kind)
	}
	want := "f-1:session.created,f-2:session.created,f-1:message.user,f-2:message.user,f-1:message.assistant"
	if strings.Join(kinds, ",") != want {
		t.Fatalf("feed = %s\nwant   %s", strings.Join(kinds, ","), want)
	}
	assistant := full.Events[len(full.Events)-1].Data
	if text, _ := assistant["text"].(string); utf8.RuneCountInString(text) != agentEventTextMaxRunes || assistant["truncated"] != true || assistant["source"] != "operator" {
		t.Fatalf("long operator reply: runes=%d data=%v", utf8.RuneCountInString(assistant["text"].(string)), map[string]any{"truncated": assistant["truncated"], "source": assistant["source"]})
	}
	if full.Gap || full.NextAfter != full.LatestSeq {
		t.Fatalf("a full read should be caught up: %+v", full)
	}

	var paged []string
	cursor := uint64(0)
	for i := 0; i < 10; i++ {
		page := readAgentFeed(t, server, "limit=2&after="+strconv.FormatUint(cursor, 10))
		for _, event := range page.Events {
			paged = append(paged, event.SessionID+":"+event.Kind)
		}
		if page.NextAfter < cursor {
			t.Fatalf("cursor went backwards: %d -> %d", cursor, page.NextAfter)
		}
		cursor = page.NextAfter
		if len(page.Events) == 0 {
			break
		}
	}
	if strings.Join(paged, ",") != want {
		t.Fatalf("paged feed = %s\nwant         %s", strings.Join(paged, ","), want)
	}
	if users := readAgentFeed(t, server, "kind=message.user"); len(users.Events) != 2 {
		t.Fatalf("kind filter: %+v", users.Events)
	}
	if ahead := readAgentFeed(t, server, "after="+strconv.FormatUint(full.LatestSeq+100, 10)); !ahead.Gap || ahead.NextAfter != full.LatestSeq || len(ahead.Events) != 0 {
		t.Fatalf("cursor ahead of the feed: %+v", ahead)
	}

	done := make(chan agentEventsPage, 1)
	begin := time.Now()
	go func() { done <- readAgentFeed(t, server, "wait=10&after="+strconv.FormatUint(full.NextAfter, 10)) }()
	time.Sleep(100 * time.Millisecond)
	appendAgentTestMessage(t, server, principal, "o-1", "m5", "user", "other agent again", nil)
	select {
	case page := <-done:
		t.Fatalf("another agent's message ended the wait: %+v", page)
	case <-time.After(300 * time.Millisecond):
	}
	appendAgentTestMessage(t, server, principal, "f-2", "m6", "assistant", "Welcome!", nil)
	select {
	case page := <-done:
		if len(page.Events) != 1 || page.Events[0].SessionID != "f-2" || page.Events[0].Kind != agentEventMessageAssistant {
			t.Fatalf("woken feed: %+v", page.Events)
		}
		if time.Since(begin) > 3*time.Second {
			t.Fatal("feed woke on its deadline, not the change")
		}
	case <-time.After(5 * time.Second):
		t.Fatal("feed wait never returned")
	}
	started := time.Now()
	idle := readAgentFeed(t, server, "wait=1&after="+strconv.FormatUint(readAgentFeed(t, server, "").NextAfter, 10))
	if len(idle.Events) != 0 || time.Since(started) < 900*time.Millisecond {
		t.Fatalf("idle feed wait: %+v after %v", idle, time.Since(started))
	}
}

// Purpose: feed entries carry only bounded, translated fields: a tool call's
// arguments, a tool answer's result or failure reason, run failures without
// their reason, and lifecycle changes; streaming deltas and system messages
// are not entries. Boundary: agentEventFromSessionEvent. Narrowest layer: the
// pure translator.
func TestAgentEventTranslation(t *testing.T) {
	event := func(eventType, payload string) pebblestore.V3SessionEvent {
		return pebblestore.V3SessionEvent{EventType: eventType, Payload: json.RawMessage(payload)}
	}
	for _, tc := range []struct {
		event pebblestore.V3SessionEvent
		kind  string
		key   string
		value any
	}{
		{event("session.assistant.delta", `{"message":{"role":"assistant","content":"x"}}`), "", "", nil},
		{event("session.message.appended", `{"message":{"role":"system","content":"run failed"}}`), "", "", nil},
		{event("permission.requested", `{"permission":{"id":"perm-1","tool_name":"lookup_order","tool_arguments":"{\"order_id\":\"AB12CD\"}"}}`), agentEventToolCalled, "arguments", `{"order_id":"AB12CD"}`},
		{event("permission.updated", `{"permission":{"id":"perm-1","tool_name":"lookup_order","status":"approved","approved_arguments":"{\"result\":1}"}}`), agentEventToolAnswered, "result", `{"result":1}`},
		{event("permission.updated", `{"permission":{"id":"perm-1","tool_name":"lookup_order","status":"denied","reason":"no such order"}}`), agentEventToolFailed, "reason", "no such order"},
		{event("permission.updated", `{"permission":{"id":"perm-1","status":"pending"}}`), "", "", nil},
		{event("session.run.failed", `{"run_intent":{"run_id":"run-1","status":"failed","blocked_reason":"provider said no"}}`), agentEventRunFailed, "run_id", "run-1"},
		{event("session.run.completed", `{"run_intent":{"run_id":"run-1","status":"completed"}}`), "", "", nil},
		{event("session.metadata.updated", `{"change":"session.ended"}`), agentEventSessionEnded, "", nil},
		{event("session.metadata.updated", `{"change":"takeover.started"}`), agentEventTakeoverStarted, "", nil},
		{event("session.metadata.updated", `{"change":"takeover.ended"}`), agentEventTakeoverEnded, "", nil},
		{event("session.metadata.updated", `{"session":{"title":"x"}}`), "", "", nil},
	} {
		kind, data, ok := agentEventFromSessionEvent(tc.event)
		if kind != tc.kind || ok != (tc.kind != "") {
			t.Fatalf("%s %s: kind %q ok=%v, want %q", tc.event.EventType, tc.event.Payload, kind, ok, tc.kind)
		}
		if tc.key != "" && data[tc.key] != tc.value {
			t.Fatalf("%s: %s = %v, want %v", kind, tc.key, data[tc.key], tc.value)
		}
		if kind == agentEventRunFailed && data["reason"] != nil {
			t.Fatal("a run failure's reason left the machine")
		}
	}
}

// Purpose: retention deletes only a sealed agent's conversations that are past
// the agent's retention_days (ended or idle that long), oldest first; a
// recent one, another sealed agent's without retention, a non-sealed agent's
// with the same setting, and one with a run in flight are kept, and a repeat
// pass deletes nothing more. An out-of-range retention is refused when the
// agent is saved. Threat: retention erasing conversations it should keep
// (coding sessions, active chats) or never erasing the ones it should.
// Boundary: SweepAgentSessionRetention, agentRetentionDays, handleAgentByNameV2
// validation. Narrowest layer: the sweep over real sessions and agents.
func TestAgentRetentionSweepDeletesOnlyExpiredSealedConversations(t *testing.T) {
	f := newGatewayTestFixture(t)
	s := f.server
	if _, _, _, err := s.agents.UpsertForAccount(f.account, agentruntime.UpsertInput{Name: "frontdesk", Mode: agentruntime.ModeSubagent, Enabled: pebblestore.BoolPtr(true), Prompt: "x",
		ToolContract: &pebblestore.AgentToolContract{Preset: "custom", Tools: map[string]pebblestore.AgentToolConfig{"lookup_order": {Enabled: pebblestore.BoolPtr(true)}}},
		Limits:       &pebblestore.AgentRunLimits{RetentionDays: 7}}); err != nil {
		t.Fatal(err)
	}
	if _, _, _, err := s.agents.UpsertForAccount(f.account, agentruntime.UpsertInput{Name: "notes", Mode: agentruntime.ModeSubagent, Enabled: pebblestore.BoolPtr(true), Prompt: "x",
		ToolContract: &pebblestore.AgentToolContract{Preset: "custom", Tools: map[string]pebblestore.AgentToolConfig{}},
		Limits:       &pebblestore.AgentRunLimits{RetentionDays: 7}}); err != nil {
		t.Fatal(err)
	}
	w := httptest.NewRecorder()
	s.handleAgentByNameV2(w, requestWithTestPrincipalForAccount(httptest.NewRequest(http.MethodPut, "/v2/agents/frontdesk", strings.NewReader(`{"mode":"subagent","prompt":"x","limits":{"retention_days":3651}}`)), f.user, f.account))
	if w.Code != http.StatusBadRequest {
		t.Fatalf("retention_days 3651 accepted: %d %s", w.Code, w.Body.String())
	}
	if profile, _, _ := s.agents.GetProfileForAccount(f.account, "frontdesk"); profile.Limits == nil || profile.Limits.RetentionDays != 7 {
		t.Fatalf("a refused save changed retention: %+v", profile.Limits)
	}

	now := time.Now()
	old := now.Add(-10 * 24 * time.Hour).UnixMilli()
	recent := now.Add(-24 * time.Hour).UnixMilli()
	f.seed(t, "old-idle", "frontdesk", old)
	f.seed(t, "old-ended", "frontdesk", old-1)
	f.seed(t, "recent", "frontdesk", recent)
	f.seed(t, "old-help", "helpdesk", old)
	f.seed(t, "old-notes", "notes", old)
	f.seed(t, "old-active", "frontdesk", old)
	endedSession, _, _ := s.sessions.GetSession("old-ended")
	endedSession.Metadata = cloneStringAnyMap(endedSession.Metadata)
	endedSession.Metadata[sessionEndedMetadataKey] = map[string]any{"at": old, "by": f.user}
	if _, err := s.sessions.ApplySessionMutation(sessionruntime.SessionMutationInput{Kind: sessionruntime.SessionMutationUpdateMetadata, SessionID: "old-ended", UserID: f.user, AccountScopeID: f.account, Session: &endedSession, IdempotencyKey: "end-old", PayloadHash: "end-old", EventPayload: json.RawMessage(`{"change":"session.ended"}`), NowUnixMs: old}); err != nil {
		t.Fatal(err)
	}
	intent := pebblestore.V3SessionRunIntent{SessionID: "old-active", UserID: f.user, AccountScopeID: f.account, RunID: "run-active", Status: sessionruntime.RunIntentPendingExecutor, UpdatedAt: old}
	if _, err := s.sessions.ApplySessionMutation(sessionruntime.SessionMutationInput{Kind: sessionruntime.SessionMutationRecordRunIntent, SessionID: "old-active", UserID: f.user, AccountScopeID: f.account, RunIntent: &intent, IdempotencyKey: "run-active", PayloadHash: "run-active", NowUnixMs: old}); err != nil {
		t.Fatal(err)
	}
	if _, active, err := s.sessions.GetSessionActiveRunIntent("old-active"); err != nil || !active {
		t.Fatalf("fixture: old-active has no active run: %v", err)
	}
	if session, _, _ := s.sessions.GetSession("old-active"); session.UpdatedAt >= now.Add(-7*24*time.Hour).UnixMilli() {
		t.Fatalf("fixture: old-active looks recent (updated %d)", session.UpdatedAt)
	}

	// One expired conversation cannot be checked: the pass reports the
	// failure (error, count, signal) and still handles the others.
	f.seed(t, "old-broken", "frontdesk", old)
	s.signals = pebblestore.NewSignalStore(f.db)
	realPerm := s.perm
	s.perm = failingPendingCountPerm{permissionService: realPerm, failFor: "old-broken"}
	result, err := s.SweepAgentSessionRetention(context.Background(), now)
	if err == nil || !strings.Contains(err.Error(), "frontdesk") {
		t.Fatalf("a failed delete was not reported: %v", err)
	}
	if result.Deleted != 2 || result.Skipped != 1 || result.Failed != 1 || result.More {
		t.Fatalf("sweep: %+v", result)
	}
	for id, kept := range map[string]bool{"old-idle": false, "old-ended": false, "recent": true, "old-help": true, "old-notes": true, "old-active": true, "old-broken": true} {
		if _, found, _ := s.sessions.GetSession(id); found != kept {
			t.Fatalf("%s: present=%v, want %v", id, found, kept)
		}
	}
	signals, err := s.signals.ListAfter(0, 10, pebblestore.SignalFilter{Account: f.account, KindPrefixes: []string{"session.retention.failed"}})
	if err != nil || len(signals.Signals) != 1 || signals.Signals[0].Attrs["agent"] != "frontdesk" || signals.Signals[0].Attrs["count"] != "1" {
		t.Fatalf("retention failure signal: %+v %v", signals.Signals, err)
	}
	s.perm = realPerm
	again, err := s.SweepAgentSessionRetention(context.Background(), now)
	if err != nil || again.Deleted != 1 || again.Skipped != 1 || again.Failed != 0 {
		t.Fatalf("second sweep: %+v %v", again, err)
	}
	if _, found, _ := s.sessions.GetSession("old-broken"); found {
		t.Fatal("the retried conversation was not deleted")
	}
}
