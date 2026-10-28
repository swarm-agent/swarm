package api

import (
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
)

// Purpose: controlMCPIntegrate must retain delivery warnings in workflow results
// and preserve the unobserved Git-state diagnostic on rejected delivery. A local
// HTTP handler exercises the adapter's real dispatch/response boundary without
// duplicating the real-Git integration tests or starting a daemon.
func TestControlMCPIntegrateDeliveryResult(t *testing.T) {
	for _, rejected := range []bool{false, true} {
		c := &controlMCPCall{request: httptest.NewRequest(http.MethodPost, "/mcp", nil)}
		c.next = http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			w.Header().Set("Content-Type", "application/json")
			if r.Method == http.MethodGet {
				_, _ = w.Write([]byte(`{"task":{"id":"task","session_id":"session","worktree_branch":"agent/task","base_branch":"dev"}}`))
			} else if rejected {
				w.WriteHeader(http.StatusConflict)
				_, _ = w.Write([]byte(`{"error":"mismatch","delivery":{"freshness":"unknown","reason_code":"lane_branch_mismatch","reason":"detached"}}`))
			} else {
				_, _ = w.Write([]byte(`{"status":"integrated","integration":{"target_branch":"dev","resulting_target_head":"tip","warning":"Reattached detached HEAD"}}`))
			}
		})
		result, err := controlMCPIntegrate(c, map[string]any{"project_id": "project", "task_id": "task"}, "merge_task", "get_task")
		if rejected {
			if err == nil || !strings.Contains(err.Error(), "could not read this task's Git state") || !strings.Contains(err.Error(), "lane_branch_mismatch") {
				t.Fatalf("lost diagnostic: %v", err)
			}
			continue
		}
		if err != nil {
			t.Fatal(err)
		}
		receipt := result.(map[string]any)["integration"].(map[string]any)
		if receipt["warning"] != "Reattached detached HEAD" {
			t.Fatalf("lost warning: %#v", result)
		}
	}
}
