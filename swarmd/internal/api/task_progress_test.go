package api

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"path/filepath"
	"strings"
	"sync"
	"testing"

	"swarm/packages/swarmd/internal/jobautomation"
	"swarm/packages/swarmd/internal/session"
	pebblestore "swarm/packages/swarmd/internal/store/pebble"
)

func progressTask(status string) pebblestore.ProjectTaskRecord {
	return pebblestore.ProjectTaskRecord{ID: "task-1", ProjectID: "proj-1", Title: "Add a rate limit", Agent: "coder", Status: status,
		WorktreeBranch: "agent/task-1", BaseBranch: "dev", CreatedAt: 1000, UpdatedAt: 5000,
		Attempts: []pebblestore.ProjectTaskAttempt{{ID: "initial", CreatedAt: 2000, Status: status}}}
}

func evaluated(followup string, deliveries ...string) *jobautomation.JobEvaluation {
	return &jobautomation.JobEvaluation{JobID: "task/proj-1/task-1", Followup: followup, Seq: 7, EndedAt: 4000, EvaluatedAt: 4100, Deliveries: append([]string{}, deliveries...)}
}

// reviewAndMerge is a delivery of a three-step workflow: a check, a reviewer
// and a merge.
func reviewAndMerge(state string, steps ...jobautomation.StepRecord) jobautomation.Delivery {
	def := []jobautomation.Step{{ID: "check", Type: jobautomation.StepRun}, {ID: "review", Type: jobautomation.StepAgent, Worker: "reviewer"}, {ID: "merge", Type: jobautomation.StepDeliver, To: jobautomation.DeliverMerge}}
	for len(steps) < len(def) {
		steps = append(steps, jobautomation.StepRecord{ID: def[len(steps)].ID, Type: def[len(steps)].Type, State: jobautomation.StatePending})
	}
	return jobautomation.Delivery{ID: "jdel_1", AutomationID: "auto-1", AutomationName: "review and merge", JobID: "task/proj-1/task-1", State: state, Round: 1,
		Definition: def, Steps: steps, CreatedAt: 4200,
		Result: jobautomation.Result{JobID: "task/proj-1/task-1", ProjectID: "proj-1", Status: jobautomation.StatusSucceeded, FinishedAt: 4000,
			Git: &jobautomation.ResultGit{Branch: "agent/task-1", BaseBranch: "dev", Commits: []jobautomation.ResultCommit{{SHA: strings.Repeat("b", 40)}}}}}
}

func step(id, typ, state string, started, finished int64) jobautomation.StepRecord {
	return jobautomation.StepRecord{ID: id, Type: typ, State: state, StartedAt: started, FinishedAt: finished}
}

// Requirement (docs/task-progress.md): a task's progress is computed from
// its record, its job's deliveries and the evaluation of its job end, with
// one state per task: waiting, working, workflow, not_integrated,
// integrated, delivered, done or failed; workflow none (evaluated, nothing
// applied) is told from pending (not evaluated yet) and absent (no job
// end); the history is ordered and bounded.
// Threat: a finished task with no workflow looking like one waiting for a
// workflow (the Orchestrator then never tells the person), a merged task
// still shown as not integrated, a failed workflow hidden.
// Owner: computeTaskProgress, a pure function.
func TestTaskProgressStates(t *testing.T) {
	merged := strings.Repeat("c", 40)
	finished := func() pebblestore.ProjectTaskRecord {
		task := progressTask("needs_review")
		task.UnintegratedCommits = 2
		return task
	}
	cases := []struct {
		name          string
		task          pebblestore.ProjectTaskRecord
		deliveries    []jobautomation.Delivery
		eval          *jobautomation.JobEvaluation
		state         string
		workflow      string // "" when absent
		summary       string // a part of it
		integration   string
		commit        string
		historyIncl   string
		deliveryState string
	}{
		{name: "held by Start tasks", task: progressTask("pending_approval"), state: progressWaiting, summary: "approval"},
		{name: "queued", task: progressTask("queued"), state: progressWaiting, summary: "queued"},
		{name: "working", task: progressTask("in_progress"), state: progressWorking, summary: "coder working"},
		{name: "planning", task: progressTask("planning"), state: progressWorking, summary: "planning"},
		{name: "not integrated, no workflow", task: finished(), eval: evaluated(""), state: progressNotIntegrated, workflow: workflowNone,
			summary: "not integrated · no workflow · 2 commits on agent/task-1", integration: progressNotIntegrated, historyIncl: "finished · 2 commits on agent/task-1"},
		{name: "not integrated, workflows not evaluated yet", task: finished(), eval: &jobautomation.JobEvaluation{JobID: "task/proj-1/task-1", Seq: 7, EndedAt: 4000},
			state: progressNotIntegrated, workflow: workflowPending, summary: "not evaluated yet", integration: progressNotIntegrated},
		{name: "finished before job ends were recorded", task: finished(), state: progressNotIntegrated, summary: "not integrated", integration: progressNotIntegrated},
		{name: "workflow running", task: finished(), eval: evaluated("", "jdel_1"),
			deliveries: []jobautomation.Delivery{reviewAndMerge(jobautomation.StateRunning, step("check", jobautomation.StepRun, jobautomation.StateSucceeded, 4300, 4400), step("review", jobautomation.StepAgent, jobautomation.StateRunning, 4500, 0))},
			state:      progressWorkflow, workflow: workflowRunning, summary: "review and merge · step 2 of 3: reviewer · round 1", integration: progressNotIntegrated,
			historyIncl: "reviewer started", deliveryState: workflowRunning},
		{name: "a pending later end does not hide a running delivery", task: finished(), eval: &jobautomation.JobEvaluation{JobID: "task/proj-1/task-1", Followup: "run-2", Seq: 9, EndedAt: 6000},
			deliveries: []jobautomation.Delivery{reviewAndMerge(jobautomation.StatePending)},
			state:      progressWorkflow, workflow: workflowRunning, summary: "step 1 of 3: run check"},
		{name: "workflow merged", task: finished(), eval: evaluated("", "jdel_1"),
			deliveries: []jobautomation.Delivery{func() jobautomation.Delivery {
				d := reviewAndMerge(jobautomation.StateSucceeded, step("check", jobautomation.StepRun, jobautomation.StateSucceeded, 4300, 4400),
					step("review", jobautomation.StepAgent, jobautomation.StateSucceeded, 4500, 4600),
					jobautomation.StepRecord{ID: "merge", Type: jobautomation.StepDeliver, State: jobautomation.StateSucceeded, StartedAt: 4700, FinishedAt: 4800, Outcome: map[string]any{"to": "merge", "where": merged}})
				d.FinishedAt = 4900
				return d
			}()},
			state: progressIntegrated, workflow: workflowDone, summary: "merged into dev at " + merged[:12], integration: progressIntegrated, commit: merged,
			historyIncl: "merged at " + merged[:12], deliveryState: workflowDone},
		{name: "workflow failed", task: finished(), eval: evaluated("", "jdel_1"),
			deliveries: []jobautomation.Delivery{func() jobautomation.Delivery {
				d := reviewAndMerge(jobautomation.StateFailed, step("check", jobautomation.StepRun, jobautomation.StateFailed, 4300, 4400))
				d.Steps[0].Reason = "exit status 1"
				d.FinishedAt = 4500
				return d
			}()},
			state: progressFailed, workflow: workflowFailed, summary: "workflow review and merge failed: exit status 1", integration: progressNotIntegrated,
			historyIncl: "run check failed: exit status 1", deliveryState: workflowFailed},
		{name: "workflow blocked", task: finished(), eval: evaluated("", "jdel_1"),
			deliveries: []jobautomation.Delivery{func() jobautomation.Delivery {
				d := reviewAndMerge(jobautomation.StateBlocked)
				d.BlockedReason = jobautomation.ReasonRoundsExhausted
				return d
			}()},
			state: progressFailed, workflow: workflowBlocked, summary: "blocked: rounds_exhausted"},
		{name: "workflow published a pull request", task: finished(), eval: evaluated("", "jdel_2"),
			deliveries: []jobautomation.Delivery{{ID: "jdel_2", AutomationID: "auto-2", AutomationName: "open a PR", State: jobautomation.StateSucceeded,
				Definition: []jobautomation.Step{{ID: "pr", Type: jobautomation.StepDeliver, To: jobautomation.DeliverPublish}},
				Steps:      []jobautomation.StepRecord{{ID: "pr", Type: jobautomation.StepDeliver, State: jobautomation.StateSucceeded, StartedAt: 4300, FinishedAt: 4400, Outcome: map[string]any{"to": "publish", "where": "https://forge.example/pr/1"}}}}},
			state: progressDelivered, workflow: workflowDone, summary: "pull request https://forge.example/pr/1"},
		{name: "integrated by the person", task: func() pebblestore.ProjectTaskRecord {
			task := progressTask("completed")
			task.IsIntegrated = true
			task.Integration = &pebblestore.ProjectTaskIntegration{State: "integrated", SourceBranch: "agent/task-1", TargetBranch: "dev", ResultingTargetHead: merged}
			return task
		}(), eval: evaluated(""), state: progressIntegrated, workflow: workflowNone, summary: "merged into dev", integration: progressIntegrated, commit: merged, historyIncl: "merged into dev"},
		{name: "done, nothing to integrate", task: func() pebblestore.ProjectTaskRecord {
			task := progressTask("needs_review")
			task.Agent, task.WorktreeBranch = "finder", ""
			return task
		}(), eval: evaluated(""), state: progressDone, workflow: workflowNone, summary: "nothing to integrate"},
		{name: "media ready", task: func() pebblestore.ProjectTaskRecord {
			task := progressTask("completed")
			task.Agent, task.WorktreeBranch = "image", ""
			task.Deliverables = []pebblestore.ProjectTaskDeliverable{{ID: "d1", Status: "ready"}}
			return task
		}(), state: progressDelivered, summary: "files ready"},
		{name: "last attempt failed", task: func() pebblestore.ProjectTaskRecord {
			task := progressTask("failed")
			task.LastError = "provider refused"
			return task
		}(), eval: evaluated(""), state: progressFailed, workflow: workflowNone, summary: "failed: provider refused", historyIncl: "failed: provider refused"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			p := computeTaskProgress(tc.task, tc.deliveries, tc.eval, 9000)
			raw, _ := json.Marshal(p)
			if p.State != tc.state || !strings.Contains(p.Summary, tc.summary) {
				t.Fatalf("state %q summary %q, want %q containing %q: %s", p.State, p.Summary, tc.state, tc.summary, raw)
			}
			if tc.workflow == "" && p.Workflow != nil {
				t.Fatalf("workflow %+v, want absent", p.Workflow)
			}
			if tc.workflow != "" && (p.Workflow == nil || p.Workflow.State != tc.workflow) {
				t.Fatalf("workflow %s, want %q", raw, tc.workflow)
			}
			if tc.deliveryState != "" && (len(p.Workflow.Deliveries) != 1 || p.Workflow.Deliveries[0].State != tc.deliveryState || p.Workflow.Deliveries[0].Steps != 3) {
				t.Fatalf("deliveries %s, want one %s", raw, tc.deliveryState)
			}
			if tc.integration != "" && (p.Integration == nil || p.Integration.State != tc.integration || p.Integration.Commit != tc.commit) {
				t.Fatalf("integration %s, want %s at %q", raw, tc.integration, tc.commit)
			}
			if tc.historyIncl != "" && !strings.Contains(string(raw), tc.historyIncl) {
				t.Fatalf("history %s lacks %q", raw, tc.historyIncl)
			}
			if len(p.Summary) == 0 || strings.Contains(p.Summary, "\n") || len([]rune(p.Summary)) > maxProgressSummary {
				t.Fatalf("summary %q is not one short line", p.Summary)
			}
			for i := 1; i < len(p.History); i++ {
				if p.History[i].At < p.History[i-1].At {
					t.Fatalf("history out of order: %s", raw)
				}
			}
			if p.UpdatedAt < tc.task.UpdatedAt {
				t.Fatalf("updated_at %d before the task's %d", p.UpdatedAt, tc.task.UpdatedAt)
			}
		})
	}
	// The JSON shape a client reads.
	p := computeTaskProgress(finished(), []jobautomation.Delivery{reviewAndMerge(jobautomation.StateRunning, step("check", jobautomation.StepRun, jobautomation.StateSucceeded, 4300, 4400), step("review", jobautomation.StepAgent, jobautomation.StateRunning, 4500, 0))}, evaluated("", "jdel_1"), 9000)
	raw, _ := json.Marshal(p)
	var shape map[string]any
	if err := json.Unmarshal(raw, &shape); err != nil {
		t.Fatal(err)
	}
	delivery := shape["workflow"].(map[string]any)["deliveries"].([]any)[0].(map[string]any)
	integration := shape["integration"].(map[string]any)
	for _, key := range []string{"id", "workflow", "state", "step", "steps", "step_type", "round"} {
		if _, ok := delivery[key]; !ok {
			t.Fatalf("delivery lacks %s: %s", key, raw)
		}
	}
	for _, key := range []string{"state", "branch", "target", "commits", "commit", "at"} {
		if _, ok := integration[key]; !ok {
			t.Fatalf("integration lacks %s: %s", key, raw)
		}
	}
	if delivery["step"].(float64) != 2 || delivery["step_type"] != "agent" || shape["updated_at"].(float64) != 5000 {
		t.Fatalf("shape %s", raw)
	}

	// History keeps the most recent 50 entries.
	long := finished()
	for i := 0; i < 80; i++ {
		long.Attempts = append(long.Attempts, pebblestore.ProjectTaskAttempt{ID: "a", CreatedAt: int64(2000 + i)})
	}
	if h := computeTaskProgress(long, nil, evaluated(""), 9000).History; len(h) != maxProgressHistory || h[len(h)-1].At != 4000 {
		t.Fatalf("history %d entries ending %+v, want the latest %d", len(h), h[len(h)-1], maxProgressHistory)
	}
}

// Requirement: every change to a task job's deliveries (created, a step
// started or finished, finished) and to the evaluation of its job end
// publishes project.updated for the task's project, so clients read the
// task again; a job outside a project publishes nothing.
// Threat: a client showing a workflow step that finished long ago, or
// polling for it.
// Owners: jobautomation.Store (SetNotify on every delivery and evaluation
// write) -> Server.installTaskProgress -> SessionStore.NotifyProjectTaskChanged
// over a real Pebble store, read back from the project publisher.
func TestDeliveryTransitionsPublishProjectUpdated(t *testing.T) {
	db, err := pebblestore.Open(filepath.Join(t.TempDir(), "progress.pebble"))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = db.Close() })
	var mu sync.Mutex
	published := []map[string]any{}
	db.SetProjectPublisher(func(record pebblestore.V3RealtimeOutboxRecord) {
		mu.Lock()
		defer mu.Unlock()
		if record.Event.EventType != pebblestore.ProjectUpdatedEventType {
			t.Errorf("event %s, want project.updated", record.Event.EventType)
		}
		var payload map[string]any
		_ = json.Unmarshal(record.Event.Payload, &payload)
		published = append(published, payload)
	})
	count := func() int {
		mu.Lock()
		defer mu.Unlock()
		return len(published)
	}
	server := &Server{sessions: session.NewService(pebblestore.NewSessionStore(db), nil)}
	store := jobautomation.NewStore(db)
	server.SetJobAutomations(jobautomation.NewRunner(jobautomation.Options{Store: store}))
	account := testPrincipal().AccountScopeID
	folder := t.TempDir()
	auto, _, err := store.Put(account, jobautomation.Automation{Name: "review", Scope: jobautomation.Scope{Folder: folder}, Enabled: true, Steps: []jobautomation.Step{
		{ID: "review", Type: jobautomation.StepAgent, Worker: "reviewer", Brief: "Review."}}})
	if err != nil {
		t.Fatal(err)
	}

	d, created, err := store.CreateDelivery(auto, jobautomation.Result{JobID: "task/proj-1/task-1", Kind: jobautomation.KindTask, ProjectID: "proj-1", RunID: "task-1", Folder: folder, Status: jobautomation.StatusSucceeded})
	if err != nil || !created {
		t.Fatal(created, err)
	}
	if count() != 1 || published[0]["project_id"] != "proj-1" || published[0]["task_id"] != "task-1" || published[0]["action"] != "task_progress" {
		t.Fatalf("delivery created: published %+v", published)
	}
	d.State, d.Steps[0].State, d.Steps[0].StartedAt = jobautomation.StateRunning, jobautomation.StateRunning, 1
	if err := store.SaveDelivery(d); err != nil {
		t.Fatal(err)
	}
	d.State, d.Steps[0].State, d.Steps[0].FinishedAt, d.FinishedAt = jobautomation.StateSucceeded, jobautomation.StateSucceeded, 2, 2
	if err := store.SaveDelivery(d); err != nil {
		t.Fatal(err)
	}
	if count() != 3 {
		t.Fatalf("step start and finish: %d publications, want 3", count())
	}
	if err := store.RecordEvaluation(account, "proj-1", "task/proj-1/task-1", "", 5, 10, []string{d.ID}); err != nil {
		t.Fatal(err)
	}
	if count() != 4 {
		t.Fatalf("evaluation: %d publications, want 4", count())
	}
	// A worker job outside a project publishes nothing.
	if _, _, err := store.CreateDelivery(auto, jobautomation.Result{JobID: "worker/w/run-1", Kind: jobautomation.KindWorker, RunID: "run-1", Folder: folder, Status: jobautomation.StatusSucceeded}); err != nil {
		t.Fatal(err)
	}
	if count() != 4 {
		t.Fatalf("a job outside a project published: %+v", published)
	}
}

// Requirement: GET /v3/projects/{id}/tasks and /tasks/{task} carry progress
// on each task, computed from the stored integration facts the collection
// response itself clears.
// Owners: the project task routes over a real session store and runner store.
func TestTaskListAndDetailCarryProgress(t *testing.T) {
	server, _, _, _, _ := newRoutedSessionTestServerWithSwarmStore(t)
	p := testPrincipal()
	db := server.sessions.Store()
	store := jobautomation.NewStore(db.Underlying())
	server.SetJobAutomations(jobautomation.NewRunner(jobautomation.Options{Store: store}))
	if err := db.PutProject(p.AccountScopeID, &pebblestore.ProjectRecord{ID: "proj-p", AccountID: p.AccountScopeID, Name: "Progress"}); err != nil {
		t.Fatal(err)
	}
	task := &pebblestore.ProjectTaskRecord{ID: "task-p", ProjectID: "proj-p", AccountID: p.AccountScopeID, Title: "Change things", Agent: "coder", Status: "needs_review",
		WorktreeBranch: "agent/task-p", BaseBranch: "dev", UnintegratedCommits: 3}
	if err := db.PutProjectTask(p.AccountScopeID, task); err != nil {
		t.Fatal(err)
	}
	if err := store.RecordEvaluation(p.AccountScopeID, "proj-p", "task/proj-p/task-p", "", 1, 1, nil); err != nil {
		t.Fatal(err)
	}
	get := func(target string) map[string]any {
		t.Helper()
		rec := httptest.NewRecorder()
		server.Handler().ServeHTTP(rec, withTestPrincipal(httptest.NewRequest(http.MethodGet, target, nil)))
		if rec.Code != http.StatusOK {
			t.Fatalf("%s: %d %s", target, rec.Code, rec.Body.String())
		}
		var out map[string]any
		if err := json.Unmarshal(rec.Body.Bytes(), &out); err != nil {
			t.Fatal(err)
		}
		return out
	}
	list := get("/v3/projects/proj-p/tasks")
	row := list["tasks"].([]any)[0].(map[string]any)
	progress, _ := row["progress"].(map[string]any)
	if progress["state"] != progressNotIntegrated || progress["workflow"].(map[string]any)["state"] != workflowNone ||
		progress["integration"].(map[string]any)["commits"].(float64) != 3 || !strings.Contains(progress["summary"].(string), "no workflow") {
		t.Fatalf("list progress %+v", progress)
	}
	detail := get("/v3/projects/proj-p/tasks/task-p")
	progress, _ = detail["task"].(map[string]any)["progress"].(map[string]any)
	if progress == nil || progress["workflow"].(map[string]any)["state"] != workflowNone || progress["summary"] == "" {
		t.Fatalf("detail progress %+v", detail["task"])
	}
}
