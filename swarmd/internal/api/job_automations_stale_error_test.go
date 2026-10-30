package api

import (
	"context"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"swarm/packages/swarmd/internal/jobautomation"
	pebblestore "swarm/packages/swarmd/internal/store/pebble"
)

// Requirement: a worker job's error describes only its latest run. The run
// receipt keeps its first run's outcome after follow-ups; once a follow-up
// starts, swarm_get_job's error is that follow-up's own (none while it runs
// or after it completed), and the workflow trigger result of a follow-up
// that succeeded carries no error.
// Threat (socials machine, 2026-10-30): an earlier "blocked: ..." error
// survived later successful follow-ups, so the job view still showed it and
// the workflow read the follow-up's end as a blocked answer to its reopen.
// Owners: controlMCPCall.jobState, JobAutomationResults and
// workerJobAutomationResult. A bounded route fixture exercises the real view
// and result conversion without provider work; it is the narrowest layer
// that sees both the receipt and the signal.
func TestWorkerJobErrorDescribesLatestRun(t *testing.T) {
	const stale = "blocked: Narration can't be generated"
	const head = "bbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbb"
	for _, tc := range []struct {
		name, followupStatus, followupError, signalKind, signalError string
		wantViewError, wantResultError, wantResultStatus             string
	}{
		{name: "follow-up completed", followupStatus: "completed", signalKind: "worker.run.succeeded",
			wantResultStatus: jobautomation.StatusSucceeded},
		{name: "follow-up running", followupStatus: "running"},
		{name: "follow-up blocked", followupStatus: "completed", signalKind: "worker.run.failed", signalError: "blocked: approval required",
			wantResultError: "blocked: approval required", wantResultStatus: jobautomation.StatusFailed},
		{name: "follow-up failed", followupStatus: "failed", followupError: "provider error", signalKind: "worker.run.failed", signalError: "provider error",
			wantViewError: "provider error", wantResultError: "provider error", wantResultStatus: jobautomation.StatusFailed},
		{name: "no follow-up keeps the run's own blocker", signalKind: "worker.run.failed", signalError: stale,
			wantViewError: stale, wantResultError: stale, wantResultStatus: jobautomation.StatusFailed},
	} {
		t.Run(tc.name, func(t *testing.T) {
			folder := t.TempDir()
			run := map[string]any{"status": "failed", "error": stale, "review": map[string]any{"project_id": "project-test", "task_id": "task-test"}}
			if tc.followupStatus != "" {
				run["followup_status"] = tc.followupStatus
			}
			if tc.followupError != "" {
				run["followup_error"] = tc.followupError
			}
			handler := http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				switch r.URL.Path {
				case "/v3/workers/worker-test/runs/run-test":
					writeJSON(w, 200, map[string]any{"run": run})
				case "/v3/workers/worker-test":
					writeJSON(w, 200, map[string]any{"worker": map[string]any{"role": "coder"}})
				case "/v3/projects/project-test/tasks/task-test":
					writeJSON(w, 200, map[string]any{"task": map[string]any{"status": "needs_review", "agent": "coder", "workspace_path": folder, "worktree_branch": "agent/test",
						"base_commit": "aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa", "delivery_assessment": map[string]any{"state": "candidate_work", "source_oid": head}}})
				default:
					http.NotFound(w, r)
				}
			})
			request := requestWithScopedToken(withTestPrincipal(httptest.NewRequest(http.MethodGet, "/mcp", nil)), &pebblestore.ScopedTokenRecord{Scopes: []string{"sessions:read", "swarm:read"}})
			server := &Server{}
			call := &controlMCPCall{request: request, next: handler, server: server}
			server.jobAutomationCallFn = func(context.Context) (*controlMCPCall, error) { return call, nil }

			state, _, err := call.jobState(controlMCPJobWorker, "worker-test", "run-test")
			if err != nil {
				t.Fatal(err)
			}
			if got, _ := state["error"].(string); got != tc.wantViewError {
				t.Fatalf("view error = %q, want %q (state %+v)", got, tc.wantViewError, state)
			}
			if tc.signalKind == "" {
				return
			}
			followup := ""
			if tc.followupStatus != "" {
				followup = "followup-1"
			}
			result, err := server.JobAutomationResults().Result(context.Background(), pebblestore.Signal{Kind: tc.signalKind, DedupKey: "worker.run:run-test:" + followup,
				Refs: map[string]string{"worker_id": "worker-test", "run_id": "run-test"}, Attrs: map[string]string{"error": tc.signalError}}, true)
			if err != nil {
				t.Fatal(err)
			}
			if result == nil || result.Status != tc.wantResultStatus || result.Error != tc.wantResultError {
				t.Fatalf("result = %+v, want status %q error %q", result, tc.wantResultStatus, tc.wantResultError)
			}
			if wantBlocked := strings.HasPrefix(tc.wantResultError, "blocked: "); result.EndedBlocked() != wantBlocked {
				t.Fatalf("EndedBlocked = %v, want %v", result.EndedBlocked(), wantBlocked)
			}
		})
	}
}
