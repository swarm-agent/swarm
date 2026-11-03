package api

// Purpose:
//   - Requirement: the incident's whole lifecycle ends correctly through the
//     product's own paths. A live run on an accepted structured plan calls
//     plan_manage complete_subtask with complete_checkpoint=true on the last
//     checkpoint; the plan lifecycle persists the plan waiting_review and its
//     final handoff seals the run's epoch (run.appendFinalPlanExecutionSystemMessage).
//     The run must still end completed, the task must settle with its exact
//     label, its job end must be signalled once and the configured workflow
//     must deliver once, and get_task, list_tasks, wait_tasks and
//     get_task_session must agree that nothing is running. The plan's own
//     review gate (waiting_review) stays for the person. After a run left
//     stuck by its sealed epoch is finalized, the canonical reopen
//     (ReopenProjectTask) must start a valid new attempt on the exact
//     retained source.
//   - Threat/regression: the run's completion was fenced by its sealed epoch,
//     so the task stayed in_progress (progress working), the session kept an
//     active run, the workflow never fired, stop_task returned 500 and reopen
//     was refused for an active writer. A wrong settled label (blocked,
//     needs_review for integrated work, completed for unintegrated work) must
//     fail these tests, as must a second workflow delivery.
//   - Boundary/authority: the provider-managed tool path Claude Code uses
//     (Request.ToolInvoker -> run.Service's provider tool invoker ->
//     executeControlPlaneToolWithLifecycleRunContext ->
//     executePlanManageToolWithLifecycleRunContext ->
//     appendPlanLifecycleMessageForToolResult -> BeginExecutionEpoch), then
//     the run's final reply (sessionV3Executor.completeRun),
//     sessionV3Executor's terminal write through applySessionV3PrimaryMutation
//     and sessionV3RunWriteEpoch, settleTaskRunEnd ->
//     reconcileProjectTaskRunLifecycle and signalTaskRunEnded, the workflow
//     runner (jobautomation.Runner over the signal feed, Automation.Matches),
//     the get_task / list_tasks routes, taskProgressFor with
//     pebblestore.ProjectTaskWaitSettled (wait_tasks' rule), the session
//     detail route with controlMCPSessionStateView (get_task_session's view),
//     Server.StopProjectTask and Server.ReopenProjectTask over real Git.
//   - Narrowest layer: the real server, executor, run service, Pebble store,
//     workflow runner and Git. Stubbed: the model provider (scripted tool call
//     and answers), the workflow's job result reader (built from the job-end
//     signal itself) and the workflow step's agent (records starts, runs no
//     model). The reopen test's stuck run is written through the executor's
//     own status writer with no executor goroutine and sealed as the final
//     handoff seals it, the state a restarted daemon finds; its task has no
//     plan binding (approve_task's acceptance receipt is not in that
//     fixture), so a plan-bound reopen's receipt check is not covered here.

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"

	agentruntime "swarm/packages/swarmd/internal/agent"
	"swarm/packages/swarmd/internal/jobautomation"
	provideriface "swarm/packages/swarmd/internal/provider/interfaces"
	sessionruntime "swarm/packages/swarmd/internal/session"
	pebblestore "swarm/packages/swarmd/internal/store/pebble"
	"swarm/packages/swarmd/internal/tool"
)

// planManageFinalProvider is the model stub, shaped as Claude Code runs its
// tools: inside the provider request it calls plan_manage complete_subtask
// with complete_checkpoint=true on the plan's last checkpoint through the
// request's ToolInvoker (the plan lifecycle then hands the plan off and
// seals the run's epoch), and then answers with its final reply in the same
// request. That reply is the run's own write after its epoch was sealed.
type planManageFinalProvider struct {
	mu      sync.Mutex
	args    string
	result  provideriface.ToolExecutionResult
	toolErr error
}

const planManageFinalReply = "All checkpoints are complete."

func (p *planManageFinalProvider) handle(ctx context.Context, req provideriface.Request, _ func(provideriface.StreamEvent)) (provideriface.Response, error) {
	if req.ToolInvoker == nil {
		return provideriface.Response{}, errors.New("no provider tool invoker")
	}
	result, err := req.ToolInvoker.ExecuteTool(ctx, provideriface.ToolInvocation{CallID: "call-final", Name: "plan_manage", Arguments: p.args})
	p.mu.Lock()
	p.result, p.toolErr = result, err
	p.mu.Unlock()
	return provideriface.Response{Text: planManageFinalReply, StopReason: "stop"}, nil
}

func (p *planManageFinalProvider) tool() (provideriface.ToolExecutionResult, error) {
	p.mu.Lock()
	defer p.mu.Unlock()
	return p.result, p.toolErr
}

// signalResults builds a task job's workflow result from its job-end signal
// alone (the job id, role, project and outcome the signal carries).
type signalResults struct{ folder string }

func (r signalResults) Result(_ context.Context, sig pebblestore.Signal, _ bool) (*jobautomation.Result, error) {
	return &jobautomation.Result{V: jobautomation.ResultVersion, JobID: sig.Attrs["job_id"], Kind: jobautomation.KindTask, Role: sig.Attrs["role"],
		ProjectID: sig.Refs["project_id"], Folder: r.folder, Status: strings.TrimPrefix(sig.Kind, "task.run.")}, nil
}

// recordingAgents is the workflow step's agent: it records each start and
// ends at once, so a delivery's step count is the number of starts.
type recordingAgents struct {
	mu      sync.Mutex
	started int
}

func (a *recordingAgents) Start(context.Context, jobautomation.AgentRequest) (string, error) {
	a.mu.Lock()
	defer a.mu.Unlock()
	a.started++
	return "task/proj-final-path/step-job", nil
}

func (a *recordingAgents) Wait(_ context.Context, _, jobID string) (jobautomation.AgentOutcome, error) {
	return jobautomation.AgentOutcome{JobID: jobID, Status: jobautomation.StatusSucceeded}, nil
}

func (a *recordingAgents) starts() int {
	a.mu.Lock()
	defer a.mu.Unlock()
	return a.started
}

// createAgentSession creates an auto-mode V3 session of agent through the
// session route, as the fixture's createSession does for frontdesk.
func createAgentSession(t *testing.T, f *confirmationFixture, title, agent string) string {
	t.Helper()
	workspace := t.TempDir()
	bindingID := seedSessionsV3PrimaryAuthority(t, f.server, workspace)
	raw, _ := json.Marshal(map[string]any{
		"client_request_id": "create-" + title, "workspace_path": workspace, "workspace_name": filepath.Base(workspace),
		"swarm_id": "host-swarm-id", "workspace_binding_id": bindingID, "target_kind": "host", "target_relationship": "self",
		"title": title, "mode": sessionruntime.ModeAuto, "agent_name": agent,
		"preference": pebblestore.ModelPreference{Provider: "test-provider", Model: "test-model"},
	})
	code, body := f.do(http.MethodPost, "/v3/sessions", string(raw))
	if code != http.StatusOK {
		t.Fatalf("create session: %d %s", code, body)
	}
	var created struct {
		Session pebblestore.SessionSnapshot `json:"session"`
	}
	if err := json.Unmarshal([]byte(body), &created); err != nil {
		t.Fatal(err)
	}
	return created.Session.ID
}

// waitTaskSettled waits (bounded) for the task to leave in_progress: the
// executor settles the task right after the run's terminal write commits,
// in the run's own goroutine.
func waitTaskSettled(t *testing.T, f *confirmationFixture, task pebblestore.ProjectTaskRecord) *pebblestore.ProjectTaskRecord {
	t.Helper()
	deadline := time.Now().Add(runCancelTestTimeout)
	for {
		stored, ok, err := f.sessions.Store().GetProjectTask(testPrincipal().AccountScopeID, task.ProjectID, task.ID)
		if err != nil || !ok {
			t.Fatalf("task: %v %v", ok, err)
		}
		if stored.Status != "in_progress" || time.Now().After(deadline) {
			return stored
		}
		time.Sleep(10 * time.Millisecond)
	}
}

// taskReads is one task as each read surface reports it.
type taskReads struct {
	getStatus, getProgress   string
	listStatus, listProgress string
	waitSettled              bool
	sessionActiveRun         bool
}

// readTask reads the task through get_task's and list_tasks' routes, the
// wait_tasks settlement rule over the same progress, and get_task_session's
// view of the session detail route.
func readTask(t *testing.T, f *confirmationFixture, task pebblestore.ProjectTaskRecord) taskReads {
	t.Helper()
	var out taskReads
	type progressed struct {
		ID       string `json:"id"`
		Status   string `json:"status"`
		Progress struct {
			State string `json:"state"`
		} `json:"progress"`
	}
	code, body := f.do(http.MethodGet, "/v3/projects/"+task.ProjectID+"/tasks/"+task.ID, "")
	var detail struct {
		Task progressed `json:"task"`
	}
	if code != http.StatusOK || json.Unmarshal([]byte(body), &detail) != nil {
		t.Fatalf("get_task route: %d %s", code, body)
	}
	out.getStatus, out.getProgress = detail.Task.Status, detail.Task.Progress.State
	code, body = f.do(http.MethodGet, "/v3/projects/"+task.ProjectID+"/tasks", "")
	var list struct {
		Tasks []progressed `json:"tasks"`
	}
	if code != http.StatusOK || json.Unmarshal([]byte(body), &list) != nil {
		t.Fatalf("list_tasks route: %d %s", code, body)
	}
	for _, item := range list.Tasks {
		if item.ID == task.ID {
			out.listStatus, out.listProgress = item.Status, item.Progress.State
		}
	}
	stored, ok, err := f.sessions.Store().GetProjectTask(testPrincipal().AccountScopeID, task.ProjectID, task.ID)
	if err != nil || !ok {
		t.Fatalf("task: %v %v", ok, err)
	}
	stored.EnsureTaskAttempts()
	attemptID := ""
	if a := stored.ActiveAttempt(); a != nil {
		attemptID = a.ID
	}
	progress := f.server.taskProgressFor(testPrincipal().AccountScopeID, *stored, time.Now().UnixMilli())
	out.waitSettled = pebblestore.ProjectTaskWaitSettled(stored.Status, "finished", pebblestore.ProjectTaskWaitProgressFrom(progress).ForAttempt(attemptID))
	code, body = f.do(http.MethodGet, "/v3/sessions/"+task.SessionID+"?message_limit=20&event_limit=0", "")
	var session map[string]any
	if code != http.StatusOK || json.Unmarshal([]byte(body), &session) != nil {
		t.Fatalf("session detail route: %d %s", code, body)
	}
	_, out.sessionActiveRun = controlMCPSessionStateView(session, controlMCPTextLimit, controlMCPToolOutputLimit, nil)["active_run"]
	return out
}

func TestV3PlanManageFinalHandoffSettlesTheTaskAndDeliversItsWorkflowOnce(t *testing.T) {
	f := newConfirmationFixture(t, filepath.Join(t.TempDir(), "store"), nil)
	t.Cleanup(f.close)
	p := testPrincipal()
	if _, _, _, err := f.server.agents.UpsertForAccount(p.AccountScopeID, agentruntime.UpsertInput{
		Name: "planner", Mode: agentruntime.ModeSubagent, Provider: "test-provider", Model: "test-model",
		Enabled: pebblestore.BoolPtr(true), Prompt: "Execute the plan.",
		ToolContract: &pebblestore.AgentToolContract{Preset: "custom", Tools: map[string]pebblestore.AgentToolConfig{"plan_manage": {Enabled: pebblestore.BoolPtr(true)}}},
	}); err != nil {
		t.Fatal(err)
	}
	sessionID := createAgentSession(t, f, "plan-run", "planner")
	const requestID = "final-checkpoint"
	runID := stableSessionsV3PrimaryRunID(sessionID, requestID)
	const planID = "plan-final-path"
	task := taskSessionFixture(t, f.server, pebblestore.SessionSnapshot{ID: sessionID}, pebblestore.ProjectTaskRecord{ID: "task_final_path", ProjectID: "proj-final-path", Agent: "coder", Status: "in_progress",
		PlanBinding: &pebblestore.ProjectTaskPlanBinding{SessionID: sessionID, PlanID: planID}})
	// The run is the task's own execution run, as the task's launch admits it.
	if _, err := f.sessions.Store().UpdateProjectTask(p.AccountScopeID, task.ProjectID, task.ID, func(t *pebblestore.ProjectTaskRecord) error {
		t.EnsureTaskAttempts()
		t.ActiveAttempt().RunID = runID
		t.ActiveAttempt().SessionID = sessionID
		return nil
	}); err != nil {
		t.Fatal(err)
	}

	// The signal feed and a workflow on the task's folder and agent, as a
	// person configures review/push: its runner reads the feed's end first,
	// as a daemon start does.
	signalsDB, err := pebblestore.Open(filepath.Join(t.TempDir(), "signals.pebble"))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = signalsDB.Close() })
	feed := pebblestore.NewSignalStore(signalsDB)
	f.server.SetSignalStore(feed)
	folder := t.TempDir()
	agents := &recordingAgents{}
	workflows := jobautomation.NewStore(signalsDB)
	runner := jobautomation.NewRunner(jobautomation.Options{Store: workflows, Feed: feed, Account: func() string { return p.AccountScopeID }, Results: signalResults{folder: folder}, Agent: agents})
	f.server.SetJobAutomations(runner)
	ctx := context.Background()
	if err := runner.ProcessOnce(ctx); err != nil {
		t.Fatal(err)
	}
	if _, _, err := workflows.Put(p.AccountScopeID, jobautomation.Automation{Name: "review and push", Scope: jobautomation.Scope{Folder: folder, Role: "coder"}, When: jobautomation.WhenSucceeded, Enabled: true,
		Steps: []jobautomation.Step{{ID: "review", Type: jobautomation.StepAgent, Worker: "finder", Brief: "Review the finished work."}}}); err != nil {
		t.Fatal(err)
	}

	// The accepted plan: checkpoint 1 done, the run executing the last one.
	doc := pebblestore.SessionPlanDocument{
		ID: planID, Title: "Final plan", ActiveCheckpointID: "cp-2",
		Checkpoints: []pebblestore.SessionPlanCheckpoint{
			{ID: "cp-1", Title: "First", Status: sessionruntime.PlanCheckpointStatusCompleted, Subtasks: []pebblestore.SessionPlanSubtask{{ID: "st-1", Title: "one", Status: "completed"}}},
			{ID: "cp-2", Title: "Last", Status: sessionruntime.PlanCheckpointStatusInProgress, AttemptID: "att-2", RunID: runID, SessionID: sessionID,
				Subtasks: []pebblestore.SessionPlanSubtask{{ID: "st-2", Title: "two", Status: "in_progress"}}, ActiveSubtaskID: "st-2"},
		},
		ExecutionState: &pebblestore.SessionPlanExecutionState{Status: sessionruntime.PlanExecutionStateInProgress, ActiveAttemptID: "att-2", CurrentRunID: runID, CurrentSessionID: sessionID, ParentSessionID: sessionID},
	}
	if _, _, err := f.sessions.SavePlanWithMetadata(sessionID, planID, "Final plan", "Do the work in two checkpoints.", "approved", "approved", true, sessionruntime.PlanSaveMetadata{Document: &doc}); err != nil {
		t.Fatal(err)
	}
	args, _ := json.Marshal(map[string]any{"action": "complete_subtask", "plan_id": planID, "checkpoint_id": "cp-2", "subtask_id": "st-2", "complete_checkpoint": true,
		"report": "Done", "changed_files": []string{"file.go"}, "validation": []string{"passed"}, "result": "done", "closing_state": "routine_clean", "summary": "Clean",
		"handoff_title": "Done", "handoff_overview": "Every checkpoint is complete.", "impact_bullets": []string{"Clean."},
		"recommendation": map[string]any{"decision": "ship", "action": "review", "reason": "Done.", "action_state": "ready"}})
	model := &planManageFinalProvider{args: string(args)}
	f.runner.handler = model.handle

	body, _ := json.Marshal(map[string]any{"client_request_id": requestID, "role": "user", "content": "finish the plan"})
	if code, out := f.do(http.MethodPost, "/v3/sessions/"+sessionID+"/messages", string(body)); code != http.StatusOK {
		t.Fatalf("message: %d %s", code, out)
	}
	f.waitRun(sessionID, runID, sessionruntime.RunIntentCompleted)
	if result, err := model.tool(); err != nil || result.Error != "" {
		t.Fatalf("plan_manage through the provider's tool invoker = %+v err=%v", result, err)
	}

	// The plan lifecycle handed the plan off for the person's review and
	// sealed the run's epoch, assigning no run.
	plan, ok, err := f.sessions.GetPlan(sessionID, planID)
	if err != nil || !ok || plan.Document.ExecutionState.Status != sessionruntime.PlanExecutionStateWaitingReview || plan.ApprovalState != "approved" {
		t.Fatalf("plan after the final checkpoint = %+v ok=%v err=%v", plan.Document.ExecutionState, ok, err)
	}
	for _, checkpoint := range plan.Document.Checkpoints {
		if checkpoint.Status != sessionruntime.PlanCheckpointStatusCompleted {
			t.Fatalf("checkpoint %s = %s", checkpoint.ID, checkpoint.Status)
		}
	}
	active, ok, err := f.sessions.GetActiveExecutionEpoch(sessionID)
	if err != nil || !ok || active.Boundary.Reason != sessionV3FinalPlanHandoffReason || active.Boundary.RunID != "" || active.ParentEpochID != finalHandoffInitialEpoch {
		t.Fatalf("active epoch = %+v ok=%v err=%v, want the final handoff's successor", active, ok, err)
	}
	if sealed, ok, err := f.sessions.GetExecutionEpoch(sessionID, finalHandoffInitialEpoch); err != nil || !ok || sealed.Status != pebblestore.ExecutionEpochStatusSealed {
		t.Fatalf("the run's epoch = %+v ok=%v err=%v, want sealed", sealed, ok, err)
	}

	// The run ended completed with no failure and no active writer.
	intent, _, _ := f.sessions.GetSessionRunIntent(sessionID, runID)
	if intent.Status != sessionruntime.RunIntentCompleted || intent.EpochID != finalHandoffInitialEpoch || intent.BlockedReason != "" {
		t.Fatalf("run intent = %+v", intent)
	}
	if _, active, err := f.sessions.GetSessionActiveRunIntent(sessionID); err != nil || active {
		t.Fatalf("the run is still the session's active writer: %v %v", active, err)
	}
	if failures := runFailureMessages(t, f.sessions, sessionID); len(failures) != 0 {
		t.Fatalf("run failures after the handoff: %q", failures)
	}
	if got := f.lastReply(sessionID); got != planManageFinalReply {
		t.Fatalf("final reply after the handoff = %q, want it saved", got)
	}

	// The task settled with its exact label: the work is not integrated, so
	// it waits for review (the plan's own review gate is separate).
	settled := waitTaskSettled(t, f, task)
	if settled.Status != "needs_review" || settled.ActionNeeded != "Action Needed: Review agent deliverables and verify outcomes" || settled.IsIntegrated {
		t.Fatalf("task = %q (%q) integrated=%v, want needs_review", settled.Status, settled.ActionNeeded, settled.IsIntegrated)
	}
	var page pebblestore.SignalPage
	for deadline := time.Now().Add(runCancelTestTimeout); ; time.Sleep(10 * time.Millisecond) {
		if page, err = feed.ListAfter(0, 100, pebblestore.SignalFilter{Account: p.AccountScopeID, KindPrefixes: []string{"task.run"}}); err != nil || len(page.Signals) > 0 || time.Now().After(deadline) {
			break
		}
	}
	if err != nil || len(page.Signals) != 1 || page.Signals[0].Kind != "task.run.succeeded" || page.Signals[0].Refs["task_id"] != task.ID {
		t.Fatalf("task.run signals = %+v err=%v, want exactly one task.run.succeeded", page.Signals, err)
	}

	// The workflow delivers once; a second pass, and a late repeat of the
	// run's terminal write (refused: the finished run no longer owns the
	// session, so its sealed epoch fences it), deliver nothing more.
	jobID := taskRunJobID(task.ProjectID, task.ID)
	if err := runner.ProcessOnce(ctx); err != nil {
		t.Fatal(err)
	}
	var fenced *pebblestore.ExecutionEpochNotActiveError
	if _, err := f.server.v3SessionExecutor.recordRunStatus(sessionV3ExecutorJob{Principal: p, SessionID: sessionID, RunID: runID, EpochID: finalHandoffInitialEpoch}, sessionruntime.RunIntentCompleted, "", "session.assistant.completed"); !errors.As(err, &fenced) {
		t.Fatalf("late repeated completion = %v, want the epoch fence", err)
	}
	if err := runner.ProcessOnce(ctx); err != nil {
		t.Fatal(err)
	}
	deliveries, err := workflows.JobDeliveries(p.AccountScopeID, jobID)
	if err != nil || len(deliveries) != 1 || agents.starts() != 1 {
		t.Fatalf("workflow deliveries for %s = %d (steps started %d) err=%v, want exactly one", jobID, len(deliveries), agents.starts(), err)
	}
	if again, err := feed.ListAfter(0, 100, pebblestore.SignalFilter{Account: p.AccountScopeID, KindPrefixes: []string{"task.run"}}); err != nil || len(again.Signals) != 1 {
		t.Fatalf("task.run signals after the late write = %+v err=%v, want still one", again.Signals, err)
	}

	// Every read agrees: the task is settled and nothing runs.
	reads := readTask(t, f, task)
	if reads.getStatus != "needs_review" || reads.listStatus != reads.getStatus || reads.getProgress == "" || reads.getProgress == progressWorking || reads.listProgress != reads.getProgress || !reads.waitSettled || reads.sessionActiveRun {
		t.Fatalf("reads disagree or still show work: %+v", reads)
	}
}

func TestV3ReopenAfterAFinalizedHandedOffRunStartsANewAttemptOnTheExactSource(t *testing.T) {
	root := t.TempDir()
	f := newConfirmationFixture(t, filepath.Join(root, "store"), nil)
	defer f.close()
	f.runner.handler = func(context.Context, provideriface.Request, func(provideriface.StreamEvent)) (provideriface.Response, error) {
		return provideriface.Response{Text: "continued", StopReason: "stop"}, nil
	}
	r := seedRetainedTask(t, f.server, f.sessions, testPrincipal(), filepath.Join(root, "repo"), "in_progress")
	// The task's work is committed on its branch and integrated into dev.
	if err := os.WriteFile(filepath.Join(r.child, "feature.go"), []byte("package feature\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	integrationGit(t, r.child, "add", "feature.go")
	integrationGit(t, r.child, "commit", "-m", "the plan's work")
	integrationGit(t, r.source, "merge", "--ff-only", r.branch)
	head := integrationGit(t, r.source, "rev-parse", "HEAD")

	// The session's plan, not a task plan binding: a plan-bound reopen also
	// needs approve_task's acceptance receipt, which this fixture does not
	// create. The handed-off check reads the handoff boundary's plan.
	const planID = "plan-retained"
	// The stuck run: running in its epoch, its plan handed off for review,
	// that epoch sealed by the handoff, and no executor holding it.
	// The retained fixture's session predates execution epochs; a context
	// compaction of the task's own run gives it the run's epoch, as a long
	// run's compaction does.
	runID := r.task(t, f.server).ExecutionRunID()
	trigger := pebblestore.MessageSnapshot{ID: "v3msg_retained_compaction", SessionID: r.sessionID, UserID: r.p.UserID, AccountScopeID: r.p.AccountScopeID, Role: "user", Content: "Continue from the recap."}
	compacted, err := f.sessions.BeginExecutionEpoch(pebblestore.BeginExecutionEpochInput{SessionID: r.sessionID, UserID: r.p.UserID, AccountScopeID: r.p.AccountScopeID, ClientRequestID: "retained-compaction", PayloadHash: "retained-compaction",
		Reason: "context_compaction_threshold", RunID: runID, SourceMessageID: trigger.ID, TriggerMessage: &trigger, SkipRunIntent: true})
	if err != nil {
		t.Fatalf("compaction boundary: %v", err)
	}
	job := sessionV3ExecutorJob{Principal: r.p, SessionID: r.sessionID, RunID: runID, EpochID: compacted.Epoch.EpochID}
	for _, step := range []struct{ status, event string }{
		{sessionruntime.RunIntentPendingExecutor, "session.assistant.queued"},
		{sessionruntime.RunIntentRunning, "session.assistant.started"},
	} {
		if _, err := f.server.v3SessionExecutor.recordRunStatus(job, step.status, "", step.event); err != nil {
			t.Fatalf("record %s: %v", step.status, err)
		}
	}
	saveFinalPlan(t, f.sessions, r.sessionID, planID, sessionruntime.PlanExecutionStateWaitingReview)
	sealByFinalPlanHandoff(t, f.sessions, r.sessionID, planID, "retained")

	reopen := func(key string) (*pebblestore.ProjectTaskRecord, error) {
		t.Helper()
		ctx, cancel := context.WithTimeout(context.Background(), 20*time.Second)
		defer cancel()
		return f.server.ReopenProjectTask(ctx, r.p, r.projectID, r.taskID, tool.ProjectTaskFollowupInput{Feedback: "one more change", ClientRequestID: key, Revision: r.task(t, f.server).Revision})
	}
	// Before: the stuck run refuses the reopen and nothing changes.
	before := r.task(t, f.server)
	if _, err := reopen("reopen-while-stuck"); err == nil {
		t.Fatal("reopen accepted while the stuck run still held the session")
	}
	if after := r.task(t, f.server); after.Revision != before.Revision || len(after.Attempts) != len(before.Attempts) || after.Status != "in_progress" {
		t.Fatalf("refused reopen changed the task: %+v", after)
	}

	stopped, err := f.server.StopProjectTask(context.Background(), r.p, r.projectID, r.taskID)
	if err != nil || stopped.Status != "completed" || stopped.RunID != job.RunID {
		t.Fatalf("stop of the handed-off run = %+v err=%v, want completed", stopped, err)
	}
	finalized := r.task(t, f.server)
	if finalized.Status != "completed" || !finalized.IsIntegrated {
		t.Fatalf("task after finalization = %q integrated=%v (%q), want completed", finalized.Status, finalized.IsIntegrated, finalized.ActionNeeded)
	}

	reopened, err := reopen("reopen-after-finalize")
	if err != nil {
		t.Fatalf("canonical reopen after finalization: %v", err)
	}
	attempt := reopened.ActiveAttempt()
	if attempt == nil || attempt.ClientRequestID != "reopen-after-finalize" || attempt.LaunchState != "launched" || len(reopened.Attempts) != len(finalized.Attempts)+1 {
		t.Fatalf("new attempt = %+v (attempts %d -> %d)", attempt, len(finalized.Attempts), len(reopened.Attempts))
	}
	// The exact source: the work is already delivered, so nothing is
	// recovered and the follow-up's own lane starts at the source's
	// integrated head; the retained lane and the source are not moved.
	if attempt.Recovery != nil || attempt.BaseCommit != head || attempt.SessionID == "" || attempt.SessionID == r.sessionID || attempt.WorkspacePath == r.child {
		t.Fatalf("new attempt source = session %q path %q base %q recovery %+v, want a new lane at %s", attempt.SessionID, attempt.WorkspacePath, attempt.BaseCommit, attempt.Recovery, head)
	}
	if got := integrationGit(t, attempt.WorkspacePath, "rev-parse", "HEAD"); got != head {
		t.Fatalf("follow-up worktree at %s, want the integrated head %s", got, head)
	}
	for name, dir := range map[string]string{"source": r.source, "retained lane": r.child} {
		if got := integrationGit(t, dir, "rev-parse", "HEAD"); got != head {
			t.Fatalf("%s moved to %s, want %s", name, got, head)
		}
	}
	if _, ok, err := f.sessions.GetSessionRunIntent(attempt.SessionID, reopened.ExecutionRunID()); err != nil || !ok || reopened.ExecutionRunID() == job.RunID {
		t.Fatalf("the new attempt's run %q was not recorded: %v %v", reopened.ExecutionRunID(), ok, err)
	}
	// The finalized run stays completed; a stop of it is refused.
	if intent, _, _ := f.sessions.GetSessionRunIntent(r.sessionID, job.RunID); intent.Status != sessionruntime.RunIntentCompleted {
		t.Fatalf("finalized run = %+v", intent)
	}
	if _, _, err := f.server.v3SessionExecutor.CancelRun(job, "late stop"); err == nil {
		t.Fatal("a stop of the finalized run was accepted")
	}
}
