package api

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"regexp"
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
	workersOnly, _ := issue(actor.AccountScopeID, []string{"automations:read"}, time.Hour)
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
	if got := rpc(read, "tools/list", nil); len(got.Result.Tools) != len(controlMCPTools()) {
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
		{"read grant creates", read, "swarm_start_session", map[string]any{"workspace_path": "/project", "prompt": "hi"}, "HTTP 403"},
		{"prompt and plan together", write, "swarm_start_session", map[string]any{"workspace_path": "/project", "prompt": "hi", "plan": map[string]any{"goal": "g", "checkpoints": []any{map[string]any{"title": "t", "acceptance_criteria": []any{"done"}}}}}, "exactly one"},
		{"limits without usage grant", write, "swarm_set_usage_limits", map[string]any{"enabled": false}, "lacks the usage:write permission"},
		{"workspaces without session grant", workersOnly, "swarm_list_workspaces", map[string]any{}, "lacks the sessions:read permission"},
		{"reserved worker id", write, "swarm_get_worker", map[string]any{"worker_id": "token"}, "not a valid id"},
		{"read grant messages", read, "swarm_send_message", map[string]any{"session_id": own.ID, "content": "hi"}, "HTTP 403"},
		{"read grant approves", read, "swarm_resolve_permission", map[string]any{"session_id": own.ID, "permission_id": ownPending.ID, "action": "allow_once"}, "HTTP 403"},
		{"persistent rule", write, "swarm_resolve_permission", map[string]any{"session_id": own.ID, "permission_id": ownPending.ID, "action": "allow_always"}, "must be one of"},
		{"undeclared argument", write, "swarm_resolve_permission", map[string]any{"session_id": own.ID, "permission_id": ownPending.ID, "action": "allow_once", "scope": "global"}, "unknown argument"},
		{"foreign message", write, "swarm_send_message", map[string]any{"session_id": foreign.ID, "content": "forbidden"}, "HTTP 404"},
		{"foreign approval", write, "swarm_resolve_permission", map[string]any{"session_id": foreign.ID, "permission_id": foreignPending.ID, "action": "allow_once"}, "HTTP 404"},
		{"cross-session approval", write, "swarm_resolve_permission", map[string]any{"session_id": own.ID, "permission_id": foreignPending.ID, "action": "allow_once"}, "HTTP 4"},
		{"path traversal", write, "swarm_get_session", map[string]any{"session_id": "../auth/tokens"}, "not a valid id"},
		{"agent model without settings grant", write, "swarm_set_agent_model", map[string]any{"role": "system-coder", "provider": "codex", "model": "m"}, "lacks the settings:write permission"},
		{"models without session grant", workersOnly, "swarm_list_models", map[string]any{}, "lacks the sessions:read permission"},
		{"orchestrator without project", write, "swarm_start_session", map[string]any{"agent": "system-orchestrator", "prompt": "hi"}, "need project_id"},
		{"orchestrator in a workspace", write, "swarm_start_session", map[string]any{"agent": "system-orchestrator", "project_id": "p1", "workspace_path": "/project", "prompt": "hi"}, "not workspace_path"},
		{"project on a regular session", write, "swarm_start_session", map[string]any{"workspace_path": "/project", "project_id": "p1", "prompt": "hi"}, "only to agent system-orchestrator"},
		{"session without workspace", write, "swarm_start_session", map[string]any{"prompt": "hi"}, "workspace_path is required"},
		{"model without provider", write, "swarm_start_session", map[string]any{"workspace_path": "/project", "prompt": "hi", "model": "m"}, "needs provider"},
		{"internal agent", write, "swarm_start_session", map[string]any{"workspace_path": "/project", "prompt": "hi", "agent": "system-router"}, "must be one of"},
		{"answer with deny", write, "swarm_resolve_permission", map[string]any{"session_id": own.ID, "permission_id": ownPending.ID, "action": "deny_once", "answer": "yes"}, "answer needs action allow_once"},
		{"unbounded wait", read, "swarm_get_session", map[string]any{"session_id": own.ID, "wait_seconds": 999}, "at most 45"},
		{"foreign wait", write, "swarm_get_session", map[string]any{"session_id": foreign.ID, "wait_seconds": 5}, "HTTP 404"},
		{"schedule without plan", write, "swarm_create_worker", map[string]any{"name": "w", "instructions": "i", "workspace_path": "/project", "schedule": map[string]any{"kind": "interval", "interval_seconds": 60}}, "go together"},
		{"sub-minute schedule", write, "swarm_create_worker", map[string]any{"name": "w", "instructions": "i", "workspace_path": "/project", "schedule": map[string]any{"kind": "interval", "interval_seconds": 30}, "scheduled_plan": map[string]any{"goal": "g", "checkpoints": []any{map[string]any{"title": "t", "objective": "o", "acceptance_criteria": []any{"done"}}}}}, "at least 60"},
		{"schedule id traversal", write, "swarm_manage_worker", map[string]any{"worker_id": "wkr_1", "action": "enable_schedule", "schedule_id": "../token"}, "not a valid id"},
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
		t.Fatalf("rejected call created a session: %d", len(sessions))
	}
	if workers, err := s.sessions.ListWorkers(actor.AccountScopeID, pebblestore.ListWorkersQuery{Limit: 10}); err == nil && len(workers.Workers) != 0 {
		t.Fatalf("rejected schedule created a worker: %d", len(workers.Workers))
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
	if records, err := sec.ListScopedTokens(actor.AccountScopeID); err != nil || len(records) != 5 {
		t.Fatalf("unauthorized token mutation: count %d err %v", len(records), err)
	}
}

// Requirement: session reads stay bounded for a supervising model. Stored
// tool-result records are reduced to tool, step, truncated arguments and
// output; non-tool and unparseable messages keep their (truncated) text.
// Owner: controlMCPToolMessageSummary. A pure-function test is the narrowest
// layer; the live headless run exercises it through the full stack.
func TestControlMCPToolMessageSummary(t *testing.T) {
	record := `{"tool_name":"bash","step":3,"arguments":"` + strings.Repeat("a", 900) + `","output":"ok","search_index_content":"secret-sized blob"}`
	got, ok := controlMCPToolMessageSummary(map[string]any{"role": "tool"}, record)
	if !ok || got["tool_name"] != "bash" || got["output"] != "ok" || got["search_index_content"] != nil {
		t.Fatalf("unexpected summary: %v", got)
	}
	if args, _ := got["arguments"].(string); !strings.Contains(args, "[truncated 600 characters]") {
		t.Fatalf("arguments not bounded: %d", len(args))
	}
	for _, tc := range []struct{ role, content string }{{"assistant", record}, {"tool", "not json"}, {"tool", `{"unrelated":1}`}} {
		if _, ok := controlMCPToolMessageSummary(map[string]any{"role": tc.role}, tc.content); ok {
			t.Fatalf("summarized %s %q", tc.role, tc.content)
		}
	}
}

// Requirement: Swarm Control reaches only its exact route shapes. Worker
// acceptance, token minting, import/migrate, plan-mode entry, raw SDK-only
// shapes and traversal/reserved ids must not match, so a crafted id cannot
// redirect a tool to an approval or credential route. Owner:
// controlMCPRouteAllowed (dispatch also enforces it). Pure-function layer.
func TestControlMCPRouteAllowlist(t *testing.T) {
	for _, tc := range []struct {
		method, path string
		want         bool
	}{
		{"GET", "/v3/sessions/abc", true},
		{"POST", "/v3/sessions/abc/plan-mode/plans/plan_1/start-automatic", true},
		{"POST", "/v3/workers/wkr_1/direct", true},
		{"POST", "/v3/workers/wkr_1/runs/run_1/cancel", true},
		{"POST", "/v3/usage/limits", true},
		{"POST", "/v3/workers/wkr_1/accept", false},
		{"POST", "/v3/workers/wkr_1/token", false},
		{"POST", "/v3/workers/import", false},
		{"POST", "/v3/workers/wkr_1/automations", true},
		{"POST", "/v3/workers/wkr_1/automations/auto_1/enable", true},
		{"PUT", "/v3/workers/wkr_1/automations/auto_1", false},
		{"DELETE", "/v3/workers/wkr_1/automations/auto_1", false},
		{"POST", "/v3/workers/wkr_1/automations/auto_1/trigger", false},
		{"POST", "/v3/workers/wkr_1/trigger", false},
		{"POST", "/v3/workers/wkr_1/test-run", false},
		{"GET", "/v1/providers", true},
		{"GET", "/v1/model/catalog", true},
		{"POST", "/v1/model/catalog", false},
		{"PATCH", "/v1/agent-model-settings", true},
		{"POST", "/v1/agent-model-settings/restore-defaults", false},
		{"POST", "/v1/model", false},
		{"POST", "/v1/permissions/bypass", false},
		{"POST", "/v1/permissions", false},
		{"PUT", "/v1/permissions/capabilities", false},
		{"POST", "/v1/onboarding/provider/credential", false},
		{"POST", "/v3/sessions/abc/preference", true},
		{"POST", "/v3/sessions/abc/settings", false},
		{"PUT", "/v3/sessions/abc/model-profile", false},
		{"POST", "/v3/sessions/abc/permissions/resolve_all", false},
		{"POST", "/v3/projects/prj_1/sessions", true},
		{"GET", "/v3/projects/prj_1/tasks", true},
		{"POST", "/v3/projects/prj_1/tasks", false},
		{"POST", "/v3/sessions/abc/plan-mode/enter", false},
		{"POST", "/v3/sessions/abc/plan-mode/plans/plan_1/submit", false},
		{"PUT", "/v3/usage/worker-budget", false},
		{"POST", "/v3/auth/tokens", false},
		{"GET", "/v3/sessions/../auth/tokens", false},
		{"GET", "/v3/sessions/a%2Fb", false},
		{"GET", "/v3/workers/token", false},
		{"GET", "/v3/sessions/abc/plans/active", true},
		{"DELETE", "/v3/sessions/abc", false},
		{"POST", "/v3/sessions:archive", false},
	} {
		if got := controlMCPRouteAllowed(tc.method, tc.path); got != tc.want {
			t.Fatalf("%s %s: got %v want %v", tc.method, tc.path, got, tc.want)
		}
	}
}

// Requirement: a caller-authored plan becomes an executable Swarm plan only
// if it passes the executor's strict validation (goal, ordered checkpoints,
// acceptance criteria); bad plans are refused before any session state is
// written. Owner: controlMCPPlanDocument with ValidateExecutablePlanDocument.
func TestControlMCPPlanDocument(t *testing.T) {
	doc, err := controlMCPPlanDocument(map[string]any{
		"goal":        "Add multiply",
		"constraints": []any{"no commits"},
		"checkpoints": []any{
			map[string]any{"title": "Implement", "tasks": []any{"edit calc.py"}, "acceptance_criteria": []any{"multiply exists"}},
			map[string]any{"title": "Document", "objective": "README", "acceptance_criteria": []any{"README mentions multiply"}},
		},
	})
	if err != nil || doc.Title != "Add multiply" || len(doc.Checkpoints) != 2 || doc.Checkpoints[1].ID != "cp2" || doc.Checkpoints[1].Order != 2 || !strings.HasPrefix(doc.ID, "plan_") {
		t.Fatalf("plan not normalized: %+v %v", doc, err)
	}
	many := []any{}
	for i := 0; i < controlMCPMaxCheckpoints+1; i++ {
		many = append(many, map[string]any{"title": "t", "acceptance_criteria": []any{"ok"}})
	}
	for name, plan := range map[string]map[string]any{
		"no goal":            {"checkpoints": []any{map[string]any{"title": "t", "acceptance_criteria": []any{"ok"}}}},
		"no checkpoints":     {"goal": "g"},
		"no criteria":        {"goal": "g", "checkpoints": []any{map[string]any{"title": "t", "tasks": []any{"x"}}}},
		"no title":           {"goal": "g", "checkpoints": []any{map[string]any{"acceptance_criteria": []any{"ok"}}}},
		"too many":           {"goal": "g", "checkpoints": many},
		"checkpoint not obj": {"goal": "g", "checkpoints": []any{"step"}},
	} {
		if _, err := controlMCPPlanDocument(plan); err == nil {
			t.Fatalf("%s: accepted", name)
		}
	}
}

// controlMCPTestCaller drives Swarm Control through the container SDK handler
// with a scoped token, as the relay device and local MCP clients do.
func controlMCPTestCaller(t *testing.T, s *Server, token string) func(tool string, args map[string]any) (string, bool) {
	t.Helper()
	handler := s.ContainerSDKHandler()
	return func(tool string, args map[string]any) (string, bool) {
		t.Helper()
		encoded, _ := json.Marshal(map[string]any{"jsonrpc": "2.0", "id": 1, "method": "tools/call", "params": map[string]any{"name": tool, "arguments": args}})
		r := httptest.NewRequest("POST", "http://127.0.0.1:7783/mcp", strings.NewReader(string(encoded)))
		r.RemoteAddr = "192.0.2.1:40000"
		r.Header.Set("Content-Type", "application/json")
		r.Header.Set("Authorization", "Bearer "+token)
		w := httptest.NewRecorder()
		handler.ServeHTTP(w, r)
		var out struct {
			Result struct {
				IsError bool `json:"isError"`
				Content []struct {
					Text string `json:"text"`
				} `json:"content"`
			} `json:"result"`
		}
		if err := json.Unmarshal(w.Body.Bytes(), &out); err != nil || len(out.Result.Content) == 0 {
			t.Fatalf("%s: HTTP %d %s", tool, w.Code, w.Body.String())
		}
		return out.Result.Content[0].Text, out.Result.IsError
	}
}

// Requirement: a supervising AI can answer an agent's ask_user question once,
// and the answer reaches the agent as the permission message of that exact
// pending record; a read grant cannot answer. Threat: answering through a
// read-only connection or turning an answer into a persistent rule. Owners:
// controlMCPResolvePermission and the V3 permission resolve handler. The
// in-process handler with real temporary stores is the narrowest layer that
// covers scope, ownership and the stored decision together.
func TestControlMCPAnswersAgentQuestion(t *testing.T) {
	s, _, sec, cleanup := setupScopedAuthTestServer(t)
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
	session := pebblestore.SessionSnapshot{ID: "question-session", UserID: actor.UserID, AccountScopeID: actor.AccountScopeID, Title: "q", Mode: "auto"}
	if _, err := s.sessions.ApplySessionMutation(sessionruntime.SessionMutationInput{Kind: sessionruntime.SessionMutationCreateSession, SessionID: session.ID, UserID: session.UserID, AccountScopeID: session.AccountScopeID, Session: &session, IdempotencyKey: "q-create", PayloadHash: "q-create", NowUnixMs: 1000}); err != nil {
		t.Fatal(err)
	}
	pending, err := s.perm.CreatePending(permission.CreateInput{SessionID: session.ID, RunID: "q-run", CallID: "q-call", ToolName: "ask_user", ToolArguments: `{"questions":[{"id":"q1","question":"Which platform?"}]}`, Requirement: "approval", Mode: "auto"})
	if err != nil {
		t.Fatal(err)
	}
	readToken, _, err := sec.CreateScopedToken("read", []string{"sessions:read"}, actor.AccountScopeID, actor.UserID, time.Hour, "", "")
	if err != nil {
		t.Fatal(err)
	}
	writeToken, _, err := sec.CreateScopedToken("write", []string{"sessions:read", "sessions:write"}, actor.AccountScopeID, actor.UserID, time.Hour, "", "")
	if err != nil {
		t.Fatal(err)
	}
	args := map[string]any{"session_id": session.ID, "permission_id": pending.ID, "action": "allow_once", "answer": "Bluesky first"}
	if text, isError := controlMCPTestCaller(t, s, readToken)("swarm_resolve_permission", args); !isError || !strings.Contains(text, "HTTP 403") {
		t.Fatalf("read grant answered: %s", text)
	}
	if open, err := s.perm.ListPending(session.ID, 10); err != nil || len(open) != 1 {
		t.Fatal("rejected answer changed the question")
	}
	detail, _ := controlMCPTestCaller(t, s, readToken)("swarm_get_session", map[string]any{"session_id": session.ID})
	if !strings.Contains(detail, `"tool_name":"ask_user"`) || !strings.Contains(detail, "Which platform?") {
		t.Fatalf("question not visible: %s", detail)
	}
	if text, isError := controlMCPTestCaller(t, s, writeToken)("swarm_resolve_permission", args); isError {
		t.Fatalf("answer failed: %s", text)
	}
	records, err := s.perm.ListPermissions(session.ID, 10)
	if err != nil || len(records) != 1 || records[0].Status == "pending" || records[0].Reason != "Bluesky first" {
		t.Fatalf("answer not recorded on the exact question: %+v %v", records, err)
	}
}

// Requirement: wait_seconds returns as soon as the session's run stops being
// active, woken by committed V3 outbox records for that session (never a
// timer poll), ignores other sessions' records, and stays bounded. Threat: a
// tool call that hangs the relay, or wakes on another session's activity.
// Owner: controlMCPCall.waitForSession over v3RealtimeOutboxHub. Run intents
// are recorded through the canonical V3 mutation store; the wake is the same
// hub publish the mutation path performs after commit.
func TestControlMCPWaitForSession(t *testing.T) {
	s, _, sec, cleanup := setupScopedAuthTestServer(t)
	defer cleanup()
	actor, err := s.identitySessions.ActorForCurrentSelection()
	if err != nil {
		t.Fatal(err)
	}
	session := pebblestore.SessionSnapshot{ID: "wait-session", UserID: actor.UserID, AccountScopeID: actor.AccountScopeID, Title: "w", Mode: "auto"}
	if _, err := s.sessions.ApplySessionMutation(sessionruntime.SessionMutationInput{Kind: sessionruntime.SessionMutationCreateSession, SessionID: session.ID, UserID: session.UserID, AccountScopeID: session.AccountScopeID, Session: &session, IdempotencyKey: "w-create", PayloadHash: "w-create", NowUnixMs: 1000}); err != nil {
		t.Fatal(err)
	}
	record := func(status string) {
		t.Helper()
		if _, err := s.sessions.Store().ApplyV3SessionMutation(pebblestore.V3SessionMutationInput{SessionID: session.ID, UserID: actor.UserID, AccountScopeID: actor.AccountScopeID, Kind: pebblestore.V3SessionMutationRecordRunIntent, ClientRequestID: status, PayloadHash: status, RunIntent: &pebblestore.V3SessionRunIntent{RunID: "wait-run", Status: status}}); err != nil {
			t.Fatal(err)
		}
	}
	token, _, err := sec.CreateScopedToken("read", []string{"sessions:read"}, actor.AccountScopeID, actor.UserID, time.Hour, "", "")
	if err != nil {
		t.Fatal(err)
	}
	call := controlMCPTestCaller(t, s, token)

	// Idle session: returns at once, settled.
	start := time.Now()
	if text, isError := call("swarm_get_session", map[string]any{"session_id": session.ID, "wait_seconds": 30}); isError || !strings.Contains(text, `"waited":{"settled":true}`) || time.Since(start) > 5*time.Second {
		t.Fatalf("idle wait: %s after %s", text, time.Since(start))
	}

	record(pebblestore.V3RunIntentPendingExecutor)
	record(pebblestore.V3RunIntentRunning)
	type result struct {
		text    string
		elapsed time.Duration
	}
	done := make(chan result, 1)
	start = time.Now()
	go func() {
		text, _ := call("swarm_get_session", map[string]any{"session_id": session.ID, "wait_seconds": 30})
		done <- result{text, time.Since(start)}
	}()
	// Wait until the call has subscribed, then wake it with another
	// session's record: it must keep waiting.
	for func() bool { s.v3RealtimeOutbox.mu.Lock(); defer s.v3RealtimeOutbox.mu.Unlock(); return len(s.v3RealtimeOutbox.subs) == 0 }() {
		time.Sleep(5 * time.Millisecond)
	}
	s.v3RealtimeOutbox.publish(pebblestore.V3RealtimeOutboxRecord{EndpointSeq: 1, SessionID: "other-session"})
	select {
	case got := <-done:
		t.Fatalf("woke on another session: %s", got.text)
	case <-time.After(200 * time.Millisecond):
	}
	record(pebblestore.V3RunIntentCompleted)
	s.v3RealtimeOutbox.publish(pebblestore.V3RealtimeOutboxRecord{EndpointSeq: 2, SessionID: session.ID})
	select {
	case got := <-done:
		if !strings.Contains(got.text, `"waited":{"settled":true}`) || got.elapsed > 10*time.Second {
			t.Fatalf("completion wait: %s after %s", got.text, got.elapsed)
		}
	case <-time.After(15 * time.Second):
		t.Fatal("wait did not wake on the session's committed record")
	}
	s.v3RealtimeOutbox.mu.Lock()
	leaked := len(s.v3RealtimeOutbox.subs)
	s.v3RealtimeOutbox.mu.Unlock()
	if leaked != 0 {
		t.Fatalf("wait leaked %d hub subscriptions", leaked)
	}
}

// Requirement: every tool this daemon serves has an explicit scope in the
// reference relay table, which the device mirrors (remote
// TestToolScopesMatchRelay). Unknown names fall back to write, so a missing
// entry would under-protect a manage tool. Owner: controlMCPTools and
// packages/swarm-relay/src/protocol.js TOOL_SCOPES.
func TestControlMCPServedToolsHaveRelayScopes(t *testing.T) {
	source, err := os.ReadFile(filepath.Join("..", "..", "..", "packages", "swarm-relay", "src", "protocol.js"))
	if err != nil {
		t.Fatal(err)
	}
	for _, tool := range controlMCPTools() {
		if !regexp.MustCompile(`(?m)^\s+` + tool.Name + `:\s+SCOPE_[A-Z]+,$`).Match(source) {
			t.Fatalf("%s has no explicit relay scope", tool.Name)
		}
	}
}
