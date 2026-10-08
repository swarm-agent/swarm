package api

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"path/filepath"
	"strconv"
	"strings"
	"testing"
	"time"

	agentruntime "swarm/packages/swarmd/internal/agent"
	"swarm/packages/swarmd/internal/permission"
	sessionruntime "swarm/packages/swarmd/internal/session"
	pebblestore "swarm/packages/swarmd/internal/store/pebble"
)

// Purpose: the token an application gateway holds must not be a key to the
// machine. An agent-bound token may only create and use its sealed agent's
// sessions and answer that agent's client tool calls. It cannot reach the full
// Swarm agent (which has bash), approve a pending bash call, use MCP, list
// sessions, or switch a session's agent; and it stops working if the agent is
// later given a built-in tool.
func TestAgentBoundTokenIsDefaultDeny(t *testing.T) {
	s, _, _, cleanup := setupScopedAuthTestServer(t)
	defer cleanup()
	db, err := pebblestore.Open(filepath.Join(t.TempDir(), "agent-bound"))
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	s.perm = permission.NewService(pebblestore.NewPermissionStore(db), nil, nil)
	agentEvents, err := pebblestore.NewEventLog(db)
	if err != nil {
		t.Fatal(err)
	}
	s.agents = agentruntime.NewService(pebblestore.NewAgentStore(db), agentEvents)
	actor, err := s.identitySessions.ActorForCurrentSelection()
	if err != nil {
		t.Fatal(err)
	}
	account := actor.AccountScopeID
	if _, err := s.agents.PutCustomToolForAccount(account, pebblestore.AgentCustomToolDefinition{Name: "lookup_order", Kind: pebblestore.AgentCustomToolKindClient, Description: "x", InputSchema: lookupOrderSchema}); err != nil {
		t.Fatal(err)
	}
	sealed := func(tools map[string]pebblestore.AgentToolConfig) {
		t.Helper()
		if _, _, _, err := s.agents.UpsertForAccount(account, agentruntime.UpsertInput{Name: "frontdesk", Mode: agentruntime.ModeSubagent, Enabled: pebblestore.BoolPtr(true), Prompt: "x",
			ToolContract: &pebblestore.AgentToolContract{Preset: "custom", Tools: tools}}); err != nil {
			t.Fatal(err)
		}
	}
	sealed(map[string]pebblestore.AgentToolConfig{"lookup_order": {Enabled: pebblestore.BoolPtr(true)}})
	if _, _, _, err := s.agents.UpsertForAccount(account, agentruntime.UpsertInput{Name: "lister", Mode: agentruntime.ModeSubagent, Enabled: pebblestore.BoolPtr(true), Prompt: "x",
		ToolContract: &pebblestore.AgentToolContract{Preset: "custom", Tools: map[string]pebblestore.AgentToolConfig{"list": {Enabled: pebblestore.BoolPtr(true)}}}}); err != nil {
		t.Fatal(err)
	}

	// Minting goes through the owner-only token route.
	mint := func(body string) (int, map[string]any) {
		r := requestWithTestPrincipalForAccount(httptest.NewRequest(http.MethodPost, "/v3/auth/tokens", strings.NewReader(body)), actor.UserID, account)
		w := httptest.NewRecorder()
		s.handleAuthTokens(w, r)
		var out map[string]any
		_ = json.Unmarshal(w.Body.Bytes(), &out)
		return w.Code, out
	}
	for _, body := range []string{
		`{"name":"gw","agent_name":"lister"}`,
		`{"name":"gw","agent_name":"swarm"}`,
		`{"name":"gw","agent_name":"frontdesk","scopes":["admin"]}`,
		`{"name":"gw","agent_name":"frontdesk","worker_id":"w"}`,
	} {
		if code, out := mint(body); code != http.StatusBadRequest {
			t.Fatalf("minted %s: %d %v", body, code, out)
		}
	}
	code, out := mint(`{"name":"gw","agent_name":"frontdesk"}`)
	if code != http.StatusOK {
		t.Fatalf("mint sealed: %d %v", code, out)
	}
	token, _ := out["token"].(string)
	record, _ := out["record"].(map[string]any)
	if record["agent_name"] != "frontdesk" || strings.Join(toStrings(record["scopes"]), ",") != "sessions:read,sessions:write" {
		t.Fatalf("record = %v", record)
	}

	seed := func(id, agent string) {
		snapshot := pebblestore.SessionSnapshot{ID: id, UserID: actor.UserID, AccountScopeID: account, Title: id, Mode: "auto", Metadata: map[string]any{"agent_name": agent}}
		if _, err := s.sessions.ApplySessionMutation(sessionruntime.SessionMutationInput{Kind: sessionruntime.SessionMutationCreateSession, SessionID: id, UserID: actor.UserID, AccountScopeID: account, Session: &snapshot, IdempotencyKey: id, PayloadHash: id, NowUnixMs: 1000}); err != nil {
			t.Fatal(err)
		}
	}
	seed("desk-session", "frontdesk")
	seed("swarm-session", "swarm")
	bash, err := s.perm.CreatePending(permission.CreateInput{SessionID: "desk-session", RunID: "r", CallID: "c1", ToolName: "bash", ToolArguments: "{}", Requirement: "approval", Mode: "auto"})
	if err != nil {
		t.Fatal(err)
	}
	lookup, err := s.perm.CreatePending(permission.CreateInput{SessionID: "desk-session", RunID: "r", CallID: "c2", ToolName: "lookup_order", ToolArguments: `{"order_id":"AB12CD"}`, Requirement: "approval", Mode: "auto"})
	if err != nil {
		t.Fatal(err)
	}

	sdk := s.ContainerSDKHandler()
	request := func(handler http.Handler, method, path, body string) *httptest.ResponseRecorder {
		r := httptest.NewRequest(method, "http://127.0.0.1:7783"+path, strings.NewReader(body))
		r.RemoteAddr = "192.0.2.1:40000"
		r.Header.Set("Authorization", "Bearer "+token)
		r.Header.Set("Content-Type", "application/json")
		w := httptest.NewRecorder()
		handler.ServeHTTP(w, r)
		return w
	}
	denied := []struct{ method, path, body string }{
		{"GET", "/v3/sessions", ""},
		{"POST", "/v3/sessions", `{"agent_name":"swarm","client_request_id":"x"}`},
		{"POST", "/v3/sessions", `{"agent_name":"lister","client_request_id":"x"}`},
		{"POST", "/v3/sessions", `{"agent_name":"frontdesk","metadata":{"agent_name":"swarm"}}`},
		{"POST", "/v3/sessions", `{"agent_name":"frontdesk","parent_session_id":"swarm-session"}`},
		{"POST", "/v3/sessions", `{"agent_name":"frontdesk","preference":{"provider":"codex","model":"most-expensive"}}`},
		{"POST", "/v3/sessions/desk-session/messages", `{"role":"user","content":"x","client_request_id":"a","artifact_selections":[{"artifact_id":"owner-secret"}]}`},
		{"POST", "/v3/sessions/desk-session/messages", `{"role":"user","content":"x","client_request_id":"b","media":[{"id":"m"}]}`},
		{"POST", "/v3/sessions/desk-session/messages", `{"role":"user","content":"x","client_request_id":"c","metadata":{"selected_worker":"w"}}`},
		{"GET", "/v3/sessions/swarm-session", ""},
		{"POST", "/v3/sessions/swarm-session/messages", `{"role":"user","content":"hi","client_request_id":"m"}`},
		{"POST", "/v3/sessions/desk-session/agent", `{"agent_name":"swarm","client_request_id":"a"}`},
		{"POST", "/v3/sessions/desk-session/permissions/" + bash.ID + "/resolve", `{"action":"allow_once"}`},
		{"POST", "/v3/sessions/desk-session/permissions/resolve_all", `{"action":"allow_once"}`},
		{"POST", "/mcp", `{"jsonrpc":"2.0","id":1,"method":"tools/list"}`},
		{"GET", "/v2/agents", ""},
		{"POST", "/v3/auth/tokens", `{"name":"x"}`},
	}
	for _, handler := range []http.Handler{sdk, s.Handler()} {
		for _, tc := range denied {
			if w := request(handler, tc.method, tc.path, tc.body); w.Code != http.StatusForbidden {
				t.Fatalf("%s %s: %d %s", tc.method, tc.path, w.Code, w.Body.String())
			}
		}
	}
	if pending, _ := s.perm.ListPending("desk-session", 10); len(pending) != 2 {
		t.Fatalf("a refused resolve changed state: %d pending", len(pending))
	}
	for _, tc := range []struct{ method, path, body string }{
		{"GET", "/v3/sessions/desk-session", ""},
		{"POST", "/v3/sessions/desk-session/permissions/" + lookup.ID + "/resolve", `{"action":"allow_once","approved_arguments":{"result":{"status":"shipped"}}}`},
	} {
		if w := request(sdk, tc.method, tc.path, tc.body); w.Code == http.StatusForbidden || w.Code == http.StatusUnauthorized {
			t.Fatalf("own agent route refused %s %s: %d %s", tc.method, tc.path, w.Code, w.Body.String())
		}
	}
	// A create for its own agent passes the gate (the handler then needs a workspace).
	if w := request(sdk, "POST", "/v3/sessions", `{"agent_name":"frontdesk","client_request_id":"x"}`); strings.Contains(w.Body.String(), "agent-bound") {
		t.Fatalf("own agent create refused by the gate: %s", w.Body.String())
	}

	// Widening the agent disables the token rather than widening it.
	sealed(map[string]pebblestore.AgentToolConfig{"lookup_order": {Enabled: pebblestore.BoolPtr(true)}, "bash": {Enabled: pebblestore.BoolPtr(true)}})
	if w := request(sdk, "GET", "/v3/sessions/desk-session", ""); w.Code != http.StatusForbidden || !strings.Contains(w.Body.String(), "not sealed") {
		t.Fatalf("widened agent still served: %d %s", w.Code, w.Body.String())
	}
	_ = time.Second
}

func toStrings(value any) []string {
	items, _ := value.([]any)
	out := make([]string, 0, len(items))
	for _, item := range items {
		if s, ok := item.(string); ok {
			out = append(out, s)
		}
	}
	return out
}

// Purpose: even a leaked or buggy gateway token can only start a bounded
// number of model runs and conversations. Limits are set when minting and the
// daemon answers 429 with Retry-After beyond them; reads are not limited.
func TestAgentBoundTokenRateLimits(t *testing.T) {
	s, _, _, cleanup := setupScopedAuthTestServer(t)
	defer cleanup()
	db, err := pebblestore.Open(filepath.Join(t.TempDir(), "agent-bound-rate"))
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	events, err := pebblestore.NewEventLog(db)
	if err != nil {
		t.Fatal(err)
	}
	s.perm = permission.NewService(pebblestore.NewPermissionStore(db), nil, nil)
	s.agents = agentruntime.NewService(pebblestore.NewAgentStore(db), events)
	actor, err := s.identitySessions.ActorForCurrentSelection()
	if err != nil {
		t.Fatal(err)
	}
	account := actor.AccountScopeID
	if _, err := s.agents.PutCustomToolForAccount(account, pebblestore.AgentCustomToolDefinition{Name: "lookup_order", Kind: pebblestore.AgentCustomToolKindClient, Description: "x", InputSchema: lookupOrderSchema}); err != nil {
		t.Fatal(err)
	}
	if _, _, _, err := s.agents.UpsertForAccount(account, agentruntime.UpsertInput{Name: "frontdesk", Mode: agentruntime.ModeSubagent, Enabled: pebblestore.BoolPtr(true), Prompt: "x",
		ToolContract: &pebblestore.AgentToolContract{Preset: "custom", Tools: map[string]pebblestore.AgentToolConfig{"lookup_order": {Enabled: pebblestore.BoolPtr(true)}}}}); err != nil {
		t.Fatal(err)
	}
	r := requestWithTestPrincipalForAccount(httptest.NewRequest(http.MethodPost, "/v3/auth/tokens", strings.NewReader(`{"name":"gw","agent_name":"frontdesk","messages_per_minute":3,"sessions_per_hour":2}`)), actor.UserID, account)
	w := httptest.NewRecorder()
	s.handleAuthTokens(w, r)
	var minted struct {
		Token  string                        `json:"token"`
		Record pebblestore.ScopedTokenRecord `json:"record"`
	}
	if err := json.Unmarshal(w.Body.Bytes(), &minted); err != nil || w.Code != http.StatusOK || minted.Record.MessagesPerMinute != 3 || minted.Record.SessionsPerHour != 2 {
		t.Fatalf("mint: %d %s", w.Code, w.Body.String())
	}
	snapshot := pebblestore.SessionSnapshot{ID: "desk", UserID: actor.UserID, AccountScopeID: account, Title: "desk", Mode: "auto", Metadata: map[string]any{"agent_name": "frontdesk"}}
	if _, err := s.sessions.ApplySessionMutation(sessionruntime.SessionMutationInput{Kind: sessionruntime.SessionMutationCreateSession, SessionID: "desk", UserID: actor.UserID, AccountScopeID: account, Session: &snapshot, IdempotencyKey: "desk", PayloadHash: "desk", NowUnixMs: 1000}); err != nil {
		t.Fatal(err)
	}
	sdk := s.ContainerSDKHandler()
	do := func(method, path, body string) *httptest.ResponseRecorder {
		req := httptest.NewRequest(method, "http://127.0.0.1:7783"+path, strings.NewReader(body))
		req.RemoteAddr = "192.0.2.1:40000"
		req.Header.Set("Authorization", "Bearer "+minted.Token)
		req.Header.Set("Content-Type", "application/json")
		rec := httptest.NewRecorder()
		sdk.ServeHTTP(rec, req)
		return rec
	}
	for i := 0; i < 3; i++ {
		if got := do("POST", "/v3/sessions", `{"agent_name":"frontdesk","client_request_id":"s`+strconv.Itoa(i)+`"}`); (got.Code == http.StatusTooManyRequests) != (i == 2) {
			t.Fatalf("session create %d: %d", i, got.Code)
		}
	}
	for i := 0; i < 4; i++ {
		got := do("POST", "/v3/sessions/desk/messages", `{"role":"user","content":"hi","client_request_id":"m`+strconv.Itoa(i)+`"}`)
		if (got.Code == http.StatusTooManyRequests) != (i == 3) {
			t.Fatalf("message %d: %d %s", i, got.Code, got.Body.String())
		}
		if i == 3 && got.Header().Get("Retry-After") == "" {
			t.Fatal("429 without Retry-After")
		}
	}
	for i := 0; i < 20; i++ {
		if got := do("GET", "/v3/sessions/desk", ""); got.Code == http.StatusTooManyRequests {
			t.Fatal("reads must not be rate limited")
		}
	}
	for _, body := range []string{`{"name":"gw","agent_name":"frontdesk","messages_per_minute":-1}`, `{"name":"gw","agent_name":"frontdesk","sessions_per_hour":1000000}`} {
		w := httptest.NewRecorder()
		s.handleAuthTokens(w, requestWithTestPrincipalForAccount(httptest.NewRequest(http.MethodPost, "/v3/auth/tokens", strings.NewReader(body)), actor.UserID, account))
		if w.Code != http.StatusBadRequest {
			t.Fatalf("out-of-range limits accepted: %s %d", body, w.Code)
		}
	}
}

// Purpose: the daily spend cap is the last cost control, so a session token
// must not be able to raise or disable it; a gateway may read today's spend.
func TestUsageLimitChangesNeedUsageWrite(t *testing.T) {
	server, _, _, _, _ := newRoutedSessionTestServerWithSwarmStore(t)
	handler := server.Handler()
	do := func(method string, scopes []string) int {
		req := withTestPrincipal(httptest.NewRequest(method, "/v3/sessions:usage-limits", strings.NewReader(`{"daily_cost_limit_usd":100000,"enabled":false}`)))
		req.Header.Set("Content-Type", "application/json")
		if scopes != nil {
			req = requestWithScopedToken(req, &pebblestore.ScopedTokenRecord{Scopes: scopes})
		}
		rec := httptest.NewRecorder()
		handler.ServeHTTP(rec, req)
		return rec.Code
	}
	if got := do(http.MethodPost, []string{"sessions:read", "sessions:write"}); got != http.StatusForbidden {
		t.Fatalf("session token changed the spend cap: %d", got)
	}
	if got := do(http.MethodGet, []string{"sessions:read"}); got != http.StatusOK {
		t.Fatalf("session token could not read spend: %d", got)
	}
	if got := do(http.MethodPost, []string{"usage:write"}); got != http.StatusOK {
		t.Fatalf("usage:write could not change the cap: %d", got)
	}
}
