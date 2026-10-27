package api

import (
	"context"
	"errors"
	"net/http"
	"net/http/httptest"
	"path/filepath"
	"testing"

	"swarm/packages/swarmd/internal/jobautomation"
	"swarm/packages/swarmd/internal/signals"
	pebblestore "swarm/packages/swarmd/internal/store/pebble"
)

// Requirement: a job's workflows start whether or not its end-of-job Git
// assessment finished in time. Threat: an assessment that hit its budget
// (inspection_timeout) is treated as "commits not settled yet", so the job
// end is retried for hours and its workflow (here a when: always run step)
// never starts. Owners: JobAutomationResults (workerJobAutomationResult,
// jobViewGitUnread over workerJobResultFromView) and jobautomation.Runner.
// A bounded route fixture feeds the real result builder, store, feed and
// runner without provider work. The negative case keeps the existing wait for
// a genuinely unsettled assessment (unavailable without a transient reason).
func TestWorkerJobEndStartsWorkflowWhenGitUnread(t *testing.T) {
	for _, tc := range []struct {
		name    string
		code    string
		started bool
	}{
		{"inspection timeout", "inspection_timeout", true},
		{"not assessed yet", "", false},
	} {
		t.Run(tc.name, func(t *testing.T) {
			folder := t.TempDir()
			writeAutomationScript(t, folder, "notify.sh")
			assessment := map[string]any{"state": "unavailable", "freshness": "unknown", "reason": "Git inspection exceeded its time budget"}
			if tc.code != "" {
				assessment["reason_code"] = tc.code
			}
			handler := http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				switch r.URL.Path {
				case "/v3/workers/worker-test/runs/run-test":
					writeJSON(w, 200, map[string]any{"run": map[string]any{"status": "succeeded", "review": map[string]any{"project_id": "project-test", "task_id": "task-test"}}})
				case "/v3/workers/worker-test":
					writeJSON(w, 200, map[string]any{"worker": map[string]any{"role": "coder", "name": "daily"}})
				case "/v3/projects/project-test/tasks/task-test":
					writeJSON(w, 200, map[string]any{"task": map[string]any{"status": "needs_review", "agent": "coder", "workspace_path": folder,
						"worktree_branch": "agent/test", "base_commit": "aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa", "delivery_assessment": assessment}})
				default:
					http.NotFound(w, r)
				}
			})
			store, err := pebblestore.Open(filepath.Join(t.TempDir(), "unread.pebble"))
			if err != nil {
				t.Fatal(err)
			}
			t.Cleanup(func() { _ = store.Close() })
			feed := pebblestore.NewSignalStore(store)
			server := &Server{signals: feed}
			request := requestWithScopedToken(withTestPrincipal(httptest.NewRequest(http.MethodGet, "/mcp", nil)), &pebblestore.ScopedTokenRecord{Scopes: []string{"sessions:read", "swarm:read"}})
			server.jobAutomationCallFn = func(context.Context) (*controlMCPCall, error) {
				return &controlMCPCall{request: request, next: handler, server: server}, nil
			}
			account := testPrincipal().AccountScopeID
			host := newAutomationHost(t)
			runner := newAutomationRunner(t, server, store, feed, account, host)
			if _, _, err := runner.Store().Put(account, jobautomation.Automation{Name: "notify", Scope: jobautomation.Scope{Folder: folder}, When: jobautomation.WhenAlways, Enabled: true,
				Steps: []jobautomation.Step{{ID: "notify", Type: jobautomation.StepRun, Command: "notify.sh"}}}); err != nil {
				t.Fatal(err)
			}
			signals.NewEmitter(feed).Emit(pebblestore.Signal{Kind: "worker.run.succeeded", Account: account, DedupKey: "worker.run:run-test", Summary: "daily check finished",
				Refs: map[string]string{"worker_id": "worker-test", "run_id": "run-test"}})
			err = runner.ProcessOnce(context.Background())
			list, listErr := runner.Store().JobDeliveries(account, "worker/worker-test/run-test")
			if listErr != nil {
				t.Fatal(listErr)
			}
			if !tc.started {
				if !errors.Is(err, jobautomation.ErrNotReady) || len(list) != 0 || len(host.ran) != 0 {
					t.Fatalf("unsettled job end started: err=%v deliveries=%d ran=%d", err, len(list), len(host.ran))
				}
				return
			}
			if err != nil {
				t.Fatal(err)
			}
			if len(list) != 1 || list[0].State != jobautomation.StateSucceeded || len(host.ran) != 1 {
				t.Fatalf("workflow did not start: deliveries=%+v ran=%d", list, len(host.ran))
			}
		})
	}
}
