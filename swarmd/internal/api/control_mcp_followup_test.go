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
