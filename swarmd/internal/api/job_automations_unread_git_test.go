package api

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"

	"swarm/packages/swarmd/internal/jobautomation"
	"swarm/packages/swarmd/internal/signals"
	pebblestore "swarm/packages/swarmd/internal/store/pebble"
)

// unreadMergeDeliverer records the deliver requests a workflow makes; the
// merge itself is outside this test (the integrate path has its own tests).
type unreadMergeDeliverer struct {
	delivered []jobautomation.DeliverRequest
}

func (d *unreadMergeDeliverer) Deliver(_ context.Context, req jobautomation.DeliverRequest) (jobautomation.DeliverOutcome, error) {
	d.delivered = append(d.delivered, req)
	out := jobautomation.DeliverOutcome{Where: "merged " + req.Head}
	for _, item := range req.Deliverables {
		out.Delivered = append(out.Delivered, jobautomation.DeliveredItem{Name: item.Name, Kind: item.Kind, Where: out.Where})
	}
	return out, nil
}

func (d *unreadMergeDeliverer) Locate(context.Context, string, jobautomation.ResultSpec, jobautomation.AgentOutcome) (jobautomation.Deliverable, error) {
	return jobautomation.Deliverable{}, errors.New("not used")
}

func unreadGit(t *testing.T, dir string, args ...string) string {
	t.Helper()
	cmd := exec.Command("git", append([]string{"-c", "user.name=Test", "-c", "user.email=test@example.invalid", "-c", "commit.gpgsign=false"}, args...)...)
	cmd.Dir = dir
	cmd.Env = append(os.Environ(), "GIT_CONFIG_GLOBAL="+os.DevNull, "GIT_CONFIG_SYSTEM="+os.DevNull)
	out, err := cmd.CombinedOutput()
	if err != nil {
		t.Fatalf("git %v: %v\n%s", args, err, out)
	}
	return strings.TrimSpace(string(out))
}

// Requirement: a job's workflows start whether or not its end-of-job Git
// assessment finished in time, provided the job's head is known. Threats:
// (1) an assessment that hit its budget (inspection_timeout) holds the job
// end back for hours so its workflow never starts; (2) a result built with
// no head and no commits has no branch deliverable, so a merge step started
// from it is refused permanently with "nothing to deliver". Owners:
// JobAutomationResults (workerJobAutomationResult, jobViewGitUnread over
// workerJobResultFromView) and jobautomation.Runner. A bounded route fixture
// feeds the real result builder, store, feed and runner (run steps through
// the real host executor) without provider work; the deliver boundary only
// records the request. Negative cases keep waiting (ErrNotReady, no delivery,
// no step run): an unread state with no resolved head or commits, and an
// unavailable state without a transient reason.
func TestWorkerJobEndStartsWorkflowWhenGitUnread(t *testing.T) {
	for _, tc := range []struct {
		name    string
		code    string
		repo    bool
		started bool
	}{
		{"inspection timeout with resolved head", "inspection_timeout", true, true},
		{"inspection timeout without head or commits", "inspection_timeout", false, false},
		{"not assessed yet", "", true, false},
	} {
		t.Run(tc.name, func(t *testing.T) {
			folder := t.TempDir()
			base, head := "aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa", ""
			if tc.repo {
				unreadGit(t, folder, "init", "-q", "-b", "dev")
				unreadGit(t, folder, "commit", "-q", "--allow-empty", "-m", "base")
				base = unreadGit(t, folder, "rev-parse", "HEAD")
				unreadGit(t, folder, "checkout", "-q", "-b", "agent/test")
				unreadGit(t, folder, "commit", "-q", "--allow-empty", "-m", "work")
				head = unreadGit(t, folder, "rev-parse", "HEAD")
				unreadGit(t, folder, "checkout", "-q", "dev")
			}
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
						"worktree_branch": "agent/test", "base_commit": base, "delivery_assessment": assessment}})
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
			deliverer := &unreadMergeDeliverer{}
			runner := jobautomation.NewRunner(jobautomation.Options{
				Store: jobautomation.NewStore(store), Feed: feed, Account: func() string { return account }, Results: server.JobAutomationResults(),
				Run: host, Agent: server.JobAutomationAgents(), Deliver: deliverer, Emit: signals.NewEmitter(feed).Emit,
			})
			server.SetJobAutomations(runner)
			if err := runner.ProcessOnce(context.Background()); err != nil { // first start: cursor at the feed's end
				t.Fatal(err)
			}
			if _, _, err := runner.Store().Put(account, jobautomation.Automation{Name: "notify", Scope: jobautomation.Scope{Folder: folder}, When: jobautomation.WhenAlways, Enabled: true,
				Steps: []jobautomation.Step{
					{ID: "notify", Type: jobautomation.StepRun, Command: "notify.sh"},
					{ID: "merge", Type: jobautomation.StepDeliver, To: jobautomation.DeliverMerge},
				}}); err != nil {
				t.Fatal(err)
			}
			signals.NewEmitter(feed).Emit(pebblestore.Signal{Kind: "worker.run.succeeded", Account: account, DedupKey: "worker.run:run-test",
				Refs: map[string]string{"worker_id": "worker-test", "run_id": "run-test"}, Summary: "daily check finished"})
			err = runner.ProcessOnce(context.Background())
			list, listErr := runner.Store().JobDeliveries(account, "worker/worker-test/run-test")
			if listErr != nil {
				t.Fatal(listErr)
			}
			if !tc.started {
				if !errors.Is(err, jobautomation.ErrNotReady) || len(list) != 0 || len(host.ran) != 0 || len(deliverer.delivered) != 0 {
					t.Fatalf("unsettled job end started: err=%v deliveries=%d ran=%d delivered=%d", err, len(list), len(host.ran), len(deliverer.delivered))
				}
				return
			}
			if err != nil {
				t.Fatal(err)
			}
			if len(list) != 1 || list[0].State != jobautomation.StateSucceeded || len(host.ran) != 1 {
				t.Fatalf("workflow did not complete: deliveries=%+v ran=%d", list, len(host.ran))
			}
			raw, err := os.ReadFile(filepath.Join(folder, "result.json"))
			if err != nil {
				t.Fatal(err)
			}
			var result jobautomation.Result
			if err := json.Unmarshal(raw, &result); err != nil {
				t.Fatalf("run step did not get result JSON: %v\n%s", err, raw)
			}
			if result.JobID != "worker/worker-test/run-test" || result.Git == nil || result.Git.Head != head || result.Git.Branch != "agent/test" {
				t.Fatalf("result JSON lacks job or head: %s", raw)
			}
			if len(deliverer.delivered) != 1 || deliverer.delivered[0].Head != head || len(deliverer.delivered[0].Deliverables) == 0 {
				t.Fatalf("merge step did not deliver the resolved head: %+v", deliverer.delivered)
			}
		})
	}
}
