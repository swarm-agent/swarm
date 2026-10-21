package api

import (
	"bytes"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"net/url"
	"path/filepath"
	"strings"
	"testing"

	agentruntime "swarm/packages/swarmd/internal/agent"
	"swarm/packages/swarmd/internal/permission"
	sessionruntime "swarm/packages/swarmd/internal/session"
	pebblestore "swarm/packages/swarmd/internal/store/pebble"
)

type gatewayTestFixture struct {
	server  *Server
	handler http.Handler
	account string
	user    string
	db      *pebblestore.Store
}

// newGatewayTestFixture is a daemon with two sealed agents (frontdesk and
// helpdesk) reached through the container SDK listener with real tokens.
func newGatewayTestFixture(t *testing.T) gatewayTestFixture {
	t.Helper()
	s, _, _, cleanup := setupScopedAuthTestServer(t)
	t.Cleanup(cleanup)
	db, err := pebblestore.Open(filepath.Join(t.TempDir(), "gateway"))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = db.Close() })
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
	for _, name := range []string{"frontdesk", "helpdesk"} {
		if _, _, _, err := s.agents.UpsertForAccount(account, agentruntime.UpsertInput{Name: name, Mode: agentruntime.ModeSubagent, Enabled: pebblestore.BoolPtr(true), Prompt: "x",
			ToolContract: &pebblestore.AgentToolContract{Preset: "custom", Tools: map[string]pebblestore.AgentToolConfig{"lookup_order": {Enabled: pebblestore.BoolPtr(true)}}}}); err != nil {
			t.Fatal(err)
		}
	}
	return gatewayTestFixture{server: s, handler: s.ContainerSDKHandler(), account: account, user: actor.UserID, db: db}
}

func (f gatewayTestFixture) mint(t *testing.T, body string) string {
	t.Helper()
	w := httptest.NewRecorder()
	f.server.handleAuthTokens(w, requestWithTestPrincipalForAccount(httptest.NewRequest(http.MethodPost, "/v3/auth/tokens", strings.NewReader(body)), f.user, f.account))
	var out struct {
		Token string `json:"token"`
	}
	if err := json.Unmarshal(w.Body.Bytes(), &out); err != nil || w.Code != http.StatusOK || out.Token == "" {
		t.Fatalf("mint %s: %d %s", body, w.Code, w.Body.String())
	}
	return out.Token
}

func (f gatewayTestFixture) seed(t *testing.T, id, agent string, updatedAt int64) {
	t.Helper()
	snapshot := pebblestore.SessionSnapshot{ID: id, UserID: f.user, AccountScopeID: f.account, Title: id, Mode: "auto", Metadata: map[string]any{"agent_name": agent}, CreatedAt: updatedAt, UpdatedAt: updatedAt}
	if _, err := f.server.sessions.ApplySessionMutation(sessionruntime.SessionMutationInput{Kind: sessionruntime.SessionMutationCreateSession, SessionID: id, UserID: f.user, AccountScopeID: f.account, Session: &snapshot, IdempotencyKey: id, PayloadHash: id, NowUnixMs: updatedAt}); err != nil {
		t.Fatal(err)
	}
}

func (f gatewayTestFixture) do(t *testing.T, token, method, path, body string) (int, map[string]any) {
	t.Helper()
	var reader *bytes.Reader
	if body != "" {
		reader = bytes.NewReader([]byte(body))
	} else {
		reader = bytes.NewReader(nil)
	}
	req := httptest.NewRequest(method, path, reader)
	req.Header.Set("Authorization", "Bearer "+token)
	if body != "" {
		req.Header.Set("Content-Type", "application/json")
	}
	w := httptest.NewRecorder()
	f.handler.ServeHTTP(w, req)
	var out map[string]any
	_ = json.Unmarshal(w.Body.Bytes(), &out)
	return w.Code, out
}

func listedSessionIDs(out map[string]any) []string {
	var ids []string
	items, _ := out["sessions"].([]any)
	for _, item := range items {
		if m, ok := item.(map[string]any); ok {
			ids = append(ids, m["id"].(string))
		}
	}
	return ids
}

func feedSessions(out map[string]any) map[string][]string {
	bySession := map[string][]string{}
	items, _ := out["events"].([]any)
	for _, item := range items {
		m := item.(map[string]any)
		bySession[m["session_id"].(string)] = append(bySession[m["session_id"].(string)], m["kind"].(string))
	}
	return bySession
}

// Purpose: a gateway token manages only its own agent's conversations. It can
// list them (paged, filtered by state), wait on them, end them and hard delete
// them; another agent's gateway token gets 403 for every one of these on them
// and never sees them in its listing or event feed, and the operator token for
// the same agent may read, wait and list but neither end nor delete. Ending
// is durable: the session refuses new messages and reports session_ended, and
// a repeated end changes nothing. Deleting removes the session and its feed
// entries while other sessions stay. Threat: a leaked or buggy gateway token
// reading, closing or erasing another agent's (or another gateway's agent's)
// visitors' conversations. Boundary: agentBoundRequestError (via
// gateAgentBoundToken in ContainerSDKHandler), handleSessionsV3AgentSessions,
// handleSessionV3End, handleSessionV3Wait, handleSessionsV3AgentEvents and the
// existing hard delete. Narrowest layer: the container SDK listener with real
// minted tokens, since the scope is the gate plus the handlers together.
func TestAgentBoundGatewayLifecycleIsScopedToItsAgent(t *testing.T) {
	f := newGatewayTestFixture(t)
	front := f.mint(t, `{"name":"front","agent_name":"frontdesk","expires_in_seconds":3600}`)
	help := f.mint(t, `{"name":"help","agent_name":"helpdesk","expires_in_seconds":3600}`)
	operator := f.mint(t, `{"name":"hub","agent_name":"frontdesk","operator":true,"expires_in_seconds":3600}`)
	f.seed(t, "desk-a", "frontdesk", 1_000)
	f.seed(t, "desk-b", "helpdesk", 2_000)
	f.seed(t, "desk-c", "frontdesk", 3_000)

	for _, req := range []struct{ method, path, body string }{
		{http.MethodGet, "/v3/sessions/desk-a/wait", ""},
		{http.MethodPost, "/v3/sessions/desk-a/end", `{"client_request_id":"x"}`},
		{http.MethodDelete, "/v3/sessions/desk-a", ""},
		{http.MethodGet, "/v3/sessions:agent?agent=frontdesk", ""},
		{http.MethodGet, "/v3/sessions:agent-events?agent=frontdesk", ""},
	} {
		if code, out := f.do(t, help, req.method, req.path, req.body); code != http.StatusForbidden {
			t.Fatalf("helpdesk token %s %s: %d %v", req.method, req.path, code, out)
		}
	}
	if code, _ := f.do(t, front, http.MethodGet, "/v3/sessions", ""); code != http.StatusForbidden {
		t.Fatalf("the general session listing opened to a gateway token: %d", code)
	}
	if code, out := f.do(t, help, http.MethodGet, "/v3/sessions:agent", ""); code != http.StatusOK || strings.Join(listedSessionIDs(out), ",") != "desk-b" {
		t.Fatalf("helpdesk listing: %d %v", code, out)
	}
	code, out := f.do(t, front, http.MethodGet, "/v3/sessions:agent?limit=1", "")
	if code != http.StatusOK || strings.Join(listedSessionIDs(out), ",") != "desk-c" || out["next_cursor"] == "" {
		t.Fatalf("frontdesk first page: %d %v", code, out)
	}
	code, out = f.do(t, front, http.MethodGet, "/v3/sessions:agent?limit=1&cursor="+url.QueryEscape(out["next_cursor"].(string)), "")
	if code != http.StatusOK || strings.Join(listedSessionIDs(out), ",") != "desk-a" || out["next_cursor"] != "" {
		t.Fatalf("frontdesk second page: %d %v", code, out)
	}

	if code, out := f.do(t, front, http.MethodGet, "/v3/sessions:agent-events", ""); code != http.StatusOK {
		t.Fatalf("frontdesk feed: %d %v", code, out)
	} else if got := feedSessions(out); len(got) != 2 || len(got["desk-a"]) != 1 || len(got["desk-c"]) != 1 || got["desk-a"][0] != agentEventSessionCreated {
		t.Fatalf("frontdesk feed should hold only its two sessions' creations: %v", got)
	}
	if code, out := f.do(t, help, http.MethodGet, "/v3/sessions:agent-events", ""); code != http.StatusOK {
		t.Fatalf("helpdesk feed: %d", code)
	} else if got := feedSessions(out); len(got) != 1 || len(got["desk-b"]) != 1 {
		t.Fatalf("helpdesk feed: %v", got)
	}

	for _, req := range []struct{ method, path, body string }{
		{http.MethodPost, "/v3/sessions/desk-a/end", `{"client_request_id":"op-end"}`},
		{http.MethodDelete, "/v3/sessions/desk-a", ""},
	} {
		if code, _ := f.do(t, operator, req.method, req.path, req.body); code != http.StatusForbidden {
			t.Fatalf("operator token %s %s: %d", req.method, req.path, code)
		}
	}
	for _, path := range []string{"/v3/sessions/desk-a/wait", "/v3/sessions:agent", "/v3/sessions:agent-events"} {
		if code, out := f.do(t, operator, http.MethodGet, path, ""); code != http.StatusOK {
			t.Fatalf("operator token GET %s: %d %v", path, code, out)
		}
	}

	code, out = f.do(t, front, http.MethodPost, "/v3/sessions/desk-a/end", `{"client_request_id":"end-1","reason":"visitor left"}`)
	ended, _ := out["ended"].(map[string]any)
	if code != http.StatusOK || ended["at"] == nil || ended["reason"] != "visitor left" || out["already_ended"] == true {
		t.Fatalf("end: %d %v", code, out)
	}
	if code, out := f.do(t, front, http.MethodPost, "/v3/sessions/desk-a/end", `{"client_request_id":"end-2"}`); code != http.StatusOK || out["already_ended"] != true {
		t.Fatalf("second end: %d %v", code, out)
	}
	if code, out := f.do(t, front, http.MethodPost, "/v3/sessions/desk-a/messages", `{"role":"user","content":"still there?","client_request_id":"late"}`); code != http.StatusConflict {
		t.Fatalf("message to an ended session: %d %v", code, out)
	}
	if code, out := f.do(t, front, http.MethodGet, "/v3/sessions/desk-a/wait?after=0", ""); code != http.StatusOK || out["ended"] != true {
		t.Fatalf("wait on an ended session: %d %v", code, out)
	} else if reasons, _ := out["reasons"].([]any); len(reasons) != 1 || reasons[0] != sessionWaitSessionEnded {
		t.Fatalf("wait on an ended session reasons: %v", out["reasons"])
	}
	if code, out := f.do(t, front, http.MethodGet, "/v3/sessions:agent?state=ended", ""); code != http.StatusOK || strings.Join(listedSessionIDs(out), ",") != "desk-a" {
		t.Fatalf("ended listing: %d %v", code, out)
	}
	if code, out := f.do(t, front, http.MethodGet, "/v3/sessions:agent?state=active", ""); code != http.StatusOK || strings.Join(listedSessionIDs(out), ",") != "desk-c" {
		t.Fatalf("active listing: %d %v", code, out)
	}
	if _, out := f.do(t, front, http.MethodGet, "/v3/sessions:agent-events?kind=session.ended", ""); strings.Join(feedSessions(out)["desk-a"], ",") != agentEventSessionEnded {
		t.Fatalf("feed should report the end: %v", out)
	}

	if code, out := f.do(t, front, http.MethodDelete, "/v3/sessions/desk-a", ""); code != http.StatusOK || out["deleted"] != true {
		t.Fatalf("delete: %d %v", code, out)
	}
	if _, found, _ := f.server.sessions.GetSession("desk-a"); found {
		t.Fatal("deleted session still exists")
	}
	if messages, err := f.server.sessions.ListSessionMessages("desk-a", 0, 10); err != nil || len(messages) != 0 {
		t.Fatalf("deleted session kept messages: %v %d", err, len(messages))
	}
	for _, id := range []string{"desk-b", "desk-c"} {
		if _, found, _ := f.server.sessions.GetSession(id); !found {
			t.Fatalf("deleting desk-a removed %s", id)
		}
	}
	if code, _ := f.do(t, help, http.MethodDelete, "/v3/sessions/desk-c", ""); code != http.StatusForbidden {
		t.Fatalf("helpdesk deleted frontdesk's session: %d", code)
	}
	if _, out := f.do(t, front, http.MethodGet, "/v3/sessions:agent-events", ""); len(feedSessions(out)["desk-a"]) != 0 {
		t.Fatalf("a deleted session's entries stayed in the feed: %v", feedSessions(out))
	}
}
