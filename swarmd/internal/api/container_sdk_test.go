package api

import (
	"encoding/json"
	"net/http/httptest"
	"path/filepath"
	"strings"
	"swarm/packages/swarmd/internal/permission"
	"testing"
	"time"

	sessionruntime "swarm/packages/swarmd/internal/session"
	pebblestore "swarm/packages/swarmd/internal/store/pebble"
)

// Requirement: the published SDK surface accepts only live scoped credentials
// bound to their exact account/user, not attach/bootstrap/local authority. The
// ContainerSDKHandler and canonical V3 scope/access checks own this boundary.
// In-process HTTP plus real temporary stores proves rejection without mutations;
// it does not claim Docker networking, provider or live-agent qualification.
func TestContainerSDKAuthenticationAndScopes(t *testing.T) {
	s, attach, sec, cleanup := setupScopedAuthTestServer(t)
	defer cleanup()
	permissionDB, err := pebblestore.Open(filepath.Join(t.TempDir(), "permissions"))
	if err != nil {
		t.Fatal(err)
	}
	defer permissionDB.Close()
	s.perm = permission.NewService(pebblestore.NewPermissionStore(permissionDB), nil, nil)
	actor, err := s.identitySessions.ActorForCurrentSelection()
	if err != nil {
		t.Fatal(err)
	}
	issue := func(user, account string, scopes []string, lifetime time.Duration) (string, string) {
		t.Helper()
		token, record, err := sec.CreateScopedToken("sdk-test", scopes, account, user, lifetime, "", "")
		if err != nil {
			t.Fatal(err)
		}
		return token, record.ID
	}
	read, _ := issue(actor.UserID, actor.AccountScopeID, []string{"sessions:read"}, time.Hour)
	write, _ := issue(actor.UserID, actor.AccountScopeID, []string{"sessions:read", "sessions:write"}, time.Hour)
	wrongAccount, _ := issue(actor.UserID, "account-other", []string{"sessions:read"}, time.Hour)
	missingUser, _ := issue("missing-user", actor.AccountScopeID, []string{"sessions:read"}, time.Hour)
	expired, _ := issue(actor.UserID, actor.AccountScopeID, []string{"sessions:read"}, -time.Second)
	revoked, revokeID := issue(actor.UserID, actor.AccountScopeID, []string{"sessions:read"}, time.Hour)
	if _, err := sec.RevokeScopedToken(actor.AccountScopeID, revokeID); err != nil {
		t.Fatal(err)
	}
	pending, err := s.perm.CreatePending(permission.CreateInput{SessionID: "foreign-session", RunID: "foreign-run", CallID: "foreign-call", ToolName: "bash", ToolArguments: "{}", Requirement: "approval", Mode: "auto"})
	if err != nil {
		t.Fatal(err)
	}
	handler := s.ContainerSDKHandler()
	request := func(method, path, token, body string) *httptest.ResponseRecorder {
		r := httptest.NewRequest(method, "http://127.0.0.1:7783"+path, strings.NewReader(body))
		r.RemoteAddr = "192.0.2.1:40000" // Container bridge, never privileged IPC.
		if token != "" {
			r.Header.Set("Authorization", "Bearer "+token)
		}
		r.Header.Set("Origin", "http://127.0.0.1:7783")
		r.Header.Set("Referer", "http://127.0.0.1:7783/app")
		r.Header.Set("Sec-Fetch-Site", "same-origin")
		w := httptest.NewRecorder()
		handler.ServeHTTP(w, r)
		return w
	}
	for _, token := range []string{"", "local", "zero-conf", "swk_invalid", attach, wrongAccount, missingUser, revoked, expired} {
		for _, path := range []string{"/v3/sessions", "/v1/onboarding", "/v1/auth/desktop/session"} {
			if w := request("GET", path, token, ""); w.Code != 401 {
				t.Fatalf("invalid credential admitted: %s %d", path, w.Code)
			}
		}
	}
	for _, path := range []string{"/v1/onboarding", "/v3/auth/tokens", "/v1/auth/desktop/session", "/healthz", "/v3/sessions:unarchive", "/v3/sessions/../auth/tokens"} {
		if w := request("POST", path, write, `{}`); w.Code != 403 {
			t.Fatalf("route exposed %s %d", path, w.Code)
		}
	}
	// Seed foreign session via the same durable mutation authority. A valid SDK
	// credential must not read it or append a message to that account.
	foreign := pebblestore.SessionSnapshot{ID: "foreign-session", UserID: "foreign-user", AccountScopeID: "account-other", Title: "private", Mode: "auto"}
	if _, err := s.sessions.ApplySessionMutation(sessionruntime.SessionMutationInput{Kind: sessionruntime.SessionMutationCreateSession, SessionID: foreign.ID, UserID: foreign.UserID, AccountScopeID: foreign.AccountScopeID, Session: &foreign, IdempotencyKey: "foreign-create", PayloadHash: "foreign-create", NowUnixMs: 1000}); err != nil {
		t.Fatal(err)
	}
	for _, tc := range []struct {
		method, path, token, body string
		want                      int
	}{
		{"GET", "/v3/sessions", read, "", 200},
		{"POST", "/v3/sessions", read, `{"client_request_id":"forbidden-create"}`, 403},
		{"GET", "/v3/sessions/foreign-session", write, "", 404},
		{"POST", "/v3/sessions/foreign-session/messages", write, `{"client_request_id":"foreign-write","role":"user","content":"forbidden"}`, 404},
		{"POST", "/v3/sessions/foreign-session/permissions/pending/resolve", read, `{"action":"allow_once"}`, 403},
		{"POST", "/v3/sessions/foreign-session/permissions/pending/resolve", write, `{"action":"allow_once"}`, 404},
	} {
		tc.path = strings.ReplaceAll(tc.path, "/pending/", "/"+pending.ID+"/")
		w := request(tc.method, tc.path, tc.token, tc.body)
		if w.Code != tc.want {
			t.Fatalf("%s %s: got %d want %d: %s", tc.method, tc.path, w.Code, tc.want, w.Body.String())
		}
		if tc.path == "/v3/sessions" && tc.method == "GET" {
			var body struct {
				Sessions []json.RawMessage `json:"sessions"`
			}
			if err := json.Unmarshal(w.Body.Bytes(), &body); err != nil || len(body.Sessions) != 0 {
				t.Fatal("foreign or unauthorized session leaked/created")
			}
		}
	}
	stillPending, err := s.perm.ListPending("foreign-session", 10)
	if err != nil || len(stillPending) != 1 || stillPending[0].ID != pending.ID {
		t.Fatal("foreign permission was resolved")
	}
	messages, err := s.sessions.ListSessionMessages(foreign.ID, 0, 10)
	if err != nil || len(messages) != 0 {
		t.Fatal("unauthorized message persisted")
	}
	// The same route may resolve an owned pending call only with write scope.
	own := foreign
	own.ID, own.UserID, own.AccountScopeID = "owned-session", actor.UserID, actor.AccountScopeID
	if _, err := s.sessions.ApplySessionMutation(sessionruntime.SessionMutationInput{Kind: sessionruntime.SessionMutationCreateSession, SessionID: own.ID, UserID: own.UserID, AccountScopeID: own.AccountScopeID, Session: &own, IdempotencyKey: "own-create", PayloadHash: "own-create", NowUnixMs: 1000}); err != nil {
		t.Fatal(err)
	}
	ownPending, err := s.perm.CreatePending(permission.CreateInput{SessionID: own.ID, RunID: "owned-run", CallID: "owned-call", ToolName: "bash", ToolArguments: "{}", Requirement: "approval", Mode: "auto"})
	if err != nil {
		t.Fatal(err)
	}
	resolvePath := "/v3/sessions/" + own.ID + "/permissions/" + ownPending.ID + "/resolve"
	if w := request("POST", resolvePath, read, `{"action":"allow_once"}`); w.Code != 403 {
		t.Fatal("read token approved a call")
	}
	if pending, err := s.perm.ListPending(own.ID, 10); err != nil || len(pending) != 1 {
		t.Fatal("read token changed pending permission")
	}
	if w := request("POST", resolvePath, write, `{"action":"allow_once"}`); w.Code != 200 {
		t.Fatalf("owned approval failed: %d %s", w.Code, w.Body.String())
	}
	if pending, err := s.perm.ListPending(own.ID, 10); err != nil || len(pending) != 0 {
		t.Fatal("approval did not resolve exact pending call")
	}
	records, err := sec.ListScopedTokens(actor.AccountScopeID)
	if err != nil || len(records) != 5 {
		t.Fatalf("unauthorized token mutation: count %d err %v", len(records), err)
	}
}
