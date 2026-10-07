package api

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"swarm/packages/swarmd/internal/permission"
	sessionruntime "swarm/packages/swarmd/internal/session"
	pebblestore "swarm/packages/swarmd/internal/store/pebble"
)

// Requirement: Swarm Control MCP is a client surface over the scoped-token SDK
// listener. It must admit only live scoped credentials bound to their account,
// reject browser cross-origin calls (DNS rebinding) and unsupported protocol
// shapes, and every tool must reach V3 state only through the allowlisted SDK
// routes so canonical scope, ownership and permission checks still decide.
// Threats: an MCP client reading or mutating another account's session, a
// read-only grant mutating state, or a tool widening approval to persistent
// rules. Owners: ContainerSDKHandler, serveControlMCP, controlMCPCall.dispatch
// and the V3 session/permission handlers. In-process HTTP with real temporary
// stores is the narrowest layer that exercises auth, dispatch and handlers
// together; it does not prove Docker networking, providers or live agents.
func TestControlMCPAuthenticationAndAuthority(t *testing.T) {
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
	issue := func(account string, scopes []string, lifetime time.Duration) (string, string) {
		t.Helper()
		token, record, err := sec.CreateScopedToken("mcp-test", scopes, account, actor.UserID, lifetime, "", "")
		if err != nil {
			t.Fatal(err)
		}
		return token, record.ID
	}
	read, _ := issue(actor.AccountScopeID, []string{"sessions:read"}, time.Hour)
	write, _ := issue(actor.AccountScopeID, []string{"sessions:read", "sessions:write"}, time.Hour)
	wrongAccount, _ := issue("account-other", []string{"sessions:read", "sessions:write"}, time.Hour)
	expired, _ := issue(actor.AccountScopeID, []string{"sessions:read"}, -time.Second)
	revoked, revokeID := issue(actor.AccountScopeID, []string{"sessions:read"}, time.Hour)
	if _, err := sec.RevokeScopedToken(actor.AccountScopeID, revokeID); err != nil {
		t.Fatal(err)
	}

	handler := s.ContainerSDKHandler()
	post := func(token, body string, headers map[string]string) *httptest.ResponseRecorder {
		r := httptest.NewRequest("POST", "http://127.0.0.1:7783/mcp", strings.NewReader(body))
		r.RemoteAddr = "192.0.2.1:40000" // Container bridge, never privileged IPC.
		r.Header.Set("Content-Type", "application/json")
		if token != "" {
			r.Header.Set("Authorization", "Bearer "+token)
		}
		for key, value := range headers {
			r.Header.Set(key, value)
		}
		w := httptest.NewRecorder()
		handler.ServeHTTP(w, r)
		return w
	}
	type rpcResponse struct {
		Result struct {
			ProtocolVersion string `json:"protocolVersion"`
			Tools           []struct {
				Name string `json:"name"`
			} `json:"tools"`
			IsError bool `json:"isError"`
			Content []struct {
				Text string `json:"text"`
			} `json:"content"`
			StructuredContent map[string]any `json:"structuredContent"`
		} `json:"result"`
		Error *struct {
			Code int `json:"code"`
		} `json:"error"`
	}
	rpc := func(token, method string, params any) rpcResponse {
		t.Helper()
		encoded, _ := json.Marshal(map[string]any{"jsonrpc": "2.0", "id": 1, "method": method, "params": params})
		w := post(token, string(encoded), nil)
		if w.Code != 200 {
			t.Fatalf("%s: HTTP %d %s", method, w.Code, w.Body.String())
		}
		var out rpcResponse
		if err := json.Unmarshal(w.Body.Bytes(), &out); err != nil {
			t.Fatalf("%s: %v", method, err)
		}
		return out
	}
	call := func(token, tool string, args map[string]any) rpcResponse {
		t.Helper()
		return rpc(token, "tools/call", map[string]any{"name": tool, "arguments": args})
	}

	initialize := `{"jsonrpc":"2.0","id":1,"method":"initialize","params":{"protocolVersion":"2025-06-18"}}`
	for _, token := range []string{"", "local", "swk_invalid", attach, wrongAccount, expired, revoked} {
		if w := post(token, initialize, nil); w.Code != 401 {
			t.Fatalf("invalid credential admitted to MCP: %d", w.Code)
		}
	}
	for _, tc := range []struct {
		name    string
		body    string
		headers map[string]string
		want    int
	}{
		{"cross-origin browser", initialize, map[string]string{"Origin": "https://attacker.example"}, 403},
		{"unsupported protocol", initialize, map[string]string{"MCP-Protocol-Version": "1999-01-01"}, 400},
		{"wrong media type", initialize, map[string]string{"Content-Type": "text/plain"}, 415},
		{"notification", `{"jsonrpc":"2.0","method":"notifications/initialized"}`, nil, 202},
	} {
		if w := post(read, tc.body, tc.headers); w.Code != tc.want {
			t.Fatalf("%s: got %d want %d", tc.name, w.Code, tc.want)
		}
	}
	if w := post(read, `[`+initialize+`]`, nil); !strings.Contains(w.Body.String(), "-32600") {
		t.Fatalf("batch accepted: %s", w.Body.String())
	}
	if got := rpc(read, "initialize", map[string]any{"protocolVersion": "2025-06-18"}); got.Result.ProtocolVersion != "2025-06-18" {
		t.Fatalf("protocol negotiation: %q", got.Result.ProtocolVersion)
	}
	if got := rpc(read, "tools/list", nil); len(got.Result.Tools) != 6 {
		t.Fatalf("tool surface changed: %d tools", len(got.Result.Tools))
	}
	if got := rpc(read, "no/such", nil); got.Error == nil || got.Error.Code != -32601 {
		t.Fatal("unknown method not rejected")
	}

	foreign := pebblestore.SessionSnapshot{ID: "foreign-session", UserID: "foreign-user", AccountScopeID: "account-other", Title: "private", Mode: "auto"}
	own := pebblestore.SessionSnapshot{ID: "owned-session", UserID: actor.UserID, AccountScopeID: actor.AccountScopeID, Title: "mine", Mode: "auto"}
	for _, session := range []pebblestore.SessionSnapshot{foreign, own} {
		session := session
		if _, err := s.sessions.ApplySessionMutation(sessionruntime.SessionMutationInput{Kind: sessionruntime.SessionMutationCreateSession, SessionID: session.ID, UserID: session.UserID, AccountScopeID: session.AccountScopeID, Session: &session, IdempotencyKey: session.ID + "-create", PayloadHash: session.ID + "-create", NowUnixMs: 1000}); err != nil {
			t.Fatal(err)
		}
	}
	foreignPending, err := s.perm.CreatePending(permission.CreateInput{SessionID: foreign.ID, RunID: "foreign-run", CallID: "foreign-call", ToolName: "bash", ToolArguments: "{}", Requirement: "approval", Mode: "auto"})
	if err != nil {
		t.Fatal(err)
	}
	ownPending, err := s.perm.CreatePending(permission.CreateInput{SessionID: own.ID, RunID: "owned-run", CallID: "owned-call", ToolName: "bash", ToolArguments: `{"command":"ls"}`, Requirement: "approval", Mode: "auto"})
	if err != nil {
		t.Fatal(err)
	}

	listed := call(read, "swarm_list_sessions", map[string]any{"limit": 50})
	if listed.Result.IsError || strings.Contains(listed.Result.Content[0].Text, foreign.ID) || !strings.Contains(listed.Result.Content[0].Text, own.ID) {
		t.Fatalf("list leaked or omitted sessions: %+v", listed.Result)
	}
	if got := call(write, "swarm_get_session", map[string]any{"session_id": foreign.ID}); !got.Result.IsError || !strings.Contains(got.Result.Content[0].Text, "HTTP 404") || strings.Contains(got.Result.Content[0].Text, "private") {
		t.Fatal("foreign session readable through MCP")
	}
	detail := call(read, "swarm_get_session", map[string]any{"session_id": own.ID})
	if detail.Result.IsError || !strings.Contains(detail.Result.Content[0].Text, ownPending.ID) {
		t.Fatalf("owned pending permission not visible: %+v", detail.Result)
	}
	for _, tc := range []struct {
		name  string
		token string
		tool  string
		args  map[string]any
		want  string
	}{
		{"read grant creates", read, "swarm_create_session", map[string]any{"workspace_path": "/project"}, "HTTP 403"},
		{"read grant messages", read, "swarm_send_message", map[string]any{"session_id": own.ID, "content": "hi"}, "HTTP 403"},
		{"read grant approves", read, "swarm_resolve_permission", map[string]any{"session_id": own.ID, "permission_id": ownPending.ID, "action": "allow_once"}, "HTTP 403"},
		{"persistent rule", write, "swarm_resolve_permission", map[string]any{"session_id": own.ID, "permission_id": ownPending.ID, "action": "allow_always"}, "must be one of"},
		{"undeclared argument", write, "swarm_resolve_permission", map[string]any{"session_id": own.ID, "permission_id": ownPending.ID, "action": "allow_once", "scope": "global"}, "unknown argument"},
		{"foreign message", write, "swarm_send_message", map[string]any{"session_id": foreign.ID, "content": "forbidden"}, "HTTP 404"},
		{"foreign approval", write, "swarm_resolve_permission", map[string]any{"session_id": foreign.ID, "permission_id": foreignPending.ID, "action": "allow_once"}, "HTTP 404"},
		{"cross-session approval", write, "swarm_resolve_permission", map[string]any{"session_id": own.ID, "permission_id": foreignPending.ID, "action": "allow_once"}, "HTTP 4"},
		{"path traversal", write, "swarm_get_session", map[string]any{"session_id": "../auth/tokens"}, "route unavailable"},
	} {
		got := call(tc.token, tc.tool, tc.args)
		if !got.Result.IsError || len(got.Result.Content) == 0 || !strings.Contains(got.Result.Content[0].Text, tc.want) {
			t.Fatalf("%s: want rejection %q, got %+v", tc.name, tc.want, got.Result)
		}
	}
	if pending, err := s.perm.ListPending(own.ID, 10); err != nil || len(pending) != 1 {
		t.Fatal("rejected calls changed the owned pending permission")
	}
	if pending, err := s.perm.ListPending(foreign.ID, 10); err != nil || len(pending) != 1 {
		t.Fatal("foreign permission was resolved")
	}
	for _, id := range []string{own.ID, foreign.ID} {
		if messages, err := s.sessions.ListSessionMessages(id, 0, 10); err != nil || len(messages) != 0 {
			t.Fatal("rejected call persisted a message")
		}
	}
	if sessions, err := s.sessions.ListSessionsForAccountUser(actor.AccountScopeID, actor.UserID, 50); err != nil || len(sessions) != 1 {
		t.Fatalf("read grant created a session: %d", len(sessions))
	}

	// The daemon's own listeners serve the same surface for local clients, but
	// only to scoped tokens: attach tokens and implicit socket ownership carry
	// no grant boundary.
	daemonPost := func(h http.Handler, token string) *httptest.ResponseRecorder {
		r := httptest.NewRequest("POST", "http://127.0.0.1:7781/mcp", strings.NewReader(initialize))
		r.RemoteAddr = "127.0.0.1:40000"
		r.Header.Set("Content-Type", "application/json")
		if token != "" {
			r.Header.Set("Authorization", "Bearer "+token)
		}
		w := httptest.NewRecorder()
		h.ServeHTTP(w, r)
		return w
	}
	for name, tc := range map[string]struct {
		handler http.Handler
		token   string
		want    int
	}{
		"attach token":           {s.Handler(), attach, 401},
		"implicit local socket":  {s.LocalTransportHandler(), "", 401},
		"revoked scoped token":   {s.Handler(), revoked, 401},
		"scoped token on API":    {s.Handler(), read, 200},
		"scoped token on socket": {s.LocalTransportHandler(), read, 200},
	} {
		if w := daemonPost(tc.handler, tc.token); w.Code != tc.want || (tc.want == 200 && !strings.Contains(w.Body.String(), `"protocolVersion"`)) {
			t.Fatalf("%s: got %d want %d: %s", name, w.Code, tc.want, w.Body.String())
		}
	}

	resolved := call(write, "swarm_resolve_permission", map[string]any{"session_id": own.ID, "permission_id": ownPending.ID, "action": "deny_once", "reason": "not needed"})
	if resolved.Result.IsError {
		t.Fatalf("owned resolution failed: %+v", resolved.Result)
	}
	if pending, err := s.perm.ListPending(own.ID, 10); err != nil || len(pending) != 0 {
		t.Fatal("resolution did not resolve the exact pending call")
	}
	if records, err := sec.ListScopedTokens(actor.AccountScopeID); err != nil || len(records) != 4 {
		t.Fatalf("unauthorized token mutation: count %d err %v", len(records), err)
	}
}
