package api

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	store "swarm/packages/swarmd/internal/store/pebble"
	"testing"
)

// Purpose: controlMCPGetJob must preserve canonical V3 review evidence including
// stale=true rather than silently presenting an old OK as current. A read-only
// transport fixture is the narrowest MCP projection proof.
func TestReviewerGetJobEvidence(t *testing.T) {
	for _, stale := range []bool{false, true} {
		evidence := map[string]any{"verdict": "ok", "head": "head", "reviewer_job_id": "worker/reviewer/run", "model": "p/m", "at": float64(123), "stale": stale, "project_id": "p1", "task_id": "t1"}
		h := http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			w.Header().Set("Content-Type", "application/json")
			if r.URL.Path == "/v3/workers/w1/runs/r1" {
				json.NewEncoder(w).Encode(map[string]any{"run": map[string]any{"status": "succeeded", "review": evidence}})
				return
			}
			if r.URL.Path == "/v3/projects/p1/tasks/t1" {
				json.NewEncoder(w).Encode(map[string]any{"task": map[string]any{"id": "t1", "project_id": "p1", "status": "needs_review"}})
				return
			}
			w.WriteHeader(404)
		})
		r := httptest.NewRequest(http.MethodPost, "/mcp", nil)
		r = r.WithContext(context.WithValue(r.Context(), productScopedTokenRequestContextKey, &store.ScopedTokenRecord{AccountScopeID: "acct", UserID: "user", Scopes: []string{"sessions:read"}}))
		result, err := controlMCPGetJob(&controlMCPCall{request: r, next: h}, map[string]any{"job_id": "worker/w1/r1"})
		if err != nil {
			t.Fatal(err)
		}
		review := controlMCPMap(controlMCPMap(result)["review"])
		for _, name := range []string{"verdict", "head", "reviewer_job_id", "model", "at", "stale"} {
			if review[name] != evidence[name] {
				t.Fatal(name, review)
			}
		}
	}
}
