package api

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"os"
	"os/exec"
	"path/filepath"
	"regexp"
	"strings"
	"testing"
	"time"

	"swarm/packages/swarmd/internal/identity"
	"swarm/packages/swarmd/internal/model"
	"swarm/packages/swarmd/internal/modelprofile"
	"swarm/packages/swarmd/internal/permission"
	sessionruntime "swarm/packages/swarmd/internal/session"
	pebblestore "swarm/packages/swarmd/internal/store/pebble"
	topologyruntime "swarm/packages/swarmd/internal/topology"
	"swarm/packages/swarmd/internal/workspace"
	worktreeruntime "swarm/packages/swarmd/internal/worktree"
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
		{"internal agent", write, "swarm_start_session", map[string]any{"workspace_path": "/project", "prompt": "hi", "agent": "system-router"}, "not a session agent"},
		{"unknown agent", write, "swarm_start_session", map[string]any{"workspace_path": "/project", "prompt": "hi", "agent": "nobody"}, "not available"},
		{"all roles without settings grant", write, "swarm_set_agent_model", map[string]any{"role": "all", "provider": "codex", "model": "m"}, "lacks the settings:write permission"},
		{"agent without admin", write, "swarm_define_agent", map[string]any{"name": "helper", "instructions": "x", "tools": "build"}, "HTTP 403"},
		{"client key without admin", write, "swarm_create_client_key", map[string]any{"name": "ui"}, "HTTP 403"},
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
		{"POST", "/v3/workers/wkr_1/automations", false},
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
		{"POST", "/v3/sessions/abc/preference", false},
		{"POST", "/v3/sessions/abc/settings", false},
		{"PUT", "/v3/sessions/abc/model-profile", true},
		{"DELETE", "/v3/sessions/abc/model-profile", false},
		{"POST", "/v3/sessions/abc/permissions/resolve_all", false},
		{"POST", "/v3/projects/prj_1/sessions", true},
		{"GET", "/v3/projects/prj_1/tasks", true},
		{"POST", "/v3/projects/prj_1/tasks", false},
		{"GET", "/v3/projects/prj_1/tasks/task_1", false},
		{"POST", "/v3/projects/prj_1/tasks/task_1/integrate", true},
		{"POST", "/v3/projects/prj_1/tasks/task_1/recover-integrate", false},
		{"POST", "/v3/projects/prj_1/tasks/task_1/reopen", false},
		{"POST", "/v3/sessions/abc/plan-mode/enter", false},
		{"POST", "/v3/sessions/abc/plan-mode/plans/plan_1/submit", false},
		{"PUT", "/v3/usage/worker-budget", false},
		{"POST", "/v3/auth/tokens", true},
		{"GET", "/v3/auth/tokens", false},
		{"POST", "/v3/auth/tokens/tok_1/revoke", false},
		{"PUT", "/v2/agents/builder", true},
		{"GET", "/v2/agents", true},
		{"DELETE", "/v2/agents/builder", false},
		{"PUT", "/v2/agents/builder/custom-tools/x", false},
		{"PUT", "/v2/custom-tools/x", false},
		{"POST", "/v2/agents/defaults/reset", false},
		{"POST", "/v1/auth/codex/oauth/start", true},
		{"GET", "/v1/auth/codex/oauth/status", true},
		{"POST", "/v1/auth/codex/oauth/complete", false},
		{"GET", "/v1/auth/credentials", false},
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
	for func() bool {
		s.v3RealtimeOutbox.mu.Lock()
		defer s.v3RealtimeOutbox.mu.Unlock()
		return len(s.v3RealtimeOutbox.subs) == 0
	}() {
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

// Requirement: a model chosen through Swarm Control must be the model the
// session runs. Swarm and orchestrator sessions capture the account role
// defaults as their model profile when created, and that profile (not the
// plain preference) decides their model, so set_session_model replaces the
// session's own profile selection. It is validated against the live catalog
// and refused for read grants, leaving the session unchanged. Threat: a
// silently ignored model choice (work billed to and run on another model), or
// a read-only connection changing a session. Owners: controlMCPSetSessionModel,
// controlMCPModelChoice and the V3 model-profile mutation. In-process HTTP with
// real temporary stores and the boot catalog is the narrowest layer that runs
// the tool, route allowlist, scope checks and the mutation together; it does
// not run a provider.
func TestControlMCPSessionModelReplacesCapturedProfile(t *testing.T) {
	s, _, sec, cleanup := setupScopedAuthTestServer(t)
	defer cleanup()
	db, err := pebblestore.Open(filepath.Join(t.TempDir(), "models"))
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	events, err := pebblestore.NewEventLog(db)
	if err != nil {
		t.Fatal(err)
	}
	s.model = model.NewService(pebblestore.NewModelStore(db), events, model.NewCatalogService(pebblestore.NewModelCatalogStore(db)))
	if err := s.model.EnsureBootDefaults(); err != nil {
		t.Fatal(err)
	}
	s.SetModelProfileService(modelprofile.NewService(pebblestore.NewModelProfileStore(db)))
	records, err := s.model.ListCatalog("codex", 10)
	if err != nil || len(records) < 2 {
		t.Fatalf("boot catalog: %d records, %v", len(records), err)
	}
	roleDefault, chosen := records[0], records[1]
	actor, err := s.identitySessions.ActorForCurrentSelection()
	if err != nil {
		t.Fatal(err)
	}
	captured := pebblestore.ModelProfileSelection{Provider: "codex", Model: roleDefault.Model, Thinking: roleDefault.DefaultThinking}
	session := pebblestore.SessionSnapshot{
		ID: "model-session", UserID: actor.UserID, AccountScopeID: actor.AccountScopeID, Title: "m", Mode: "auto",
		Metadata:     map[string]any{"agent_name": "swarm"},
		Preference:   pebblestore.ModelPreference{Provider: "codex", Model: roleDefault.Model, Thinking: roleDefault.DefaultThinking},
		ModelProfile: &pebblestore.SessionModelProfileSnapshot{Source: pebblestore.SessionModelProfileSourceSwarmSettings, UseAccountDefault: true, Action: captured, Plan: &captured, AppliedAt: 1},
	}
	if _, err := s.sessions.ApplySessionMutation(sessionruntime.SessionMutationInput{Kind: sessionruntime.SessionMutationCreateSession, SessionID: session.ID, UserID: session.UserID, AccountScopeID: session.AccountScopeID, Session: &session, IdempotencyKey: "m-create", PayloadHash: "m-create", NowUnixMs: 1000}); err != nil {
		t.Fatal(err)
	}
	unchanged := func(label string) {
		t.Helper()
		stored, ok, err := s.sessions.GetSession(session.ID)
		if err != nil || !ok || stored.Preference.Model != roleDefault.Model || stored.ModelProfile == nil || stored.ModelProfile.Source != pebblestore.SessionModelProfileSourceSwarmSettings {
			t.Fatalf("%s changed the session: %+v %v", label, stored, err)
		}
	}
	readToken, _, err := sec.CreateScopedToken("read", []string{"sessions:read"}, actor.AccountScopeID, actor.UserID, time.Hour, "", "")
	if err != nil {
		t.Fatal(err)
	}
	writeToken, _, err := sec.CreateScopedToken("write", []string{"sessions:read", "sessions:write"}, actor.AccountScopeID, actor.UserID, time.Hour, "", "")
	if err != nil {
		t.Fatal(err)
	}
	args := map[string]any{"session_id": session.ID, "provider": "codex", "model": chosen.Model}
	if text, isError := controlMCPTestCaller(t, s, readToken)("swarm_set_session_model", args); !isError || !strings.Contains(text, "HTTP 403") {
		t.Fatalf("read grant changed the model: %s", text)
	}
	unchanged("read grant")
	if text, isError := controlMCPTestCaller(t, s, writeToken)("swarm_set_session_model", map[string]any{"session_id": session.ID, "provider": "codex", "model": "no-such-model"}); !isError || !strings.Contains(text, "not in the catalog") {
		t.Fatalf("uncatalogued model accepted: %s", text)
	}
	unchanged("uncatalogued model")
	text, isError := controlMCPTestCaller(t, s, writeToken)("swarm_set_session_model", args)
	if isError || !strings.Contains(text, chosen.Model) {
		t.Fatalf("model change failed: %s", text)
	}
	stored, ok, err := s.sessions.GetSession(session.ID)
	if err != nil || !ok || stored.Preference.Model != chosen.Model || stored.ModelProfile == nil || stored.ModelProfile.Source != pebblestore.SessionModelProfileSourceTemporary || stored.ModelProfile.Action.Model != chosen.Model {
		t.Fatalf("session does not use the chosen model: %+v %v", stored, err)
	}
	if policy := s.sessionsV3AgentModelPolicy(stored, stored.Preference, 0, 0); policy.Preference.Model != chosen.Model {
		t.Fatalf("effective model policy = %+v", policy)
	}
	if choice := controlMCPModelChoice(map[string]any{"provider": "codex", "model": chosen.Model, "thinking": "low"}); controlMCPMap(choice["temporary"])["thinking"] != "low" {
		t.Fatalf("thinking dropped from the session choice: %+v", choice)
	}
}

// Requirement: create_worker's schedule and scheduled plan are part of the
// worker it creates (approved with it), and the plan must be accepted as an
// unexecuted template while keeping the caller's checkpoints. A template that
// carries plan identity is refused with no worker created. Threat: every
// scheduled worker created through Swarm Control failing after creation and
// left without its schedule, or a schedule staged for an owner review Swarm
// Control cannot accept. Once bound and enabled, get_worker reports the next
// scheduled run. Owners: controlMCPWorkerCreateBody, controlMCPAutomation,
// controlMCPGetWorker, the worker store's validateUnexecutedPlanDocument behind
// POST /v3/workers, worker activation and the summary route. Worker routes
// with a real temporary store, execution service and Git workspace are the
// narrowest layer that validates the exact bodies the tool sends; they do not
// run a scheduled plan.
func TestControlMCPWorkerScheduleIsPartOfCreate(t *testing.T) {
	s, db, h := setupWorkerAPITestServer(t)
	workspaceID := setupWorkerAPIExecution(t, s, db)
	args := map[string]any{
		"name": "Drafts", "instructions": "Write drafts.", "workspace_path": "/project",
		"schedule": map[string]any{"kind": "interval", "interval_seconds": float64(120)},
		"scheduled_plan": map[string]any{"title": "Draft", "goal": "Write one draft file.", "checkpoints": []any{
			map[string]any{"title": "Write", "objective": "Write drafts/x.md", "acceptance_criteria": []any{"drafts/x.md exists"}},
		}},
	}
	create := func(body map[string]any) *httptest.ResponseRecorder {
		encoded, _ := json.Marshal(body)
		return executeWorkerAPI(h, http.MethodPost, "", string(encoded), workerAPICallOptions{scopes: []string{"automations:write"}})
	}
	workers := pebblestore.NewWorkerStore(db)

	body, automation, err := controlMCPWorkerCreateBody(args)
	if err != nil || automation == nil {
		t.Fatalf("body: %v", err)
	}
	withIdentity, _, _ := controlMCPWorkerCreateBody(args)
	identified := map[string]any{}
	for key, value := range automation {
		identified[key] = value
	}
	document := *automation["plan_document"].(*pebblestore.SessionPlanDocument)
	document.ID = "plan_preset"
	identified["plan_document"] = &document
	withIdentity["automations"] = []map[string]any{identified}
	if w := create(withIdentity); w.Code < 400 || !strings.Contains(w.Body.String(), "execution state") {
		t.Fatalf("plan with identity accepted: %d %s", w.Code, w.Body.String())
	}
	if listed, err := workers.ListWorkers("acct-test", pebblestore.ListWorkersQuery{Limit: 10}); err != nil || len(listed.Workers) != 0 {
		t.Fatalf("rejected create left a worker: %+v %v", listed, err)
	}

	w := create(body)
	var created struct {
		Worker pebblestore.WorkerRecord `json:"worker"`
	}
	if err := json.Unmarshal(w.Body.Bytes(), &created); err != nil || w.Code != http.StatusCreated {
		t.Fatalf("scheduled worker rejected: %d %s", w.Code, w.Body.String())
	}
	stored, ok, err := workers.GetWorker("acct-test", created.Worker.ID)
	if err != nil || !ok || stored.PendingReview != nil || len(stored.Automations) != 1 {
		t.Fatalf("schedule not part of the created worker: %+v %v", stored, err)
	}
	attached := stored.Automations[0]
	if attached.Enabled || attached.Schedule == nil || attached.Schedule.IntervalSeconds != 120 || len(attached.PlanDocument.Checkpoints) != 1 || attached.PlanDocument.Checkpoints[0].Title != "Write" || attached.PlanDocument.ID != "" {
		t.Fatalf("schedule lost the caller's plan or started enabled: %+v", attached)
	}

	activate, _ := json.Marshal(map[string]any{"expected_revision": stored.Revision, "local_bindings": map[string]string{"primary": workspaceID}})
	if w := executeWorkerAPI(h, http.MethodPost, "/"+stored.ID+"/activate", string(activate), workerAPICallOptions{scopes: []string{"automations:write"}}); w.Code != http.StatusOK {
		t.Fatalf("activate: %d %s", w.Code, w.Body.String())
	}
	active, _, _ := workers.GetWorker("acct-test", stored.ID)
	enable, _ := json.Marshal(map[string]any{"expected_worker_revision": active.Revision})
	if w := executeWorkerAPI(h, http.MethodPost, "/"+stored.ID+"/automations/"+attached.ID+"/enable", string(enable), workerAPICallOptions{scopes: []string{"automations:write"}}); w.Code != http.StatusOK {
		t.Fatalf("enable: %d %s", w.Code, w.Body.String())
	}
	r := httptest.NewRequest(http.MethodPost, "/mcp", nil)
	r = r.WithContext(context.WithValue(context.WithValue(r.Context(), productPrincipalRequestContextKey, identity.Principal{Type: "user", UserID: "user-test", AccountScopeID: "acct-test"}),
		productScopedTokenRequestContextKey, &pebblestore.ScopedTokenRecord{AccountScopeID: "acct-test", UserID: "user-test", Scopes: []string{"automations:read"}}))
	got, err := controlMCPGetWorker(&controlMCPCall{request: r, next: h}, map[string]any{"worker_id": stored.ID})
	if err != nil {
		t.Fatal(err)
	}
	view := got.(map[string]any)
	nextFloat, _ := view["next_scheduled_at"].(float64)
	next := int64(nextFloat)
	if next <= time.Now().UnixMilli() || next > time.Now().Add(121*time.Second).UnixMilli() || view["lifecycle_state"] != string(pebblestore.WorkerLifecycleStateActive) {
		t.Fatalf("get_worker does not report the next scheduled run: %+v", view)
	}
}

// Requirement: a supervising AI can review and integrate finished project
// work: list_tasks shows tasks waiting in needs_review with their branches,
// and integrate_task performs the real git integration of the task's own
// agent branch into its captured target branch (never a branch the caller
// picks), recording the receipt. A task without a captured target is refused
// with no Git or task change. Threats: work stranded on agent branches with
// no remote way to accept it, a merge into an arbitrary branch, or a task
// marked integrated without a merge. Owners: controlMCPListTasks,
// controlMCPIntegrateTask and POST /v3/projects/{id}/tasks/{tid}/integrate.
// Real Git with temporary stores through the API mux is the narrowest layer
// that proves the merge; relay scope (swarm:approve) is covered by
// TestToolScopesMatchRelay.
func TestControlMCPIntegratesReviewedTask(t *testing.T) {
	f := setupMatrixTestFixture(t)
	defer func() { f.db.Close() }()
	root := t.TempDir()
	t.Setenv("HOME", root)
	t.Setenv("XDG_DATA_HOME", filepath.Join(root, "data"))
	t.Setenv("GIT_CONFIG_GLOBAL", os.DevNull)
	t.Setenv("GIT_CONFIG_NOSYSTEM", "1")
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()
	git := func(path string, args ...string) string {
		t.Helper()
		out, err := exec.CommandContext(ctx, "git", append([]string{"-C", path, "-c", "user.name=Fixture", "-c", "user.email=fixture@example.invalid"}, args...)...).CombinedOutput()
		if err != nil {
			t.Fatalf("git %v: %v %s", args, err, out)
		}
		return strings.TrimSpace(string(out))
	}
	write := func(dir, name, text string) {
		t.Helper()
		if err := os.MkdirAll(filepath.Dir(filepath.Join(dir, name)), 0o700); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(filepath.Join(dir, name), []byte(text), 0o600); err != nil {
			t.Fatal(err)
		}
	}
	repo := filepath.Join(root, "repo")
	if err := os.Mkdir(repo, 0o700); err != nil {
		t.Fatal(err)
	}
	git(repo, "init", "-b", "main")
	// The integration merge commit needs a hermetic repository identity.
	git(repo, "config", "user.name", "Fixture")
	git(repo, "config", "user.email", "fixture@example.invalid")
	write(repo, "README.md", "base\n")
	git(repo, "add", ".")
	git(repo, "commit", "-m", "base")
	base := git(repo, "rev-parse", "HEAD")
	entry, err := pebblestore.NewWorkspaceStore(f.db).AddForAccount(f.accountID, repo, "Bot")
	if err != nil {
		t.Fatal(err)
	}
	ss := pebblestore.NewSessionStore(f.db)
	el, err := pebblestore.NewEventLog(f.db)
	if err != nil {
		t.Fatal(err)
	}
	f.server.sessions = sessionruntime.NewService(ss, el)
	f.server.workspace = workspace.NewService(pebblestore.NewWorkspaceStore(f.db))
	worktrees := worktreeruntime.NewService(pebblestore.NewWorktreeStore(f.db), f.server.workspace, nil)
	f.server.worktrees = worktrees
	binding := pebblestore.ProjectTaskSource{WorkspaceID: entry.WorkspaceID, WorkspaceGeneration: entry.WorkspaceGeneration, Path: repo, Provenance: "explicit"}
	seedTaskSessionBinding(t, f, binding)
	p := identity.Principal{Type: "user", UserID: f.userID, AccountScopeID: f.accountID}
	alloc, err := worktrees.AllocateProjectTaskFollowup(p, repo, "draft-run", "agent/worker-draft", base, "main")
	if err != nil {
		t.Fatal(err)
	}
	write(alloc.WorkspacePath, "drafts/draft.md", "a draft\n")
	git(alloc.WorkspacePath, "add", ".")
	git(alloc.WorkspacePath, "commit", "-m", "draft")
	head := git(alloc.WorkspacePath, "rev-parse", "HEAD")
	write(repo, "NOTES.md", "target moved on\n")
	git(repo, "add", ".")
	git(repo, "commit", "-m", "target")
	target := git(repo, "rev-parse", "HEAD")

	db := f.server.sessions.Store()
	if err := db.PutProject(f.accountID, &pebblestore.ProjectRecord{ID: "project", Name: "Bot", Workspaces: []pebblestore.ProjectWorkspaceRef{{WorkspaceID: entry.WorkspaceID, Path: repo}}}); err != nil {
		t.Fatal(err)
	}
	for _, task := range []pebblestore.ProjectTaskRecord{
		{ID: "task_ready", ProjectID: "project", AccountID: f.accountID, Title: "Draft", Status: "needs_review", SessionID: "draft-run", Agent: "swarm", WorkspacePath: alloc.WorkspacePath, WorktreeBranch: alloc.BranchName, BaseBranch: "main", BaseCommit: base, SourceWorkspace: binding},
		{ID: "task_foreign", ProjectID: "project", AccountID: f.accountID, Title: "Unowned", Status: "needs_review", SessionID: "missing-session", Agent: "swarm", WorkspacePath: alloc.WorkspacePath, WorktreeBranch: alloc.BranchName, BaseBranch: "main", BaseCommit: base, SourceWorkspace: binding},
	} {
		task := task
		if err := db.PutProjectTask(f.accountID, &task); err != nil {
			t.Fatal(err)
		}
	}
	snapshot := pebblestore.SessionSnapshot{ID: "draft-run", UserID: p.UserID, AccountScopeID: p.AccountScopeID, WorkspacePath: alloc.WorkspacePath, WorktreeEnabled: true, WorktreeRootPath: alloc.WorkspacePath, WorktreeBranch: alloc.BranchName, WorktreeBaseBranch: "main", Mode: "auto", Metadata: map[string]any{"project_id": "project", "task_id": "task_ready", "base_commit": base, "swarm_v3_source_workspace_path": repo, "swarm_v3_source_workspace_id": binding.WorkspaceID, "swarm_v3_source_workspace_generation": binding.WorkspaceGeneration, "swarm_v3_worktree_owner_session_id": "draft-run", "swarm_v3_runtime_workspace_path": alloc.WorkspacePath}}
	if _, err := applyProjectLifecycleFixture(f.server, sessionruntime.SessionMutationInput{SessionID: snapshot.ID, UserID: p.UserID, AccountScopeID: p.AccountScopeID, ClientRequestID: "create-draft", IdempotencyKey: "create-draft", PayloadHash: "create-draft", RequestHash: "create-draft", Kind: sessionruntime.SessionMutationCreateSession, Session: &snapshot, WorktreeAdmission: &pebblestore.WorktreeAdmissionEvidence{Kind: "allocated", Path: alloc.WorkspacePath, SourcePath: repo, OwnerSessionID: snapshot.ID, Branch: alloc.BranchName, AllocatedRuntimeRoot: true}}); err != nil {
		t.Fatal(err)
	}
	r := httptest.NewRequest(http.MethodPost, "/mcp", nil)
	r = r.WithContext(context.WithValue(context.WithValue(ctx, productPrincipalRequestContextKey, p),
		productScopedTokenRequestContextKey, &pebblestore.ScopedTokenRecord{AccountScopeID: p.AccountScopeID, UserID: p.UserID, Scopes: []string{"sessions:read", "sessions:write"}}))
	c := &controlMCPCall{request: r, next: f.server.apiMux()}

	listed, err := controlMCPListTasks(c, map[string]any{"project_id": "project", "status": "needs_review"})
	if err != nil || len(listed.(map[string]any)["tasks"].([]map[string]any)) != 2 {
		t.Fatalf("list_tasks: %+v %v", listed, err)
	}
	if _, err := controlMCPIntegrateTask(c, map[string]any{"project_id": "project", "task_id": "task_foreign"}); err == nil || !strings.Contains(err.Error(), "HTTP 409") {
		t.Fatalf("task without its own session integrated: %v", err)
	}
	if foreign, _, _ := db.GetProjectTask(f.accountID, "project", "task_foreign"); git(repo, "rev-parse", "main") != target || foreign.IsIntegrated {
		t.Fatal("refused integration changed the target or the task")
	}
	result, err := controlMCPIntegrateTask(c, map[string]any{"project_id": "project", "task_id": "task_ready"})
	if err != nil {
		t.Fatal(err)
	}
	out := result.(map[string]any)
	stored, found, err := db.GetProjectTask(f.accountID, "project", "task_ready")
	if err != nil || !found || out["status"] != "integrated" || !stored.IsIntegrated || stored.Status != "completed" || stored.Integration == nil || stored.Integration.ResultingTargetHead != git(repo, "rev-parse", "main") {
		t.Fatalf("integration not recorded: %+v %+v %v", out, stored, err)
	}
	git(repo, "merge-base", "--is-ancestor", head, "main")
	git(repo, "merge-base", "--is-ancestor", target, "main")
	if git(repo, "status", "--porcelain") != "" || git(repo, "show", "main:drafts/draft.md") != "a draft" {
		t.Fatal("draft not on main or target left dirty")
	}
}

// Requirement: a worker run's work can be reviewed and integrated like any
// project task: the run forks from the source workspace's current branch (a
// real integration target, not a literal HEAD) and records the same captured
// lineage (task source binding, session base commit), so integrate_task merges
// its committed work into that branch. Threat: scheduled worker output stranded
// on agent branches that the integrate route refuses, or a merge target of
// "HEAD". Owners: WorkerExecutionService dispatch preparation
// (worker_execution.go), controlMCPIntegrateTask and the project task integrate
// route. The worker HTTP dispatch with the real execution service, Git and
// temporary stores is the narrowest layer covering both; the agent's own work
// is a fixture commit (no provider runs).
func TestControlMCPIntegratesWorkerRun(t *testing.T) {
	s, db, h := setupWorkerAPITestServer(t)
	workspaceID := setupWorkerAPIExecution(t, s, db)
	s.workspace = workspace.NewService(pebblestore.NewWorkspaceStore(db))
	w, err := s.sessions.Store().WorkerStore().CreateWorker("acct-test", "user-test", pebblestore.CreateWorkerRequest{Name: "Drafts", WorkspaceRequirements: []pebblestore.WorkerWorkspaceRequirement{{Role: "primary", Required: true}}}, nil)
	if err != nil {
		t.Fatal(err)
	}
	w = activateWorkerAPIFixture(t, s, w, workspaceID)
	if response := executeWorkerAPI(h, http.MethodPost, "/"+w.ID+"/direct", `{"prompt":"Write a draft","idempotency_key":"draft-run"}`, workerAPICallOptions{scopes: []string{"automations:write"}}); response.Code != http.StatusCreated {
		t.Fatalf("dispatch: %d %s", response.Code, response.Body.String())
	}
	p := identity.Principal{Type: identity.PrincipalTypeUser, AccountScopeID: "acct-test", UserID: "user-test"}
	runs, _, err := s.sessions.ListWorkerRuns(p.AccountScopeID, w.ID, 10, "")
	if err != nil || len(runs) != 1 {
		t.Fatalf("runs: %+v %v", runs, err)
	}
	session, found, err := s.sessions.GetSession(runs[0].SessionID)
	if err != nil || !found {
		t.Fatalf("run session: %v", err)
	}
	repo, _ := session.Metadata["swarm_v3_source_workspace_path"].(string)
	taskID, _ := session.Metadata["task_id"].(string)
	task, found, err := s.sessions.Store().GetProjectTask(p.AccountScopeID, "project_workers", taskID)
	if err != nil || !found || task.BaseBranch != "dev" || session.WorktreeBaseBranch != "dev" || task.SourceWorkspace.WorkspaceID != workspaceID || task.SourceWorkspace.Path != repo || task.BaseCommit == "" || session.Metadata["base_commit"] != task.BaseCommit {
		t.Fatalf("worker run lineage is not integrable: task=%+v session=%+v %v", task, session, err)
	}
	ctx, cancel := context.WithTimeout(context.Background(), 20*time.Second)
	defer cancel()
	git := func(path string, args ...string) string {
		t.Helper()
		out, err := exec.CommandContext(ctx, "git", append([]string{"-C", path, "-c", "user.name=Fixture", "-c", "user.email=fixture@example.invalid"}, args...)...).CombinedOutput()
		if err != nil {
			t.Fatalf("git %v: %v %s", args, err, out)
		}
		return strings.TrimSpace(string(out))
	}
	git(repo, "config", "user.name", "Fixture")
	git(repo, "config", "user.email", "fixture@example.invalid")
	if err := os.MkdirAll(filepath.Join(session.WorktreeRootPath, "drafts"), 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(session.WorktreeRootPath, "drafts", "draft.md"), []byte("a draft\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	git(session.WorktreeRootPath, "add", ".")
	git(session.WorktreeRootPath, "commit", "-m", "draft")
	head := git(session.WorktreeRootPath, "rev-parse", "HEAD")

	r := httptest.NewRequest(http.MethodPost, "/mcp", nil)
	r = r.WithContext(context.WithValue(context.WithValue(ctx, productPrincipalRequestContextKey, p),
		productScopedTokenRequestContextKey, &pebblestore.ScopedTokenRecord{AccountScopeID: p.AccountScopeID, UserID: p.UserID, Scopes: []string{"sessions:read", "sessions:write"}}))
	result, err := controlMCPIntegrateTask(&controlMCPCall{request: r, next: h}, map[string]any{"project_id": "project_workers", "task_id": taskID})
	if err != nil {
		t.Fatal(err)
	}
	stored, _, _ := s.sessions.Store().GetProjectTask(p.AccountScopeID, "project_workers", taskID)
	if result.(map[string]any)["status"] != "integrated" || stored == nil || !stored.IsIntegrated {
		t.Fatalf("worker run not integrated: %+v %+v", result, stored)
	}
	git(repo, "merge-base", "--is-ancestor", head, "dev")
	if git(repo, "show", "dev:drafts/draft.md") != "a draft" {
		t.Fatal("draft not on the source branch")
	}
}

// Requirement: the task-session lane check accepts a worker run's session
// shape (source workspace as session workspace, owned worktree as runtime)
// only for that task's own worker run; every other shape mismatch still
// fails. Threat: another run's, worker's or a non-worker session passing as
// the task's lane and having its worktree integrated. Owner:
// verifyProjectTaskSession / workerRunTaskSession; a pure function test is the
// narrowest layer.
func TestVerifyProjectTaskSessionWorkerRunShape(t *testing.T) {
	task := &pebblestore.ProjectTaskRecord{ID: "task_wrun_1", ProjectID: "p", SessionID: "worker-execution-wrun_1", WorkerID: "worker_1", WorkerRunID: "wrun_1", WorkspacePath: "/wt/run", SourceWorkspace: pebblestore.ProjectTaskSource{WorkspaceID: "ws_1", WorkspaceGeneration: 1, Path: "/repo", Provenance: "worker_binding"}}
	session := func(edit func(*pebblestore.SessionSnapshot)) pebblestore.SessionSnapshot {
		s := pebblestore.SessionSnapshot{ID: "worker-execution-wrun_1", AccountScopeID: "acct", WorkspacePath: "/repo", WorktreeEnabled: true, WorktreeRootPath: "/wt/run", Metadata: map[string]any{
			"project_id": "p", "task_id": "task_wrun_1", "swarm_v3_source_workspace_path": "/repo", "swarm_v3_source_workspace_id": "ws_1", "swarm_v3_source_workspace_generation": int64(1),
			"swarm_v3_worktree_owner_session_id": "worker-execution-wrun_1", "swarm_v3_runtime_workspace_path": "/wt/run",
			pebblestore.SessionPurposeMetadataKey: pebblestore.SessionPurposeAutomationExecution, "worker_execution_run_id": "wrun_1", "worker_id": "worker_1",
		}}
		if edit != nil {
			edit(&s)
		}
		return s
	}
	if err := verifyProjectTaskSession(task, session(nil), "acct"); err != nil {
		t.Fatalf("own worker run refused: %v", err)
	}
	for name, edit := range map[string]func(*pebblestore.SessionSnapshot){
		"other run":          func(s *pebblestore.SessionSnapshot) { s.Metadata["worker_execution_run_id"] = "wrun_2" },
		"other worker":       func(s *pebblestore.SessionSnapshot) { s.Metadata["worker_id"] = "worker_2" },
		"not a worker run":   func(s *pebblestore.SessionSnapshot) { delete(s.Metadata, pebblestore.SessionPurposeMetadataKey) },
		"foreign workspace":  func(s *pebblestore.SessionSnapshot) { s.WorkspacePath = "/elsewhere" },
		"foreign runtime":    func(s *pebblestore.SessionSnapshot) { s.Metadata["swarm_v3_runtime_workspace_path"] = "/wt/other" },
		"foreign lane owner": func(s *pebblestore.SessionSnapshot) { s.Metadata["swarm_v3_worktree_owner_session_id"] = "other" },
	} {
		if err := verifyProjectTaskSession(task, session(edit), "acct"); err == nil {
			t.Fatalf("%s accepted as the task's lane", name)
		}
	}
	plain := *task
	plain.WorkerID, plain.WorkerRunID = "", ""
	if err := verifyProjectTaskSession(&plain, session(nil), "acct"); err == nil {
		t.Fatal("non-worker task accepted a source-workspace session")
	}
}

// Requirement: an AI with a write grant can take a fresh machine from zero
// workspaces to one it can start sessions in. create_workspace creates an
// empty repository with one initial commit under the daemon's startup folder
// and registers it; repeating the call returns the same workspace; names that
// could leave that folder are refused; a read grant cannot create; a folder
// that already holds other content is not taken over. Threat: an AI creating
// folders anywhere on the machine or adopting existing files. Owners:
// controlMCPCreateWorkspace and the workspace setup and add handlers. The
// in-process handler with real temporary stores and Git is the narrowest
// layer that runs scope, route allowlist, setup and registration together.
func TestControlMCPCreatesWorkspaceFromZero(t *testing.T) {
	if _, err := exec.LookPath("git"); err != nil {
		t.Skip("git is required")
	}
	t.Setenv("GIT_CONFIG_GLOBAL", os.DevNull)
	t.Setenv("GIT_CONFIG_NOSYSTEM", "1")
	s, _, sec, cleanup := setupScopedAuthTestServer(t)
	defer cleanup()
	db, err := pebblestore.Open(filepath.Join(t.TempDir(), "workspaces"))
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	swarmStore := pebblestore.NewSwarmStore(db, nil)
	if _, err := swarmStore.PutLocalNode(pebblestore.SwarmLocalNodeRecord{SwarmID: "create-workspace-test", Name: "Primary", Role: bootstrapRoleMaster}); err != nil {
		t.Fatal(err)
	}
	s.workspace = workspace.NewService(pebblestore.NewWorkspaceStore(db))
	s.SetTopologyService(topologyruntime.NewService(pebblestore.NewTopologyStore(db), swarmStore))
	root := t.TempDir()
	s.SetWorkspaceRoot(root)
	actor, err := s.identitySessions.ActorForCurrentSelection()
	if err != nil {
		t.Fatal(err)
	}
	token := func(scopes ...string) string {
		value, _, err := sec.CreateScopedToken("create-workspace", scopes, actor.AccountScopeID, actor.UserID, time.Hour, "", "")
		if err != nil {
			t.Fatal(err)
		}
		return value
	}
	read := controlMCPTestCaller(t, s, token("sessions:read"))
	write := controlMCPTestCaller(t, s, token("sessions:read", "sessions:write"))

	if text, isError := read("swarm_create_workspace", map[string]any{"name": "bot"}); !isError || !strings.Contains(text, "sessions:write") {
		t.Fatalf("read grant created a workspace: %s", text)
	}
	for _, name := range []string{"..", "../x", "a/b", ".hidden", "Bot", ""} {
		if text, isError := write("swarm_create_workspace", map[string]any{"name": name}); !isError {
			t.Fatalf("name %q accepted: %s", name, text)
		}
	}
	resolvedRoot, err := filepath.EvalSymlinks(root)
	if err != nil {
		t.Fatal(err)
	}
	want := filepath.Join(resolvedRoot, "bot")
	text, isError := write("swarm_create_workspace", map[string]any{"name": "bot"})
	if isError || !strings.Contains(text, `"created":true`) || !strings.Contains(text, want) {
		t.Fatalf("create: %s", text)
	}
	if out, err := exec.Command("git", "-C", want, "rev-list", "--count", "HEAD").CombinedOutput(); err != nil || strings.TrimSpace(string(out)) != "1" {
		t.Fatalf("initial commit: %v %s", err, out)
	}
	if text, isError := write("swarm_create_workspace", map[string]any{"name": "bot"}); isError || !strings.Contains(text, `"created":false`) {
		t.Fatalf("repeat: %s", text)
	}
	if text, _ := read("swarm_list_workspaces", map[string]any{}); !strings.Contains(text, want) {
		t.Fatalf("list: %s", text)
	}
	occupied := filepath.Join(resolvedRoot, "notes")
	if err := os.MkdirAll(occupied, 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(occupied, "keep.txt"), []byte("mine"), 0o600); err != nil {
		t.Fatal(err)
	}
	if text, isError := write("swarm_create_workspace", map[string]any{"name": "notes"}); !isError {
		t.Fatalf("occupied folder adopted: %s", text)
	}
	if _, err := os.Stat(filepath.Join(occupied, ".git")); !os.IsNotExist(err) {
		t.Fatalf("occupied folder changed: %v", err)
	}
	s.SetWorkspaceRoot("")
	if text, isError := write("swarm_create_workspace", map[string]any{"name": "other"}); !isError || !strings.Contains(text, "no workspace folder") {
		t.Fatalf("no root: %s", text)
	}
}
