package api

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"path/filepath"
	"strings"
	"testing"
	"time"

	agentruntime "swarm/packages/swarmd/internal/agent"
	"swarm/packages/swarmd/internal/permission"
	sessionruntime "swarm/packages/swarmd/internal/session"
	pebblestore "swarm/packages/swarmd/internal/store/pebble"
)

// lookupOrderSchema is the sealed agent's one client tool in these tests.
var lookupOrderSchema = map[string]any{
	"type": "object",
	"properties": map[string]any{
		"order_id": map[string]any{"type": "string", "pattern": "^[A-Z0-9]{6}$"},
	},
	"required":             []any{"order_id"},
	"additionalProperties": false,
}

// Purpose: a gateway token lives on an internet-facing host, so it must always
// expire, within 30 days. A missing, zero, negative or longer lifetime is
// refused at mint time with nothing created; 30 days exactly is accepted.
// Boundary: Server.handleAuthTokens agent-bound branch and security.AgentBoundMaxLifetime.
// Narrowest layer: the owner-only mint handler with a real sealed agent.
func TestAgentBoundTokenMustExpireWithinThirtyDays(t *testing.T) {
	s, _, _, cleanup := setupScopedAuthTestServer(t)
	defer cleanup()
	db, err := pebblestore.Open(filepath.Join(t.TempDir(), "agent-bound-expiry"))
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	events, err := pebblestore.NewEventLog(db)
	if err != nil {
		t.Fatal(err)
	}
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
	mint := func(body string) (int, pebblestore.ScopedTokenRecord) {
		w := httptest.NewRecorder()
		s.handleAuthTokens(w, requestWithTestPrincipalForAccount(httptest.NewRequest(http.MethodPost, "/v3/auth/tokens", strings.NewReader(body)), actor.UserID, account))
		var out struct {
			Record pebblestore.ScopedTokenRecord `json:"record"`
		}
		_ = json.Unmarshal(w.Body.Bytes(), &out)
		return w.Code, out.Record
	}
	before, err := s.security.ListScopedTokens(account)
	if err != nil {
		t.Fatal(err)
	}
	for _, body := range []string{
		`{"name":"gw","agent_name":"frontdesk"}`,
		`{"name":"gw","agent_name":"frontdesk","expires_in_seconds":0}`,
		`{"name":"gw","agent_name":"frontdesk","expires_in_seconds":-60}`,
		`{"name":"gw","agent_name":"frontdesk","expires_in_seconds":2592001}`,
	} {
		if code, _ := mint(body); code != http.StatusBadRequest {
			t.Fatalf("minted a gateway token without a bounded expiry: %s -> %d", body, code)
		}
	}
	if after, _ := s.security.ListScopedTokens(account); len(after) != len(before) {
		t.Fatalf("a refused mint stored a token: %d -> %d", len(before), len(after))
	}
	code, record := mint(`{"name":"gw","agent_name":"frontdesk","expires_in_seconds":2592000}`)
	if code != http.StatusOK || record.ExpiresAt <= 0 {
		t.Fatalf("30-day gateway token: %d expires_at=%d", code, record.ExpiresAt)
	}
}

// Purpose: an agent-bound token may send only the session-create fields a
// gateway needs (the SDK's sessions.create shape); every other field, including
// ones added to the create request later, is refused before the handler runs.
// Threat: worktree, session ID, swarm/target routing or workspace naming fields
// steering a public session into owner state. Boundary: checkAgentBoundCreate
// and agentBoundCreateFields. Narrowest layer: the gate function itself.
func TestAgentBoundCreateAllowsOnlyReviewedFields(t *testing.T) {
	check := func(body string) error {
		return checkAgentBoundCreate(httptest.NewRequest(http.MethodPost, "/v3/sessions", strings.NewReader(body)), "frontdesk")
	}
	if err := check(`{"agent_name":"frontdesk","client_request_id":"r1","idempotency_key":"k1","title":"Visitor","mode":"auto","workspace_path":"/project","workspace_id":"w1"}`); err != nil {
		t.Fatalf("the SDK create shape was refused: %v", err)
	}
	for _, field := range []string{"project_id", "parent_session_id", "model_profile", "metadata", "preference",
		"session_id", "purpose", "workspace_name", "workspace_binding_id", "swarm_id", "target_kind",
		"target_relationship", "host_workspace_path", "runtime_workspace_path", "managed_worktree_requested",
		"worktree_mode", "worktree_use_current_branch", "worktree_base_branch", "worktree_branch_name",
		"worktree_existing_path", "a_future_field"} {
		err := check(`{"agent_name":"frontdesk","` + field + `":"x"}`)
		if err == nil || !strings.Contains(err.Error(), field) {
			t.Fatalf("field %q was not refused: %v", field, err)
		}
	}
	if err := check(`{"agent_name":"swarm"}`); err == nil {
		t.Fatal("create for another agent was allowed")
	}
}

// Purpose: an agent's concurrent model runs stay within the gateway token's
// limit. Running sessions and live reservations both hold a slot; a message to a
// session that already holds one queues and is admitted; a reservation frees its
// slot when it expires or its run starts. Threat: a burst of visitors (or a
// leaked token) starting unbounded parallel runs and cost. Boundary:
// agentBoundConcurrency.admit. Narrowest layer: the admission function with a
// fixed clock.
func TestAgentBoundConcurrencyAdmission(t *testing.T) {
	c := &agentBoundConcurrency{reserved: map[string]map[string]time.Time{}}
	now := time.Unix(1000, 0)
	none := map[string]bool{}
	if !c.admit("tok", "a", none, 2, now) || !c.admit("tok", "b", none, 2, now) {
		t.Fatal("first two conversations were refused")
	}
	if c.admit("tok", "c", none, 2, now) {
		t.Fatal("a third concurrent run was admitted over a limit of 2")
	}
	if !c.admit("tok", "a", none, 2, now) {
		t.Fatal("a follow-up message to a session that already holds a slot was refused")
	}
	if c.admit("tok", "c", map[string]bool{"x": true}, 2, now) {
		t.Fatal("a running session of the agent did not count against the limit")
	}
	// Session a's run started: it now counts once (running), not twice.
	if c.admit("tok", "c", map[string]bool{"a": true}, 2, now) {
		t.Fatal("running a plus reserved b should fill a limit of 2")
	}
	if !c.admit("tok", "c", map[string]bool{"a": true}, 3, now) {
		t.Fatal("a running session was counted twice")
	}
	later := now.Add(agentBoundRunReservation + time.Second)
	if !c.admit("tok", "d", none, 1, later) {
		t.Fatal("expired reservations still held slots")
	}
	if !c.admit("other", "e", none, 1, now) {
		t.Fatal("another token's reservations counted against this token")
	}
}

// Purpose: the limit is enforced at the gateway gate, before the handler, and is
// set when minting (0..64). With max_concurrent_runs 1, a message to a second
// conversation is refused with 429 and Retry-After while the first holds the
// slot; an out-of-range limit is refused at mint time. Boundary:
// Server.gateAgentBoundToken, handleAuthTokens. Narrowest layer: the container SDK
// handler with a real minted token and seeded sessions.
func TestAgentBoundTokenConcurrentRunLimit(t *testing.T) {
	s, _, _, cleanup := setupScopedAuthTestServer(t)
	defer cleanup()
	db, err := pebblestore.Open(filepath.Join(t.TempDir(), "agent-bound-concurrency"))
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
	mint := func(body string) (int, string, pebblestore.ScopedTokenRecord) {
		w := httptest.NewRecorder()
		s.handleAuthTokens(w, requestWithTestPrincipalForAccount(httptest.NewRequest(http.MethodPost, "/v3/auth/tokens", strings.NewReader(body)), actor.UserID, account))
		var out struct {
			Token  string                        `json:"token"`
			Record pebblestore.ScopedTokenRecord `json:"record"`
		}
		_ = json.Unmarshal(w.Body.Bytes(), &out)
		return w.Code, out.Token, out.Record
	}
	if code, _, _ := mint(`{"name":"gw","agent_name":"frontdesk","expires_in_seconds":3600,"max_concurrent_runs":65}`); code != http.StatusBadRequest {
		t.Fatalf("max_concurrent_runs 65 accepted: %d", code)
	}
	code, token, record := mint(`{"name":"gw","agent_name":"frontdesk","expires_in_seconds":3600,"max_concurrent_runs":1}`)
	if code != http.StatusOK || record.MaxConcurrentRuns != 1 {
		t.Fatalf("mint: %d %+v", code, record)
	}
	for _, id := range []string{"desk-a", "desk-b"} {
		snapshot := pebblestore.SessionSnapshot{ID: id, UserID: actor.UserID, AccountScopeID: account, Title: id, Mode: "auto", Metadata: map[string]any{"agent_name": "frontdesk"}}
		if _, err := s.sessions.ApplySessionMutation(sessionruntime.SessionMutationInput{Kind: sessionruntime.SessionMutationCreateSession, SessionID: id, UserID: actor.UserID, AccountScopeID: account, Session: &snapshot, IdempotencyKey: id, PayloadHash: id, NowUnixMs: 1000}); err != nil {
			t.Fatal(err)
		}
	}
	if token == "" {
		t.Fatal("no token minted")
	}
	// The gate alone decides admission; calling it directly keeps the handler
	// (and a real model run) out of this test.
	message := func(session, requestID string) (bool, *httptest.ResponseRecorder) {
		req := httptest.NewRequest(http.MethodPost, "/v3/sessions/"+session+"/messages", strings.NewReader(`{"role":"user","content":"hi","client_request_id":"`+requestID+`"}`))
		rec := httptest.NewRecorder()
		return s.gateAgentBoundToken(rec, req, &record), rec
	}
	if ok, got := message("desk-a", "m1"); !ok {
		t.Fatalf("first conversation refused: %d %s", got.Code, got.Body.String())
	}
	if ok, got := message("desk-a", "m2"); !ok {
		t.Fatalf("follow-up in the same conversation refused: %d %s", got.Code, got.Body.String())
	}
	ok, got := message("desk-b", "m3")
	if ok || got.Code != http.StatusTooManyRequests || !strings.Contains(got.Body.String(), "concurrent") || got.Header().Get("Retry-After") == "" {
		t.Fatalf("second conversation over a limit of 1: ok=%v %d %s", ok, got.Code, got.Body.String())
	}
}
