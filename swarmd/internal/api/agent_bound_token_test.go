package api

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"path/filepath"
	"strings"
	"testing"

	agentruntime "swarm/packages/swarmd/internal/agent"
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
// Boundary: Server.handleAuthTokens agent-bound branch and agentBoundMaxLifetime.
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
