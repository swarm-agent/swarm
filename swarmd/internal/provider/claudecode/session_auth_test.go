package claudecode

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync/atomic"
	"testing"

	provideriface "swarm/packages/swarmd/internal/provider/interfaces"
)

type countingInvoker struct{ calls atomic.Int64 }

func (i *countingInvoker) ExecuteTool(_ context.Context, call provideriface.ToolInvocation) (provideriface.ToolExecutionResult, error) {
	i.calls.Add(1)
	return provideriface.ToolExecutionResult{Output: call.CallID}, nil
}

// Purpose: SessionMCP must bind each token to exactly one owner/session and expose
// only the run-loop supplied inventory. A caller cannot choose another session,
// including across accounts, or invoke unoffered tools. The HTTP handler is the
// narrowest boundary proving rejection and absence of executor side effects.
func TestSessionMCPTokenIsolationAndToolInventories(t *testing.T) {
	endpoint := testEndpoint(t)
	server := httptest.NewServer(endpoint)
	defer server.Close()
	if err := endpoint.SetAddress(server.Listener.Addr().String()); err != nil {
		t.Fatal(err)
	}
	p := testPrincipal()
	invoker := &countingInvoker{}
	for _, fixture := range []struct {
		id    string
		names []string
	}{{"orchestrator", []string{"manage_projects", "manage_worktrees", "task", "read"}}, {"coder", []string{"bash", "read", "edit", "search", "task_progress"}}} {
		defs := make([]provideriface.ToolDefinition, 0, len(fixture.names))
		for _, name := range fixture.names {
			defs = append(defs, provideriface.ToolDefinition{Name: name})
		}
		plan := turnPlan{input: &turnInput{}, restart: &restartSignal{}, correlat: newToolUseCorrelator()}
		detach, err := endpoint.attach(context.Background(), p, provideriface.Request{SessionID: fixture.id, Tools: defs, ToolInvoker: invoker}, &plan)
		if err != nil {
			t.Fatal(err)
		}
		defer detach()
	}
	token, err := endpoint.security.CreateSessionToken(p.AccountScopeID, p.UserID, "coder")
	if err != nil {
		t.Fatal(err)
	}
	post := func(token, session, body string) *httptest.ResponseRecorder {
		req := httptest.NewRequest(http.MethodPost, "/mcp", strings.NewReader(body))
		req.Header.Set("Authorization", "Bearer "+token)
		if session != "" {
			req.Header.Set("Mcp-Session-Id", session)
		}
		w := httptest.NewRecorder()
		endpoint.ServeHTTP(w, req)
		return w
	}
	for _, target := range []string{"orchestrator", "other-session"} {
		w := post(token, target, `{"jsonrpc":"2.0","id":1,"method":"tools/call","params":{"name":"read"}}`)
		if w.Code != http.StatusForbidden {
			t.Fatalf("cross-session status=%d", w.Code)
		}
	}
	foreign, err := endpoint.security.CreateSessionToken("other-account", p.UserID, "coder")
	if err != nil {
		t.Fatal(err)
	}
	if w := post(foreign, "", `{"jsonrpc":"2.0","id":1,"method":"tools/call","params":{"name":"read"}}`); w.Code != http.StatusServiceUnavailable {
		t.Fatalf("foreign token reached executor: %s", w.Body)
	}
	w := post(token, "", `{"jsonrpc":"2.0","id":1,"method":"tools/call","params":{"name":"manage_projects"}}`)
	if !strings.Contains(w.Body.String(), "unknown tool") || invoker.calls.Load() != 0 {
		t.Fatalf("unoffered execution: %s calls=%d", w.Body, invoker.calls.Load())
	}
	for _, id := range []string{"orchestrator", "coder"} {
		key, err := endpoint.security.CreateSessionToken(p.AccountScopeID, p.UserID, id)
		if err != nil {
			t.Fatal(err)
		}
		w := post(key, "", `{"jsonrpc":"2.0","id":1,"method":"tools/list"}`)
		var response struct {
			Result struct {
				Tools []mcpTool `json:"tools"`
			} `json:"result"`
		}
		if err = json.Unmarshal(w.Body.Bytes(), &response); err != nil {
			t.Fatal(err)
		}
		names := map[string]bool{}
		for _, tool := range response.Result.Tools {
			names[tool.Name] = true
		}
		if id == "coder" && (!names["bash"] || !names["task_progress"] || names["manage_projects"]) {
			t.Fatalf("coder inventory=%v", names)
		}
		if id == "orchestrator" && (!names["manage_projects"] || names["bash"]) {
			t.Fatalf("orchestrator inventory=%v", names)
		}
	}
	w = post(token, "", `{"jsonrpc":"2.0","id":1,"method":"tools/call","params":{"name":"read","_meta":{"claudecode/toolUseId":"exact-use-id"}}}`)
	if invoker.calls.Load() != 1 || !strings.Contains(w.Body.String(), "exact-use-id") {
		t.Fatalf("metadata not retained: %s", w.Body)
	}
	rec, err := endpoint.security.ValidateScopedToken(token)
	if err != nil {
		t.Fatal(err)
	}
	if _, err = endpoint.security.RevokeScopedToken(p.AccountScopeID, rec.ID); err != nil {
		t.Fatal(err)
	}
	w = post(token, "", `{"jsonrpc":"2.0","id":1,"method":"tools/call","params":{"name":"read"}}`)
	if w.Code != http.StatusUnauthorized || invoker.calls.Load() != 1 {
		t.Fatalf("revoked execution: %d calls=%d", w.Code, invoker.calls.Load())
	}
}
