package api

import (
	"context"
	"net/http"
	"net/http/httptest"
	"testing"

	pebblestore "swarm/packages/swarmd/internal/store/pebble"
)

// Requirement: a completed follow-up must obtain its head through the canonical
// task result even when the public job state remains blocked/in_progress.
// Threat: presentation status drops Git and creates a headless delivery.
// Owners: JobAutomationResults and workerJobAutomationResult. A bounded route
// fixture exercises the real view/result conversion, including missing commit
// assessment and the emitted exact-follow-up blocker, without provider work.
func TestWorkerAutomationFollowupHead(t *testing.T) {
	for _, status := range []string{"blocked", "in_progress"} {
		t.Run(status, func(t *testing.T) {
			folder := t.TempDir()
			const head = "bbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbb"
			handler := http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				switch r.URL.Path {
				case "/v3/workers/worker-test/runs/run-test":
					writeJSON(w, 200, map[string]any{"run": map[string]any{"status": "succeeded", "followup_status": "completed", "review": map[string]any{"project_id": "project-test", "task_id": "task-test"}}})
				case "/v3/workers/worker-test":
					writeJSON(w, 200, map[string]any{"worker": map[string]any{"role": "coder"}})
				case "/v3/projects/project-test/tasks/task-test":
					writeJSON(w, 200, map[string]any{"task": map[string]any{"status": status, "agent": "coder", "workspace_path": folder, "worktree_branch": "agent/test", "base_commit": "aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa", "delivery_assessment": map[string]any{"state": "candidate_work", "source_oid": head}}})
				default:
					http.NotFound(w, r)
				}
			})
			request := requestWithScopedToken(withTestPrincipal(httptest.NewRequest(http.MethodGet, "/mcp", nil)), &pebblestore.ScopedTokenRecord{Scopes: []string{"sessions:read", "swarm:read"}})
			server := &Server{}
			server.jobAutomationCallFn = func(context.Context) (*controlMCPCall, error) {
				return &controlMCPCall{request: request, next: handler, server: server}, nil
			}
			result, err := server.JobAutomationResults().Result(context.Background(), pebblestore.Signal{Kind: "worker.run.failed", Refs: map[string]string{"worker_id": "worker-test", "run_id": "run-test"}, Attrs: map[string]string{"error": "blocked: approval required"}}, true)
			if err != nil {
				t.Fatal(err)
			}
			if result == nil || result.Git == nil || result.Git.Head != head || result.Error != "blocked: approval required" || result.Folder != folder {
				t.Fatalf("result = %+v", result)
			}
		})
	}
}
