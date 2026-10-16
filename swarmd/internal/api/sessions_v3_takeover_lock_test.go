package api

import (
	"errors"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"swarm/packages/swarmd/internal/identity"
	runruntime "swarm/packages/swarmd/internal/run"
	sessionruntime "swarm/packages/swarmd/internal/session"
	pebblestore "swarm/packages/swarmd/internal/store/pebble"
)

// Purpose: takeover, operator replies and visitor messages read and write a
// session under LockClientToolConfirmations, the lock POST .../end and
// confirmations write under. Each waits while the lock is held and then acts on
// the session as it is after the holder's write: a takeover keeps an open
// confirmation and the ended state, a takeover start on a conversation that
// just ended is refused, and neither an operator reply nor a visitor message
// lands after the end.
// Threat: a takeover racing an end or a confirmation and writing back stale
// metadata (wiping "ended" or the confirmation), or a message admitted on a
// pre-end read.
// Boundary: handleSessionV3Takeover, handleSessionV3OperatorMessages,
// acceptSessionsV3Message, runruntime.LockClientToolConfirmations.
// Narrowest layer: the handlers on a real V3 session; the test holds the lock
// itself and commits the competing write while a request waits on it, which
// fixes the interleaving instead of relying on scheduling.
func TestSessionTakeoverWritesUnderTheConfirmationLock(t *testing.T) {
	server, _, _, _, _ := newRoutedSessionTestServerWithSwarmStore(t)
	server.v3SessionExecutor = newSessionV3Executor(server)
	principal := testPrincipal()
	agent := seedSealedTestAgent(t, server)
	newSession := func(name string) string {
		return createSessionsV3PrimaryTestSessionForAgent(t, server, name+"-create", name, t.TempDir(), pebblestore.ModelPreference{Provider: "test-provider", Model: "test-model", Thinking: "medium"}, agent).ID
	}
	get := func(id string) pebblestore.SessionSnapshot {
		t.Helper()
		session, ok, err := server.sessions.GetSession(id)
		if err != nil || !ok {
			t.Fatalf("session %s: %v %v", id, ok, err)
		}
		return session
	}
	// whileLocked runs request while the test holds the session's lock, checks
	// it waits, commits write (as end or a confirmation would, under the lock),
	// releases and returns the request's status.
	whileLocked := func(id string, request func() int, write func()) int {
		t.Helper()
		unlock := runruntime.LockClientToolConfirmations(id)
		done := make(chan int, 1)
		go func() { done <- request() }()
		select {
		case code := <-done:
			unlock()
			t.Fatalf("request finished (%d) while the session lock was held", code)
		case <-time.After(150 * time.Millisecond):
		}
		write()
		unlock()
		select {
		case code := <-done:
			return code
		case <-time.After(5 * time.Second):
			t.Fatal("request never finished after the lock was released")
		}
		return 0
	}
	post := func(handler func(http.ResponseWriter, *http.Request, identity.Principal, string), id, body string) func() int {
		return func() int {
			w := httptest.NewRecorder()
			handler(w, httptest.NewRequest(http.MethodPost, "/", strings.NewReader(body)), principal, id)
			return w.Code
		}
	}
	endNow := func(id string) func() {
		return func() {
			session := get(id)
			next := session
			next.Metadata = cloneStringAnyMap(session.Metadata)
			next.Metadata[sessionEndedMetadataKey] = map[string]any{"at": int64(5000), "by": principal.UserID}
			if _, err := server.applySessionV3PrimaryMutation(sessionruntime.SessionMutationInput{
				SessionID: id, UserID: principal.UserID, AccountScopeID: principal.AccountScopeID, ClientRequestID: "end-" + id, IdempotencyKey: "end-" + id, PayloadHash: "end-" + id, RequestHash: "end-" + id,
				Kind: sessionruntime.SessionMutationUpdateMetadata, Session: &next, NowUnixMs: 5000,
			}); err != nil {
				t.Error(err)
			}
		}
	}
	confirmation := runruntime.ClientToolConfirmation{ID: "conf_race", Kind: "client_tool_confirmation", Status: "pending", Tool: "place_order", Summary: "Order 2", Arguments: map[string]any{"quantity": 2}, ArgumentsHash: "sha256:x", CreatedAt: 4000, ExpiresAt: 9_000_000_000_000}
	openConfirmation := func(session pebblestore.SessionSnapshot) bool {
		c, ok := runruntime.ClientToolConfirmationFromSession(session)
		return ok && c.ID == confirmation.ID && c.Open()
	}

	// A takeover waiting on a confirmation write keeps the confirmation.
	held := newSession("held")
	code := whileLocked(held, post(server.handleSessionV3Takeover, held, `{"action":"start","client_request_id":"take-held"}`), func() {
		if err := runruntime.PutClientToolConfirmation(server.applySessionV3PrimaryMutation, get(held), confirmation, 4000); err != nil {
			t.Error(err)
		}
	})
	if session := get(held); code != http.StatusOK || !sessionTakeoverActive(session) || !openConfirmation(session) {
		t.Fatalf("takeover after a confirmation: %d takeover=%v confirmation=%v", code, sessionTakeoverActive(session), openConfirmation(session))
	}
	// An operator reply waiting on an end is refused, and adds no message.
	before, err := server.sessions.ListSessionMessages(held, 0, 50)
	if err != nil {
		t.Fatal(err)
	}
	if code := whileLocked(held, post(server.handleSessionV3OperatorMessages, held, `{"content":"a person here","client_request_id":"op-held"}`), endNow(held)); code != http.StatusConflict {
		t.Fatalf("operator reply after the end: %d, want 409", code)
	}
	if after, _ := server.sessions.ListSessionMessages(held, 0, 50); len(after) != len(before) {
		t.Fatalf("operator reply landed after the end: %d -> %d messages", len(before), len(after))
	}
	// Handing back an ended conversation keeps it ended and keeps the confirmation.
	if code := post(server.handleSessionV3Takeover, held, `{"action":"end","client_request_id":"back-held"}`)(); code != http.StatusOK {
		t.Fatalf("hand back: %d", code)
	}
	if session := get(held); !sessionEnded(session) || sessionTakeoverActive(session) || !openConfirmation(session) {
		t.Fatalf("hand back lost state: ended=%v takeover=%v confirmation=%v", sessionEnded(session), sessionTakeoverActive(session), openConfirmation(session))
	}

	// A takeover start waiting on an end is refused and leaves it ended.
	ending := newSession("ending")
	if code := whileLocked(ending, post(server.handleSessionV3Takeover, ending, `{"action":"start","client_request_id":"take-ending"}`), endNow(ending)); code != http.StatusConflict {
		t.Fatalf("takeover after the end: %d, want 409", code)
	}
	if session := get(ending); !sessionEnded(session) || sessionTakeoverActive(session) {
		t.Fatalf("takeover after the end: ended=%v takeover=%v", sessionEnded(session), sessionTakeoverActive(session))
	}

	// A visitor message waiting on an end is refused and starts nothing.
	visited := newSession("visited")
	visitorBefore, err := server.sessions.ListSessionMessages(visited, 0, 50)
	if err != nil {
		t.Fatal(err)
	}
	var acceptErr error
	queued := false
	whileLocked(visited, func() int {
		_, job, err := server.acceptSessionsV3Message(principal, visited, sessionsV3MessageRequest{ClientRequestID: "visitor-late", Role: "user", Content: "hello?"})
		acceptErr, queued = err, job != nil
		return 0
	}, endNow(visited))
	if !errors.Is(acceptErr, errSessionV3Ended) || queued {
		t.Fatalf("visitor message after the end: err=%v queued=%v, want errSessionV3Ended and no run", acceptErr, queued)
	}
	if after, _ := server.sessions.ListSessionMessages(visited, 0, 50); len(after) != len(visitorBefore) {
		t.Fatalf("visitor message landed after the end: %d -> %d messages", len(visitorBefore), len(after))
	}
}
