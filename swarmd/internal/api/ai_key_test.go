package api

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"
)

// Purpose: an AI key reached over a tunnel is the only credential between an
// AI client and this machine, so its level must hold on the gateway itself.
// A read key sees and calls only read tools; a write key may also start and
// steer sessions; neither may approve tool calls or manage workers, limits,
// models, custom agents, client keys or ChatGPT sign-in. A full key (the owner
// chose to let an AI run the box) sees every tool but, like every AI key,
// reaches nothing outside /mcp even though it carries admin. Ordinary scoped
// tokens are unchanged.
func TestAIKeysLimitSwarmControlTools(t *testing.T) {
	s, _, sec, cleanup := setupScopedAuthTestServer(t)
	defer cleanup()
	actor, err := s.identitySessions.ActorForCurrentSelection()
	if err != nil {
		t.Fatal(err)
	}
	mint := func(body string) (int, map[string]any) {
		t.Helper()
		r := requestWithTestPrincipalForAccount(httptest.NewRequest(http.MethodPost, "/v3/auth/tokens", strings.NewReader(body)), actor.UserID, actor.AccountScopeID)
		w := httptest.NewRecorder()
		s.handleAuthTokens(w, r)
		var out map[string]any
		_ = json.Unmarshal(w.Body.Bytes(), &out)
		return w.Code, out
	}
	for _, body := range []string{
		`{"name":"k","ai_access":"admin"}`,
		`{"name":"k","ai_access":"read","scopes":["admin"]}`,
		`{"name":"k","ai_access":"write","agent_name":"frontdesk"}`,
		`{"name":"k","ai_access":"read","worker_id":"w"}`,
		`{"name":"k","ai_access":"read","expires_in_seconds":40000000}`,
	} {
		if code, out := mint(body); code != http.StatusBadRequest {
			t.Fatalf("minted %s: %d %v", body, code, out)
		}
	}
	key := func(level string) string {
		t.Helper()
		code, out := mint(`{"name":"claude","ai_access":"` + level + `"}`)
		if code != http.StatusOK {
			t.Fatalf("mint %s: %d %v", level, code, out)
		}
		record, _ := out["record"].(map[string]any)
		expires, _ := record["expires_at"].(float64)
		if left := time.Until(time.UnixMilli(int64(expires))); left < 29*24*time.Hour || left > 31*24*time.Hour {
			t.Fatalf("%s key default lifetime = %s, want 30 days", level, left)
		}
		return out["token"].(string)
	}
	readKey, writeKey := key("read"), key("write")
	plain, _, err := sec.CreateScopedToken("sdk", []string{"sessions:read", "sessions:write"}, actor.AccountScopeID, actor.UserID, time.Hour, "", "")
	if err != nil {
		t.Fatal(err)
	}

	handler := s.ContainerSDKHandler()
	listTools := func(token string) map[string]bool {
		t.Helper()
		r := httptest.NewRequest("POST", "http://127.0.0.1:7783/mcp", strings.NewReader(`{"jsonrpc":"2.0","id":1,"method":"tools/list"}`))
		r.RemoteAddr = "192.0.2.1:40000"
		r.Header.Set("Content-Type", "application/json")
		r.Header.Set("Authorization", "Bearer "+token)
		w := httptest.NewRecorder()
		handler.ServeHTTP(w, r)
		var out struct {
			Result struct {
				Tools []struct {
					Name string `json:"name"`
				} `json:"tools"`
			} `json:"result"`
		}
		if err := json.Unmarshal(w.Body.Bytes(), &out); err != nil || len(out.Result.Tools) == 0 {
			t.Fatalf("tools/list: %d %s", w.Code, w.Body.String())
		}
		names := map[string]bool{}
		for _, tool := range out.Result.Tools {
			names[tool.Name] = true
		}
		return names
	}
	readTools, writeTools, plainTools := listTools(readKey), listTools(writeKey), listTools(plain)
	for _, name := range []string{"swarm_list_sessions", "swarm_get_session", "swarm_get_usage", "swarm_list_models"} {
		if !readTools[name] || !writeTools[name] {
			t.Fatalf("%s missing for AI keys", name)
		}
	}
	for _, name := range []string{"swarm_start_session", "swarm_send_message", "swarm_stop_run"} {
		if readTools[name] {
			t.Fatalf("read key lists %s", name)
		}
		if !writeTools[name] {
			t.Fatalf("write key misses %s", name)
		}
	}
	for _, name := range []string{"swarm_resolve_permission", "swarm_integrate_task", "swarm_create_worker", "swarm_set_usage_limits", "swarm_set_agent_model",
		"swarm_define_agent", "swarm_create_client_key", "swarm_connect_chatgpt"} {
		if readTools[name] || writeTools[name] {
			t.Fatalf("AI key lists %s", name)
		}
	}
	if !readTools["swarm_list_agents"] || !writeTools["swarm_list_agents"] {
		t.Fatal("read and write keys must list agents")
	}
	// A full key (the AI that runs this box) sees every tool.
	fullKey := key("full")
	fullTools := listTools(fullKey)
	for _, tool := range controlMCPTools() {
		if !fullTools[tool.Name] {
			t.Fatalf("full key misses %s", tool.Name)
		}
	}
	if !plainTools["swarm_start_session"] || !plainTools["swarm_resolve_permission"] {
		t.Fatal("ordinary scoped tokens must keep the full tool list")
	}

	// Calls are refused at the gateway, before any route runs.
	callRead := controlMCPTestCaller(t, s, readKey)
	if text, isErr := callRead("swarm_start_session", map[string]any{"prompt": "rm -rf /"}); !isErr || !strings.Contains(text, "limited to swarm:read") {
		t.Fatalf("read key started a session: %v %s", isErr, text)
	}
	if text, isErr := callRead("swarm_list_sessions", map[string]any{}); isErr {
		t.Fatalf("read key cannot list sessions: %s", text)
	}
	callWrite := controlMCPTestCaller(t, s, writeKey)
	for tool, args := range map[string]map[string]any{
		"swarm_resolve_permission": {"session_id": "s", "permission_id": "p", "decision": "approve"},
		"swarm_set_usage_limits":   {"daily_usd": 1000},
	} {
		if text, isErr := callWrite(tool, args); !isErr || !strings.Contains(text, "limited to") {
			t.Fatalf("write key called %s: %v %s", tool, isErr, text)
		}
	}

	// The raw routes would bypass the per-tool level: AI keys get /mcp only,
	// on the gateway and on the daemon's own API.
	// A full key carries admin, so it must be held to /mcp just the same.
	for _, h := range []http.Handler{s.ContainerSDKHandler(), s.Handler()} {
		for _, token := range []string{writeKey, fullKey} {
			for _, route := range []struct{ method, path, body string }{
				{"GET", "/v3/sessions", ""},
				{"POST", "/v3/sessions/s/permissions/p/resolve", `{"action":"allow_once"}`},
				{"POST", "/v3/sessions/s/messages", `{"content":"x"}`},
				{"GET", "/v1/auth/credentials", ""},
				{"POST", "/v3/auth/tokens", `{"name":"x","scopes":["admin"]}`},
			} {
				r := httptest.NewRequest(route.method, "http://127.0.0.1:7783"+route.path, strings.NewReader(route.body))
				r.RemoteAddr = "192.0.2.1:40000"
				r.Header.Set("Content-Type", "application/json")
				r.Header.Set("Authorization", "Bearer "+token)
				w := httptest.NewRecorder()
				h.ServeHTTP(w, r)
				if w.Code != http.StatusForbidden || !strings.Contains(w.Body.String(), "only through Swarm Control") {
					t.Fatalf("AI key reached %s %s: %d %s", route.method, route.path, w.Code, w.Body.String())
				}
			}
		}
	}
}
