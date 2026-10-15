package api

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	pebblestore "swarm/packages/swarmd/internal/store/pebble"
	"testing"
)

// Purpose: the MCP get-job boundary must not describe interrupted work as done.
// Stub transport isolates jobView's precedence over a successful run receipt.
func TestControlMCPGetJobFollowupFailure(t *testing.T) {
	for _, status := range []string{"interrupted", "failed"} {
		h := http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			w.Header().Set("Content-Type", "application/json")
			json.NewEncoder(w).Encode(map[string]any{"run": map[string]any{"status": "succeeded", "followup_status": status, "followup_error": "daemon shutdown", "sandbox_cleanup_error": "engine unavailable"}})
		})
		request := httptest.NewRequest("POST", "/mcp", nil)
		request = request.WithContext(context.WithValue(request.Context(), productScopedTokenRequestContextKey, &pebblestore.ScopedTokenRecord{AccountScopeID: "acct", UserID: "user", Scopes: []string{"sessions:read"}}))
		out, err := controlMCPGetJob(&controlMCPCall{request: request, next: h}, map[string]any{"job_id": "worker/w1/r1"})
		if err != nil {
			t.Fatal(err)
		}
		b, _ := json.Marshal(out)
		for _, want := range []string{`"status":"failed"`, `"followup_status":"` + status + `"`, `"followup_error":"daemon shutdown"`, `"sandbox_cleanup_error":"engine unavailable"`} {
			if !strings.Contains(string(b), want) {
				t.Fatalf("missing %s: %s", want, b)
			}
		}
		if strings.Contains(string(b), "the work is done") {
			t.Fatal(string(b))
		}
	}
}

// Purpose: jobView must preserve actionable permission waits over active
// follow-up receipts. The MCP tool boundary proves status and caller guidance.
func TestControlMCPGetJobFollowupPermissionWait(t *testing.T) {
	for _, followup := range []string{"running", "queued"} {
		for _, tc := range []struct{ tool, status, next string }{{"ask_user", "needs_input", "swarm_answer_job"}, {"bash", "waiting_on_approval", "see waiting_on"}} {
			t.Run(followup+tc.status, func(t *testing.T) {
				h := http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
					w.Header().Set("Content-Type", "application/json")
					if strings.Contains(r.URL.Path, "/sessions/") {
						json.NewEncoder(w).Encode(map[string]any{"pending_permissions": []map[string]any{{"id": "permission", "tool_name": tc.tool}}, "active_run_intent": map[string]any{"status": "running"}})
					} else {
						json.NewEncoder(w).Encode(map[string]any{"run": map[string]any{"status": "succeeded", "session_id": "session", "followup_status": followup}})
					}
				})
				request := httptest.NewRequest("POST", "/mcp", nil)
				request = request.WithContext(context.WithValue(request.Context(), productScopedTokenRequestContextKey, &pebblestore.ScopedTokenRecord{AccountScopeID: "acct", UserID: "user", Scopes: []string{"sessions:read"}}))
				out, err := controlMCPGetJob(&controlMCPCall{request: request, next: h}, map[string]any{"job_id": "worker/w1/r1"})
				if err != nil {
					t.Fatal(err)
				}
				b, _ := json.Marshal(out)
				if !strings.Contains(string(b), `"status":"`+tc.status+`"`) || !strings.Contains(string(b), tc.next) {
					t.Fatal(string(b))
				}
			})
		}
	}
}
