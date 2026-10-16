package api

import (
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"swarm/packages/swarmd/internal/identity"
	sessionruntime "swarm/packages/swarmd/internal/session"
	pebblestore "swarm/packages/swarmd/internal/store/pebble"
)

// Purpose: an ordinary scoped key (an SDK key or a client app key, not bound
// to an agent) cannot take over, reply as an operator, hand back or decide a
// confirmation on any session in its account, on the container SDK listener
// or at the handlers (which any listener reaches). It keeps waiting on, and
// reading the confirmations of, its own user's sessions, and not another
// user's. The agent's operator token still takes over its sealed agent's
// conversation, and the owner can take over only a sealed agent's.
// Threat: an SDK or client app key (for example one embedded in an app)
// impersonating staff in a public conversation or approving a side effect the
// visitor never approved.
// Boundary: containerSDKRouteAllowed (ContainerSDKHandler, gateClientAppKey),
// sessionTakeoverRefused (handleSessionV3Takeover,
// handleSessionV3OperatorMessages), handleSessionV3Confirmations.
// Narrowest layer: the container SDK listener with real minted keys, plus the
// handlers called directly with each key's record for the other listeners.
func TestScopedKeysCannotTakeOverOrDecideConfirmations(t *testing.T) {
	f := newGatewayTestFixture(t)
	sdk := f.mint(t, `{"name":"sdk","scopes":["sessions:read","sessions:write"],"expires_in_seconds":3600}`)
	app := f.mint(t, `{"name":"app","scopes":["sessions:read","sessions:write","client:app"],"expires_in_seconds":3600}`)
	operator := f.mint(t, `{"name":"hub","agent_name":"frontdesk","operator":true,"expires_in_seconds":3600}`)
	f.seed(t, "desk", "frontdesk", 1000)
	f.seed(t, "coding", "swarm", 1000)
	other := pebblestore.SessionSnapshot{ID: "other-user", UserID: "someone-else", AccountScopeID: f.account, Title: "x", Mode: "auto", Metadata: map[string]any{"agent_name": "frontdesk"}, CreatedAt: 1000, UpdatedAt: 1000}
	if _, err := f.server.sessions.ApplySessionMutation(sessionruntime.SessionMutationInput{Kind: sessionruntime.SessionMutationCreateSession, SessionID: other.ID, UserID: other.UserID, AccountScopeID: f.account, Session: &other, IdempotencyKey: other.ID, PayloadHash: other.ID, NowUnixMs: 1000}); err != nil {
		t.Fatal(err)
	}

	refused := []struct{ method, path, body string }{
		{"GET", "/v3/sessions/desk/takeover", ""},
		{"POST", "/v3/sessions/desk/takeover", `{"action":"start","client_request_id":"t1"}`},
		{"POST", "/v3/sessions/desk/takeover", `{"action":"end","client_request_id":"t2"}`},
		{"POST", "/v3/sessions/desk/operator-messages", `{"content":"staff here","client_request_id":"o1"}`},
		{"POST", "/v3/sessions/desk/confirmations/conf_1", `{"decision":"approve","arguments_hash":"sha256:x"}`},
	}
	for _, key := range []string{sdk, app} {
		for _, tc := range refused {
			if code, out := f.do(t, key, tc.method, tc.path, tc.body); code != http.StatusForbidden {
				t.Fatalf("scoped key %s %s: %d %v, want 403", tc.method, tc.path, code, out)
			}
		}
		if code, out := f.do(t, key, "GET", "/v3/sessions/desk/wait", ""); code != http.StatusOK {
			t.Fatalf("scoped key wait on its own session: %d %v", code, out)
		}
		if code, out := f.do(t, key, "GET", "/v3/sessions/desk/confirmations", ""); code != http.StatusOK {
			t.Fatalf("scoped key reading confirmations: %d %v", code, out)
		}
		if code, _ := f.do(t, key, "GET", "/v3/sessions/other-user/wait", ""); code != http.StatusNotFound {
			t.Fatalf("scoped key wait on another user's session: %d, want 404", code)
		}
	}
	if session, _, _ := f.server.sessions.GetSession("desk"); sessionTakeoverActive(session) {
		t.Fatal("a refused takeover changed the session")
	}

	// The handlers refuse the same keys on any listener.
	principal := identity.Principal{Type: identity.PrincipalTypeUser, UserID: f.user, AccountScopeID: f.account}
	call := func(rec *pebblestore.ScopedTokenRecord, handler func(http.ResponseWriter, *http.Request, identity.Principal, string), method, body string) int {
		r := httptest.NewRequest(method, "/", strings.NewReader(body))
		if rec != nil {
			r = requestWithScopedToken(r, rec)
		}
		w := httptest.NewRecorder()
		handler(w, r, principal, "desk")
		return w.Code
	}
	decide := func(w http.ResponseWriter, r *http.Request, p identity.Principal, id string) {
		f.server.handleSessionV3Confirmations(w, r, p, id, "conf_1")
	}
	for _, rec := range []*pebblestore.ScopedTokenRecord{
		{ID: "tok_sdk", AccountScopeID: f.account, UserID: f.user, Scopes: []string{"sessions:read", "sessions:write"}},
		{ID: "tok_app", AccountScopeID: f.account, UserID: f.user, Scopes: []string{"sessions:read", "sessions:write", "client:app"}},
	} {
		for _, tc := range []struct {
			name    string
			handler func(http.ResponseWriter, *http.Request, identity.Principal, string)
			method  string
			body    string
		}{
			{"takeover read", f.server.handleSessionV3Takeover, "GET", ""},
			{"takeover start", f.server.handleSessionV3Takeover, "POST", `{"action":"start","client_request_id":"h1"}`},
			{"hand back", f.server.handleSessionV3Takeover, "POST", `{"action":"end","client_request_id":"h2"}`},
			{"operator reply", f.server.handleSessionV3OperatorMessages, "POST", `{"content":"staff here","client_request_id":"h3"}`},
			{"decide confirmation", decide, "POST", `{"decision":"reject"}`},
		} {
			if code := call(rec, tc.handler, tc.method, tc.body); code != http.StatusForbidden {
				t.Fatalf("%s: %s with a scoped key: %d, want 403", rec.ID, tc.name, code)
			}
		}
	}
	if code := call(&pebblestore.ScopedTokenRecord{ID: "tok_op", AgentName: "frontdesk", AgentOperator: true, AccountScopeID: f.account, UserID: f.user}, decide, "POST", `{"decision":"reject"}`); code != http.StatusForbidden {
		t.Fatalf("operator token deciding a confirmation: %d, want 403", code)
	}
	if session, _, _ := f.server.sessions.GetSession("desk"); sessionTakeoverActive(session) {
		t.Fatal("a refused takeover changed the session")
	}

	// The operator token takes over its sealed agent's conversation; the owner
	// cannot take over an ordinary agent's session.
	if code, out := f.do(t, operator, "POST", "/v3/sessions/desk/takeover", `{"action":"start","client_request_id":"op-1"}`); code != http.StatusOK {
		t.Fatalf("operator takeover: %d %v", code, out)
	}
	if session, _, _ := f.server.sessions.GetSession("desk"); !sessionTakeoverActive(session) {
		t.Fatal("operator takeover not recorded")
	}
	owner := func(w http.ResponseWriter, r *http.Request) {
		f.server.handleSessionV3Takeover(w, r, principal, "coding")
	}
	w := httptest.NewRecorder()
	owner(w, httptest.NewRequest(http.MethodPost, "/", strings.NewReader(`{"action":"start","client_request_id":"own-1"}`)))
	if w.Code != http.StatusConflict {
		t.Fatalf("owner takeover of an ordinary agent's session: %d, want 409", w.Code)
	}
}
