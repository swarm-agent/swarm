package api

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"path/filepath"
	"strings"
	"testing"

	agentruntime "swarm/packages/swarmd/internal/agent"
	"swarm/packages/swarmd/internal/permission"
	sessionruntime "swarm/packages/swarmd/internal/session"
	pebblestore "swarm/packages/swarmd/internal/store/pebble"
)

// Purpose: a person can take a conversation from the agent, reply, and hand it
// back, all as durable session state. While taken over, a visitor message is
// kept but starts no model run (dispatch_blocked, human_takeover); a person's
// reply is refused until the takeover starts, then stored as an assistant
// message with source "operator"; after hand-back a visitor message runs again.
// Threat: the bot answering over a person, operator text forged outside a
// takeover, or a takeover that silently drops visitor messages.
// Boundary: handleSessionV3Takeover, handleSessionV3OperatorMessages,
// sessionsV3PrimaryRunIntentStatus (via acceptSessionsV3Message).
// Narrowest layer: the handlers and message admission on a real V3 session;
// the returned enqueue job shows whether a run would start, so no provider runs.
func TestSessionTakeoverHoldsRunsAndRecordsOperatorReplies(t *testing.T) {
	server, _, _, _, _ := newRoutedSessionTestServerWithSwarmStore(t)
	// Attach an executor so admission reports the run job it would enqueue; the
	// test never enqueues it, so no run starts.
	server.v3SessionExecutor = newSessionV3Executor(server)
	principal := testPrincipal()
	created := createSessionsV3PrimaryTestSessionWithWorkspaceAndPreference(t, server, "takeover-create", "takeover", t.TempDir(), pebblestore.ModelPreference{Provider: "test-provider", Model: "test-model", Thinking: "medium"})
	id := created.ID

	call := func(handler func(http.ResponseWriter, *http.Request), method, path, body string) (int, map[string]any) {
		w := httptest.NewRecorder()
		handler(w, httptest.NewRequest(method, path, strings.NewReader(body)))
		var out map[string]any
		_ = json.Unmarshal(w.Body.Bytes(), &out)
		return w.Code, out
	}
	takeover := func(w http.ResponseWriter, r *http.Request) { server.handleSessionV3Takeover(w, r, principal, id) }
	operator := func(w http.ResponseWriter, r *http.Request) {
		server.handleSessionV3OperatorMessages(w, r, principal, id)
	}
	visitor := func(requestID string) (string, string, bool) {
		result, job, err := server.acceptSessionsV3Message(principal, id, sessionsV3MessageRequest{ClientRequestID: requestID, Role: "user", Content: "hello?"})
		if err != nil || result.RunIntent == nil {
			t.Fatalf("visitor message %s: %v %+v", requestID, err, result.RunIntent)
		}
		return result.RunIntent.Status, result.RunIntent.BlockedReason, job != nil
	}

	if code, _ := call(operator, http.MethodPost, "/", `{"content":"Hi, Roy here","client_request_id":"op-early"}`); code != http.StatusConflict {
		t.Fatalf("operator reply before takeover: %d, want 409", code)
	}
	if code, _ := call(takeover, http.MethodPost, "/", `{"action":"pause","client_request_id":"bad"}`); code != http.StatusBadRequest {
		t.Fatalf("unknown action accepted: %d", code)
	}
	code, out := call(takeover, http.MethodPost, "/", `{"action":"start","client_request_id":"take-1"}`)
	if state, _ := out["takeover"].(map[string]any); code != http.StatusOK || state["state"] != "human" || state["by"] != principal.UserID {
		t.Fatalf("start takeover: %d %v", code, out)
	}
	if session, ok, err := server.sessions.GetSession(id); err != nil || !ok || !sessionTakeoverActive(session) {
		t.Fatalf("takeover not durable on the session: %v %v", ok, err)
	}

	status, reason, enqueued := visitor("visitor-during")
	if status != sessionruntime.RunIntentDispatchBlocked || reason != sessionTakeoverBlockedReason || enqueued {
		t.Fatalf("visitor message during takeover: status=%s reason=%s enqueued=%v", status, reason, enqueued)
	}

	code, out = call(operator, http.MethodPost, "/", `{"content":"Hi, Roy here. Happy to help.","client_request_id":"op-1"}`)
	if code != http.StatusOK {
		t.Fatalf("operator reply: %d %v", code, out)
	}
	messages, err := server.sessions.ListSessionMessages(id, 0, 50)
	if err != nil {
		t.Fatal(err)
	}
	var reply *pebblestore.MessageSnapshot
	for i := range messages {
		if messages[i].Metadata["source"] == "operator" {
			reply = &messages[i]
		}
	}
	if reply == nil || reply.Role != "assistant" || reply.Content != "Hi, Roy here. Happy to help." || reply.Metadata["operator_user_id"] != principal.UserID {
		t.Fatalf("operator reply not stored as an operator assistant message: %+v", reply)
	}
	// Retrying the same reply does not duplicate it.
	if code, _ := call(operator, http.MethodPost, "/", `{"content":"Hi, Roy here. Happy to help.","client_request_id":"op-1"}`); code != http.StatusOK {
		t.Fatalf("operator reply retry: %d", code)
	}
	if again, _ := server.sessions.ListSessionMessages(id, 0, 50); len(again) != len(messages) {
		t.Fatalf("retried operator reply duplicated: %d -> %d messages", len(messages), len(again))
	}

	// After hand-back the agent sees the person's reply as part of the
	// conversation (an assistant turn in its provider input).
	foundInInput := false
	for _, item := range sessionsV3ProviderInputWithOptions(messages, sessionsV3ProviderInputOptions{}) {
		if raw, _ := json.Marshal(item); item["role"] == "assistant" && strings.Contains(string(raw), "Happy to help.") {
			foundInInput = true
		}
	}
	if !foundInInput {
		t.Fatal("the operator reply is missing from the agent's provider input")
	}

	if code, out := call(takeover, http.MethodPost, "/", `{"action":"end","client_request_id":"hand-back"}`); code != http.StatusOK {
		t.Fatalf("hand back: %d %v", code, out)
	}
	if status, _, enqueued := visitor("visitor-after"); status != sessionruntime.RunIntentPendingExecutor || !enqueued {
		t.Fatalf("visitor message after hand-back: status=%s enqueued=%v", status, enqueued)
	}
	if code, _ := call(operator, http.MethodPost, "/", `{"content":"late","client_request_id":"op-late"}`); code != http.StatusConflict {
		t.Fatalf("operator reply after hand-back: %d, want 409", code)
	}
}

// Purpose: the human-takeover key for a sealed agent (an agent-bound token
// minted with operator) can only read that agent's conversations, take them
// over or hand them back, and reply as a person; it cannot post as the visitor,
// create or list sessions, or answer tool calls. The gateway key cannot reach
// takeover or operator replies at all.
// Threat: a leaked hub credential steering the bot or impersonating visitors,
// or a leaked gateway key letting the public post "staff" replies.
// Boundary: agentBoundRequestError / agentOperatorRouteError, handleAuthTokens.
// Narrowest layer: the gate with real minted tokens, a real sealed agent and
// seeded sessions; handlers are not run.
func TestOperatorTokenIsLimitedToTakeover(t *testing.T) {
	s, _, _, cleanup := setupScopedAuthTestServer(t)
	defer cleanup()
	db, err := pebblestore.Open(filepath.Join(t.TempDir(), "operator-token"))
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
	for _, seed := range []struct{ id, agent string }{{"desk", "frontdesk"}, {"owner", "swarm"}} {
		snapshot := pebblestore.SessionSnapshot{ID: seed.id, UserID: actor.UserID, AccountScopeID: account, Title: seed.id, Mode: "auto", Metadata: map[string]any{"agent_name": seed.agent}}
		if _, err := s.sessions.ApplySessionMutation(sessionruntime.SessionMutationInput{Kind: sessionruntime.SessionMutationCreateSession, SessionID: seed.id, UserID: actor.UserID, AccountScopeID: account, Session: &snapshot, IdempotencyKey: seed.id, PayloadHash: seed.id, NowUnixMs: 1000}); err != nil {
			t.Fatal(err)
		}
	}
	mint := func(body string) pebblestore.ScopedTokenRecord {
		w := httptest.NewRecorder()
		s.handleAuthTokens(w, requestWithTestPrincipalForAccount(httptest.NewRequest(http.MethodPost, "/v3/auth/tokens", strings.NewReader(body)), actor.UserID, account))
		var out struct {
			Record pebblestore.ScopedTokenRecord `json:"record"`
		}
		if err := json.Unmarshal(w.Body.Bytes(), &out); err != nil || w.Code != http.StatusOK {
			t.Fatalf("mint %s: %d %s", body, w.Code, w.Body.String())
		}
		return out.Record
	}
	operator := mint(`{"name":"hub","agent_name":"frontdesk","operator":true,"expires_in_seconds":3600}`)
	gateway := mint(`{"name":"gw","agent_name":"frontdesk","expires_in_seconds":3600}`)
	if !operator.AgentOperator || gateway.AgentOperator {
		t.Fatalf("operator flag: operator=%v gateway=%v", operator.AgentOperator, gateway.AgentOperator)
	}
	allowed := func(rec pebblestore.ScopedTokenRecord, method, path, body string) bool {
		return s.agentBoundRequestError(httptest.NewRequest(method, path, strings.NewReader(body)), &rec) == nil
	}
	for _, tc := range []struct{ method, path, body string }{
		{"GET", "/v3/sessions/desk", ""},
		{"GET", "/v3/sessions/desk/takeover", ""},
		{"POST", "/v3/sessions/desk/takeover", `{"action":"start","client_request_id":"t"}`},
		{"POST", "/v3/sessions/desk/operator-messages", `{"content":"hi","client_request_id":"o"}`},
	} {
		if !allowed(operator, tc.method, tc.path, tc.body) {
			t.Fatalf("operator token refused %s %s", tc.method, tc.path)
		}
	}
	for _, tc := range []struct{ method, path, body string }{
		{"GET", "/v3/sessions", ""},
		{"POST", "/v3/sessions", `{"agent_name":"frontdesk"}`},
		{"POST", "/v3/sessions/desk/messages", `{"role":"user","content":"I am the visitor","client_request_id":"v"}`},
		{"POST", "/v3/sessions/desk/run/stop", `{}`},
		{"POST", "/v3/sessions/desk/permissions/p1/resolve", `{"action":"allow_once"}`},
		{"POST", "/v3/sessions/desk/agent", `{"agent_name":"swarm"}`},
		{"GET", "/v3/sessions/owner", ""},
		{"POST", "/v3/sessions/owner/takeover", `{"action":"start","client_request_id":"t"}`},
		{"POST", "/v3/sessions/owner/operator-messages", `{"content":"hi","client_request_id":"o"}`},
	} {
		if allowed(operator, tc.method, tc.path, tc.body) {
			t.Fatalf("operator token allowed %s %s", tc.method, tc.path)
		}
	}
	for _, tc := range []struct{ method, path, body string }{
		{"GET", "/v3/sessions/desk/takeover", ""},
		{"POST", "/v3/sessions/desk/takeover", `{"action":"start","client_request_id":"t"}`},
		{"POST", "/v3/sessions/desk/operator-messages", `{"content":"staff reply","client_request_id":"o"}`},
	} {
		if allowed(gateway, tc.method, tc.path, tc.body) {
			t.Fatalf("gateway token allowed %s %s", tc.method, tc.path)
		}
	}
}
