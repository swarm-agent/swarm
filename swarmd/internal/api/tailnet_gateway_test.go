package api

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
)

// Purpose: behind Tailscale Serve the tailnet policy is the only credential an
// AI device carries, so its grant must set the ceiling exactly as an AI key
// does. Read sees and calls only read tools; write may also start and steer
// sessions; approve and manage are never honored. The header is trusted only
// from loopback (Serve on this machine), only on /mcp, and a bearer request
// keeps the ordinary path.
func TestTailnetGatewayLevels(t *testing.T) {
	s, _, _, cleanup := setupScopedAuthTestServer(t)
	defer cleanup()
	handler := s.TailnetGatewayHandler()

	send := func(path, remote, caps, body string) *httptest.ResponseRecorder {
		t.Helper()
		r := httptest.NewRequest("POST", "http://127.0.0.1:7783"+path, strings.NewReader(body))
		r.RemoteAddr = remote
		r.Header.Set("Content-Type", "application/json")
		if caps != "" {
			r.Header.Set(TailscaleAppCapsHeader, caps)
		}
		w := httptest.NewRecorder()
		handler.ServeHTTP(w, r)
		return w
	}
	grant := func(levels ...string) string {
		values := make([]map[string]string, 0, len(levels))
		for _, level := range levels {
			values = append(values, map[string]string{"level": level})
		}
		encoded, _ := json.Marshal(map[string]any{SwarmTailnetCapability: values, "example.com/cap/other": []map[string]string{{"level": "write"}}})
		return string(encoded)
	}
	listTools := func(caps string) map[string]bool {
		t.Helper()
		w := send("/mcp", "127.0.0.1:50000", caps, `{"jsonrpc":"2.0","id":1,"method":"tools/list"}`)
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

	read, write := listTools(grant("read")), listTools(grant("read", "write"))
	escalate := listTools(grant("read", "approve", "manage"))
	if !read["swarm_list_sessions"] || !write["swarm_list_sessions"] {
		t.Fatal("read tools missing")
	}
	if read["swarm_start_session"] || escalate["swarm_start_session"] || !write["swarm_start_session"] {
		t.Fatal("write tools do not follow the grant")
	}
	for _, name := range []string{"swarm_resolve_permission", "swarm_create_worker", "swarm_set_usage_limits", "swarm_set_agent_model"} {
		if read[name] || write[name] || escalate[name] {
			t.Fatalf("tailnet grant lists %s", name)
		}
	}

	call := func(caps, tool string) string {
		t.Helper()
		body, _ := json.Marshal(map[string]any{"jsonrpc": "2.0", "id": 1, "method": "tools/call", "params": map[string]any{"name": tool, "arguments": map[string]any{"prompt": "hi"}}})
		return send("/mcp", "127.0.0.1:50000", caps, string(body)).Body.String()
	}
	if out := call(grant("read"), "swarm_start_session"); !strings.Contains(out, "limited to swarm:read") {
		t.Fatalf("read grant started a session: %s", out)
	}

	for name, tc := range map[string]struct {
		path, remote, caps string
		want               int
	}{
		"no grant":            {"/mcp", "127.0.0.1:50000", "", http.StatusUnauthorized},
		"other app only":      {"/mcp", "127.0.0.1:50000", `{"example.com/cap/other":[{"level":"write"}]}`, http.StatusForbidden},
		"approve only":        {"/mcp", "127.0.0.1:50000", grant("approve", "manage"), http.StatusForbidden},
		"malformed":           {"/mcp", "127.0.0.1:50000", `not json`, http.StatusForbidden},
		"not from serve":      {"/mcp", "192.0.2.1:40000", grant("write"), http.StatusUnauthorized},
		"raw route":           {"/v3/sessions", "127.0.0.1:50000", grant("write"), http.StatusForbidden},
		"permission resolver": {"/v3/sessions/s1/permissions/p1/resolve", "127.0.0.1:50000", grant("write"), http.StatusForbidden},
	} {
		if w := send(tc.path, tc.remote, tc.caps, `{"jsonrpc":"2.0","id":1,"method":"tools/list"}`); w.Code != tc.want {
			t.Fatalf("%s: HTTP %d, want %d: %s", name, w.Code, tc.want, w.Body.String())
		}
	}

	// A bearer request ignores the header and keeps the ordinary checks.
	r := httptest.NewRequest("POST", "http://127.0.0.1:7783/mcp", strings.NewReader(`{"jsonrpc":"2.0","id":1,"method":"tools/list"}`))
	r.RemoteAddr = "127.0.0.1:50000"
	r.Header.Set("Content-Type", "application/json")
	r.Header.Set("Authorization", "Bearer swk_not-a-real-token")
	r.Header.Set(TailscaleAppCapsHeader, grant("write"))
	w := httptest.NewRecorder()
	handler.ServeHTTP(w, r)
	if w.Code != http.StatusUnauthorized {
		t.Fatalf("invalid bearer with a grant header: HTTP %d %s", w.Code, w.Body.String())
	}

	// The relay's control handler never reads the header.
	plain := httptest.NewRequest("POST", "http://127.0.0.1:7783/mcp", strings.NewReader(`{"jsonrpc":"2.0","id":1,"method":"tools/list"}`))
	plain.RemoteAddr = "127.0.0.1:50000"
	plain.Header.Set("Content-Type", "application/json")
	plain.Header.Set(TailscaleAppCapsHeader, grant("write"))
	w = httptest.NewRecorder()
	s.ContainerSDKHandler().ServeHTTP(w, plain)
	if w.Code != http.StatusUnauthorized {
		t.Fatalf("SDK handler honored a tailnet header: HTTP %d", w.Code)
	}
}

func TestTailnetSwarmLevelEncodings(t *testing.T) {
	for raw, want := range map[string]string{
		`{"swarmagent.dev/cap/swarm":[{"level":"read"}]}`:                   "read",
		`{"swarmagent.dev/cap/swarm":[{"level":"WRITE"},{"level":"read"}]}`: "write",
		`{"swarmagent.dev/cap/swarm":[{"level":"manage"}]}`:                 "",
		`{"swarmagent.dev/cap/swarm":["read"]}`:                             "",
		`=?utf-8?q?{"swarmagent.dev/cap/swarm":[{"level":"read"}]}?=`:       "read",
		`{"swarmagent.dev/cap/swarmx":[{"level":"write"}]}`:                 "",
	} {
		if got, _ := tailnetSwarmLevel(raw); got != want {
			t.Errorf("%s: got %q, want %q", raw, got, want)
		}
	}
}
