package api

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"testing"

	store "swarm/packages/swarmd/internal/store/pebble"
)

// Requirement: MCP can cancel ordinary delegated Coder sessions through the
// existing authenticated V3 subagent stop route. The primary executor identity
// is absent on these children. Owners: controlMCPStopRun, dispatch and
// handleSessionsV3SubagentStop. This adapter test covers exact routing and
// rejection without pretending to prove process cancellation; the live F2.2
// fixture separately checks a real model's active Bash and descendant cleanup.
func TestControlMCPDelegatedSubagentStop(t *testing.T) {
	for _, scenario := range []string{"accepted", "read-only", "foreign-session", "unrelated-session", "stop-rejected"} {
		t.Run(scenario, func(t *testing.T) {
			writes := 0
			scopes := []string{"sessions:read", "sessions:write"}
			if scenario == "read-only" {
				scopes = []string{"sessions:read"}
			}
			c := &controlMCPCall{request: requestWithScopedToken(httptest.NewRequest(http.MethodPost, "/mcp", nil), &store.ScopedTokenRecord{Scopes: scopes})}
			c.next = http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				if r.Method == http.MethodGet && r.URL.Path == "/v3/sessions/child-test" {
					if scenario == "foreign-session" {
						writeJSON(w, http.StatusNotFound, map[string]any{"error": "session not found"})
						return
					}
					lineage := "delegated_subagent"
					if scenario == "unrelated-session" {
						lineage = ""
					}
					writeJSON(w, http.StatusOK, map[string]any{"session": map[string]any{"id": "child-test", "metadata": map[string]any{"lineage_kind": lineage}}})
					return
				}
				if r.Method != http.MethodPost || r.URL.Path != "/v3/subagents:stop" {
					t.Fatalf("unexpected route: %s %s", r.Method, r.URL.Path)
				}
				writes++
				var body map[string]any
				if err := json.NewDecoder(r.Body).Decode(&body); err != nil {
					t.Fatal(err)
				}
				if len(body) != 1 || body["session_id"] != "child-test" {
					t.Fatalf("unexpected stop body: %#v", body)
				}
				if scenario == "stop-rejected" {
					writeJSON(w, http.StatusForbidden, map[string]any{"error": "stop refused"})
					return
				}
				writeJSON(w, http.StatusOK, map[string]any{"status": "cancelled"})
			})
			out, err := controlMCPStopRun(c, map[string]any{"session_id": "child-test"})
			if scenario == "accepted" {
				if err != nil || out.(map[string]any)["run_status"] != "stop_requested" || writes != 1 {
					t.Fatalf("stop result=%v err=%v writes=%d", out, err, writes)
				}
			} else {
				if err == nil || out != nil {
					t.Fatalf("rejection lost: out=%v err=%v", out, err)
				}
				expected := 0
				if scenario == "stop-rejected" {
					expected = 1
				}
				if writes != expected {
					t.Fatalf("unauthorized or duplicate mutation: %d", writes)
				}
			}
		})
	}
}
