package api

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strconv"
	"strings"
	"testing"
	"time"

	"swarm/packages/swarmd/internal/identity"
	sessionruntime "swarm/packages/swarmd/internal/session"
	pebblestore "swarm/packages/swarmd/internal/store/pebble"
)

type sessionWaitTestResponse struct {
	Cursor       uint64   `json:"cursor"`
	Reasons      []string `json:"reasons"`
	TimedOut     bool     `json:"timed_out"`
	Ended        bool     `json:"ended"`
	PendingCalls []any    `json:"pending_calls"`
	Run          *struct {
		Status string `json:"status"`
		Active bool   `json:"active"`
	} `json:"run"`
}

func callSessionWait(t *testing.T, server *Server, principal identity.Principal, sessionID, query string) (int, sessionWaitTestResponse) {
	t.Helper()
	w := httptest.NewRecorder()
	server.handleSessionV3Wait(w, httptest.NewRequest(http.MethodGet, "/v3/sessions/"+sessionID+"/wait?"+query, nil), principal, sessionID)
	var out sessionWaitTestResponse
	_ = json.Unmarshal(w.Body.Bytes(), &out)
	return w.Code, out
}

// recordTestRunEvent writes one run event the way the executor does, through
// applySessionV3PrimaryMutation, so its post-commit wake runs.
func recordTestRunEvent(t *testing.T, server *Server, job sessionV3ExecutorJob, requestID, eventType, status string, payload map[string]any, message *pebblestore.MessageSnapshot) {
	t.Helper()
	now := time.Now().UnixMilli()
	intent := sessionV3RunIntentForJob(job, status, now)
	input := sessionruntime.SessionMutationInput{
		SessionID: job.SessionID, UserID: job.Principal.UserID, AccountScopeID: job.Principal.AccountScopeID,
		ClientRequestID: requestID, IdempotencyKey: requestID, PayloadHash: requestID, RequestHash: requestID,
		Kind: sessionruntime.SessionMutationRecordRunIntent, EventType: eventType, RunIntent: &intent, NowUnixMs: now,
	}
	if payload != nil {
		raw, err := json.Marshal(payload)
		if err != nil {
			t.Fatal(err)
		}
		input.EventPayload = raw
	}
	if message != nil {
		input.Kind = sessionruntime.SessionMutationAppendMessage
		input.Message = message
	}
	if _, err := server.applySessionV3PrimaryMutation(input); err != nil {
		t.Fatalf("record %s: %v", eventType, err)
	}
}

// Purpose: a gateway waits instead of polling. GET /v3/sessions/{id}/wait
// without a cursor answers at once with the session's cursor; with a cursor it
// blocks through events a gateway does not act on (streaming deltas, a run
// starting) and returns, woken by the post-commit notifier rather than a timer,
// as soon as a tool call is pending, an assistant message lands or the run
// ends; with nothing new it returns when the wait is over, marked timed_out.
// Threat: gateways polling every few hundred milliseconds, or a wait that
// misses an event committed while it was being set up. Boundary:
// handleSessionV3Wait, sessionWaitReasons, sessionChangeNotifier via
// publishCommittedV3RealtimeOutbox. Narrowest layer: the handler on a real V3
// session whose events are written through applySessionV3PrimaryMutation.
func TestSessionWaitWakesOnlyForWhatAGatewayActsOn(t *testing.T) {
	server, _, _, _, _ := newRoutedSessionTestServerWithSwarmStore(t)
	server.v3SessionExecutor = newSessionV3Executor(server)
	principal := testPrincipal()
	created := createSessionsV3PrimaryTestSessionWithWorkspaceAndPreference(t, server, "wait-create", "wait", t.TempDir(), pebblestore.ModelPreference{Provider: "test-provider", Model: "test-model", Thinking: "medium"})
	id := created.ID

	code, initial := callSessionWait(t, server, principal, id, "")
	if code != http.StatusOK || initial.Cursor == 0 || len(initial.Reasons) != 0 || initial.TimedOut {
		t.Fatalf("initial wait: %d %+v", code, initial)
	}
	if code, _ := callSessionWait(t, server, principal, id, "after=1&wait_seconds=31"); code != http.StatusBadRequest {
		t.Fatalf("wait_seconds over 30 accepted: %d", code)
	}

	// Nothing new: the wait ends on its deadline.
	started := time.Now()
	code, idle := callSessionWait(t, server, principal, id, "after="+itoa(initial.Cursor)+"&wait_seconds=1")
	if code != http.StatusOK || !idle.TimedOut || len(idle.Reasons) != 0 || idle.Cursor != initial.Cursor {
		t.Fatalf("idle wait: %d %+v", code, idle)
	}
	if elapsed := time.Since(started); elapsed < 900*time.Millisecond || elapsed > 5*time.Second {
		t.Fatalf("idle wait returned after %v, want about 1s", elapsed)
	}

	_, job, err := server.acceptSessionsV3Message(principal, id, sessionsV3MessageRequest{ClientRequestID: "visitor-1", Role: "user", Content: "where is my order?"})
	if err != nil || job == nil {
		t.Fatalf("visitor message: %v job=%v", err, job)
	}

	type waitResult struct {
		code int
		out  sessionWaitTestResponse
		at   time.Duration
	}
	waitFrom := func(cursor uint64) (chan waitResult, time.Time) {
		done := make(chan waitResult, 1)
		begin := time.Now()
		go func() {
			code, out := callSessionWait(t, server, principal, id, "after="+itoa(cursor)+"&wait_seconds=10")
			done <- waitResult{code, out, time.Since(begin)}
		}()
		return done, begin
	}
	expectBlocked := func(done chan waitResult, why string) {
		select {
		case got := <-done:
			t.Fatalf("wait returned for %s: %+v", why, got)
		case <-time.After(300 * time.Millisecond):
		}
	}
	expectWoken := func(done chan waitResult, reason string) sessionWaitTestResponse {
		select {
		case got := <-done:
			if got.code != http.StatusOK || got.out.TimedOut || len(got.out.Reasons) == 0 || got.out.Reasons[0] != reason {
				t.Fatalf("wait for %s: %d %+v", reason, got.code, got.out)
			}
			if got.at > 3*time.Second {
				t.Fatalf("wait for %s took %v: woken by the deadline, not the change", reason, got.at)
			}
			return got.out
		case <-time.After(5 * time.Second):
			t.Fatalf("wait for %s never returned", reason)
		}
		return sessionWaitTestResponse{}
	}

	// The visitor message and the run starting are not events to act on.
	done, _ := waitFrom(idle.Cursor)
	time.Sleep(100 * time.Millisecond)
	recordTestRunEvent(t, server, *job, "run-running", "session.run.started", sessionruntime.RunIntentRunning, nil, nil)
	recordTestRunEvent(t, server, *job, "delta-1", "session.assistant.delta", sessionruntime.RunIntentRunning, map[string]any{"role": "assistant", "delta": "Let me"}, nil)
	expectBlocked(done, "a run starting and a streaming delta")

	recordTestRunEvent(t, server, *job, "perm-1", "permission.requested", sessionruntime.RunIntentRunning, map[string]any{"type": "permission.requested", "call_id": "call-1", "tool_name": "lookup_order"}, nil)
	toolWake := expectWoken(done, sessionWaitToolCall)
	if toolWake.Run == nil || !toolWake.Run.Active {
		t.Fatalf("tool call wake should report the run in flight: %+v", toolWake.Run)
	}

	done, _ = waitFrom(toolWake.Cursor)
	time.Sleep(100 * time.Millisecond)
	answer := pebblestore.MessageSnapshot{ID: sessionV3AssistantMessageID(id, job.RunID), Role: "assistant", Content: "It ships tomorrow.", CreatedAt: time.Now().UnixMilli()}
	recordTestRunEvent(t, server, *job, "done-1", "session.assistant.completed", sessionruntime.RunIntentCompleted, nil, &answer)
	final := expectWoken(done, sessionWaitAssistantMessage)
	if len(final.Reasons) < 2 || final.Reasons[1] != sessionWaitRunEnded {
		t.Fatalf("the completing assistant message should also report the run ended: %+v", final.Reasons)
	}
	if final.Run == nil || final.Run.Active || final.Run.Status != sessionruntime.RunIntentCompleted {
		t.Fatalf("run after completion: %+v", final.Run)
	}
	if server.sessionChanges.watchedKeys() != 0 {
		t.Fatalf("finished waits left %d watchers behind", server.sessionChanges.watchedKeys())
	}
}

// Purpose: ending a conversation stops its run in flight and closes it for
// good: a waiter is woken with session_ended, and afterwards visitor messages,
// takeovers and operator replies are all refused while the session stays
// readable. Threat: a closed public chat that keeps spending on a run, or that
// a late visitor message reopens. Boundary: handleSessionV3End,
// stopActiveSessionRun, acceptSessionsV3Message, handleSessionV3Takeover,
// handleSessionV3OperatorMessages. Narrowest layer: the handlers on a real V3
// session with a pending run (no provider runs).
func TestSessionEndStopsTheRunAndRefusesNewTurns(t *testing.T) {
	server, _, _, _, _ := newRoutedSessionTestServerWithSwarmStore(t)
	server.v3SessionExecutor = newSessionV3Executor(server)
	principal := testPrincipal()
	created := createSessionsV3PrimaryTestSessionForAgent(t, server, "end-create", "end", t.TempDir(), pebblestore.ModelPreference{Provider: "test-provider", Model: "test-model", Thinking: "medium"}, seedSealedTestAgent(t, server))
	id := created.ID
	_, job, err := server.acceptSessionsV3Message(principal, id, sessionsV3MessageRequest{ClientRequestID: "visitor-1", Role: "user", Content: "hello?"})
	if err != nil || job == nil {
		t.Fatalf("visitor message: %v", err)
	}
	if _, active, _ := server.sessions.GetSessionActiveRunIntent(id); !active {
		t.Fatal("fixture: no active run before end")
	}
	_, before := callSessionWait(t, server, principal, id, "")
	done := make(chan sessionWaitTestResponse, 1)
	go func() {
		_, out := callSessionWait(t, server, principal, id, "after="+itoa(before.Cursor)+"&wait_seconds=10")
		done <- out
	}()
	time.Sleep(100 * time.Millisecond)

	call := func(handler func(http.ResponseWriter, *http.Request), body string) (int, map[string]any) {
		w := httptest.NewRecorder()
		handler(w, httptest.NewRequest(http.MethodPost, "/", strings.NewReader(body)))
		var out map[string]any
		_ = json.Unmarshal(w.Body.Bytes(), &out)
		return w.Code, out
	}
	end := func(w http.ResponseWriter, r *http.Request) { server.handleSessionV3End(w, r, principal, id) }
	if code, _ := call(end, `{"client_request_id":"end-x","reason":"`+strings.Repeat("x", sessionEndReasonMaxRunes+1)+`"}`); code != http.StatusBadRequest {
		t.Fatalf("over-long reason accepted: %d", code)
	}
	if session, _, _ := server.sessions.GetSession(id); sessionEnded(session) {
		t.Fatal("a refused end ended the session")
	}
	code, out := call(end, `{"client_request_id":"end-1"}`)
	if code != http.StatusOK || out["stopped_run_id"] != job.RunID {
		t.Fatalf("end: %d %v", code, out)
	}
	if _, active, _ := server.sessions.GetSessionActiveRunIntent(id); active {
		t.Fatal("the run is still active after end")
	}
	select {
	case got := <-done:
		if !got.Ended || len(got.Reasons) == 0 {
			t.Fatalf("waiter after end: %+v", got)
		}
	case <-time.After(5 * time.Second):
		t.Fatal("the waiter was not woken by end")
	}

	if _, _, err := server.acceptSessionsV3Message(principal, id, sessionsV3MessageRequest{ClientRequestID: "visitor-2", Role: "user", Content: "still there?"}); err != errSessionV3Ended {
		t.Fatalf("message after end: %v", err)
	}
	takeover := func(w http.ResponseWriter, r *http.Request) { server.handleSessionV3Takeover(w, r, principal, id) }
	if code, _ := call(takeover, `{"action":"start","client_request_id":"take-1"}`); code != http.StatusConflict {
		t.Fatalf("takeover after end: %d", code)
	}
	operator := func(w http.ResponseWriter, r *http.Request) {
		server.handleSessionV3OperatorMessages(w, r, principal, id)
	}
	if code, _ := call(operator, `{"content":"hi","client_request_id":"op-1"}`); code != http.StatusConflict {
		t.Fatalf("operator reply after end: %d", code)
	}
	if code, out := callSessionWait(t, server, principal, id, ""); code != http.StatusOK || !out.Ended {
		t.Fatalf("an ended session should stay readable: %d %+v", code, out)
	}
}

// Purpose: each scoped token holds a bounded number of long-polls open; the
// next is refused with 429 and Retry-After, and a finished wait frees its
// slot. The owner (no token) is not limited. Threat: one leaked or buggy
// gateway token holding unbounded connections and goroutines. Boundary:
// acquireLongPoll / longPollLimiter. Narrowest layer: the limiter with a
// request carrying a scoped token.
func TestScopedTokenLongPollsAreBounded(t *testing.T) {
	limiter := &longPollLimiter{active: map[string]int{}}
	var releases []func()
	for i := 0; i < 3; i++ {
		release, ok := limiter.acquire("tok", 3)
		if !ok {
			t.Fatalf("slot %d refused under a limit of 3", i)
		}
		releases = append(releases, release)
	}
	if _, ok := limiter.acquire("tok", 3); ok {
		t.Fatal("a fourth long-poll was admitted over a limit of 3")
	}
	if _, ok := limiter.acquire("other", 3); !ok {
		t.Fatal("another token's long-polls counted against this token")
	}
	releases[0]()
	releases[0]() // releasing twice frees one slot, not two
	if _, ok := limiter.acquire("tok", 3); !ok {
		t.Fatal("a released slot was not freed")
	}
	if _, ok := limiter.acquire("tok", 3); ok {
		t.Fatal("a double release freed two slots")
	}

	rec := &pebblestore.ScopedTokenRecord{ID: "tok_longpoll_test", AgentName: "frontdesk"}
	req := requestWithScopedToken(httptest.NewRequest(http.MethodGet, "/v3/sessions/x/wait", nil), rec)
	var held []func()
	for i := 0; i < scopedTokenMaxLongPolls; i++ {
		release, ok := acquireLongPoll(httptest.NewRecorder(), req)
		if !ok {
			t.Fatalf("long-poll %d refused below the limit", i)
		}
		held = append(held, release)
	}
	w := httptest.NewRecorder()
	if _, ok := acquireLongPoll(w, req); ok || w.Code != http.StatusTooManyRequests || w.Header().Get("Retry-After") == "" {
		t.Fatalf("long-poll over the limit: ok=%v %d", ok, w.Code)
	}
	for _, release := range held {
		release()
	}
	if _, ok := acquireLongPoll(httptest.NewRecorder(), httptest.NewRequest(http.MethodGet, "/v3/sessions/x/wait", nil)); !ok {
		t.Fatal("the owner was limited")
	}
}

// Purpose: only events a gateway acts on end a wait, and a wake can never be
// lost between taking the watch and reading durable state. Boundary:
// sessionWaitReasons, sessionChangeNotifier. Narrowest layer: the pure
// classifier and notifier.
func TestSessionWaitReasonsAndNotifier(t *testing.T) {
	event := func(eventType, payload string) pebblestore.V3SessionEvent {
		return pebblestore.V3SessionEvent{EventType: eventType, Payload: json.RawMessage(payload)}
	}
	for _, tc := range []struct {
		event pebblestore.V3SessionEvent
		want  string
	}{
		{event("session.assistant.delta", `{"role":"assistant","run_intent":{"status":"completed"}}`), ""},
		{event("session.message.appended", `{"role":"user","message":{"role":"user"},"run_intent":{"status":"pending_executor"}}`), ""},
		{event("session.run.started", `{"run_intent":{"status":"running"}}`), ""},
		{event("permission.updated", `{"status":"completed"}`), ""},
		{event("permission.requested", `{"call_id":"c"}`), sessionWaitToolCall},
		{event("session.message.appended", `{"message":{"role":"assistant"}}`), sessionWaitAssistantMessage},
		{event("session.run.failed", `{"run_intent":{"status":"failed"}}`), sessionWaitRunEnded},
		{event("session.run_intent.recorded", `{"run_intent":{"status":"dispatch_blocked"}}`), sessionWaitRunEnded},
		{event("session.metadata.updated", `{"change":"session.ended"}`), sessionWaitSessionEnded},
		{event("session.metadata.updated", `{"change":"takeover.started"}`), ""},
	} {
		got := strings.Join(sessionWaitReasons(tc.event), ",")
		if got != tc.want {
			t.Fatalf("%s %s: reasons %q, want %q", tc.event.EventType, tc.event.Payload, got, tc.want)
		}
	}

	n := newSessionChangeNotifier()
	changed, release := n.watch("session:a")
	other, releaseOther := n.watch("session:b")
	n.notify("session:a")
	select {
	case <-changed:
	default:
		t.Fatal("a notify before the read was lost")
	}
	select {
	case <-other:
		t.Fatal("another session's watcher was woken")
	default:
	}
	release()
	releaseOther()
	if n.watchedKeys() != 0 {
		t.Fatalf("released watchers left %d keys", n.watchedKeys())
	}
}

func itoa(v uint64) string { return strconv.FormatUint(v, 10) }
