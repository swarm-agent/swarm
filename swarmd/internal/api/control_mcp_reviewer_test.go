package api

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	store "swarm/packages/swarmd/internal/store/pebble"
	"testing"
)

// Purpose: controlMCPDeliverJob must refuse reviewer deliveries before any
// publication/integration mutation, even when a task receipt suggests otherwise.
// Stubbed canonical reads are the narrowest tool-boundary proof; this does not
// prove detached admission or direct task-route refusal.
func TestControlMCPReviewerDeliveryRefused(t *testing.T) {
	mutations := 0
	h := http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		if r.Method != http.MethodGet {
			mutations++
			w.WriteHeader(500)
			return
		}
		if r.URL.Path != "/v3/workers/w1/runs/r1" {
			t.Errorf("unexpected read %s", r.URL.Path)
			w.WriteHeader(404)
			return
		}
		json.NewEncoder(w).Encode(map[string]any{"run": map[string]any{"role": "reviewer", "review": map[string]any{"project_id": "p1", "task_id": "t1"}}})
	})
	r := httptest.NewRequest(http.MethodPost, "/mcp", nil)
	r = r.WithContext(context.WithValue(r.Context(), productScopedTokenRequestContextKey, &store.ScopedTokenRecord{AccountScopeID: "acct", UserID: "user", Scopes: []string{"sessions:read", "sessions:write"}}))
	_, err := controlMCPDeliverJob(&controlMCPCall{request: r, next: h}, map[string]any{"job_id": "worker/w1/r1"})
	if err == nil || !strings.Contains(err.Error(), "report-only") {
		t.Fatal(err)
	}
	if mutations != 0 {
		t.Fatalf("unauthorized delivery mutations: %d", mutations)
	}
}
