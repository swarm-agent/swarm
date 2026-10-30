package api

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"path/filepath"
	"strings"
	"testing"

	pebblestore "swarm/packages/swarmd/internal/store/pebble"
)

// Requirement: a new run of a worker job starts with no earlier run's outcome
// on its task. When a follow-up run of the job is admitted (its receipt's own
// run settled), the task's last_error and its failed/blocked action_needed are
// cleared, so swarm_get_job shows none of them while the follow-up runs. An
// integration error and integration guidance stay; the job's first run, a
// task moved to another session and an archived task are left unchanged.
// Threat (socials machine, 2026-10-30): while a follow-up ran, the job view
// still showed the morning's "blocked: Narration..." last_error and
// "Action Needed: Worker run failed (...)" next to status in_progress.
// Owners: sessionV3Executor.clearWorkerFollowupTaskOutcome (called from
// EnqueueRun when a run is admitted) and
// SessionStore.ClearProjectTaskRunOutcomeForSession over a real Pebble store;
// controlMCPCall.jobState reads the stored task. Calling the hook directly is
// the narrowest layer: a provider turn adds nothing to what the hook writes.
func TestWorkerFollowupStartClearsStaleTaskOutcome(t *testing.T) {
	const blocker = "blocked: Narration can't be generated"
	const staleAction = "Action Needed: Worker run failed (" + blocker + "). Retry or reassign task."
	const integrateAction = "Action Needed: Review changes and integrate 2 commit(s) into dev"
	for _, tc := range []struct {
		name                      string
		receiptStatus             string
		taskSession               string
		archived                  bool
		integrationError          string
		action                    string
		wantLastError, wantAction string
	}{
		{name: "follow-up after a blocked run", receiptStatus: "failed", action: staleAction},
		{name: "follow-up after a succeeded receipt", receiptStatus: "succeeded", action: staleAction},
		{name: "integration error and guidance stay", receiptStatus: "failed", integrationError: "merge conflict", action: integrateAction,
			wantLastError: blocker, wantAction: integrateAction},
		{name: "first run still running", receiptStatus: "running", action: staleAction, wantLastError: blocker, wantAction: staleAction},
		{name: "task moved to another session", receiptStatus: "failed", taskSession: "other-session", action: staleAction, wantLastError: blocker, wantAction: staleAction},
		{name: "archived task", receiptStatus: "failed", archived: true, action: staleAction, wantLastError: blocker, wantAction: staleAction},
	} {
		t.Run(tc.name, func(t *testing.T) {
			f := newConfirmationFixture(t, filepath.Join(t.TempDir(), "store"), nil)
			p := testPrincipal()
			db := f.sessions.Store()
			w, err := db.WorkerStore().CreateWorker(p.AccountScopeID, p.UserID, pebblestore.CreateWorkerRequest{Name: "Narrator", Role: pebblestore.WorkerRoleCoder, IdempotencyKey: "narrator"}, nil)
			if err != nil {
				t.Fatal(err)
			}
			const sessionID = "worker-execution-narrator"
			if _, err := db.WorkerStore().RecordWorkerRun(p.AccountScopeID, pebblestore.WorkerRunRecord{ID: "receipt-1", WorkerID: w.ID, SessionID: sessionID, Status: tc.receiptStatus, Error: blocker}); err != nil {
				t.Fatal(err)
			}
			taskSession := sessionID
			if tc.taskSession != "" {
				taskSession = tc.taskSession
			}
			task := &pebblestore.ProjectTaskRecord{ID: "task-job", ProjectID: "project-a", AccountID: p.AccountScopeID, Title: "Narrated video", Agent: "coder",
				WorkerID: w.ID, WorkerRunID: "receipt-1", SessionID: taskSession, Status: "failed", LastError: blocker, ActionNeeded: tc.action, Archived: tc.archived}
			if tc.integrationError != "" {
				task.Integration = &pebblestore.ProjectTaskIntegration{Error: tc.integrationError}
			}
			if err := db.PutProjectTask(p.AccountScopeID, task); err != nil {
				t.Fatal(err)
			}

			f.server.v3SessionExecutor.clearWorkerFollowupTaskOutcome(sessionV3ExecutorJob{Principal: p, SessionID: sessionID, RunID: "followup-1"},
				map[string]any{"worker_id": w.ID, "worker_execution_run_id": "receipt-1", "project_id": "project-a", "task_id": "task-job"})

			stored, found, err := db.GetProjectTask(p.AccountScopeID, "project-a", "task-job")
			if err != nil || !found {
				t.Fatalf("task: %v %v", found, err)
			}
			if stored.LastError != tc.wantLastError || stored.ActionNeeded != tc.wantAction {
				t.Fatalf("task last_error %q action %q, want %q %q", stored.LastError, stored.ActionNeeded, tc.wantLastError, tc.wantAction)
			}

			// The job view while the follow-up runs reads the stored task.
			raw, _ := json.Marshal(stored)
			var taskView map[string]any
			_ = json.Unmarshal(raw, &taskView)
			taskView["status"] = "in_progress"
			taskServed := false
			handler := http.HandlerFunc(func(rw http.ResponseWriter, r *http.Request) {
				if strings.HasPrefix(r.URL.Path, "/v3/sessions/") {
					writeJSON(rw, 200, map[string]any{})
					return
				}
				switch r.URL.Path {
				case "/v3/workers/worker-test/runs/run-test":
					writeJSON(rw, 200, map[string]any{"run": map[string]any{"status": tc.receiptStatus, "error": blocker, "followup_status": "running",
						"review": map[string]any{"project_id": "project-a", "task_id": "task-job"}}})
				case "/v3/workers/worker-test":
					writeJSON(rw, 200, map[string]any{"worker": map[string]any{"role": "coder"}})
				case "/v3/projects/project-a/tasks/task-job":
					taskServed = true
					writeJSON(rw, 200, map[string]any{"task": taskView})
				default:
					http.NotFound(rw, r)
				}
			})
			request := requestWithScopedToken(withTestPrincipal(httptest.NewRequest(http.MethodGet, "/mcp", nil)), &pebblestore.ScopedTokenRecord{Scopes: []string{"sessions:read", "swarm:read"}})
			call := &controlMCPCall{request: request, next: handler, server: &Server{}}
			state, _, err := call.jobState(controlMCPJobWorker, "worker-test", "run-test")
			if err != nil {
				t.Fatal(err)
			}
			if !taskServed || state["status"] != "in_progress" {
				t.Fatalf("job view did not read the running task: served %v state %+v", taskServed, state)
			}
			got := func(key string) string { s, _ := state[key].(string); return s }
			if got("error") != "" || got("last_error") != tc.wantLastError || got("action_needed") != tc.wantAction {
				t.Fatalf("job view error %q last_error %q action_needed %q, want \"\" %q %q", got("error"), got("last_error"), got("action_needed"), tc.wantLastError, tc.wantAction)
			}
		})
	}
}
