package api

import (
	"context"
	"errors"
	"fmt"
	"net/http"
	"net/http/httptest"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"swarm/packages/swarmd/internal/identity"
	"swarm/packages/swarmd/internal/jobautomation"
	"swarm/packages/swarmd/internal/permission"
	runruntime "swarm/packages/swarmd/internal/run"
	"swarm/packages/swarmd/internal/sandbox"
	sessionruntime "swarm/packages/swarmd/internal/session"
	pebblestore "swarm/packages/swarmd/internal/store/pebble"
	"swarm/packages/swarmd/internal/workspace"
	"swarm/packages/swarmd/internal/worktree"
)

// taskSessionFixture binds a real V3 session to a project task, as task
// creation does (session metadata project_id/task_id, the task's session).
func taskSessionFixture(t *testing.T, server *Server, session pebblestore.SessionSnapshot, task pebblestore.ProjectTaskRecord) pebblestore.ProjectTaskRecord {
	t.Helper()
	p := testPrincipal()
	db := server.sessions.Store()
	if err := db.PutProject(p.AccountScopeID, &pebblestore.ProjectRecord{ID: task.ProjectID, Name: "Project"}); err != nil {
		t.Fatal(err)
	}
	stored, ok, err := db.GetSession(session.ID)
	if err != nil || !ok {
		t.Fatalf("session %s: %v %v", session.ID, ok, err)
	}
	if stored.Metadata == nil {
		stored.Metadata = map[string]any{}
	}
	stored.Metadata["project_id"], stored.Metadata["task_id"] = task.ProjectID, task.ID
	if err := db.UpdateSession(stored); err != nil {
		t.Fatal(err)
	}
	task.AccountID, task.SessionID = p.AccountScopeID, session.ID
	if task.Title == "" {
		task.Title = "Add a rate limit"
	}
	if err := db.PutProjectTask(p.AccountScopeID, &task); err != nil {
		t.Fatal(err)
	}
	return task
}

// Purpose: a run in the own session of a task the person or their AI started
// (swarm_run_task: swarm, coder, finder, designer) ends as one task.run.*
// signal, so workflows follow Swarm's own agents like worker jobs: the task's
// first run with no follow-up id, each later run named by its run id. A
// worker job's task, a task an agent created inside another run, a planning
// run and a replayed status write raise nothing; a task a workflow step
// started is marked source automation (the loop guard).
// Threat: workflows blind to the coder that comes with Swarm, a workflow
// firing for every internal sub-task, a replay running a workflow twice, or a
// workflow step's own task starting workflows in a loop.
// Boundary: sessionV3Executor.recordRunStatus -> Server.signalTaskRunEnded on
// real sessions and project tasks, read back from the signal store; no
// provider is involved.
func TestTaskRunEndRaisesOneSignalForSwarmsOwnAgentsTasks(t *testing.T) {
	server, _, _, _, _ := newRoutedSessionTestServerWithSwarmStore(t)
	db, err := pebblestore.Open(filepath.Join(t.TempDir(), "signals.pebble"))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = db.Close() })
	server.SetSignalStore(pebblestore.NewSignalStore(db))
	exec := newSessionV3Executor(server)
	taskSignals := func(taskID string) []pebblestore.Signal {
		page, err := server.signals.ListAfter(0, 100, pebblestore.SignalFilter{Account: testPrincipal().AccountScopeID, KindPrefixes: []string{"task.run"}})
		if err != nil {
			t.Fatal(err)
		}
		var out []pebblestore.Signal
		for _, sig := range page.Signals {
			if sig.Refs["task_id"] == taskID {
				out = append(out, sig)
			}
		}
		return out
	}
	end := func(session pebblestore.SessionSnapshot, runID, terminal, event string) {
		t.Helper()
		job := sessionV3ExecutorJob{Principal: testPrincipal(), SessionID: session.ID, RunID: runID, EpochID: "epoch-00000000000000000001"}
		for _, step := range []struct{ status, event string }{
			{sessionruntime.RunIntentPendingExecutor, "session.assistant.queued"},
			{sessionruntime.RunIntentRunning, "session.assistant.started"},
			{terminal, event},
			{terminal, event}, // a replay of the same terminal write
		} {
			if _, err := exec.recordRunStatus(job, step.status, "", step.event); err != nil {
				t.Fatalf("record %s: %v", step.status, err)
			}
		}
	}
	newTask := func(id string, task pebblestore.ProjectTaskRecord) (pebblestore.SessionSnapshot, pebblestore.ProjectTaskRecord) {
		session := createSessionsV3PrimaryTestSessionWithWorkspaceAndPreference(t, server, "task-signal-"+id, id, t.TempDir(), pebblestore.ModelPreference{Provider: "test-provider", Model: "test-model", Thinking: "medium"})
		task.ID, task.ProjectID = id, "proj-flow"
		if task.Status == "" {
			task.Status = "in_progress"
		}
		return session, taskSessionFixture(t, server, session, task)
	}

	session, task := newTask("task_coder", pebblestore.ProjectTaskRecord{Agent: "coder"})
	end(session, task.ExecutionRunID(), sessionruntime.RunIntentCompleted, "session.run.completed")
	got := taskSignals(task.ID)
	if len(got) != 1 {
		t.Fatalf("first run end: %d task.run signals, want exactly 1", len(got))
	}
	sig := got[0]
	if sig.Kind != "task.run.succeeded" || sig.DedupKey != "task.run:"+task.ID+":" || sig.Refs["project_id"] != "proj-flow" || sig.Refs["session_id"] != session.ID ||
		sig.Attrs["job_id"] != "task/proj-flow/"+task.ID || sig.Attrs["role"] != "coder" || sig.Attrs["source"] != "task" {
		t.Fatalf("first run end signal = %+v", sig)
	}
	// A follow-up sent into the same session ends as its own run.
	end(session, "run-followup-1", sessionruntime.RunIntentFailed, "session.run.failed")
	got = taskSignals(task.ID)
	if len(got) != 2 || got[1].Kind != "task.run.failed" || got[1].DedupKey != "task.run:"+task.ID+":run-followup-1" {
		t.Fatalf("follow-up end signals = %+v", got)
	}

	// A workflow step's task carries source automation.
	session, task = newTask("task_wf", pebblestore.ProjectTaskRecord{Agent: "finder", AutomationID: "workflow:jdel_x.check"})
	end(session, task.ExecutionRunID(), sessionruntime.RunIntentCompleted, "session.run.completed")
	if got := taskSignals(task.ID); len(got) != 1 || got[0].Attrs["source"] != jobautomation.RequestSourceAutomation {
		t.Fatalf("workflow step task signals = %+v", got)
	}

	// Nothing for a worker job's task (it ends as worker.run.*), a sub-task an
	// agent created, a media task or a planning run.
	p := testPrincipal()
	worker, err := server.sessions.CreateWorker(context.Background(), p.AccountScopeID, p.UserID, pebblestore.CreateWorkerRequest{Role: pebblestore.WorkerRoleCoder, Name: "Coder", IdempotencyKey: "task-signal-coder"})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := server.sessions.Store().WorkerStore().RecordWorkerRun(p.AccountScopeID, pebblestore.WorkerRunRecord{ID: "r1", WorkerID: worker.ID, UserID: p.UserID, Status: "running", RequestSource: "direct", CreatedAt: 1}); err != nil {
		t.Fatal(err)
	}
	for id, task := range map[string]pebblestore.ProjectTaskRecord{
		"task_worker":   {Agent: "coder", WorkerID: worker.ID, WorkerRunID: "r1"},
		"task_subtask":  {Agent: "coder", OriginSessionID: "parent-session"},
		"task_image":    {Agent: "image"},
		"task_planning": {Agent: "swarm", Status: "planning"},
	} {
		session, task := newTask(id, task)
		end(session, task.ExecutionRunID(), sessionruntime.RunIntentCompleted, "session.run.completed")
		if got := taskSignals(task.ID); len(got) != 0 {
			t.Fatalf("%s raised %+v", id, got)
		}
	}
}

// Purpose: a reopen step's follow-up for a task goes into the task's own
// session (the same session and worktree, never a new task), accepted with
// the follow-up's run id; a running task is answered busy so the step waits,
// a cancelled task is refused, and another account cannot reopen it.
// Threat: a fix made in a new task instead of the coder's own session, a
// follow-up sent while the coder is still working, or a cancelled task
// revived.
// Owners: jobAutomationReopener.Reopen -> Server.reopenTask over the real
// session routes and project task records; no provider is reached.
func TestReopenSendsTheFollowupIntoTheTasksOwnSession(t *testing.T) {
	f := newReopenFixture(t)
	p := testPrincipal()
	task := taskSessionFixture(t, f.server, f.session, pebblestore.ProjectTaskRecord{ID: "task_reopen", ProjectID: "proj-reopen", Agent: "coder", Status: "in_progress"})
	// The task's first run has ended: continuation needs a settled run.
	exec := newSessionV3Executor(f.server)
	first := sessionV3ExecutorJob{Principal: p, SessionID: f.session.ID, RunID: pebblestore.InitialTaskRunID(task.ID), EpochID: "epoch-00000000000000000001"}
	for _, step := range []struct{ status, event string }{
		{sessionruntime.RunIntentPendingExecutor, "session.assistant.queued"},
		{sessionruntime.RunIntentRunning, "session.assistant.started"},
		{sessionruntime.RunIntentCompleted, "session.run.completed"},
	} {
		if _, err := exec.recordRunStatus(first, step.status, "", step.event); err != nil {
			t.Fatalf("record %s: %v", step.status, err)
		}
	}
	jobID := "task/proj-reopen/" + task.ID
	reopener := f.server.JobAutomationReopener()
	text := "The reviewer blocked your work.\n\n1. high api/upload.go:40 the limit is never checked\n\n" + jobautomation.ReopenClosing
	before := len(f.userMessages(t, f.session.ID))

	followup, err := reopener.Reopen(context.Background(), jobautomation.ReopenRequest{Account: p.AccountScopeID, JobID: jobID, Message: text, RequestID: "jdel_task.fix"})
	if err != nil || followup == "" {
		t.Fatalf("reopen = %q %v", followup, err)
	}
	got := f.userMessages(t, f.session.ID)
	if len(got) != before+1 || got[len(got)-1] != text {
		t.Fatalf("session user messages = %q", got)
	}
	tasks, err := f.server.sessions.Store().ListProjectTasks(p.AccountScopeID, "proj-reopen", 10)
	if err != nil || len(tasks) != 1 {
		t.Fatalf("tasks after reopen = %d %v (a reopen must not start a new task)", len(tasks), err)
	}

	if _, err := reopener.Reopen(context.Background(), jobautomation.ReopenRequest{Account: "other-account", JobID: jobID, Message: text, RequestID: "jdel_other.fix"}); err == nil {
		t.Fatal("another account reopened the task")
	}
	set := func(status string) {
		t.Helper()
		if _, err := f.server.sessions.Store().UpdateProjectTask(p.AccountScopeID, "proj-reopen", task.ID, func(t *pebblestore.ProjectTaskRecord) error {
			t.Status = status
			return nil
		}); err != nil {
			t.Fatal(err)
		}
	}
	set("in_progress")
	_, err = reopener.Reopen(context.Background(), jobautomation.ReopenRequest{Account: p.AccountScopeID, JobID: jobID, Message: "fix it", RequestID: "jdel_task.busy"})
	var busy interface{ Busy() bool }
	if !errors.As(err, &busy) || !busy.Busy() {
		t.Fatalf("running task: err = %v, want busy", err)
	}
	set("cancelled")
	_, err = reopener.Reopen(context.Background(), jobautomation.ReopenRequest{Account: p.AccountScopeID, JobID: jobID, Message: "fix it", RequestID: "jdel_task.cancelled"})
	var refused *jobautomation.RefusedError
	if !errors.As(err, &refused) {
		t.Fatalf("cancelled task: err = %v, want refused", err)
	}
	if n := len(f.userMessages(t, f.session.ID)); n != before+1 {
		t.Fatalf("a busy or cancelled task got a follow-up: %d user messages", n)
	}
}

// Purpose: a task's result lists its own commits by full id and the files
// they change, read with Git from its worktree of the folder, from its base
// to its head; a task with no commits lists none.
// Threat: a workflow's run step pushing or a reviewer reviewing a head whose
// commits were listed short, cut or from the wrong base.
// Owner: taskResultGit over a real repository and worktree.
func TestTaskResultGitListsTheTasksOwnCommits(t *testing.T) {
	t.Setenv("GIT_CONFIG_GLOBAL", os.DevNull)
	t.Setenv("GIT_CONFIG_NOSYSTEM", "1")
	git := func(dir string, args ...string) string {
		t.Helper()
		out, err := exec.Command("git", append([]string{"-C", dir, "-c", "user.name=t", "-c", "user.email=t@example.invalid"}, args...)...).CombinedOutput()
		if err != nil {
			t.Fatalf("git %v: %v %s", args, err, out)
		}
		return strings.TrimSpace(string(out))
	}
	folder, worktree := t.TempDir(), filepath.Join(t.TempDir(), "task")
	git(folder, "init", "-q", "-b", "dev")
	git(folder, "commit", "-q", "--allow-empty", "-m", "base")
	base := git(folder, "rev-parse", "HEAD")
	git(folder, "worktree", "add", "-q", "-b", "agent/task", worktree)
	if err := os.WriteFile(filepath.Join(worktree, "limit.go"), []byte("package api\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	git(worktree, "add", "limit.go")
	git(worktree, "commit", "-q", "-m", "add the rate limit")
	git(worktree, "commit", "-q", "--allow-empty", "-m", "test it")
	head := git(worktree, "rev-parse", "HEAD")

	g, err := taskResultGit(context.Background(), worktree, base, head)
	if err != nil {
		t.Fatal(err)
	}
	if g.Head != head || g.BaseCommit != base || g.Worktree != worktree || len(g.Commits) != 2 || g.Commits[0].SHA != head || g.Commits[0].Subject != "test it" ||
		g.Commits[1].Subject != "add the rate limit" || len(g.Commits[1].SHA) != 40 || strings.Join(g.Files, ",") != "limit.go" {
		t.Fatalf("task git = %+v", g)
	}
	if empty, err := taskResultGit(context.Background(), worktree, head, head); err != nil || len(empty.Commits) != 0 {
		t.Fatalf("no commits = %+v %v", empty, err)
	}
}

// Purpose: an agent step that names one of Swarm's own agents (here coder)
// runs it as a task in the triggering job's project and folder, exactly as
// swarm_run_task would, with the step's brief and the triggering job's
// result; the task records the workflow that started it (so its end never
// starts workflows) and a coder's worktree starts at the delivery head. The
// same step started again (a restart) is the same task. fixes, and a job with
// no project, are refused before anything is created.
// Threat: a built-in agent step that silently runs a worker preset instead,
// a coder that starts from the folder's branch without the work it follows,
// a workflow step's task starting workflows in a loop, or a duplicate task
// after a restart.
// Owners: jobAutomationAgents.Start -> Server.startBuiltinAgentStep ->
// Server.CreateProjectTask (the internal workflow marker) over the real
// project store; the task's run is not executed (no provider). The native
// reviewer extension also proves exact-head same/new admission with zero
// workers and persisted OK/BLOCK/malformed findings through waitTaskJob;
// the same API service reads the identical persisted verdict after settlement.
// Fix-enabled cases exercise the real Runner and native admission with real Git
// commits to prevent oversized, blocked or unreadable fixes moving delivery heads.
// This service-level harness is the narrowest layer spanning those boundaries.
// Purpose: workflow agent admission and native review settlement must preserve
// the delivery checkout and unattended consent refusal. With bypass disabled,
// this integration fixture exercises taskWorkflowAgents, AuthorizeToolCall and
// rejectWorkerPrompt against real task/session records, proving provenance
// wiring rejects owner ask rules without creating unreachable pending prompts.
func TestBuiltinAgentStepStartsATaskAtTheDeliveryHead(t *testing.T) {
	storePath := filepath.Join(t.TempDir(), "native-review.pebble")
	server, sessions, _, closeStore, _ := newRoutedSessionTestServerAt(t, storePath, false)
	var savedOKTaskID string
	store := sessions.Store().Underlying()
	principal := testPrincipal()
	ids := pebblestore.NewIdentityStore(store)
	if _, err := ids.PutUser(principal.User); err != nil {
		t.Fatal(err)
	}
	if _, err := ids.PutAccountScope(principal.AccountScope); err != nil {
		t.Fatal(err)
	}
	if _, err := ids.PutAccountUser(principal.AccountUser); err != nil {
		t.Fatal(err)
	}
	workspaces := workspace.NewService(pebblestore.NewWorkspaceStore(store))
	server.workspace = workspaces
	trees := worktree.NewService(pebblestore.NewWorktreeStore(store), workspaces, nil)
	server.SetWorktreeService(trees)
	for ready := false; !ready; {
		var err error
		if ready, err = sessions.Store().BackfillRepositoryHistory(100); err != nil {
			t.Fatal(err)
		}
	}
	folder := t.TempDir()
	outboxPinGit(t, folder, "init", "-q", "-b", "main")
	outboxPinGit(t, folder, "commit", "-q", "--allow-empty", "-m", "start")
	start := outboxPinGit(t, folder, "rev-parse", "HEAD")
	outboxPinGit(t, folder, "commit", "-q", "--allow-empty", "-m", "the coder's work")
	head := outboxPinGit(t, folder, "rev-parse", "HEAD")
	// The folder's branch is back where the coder started; the work is on its own branch.
	outboxPinGit(t, folder, "branch", "-q", "agent/first", head)
	outboxPinGit(t, folder, "reset", "-q", "--hard", start)
	added, err := workspaces.AddForPrincipal(principal, folder, "site", "", false)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := server.topology.EnsureLocalSelfPlacementForPrincipal(principal.AccountScopeID, principal.UserID); err != nil {
		t.Fatal(err)
	}
	entry, found, err := workspaces.GetByWorkspaceIDForPrincipal(principal, added.WorkspaceID)
	if err != nil || !found {
		t.Fatal(err)
	}
	if _, err := server.topology.EnsureLocalWorkspaceSelfBindingForPrincipal(principal.AccountScopeID, principal.UserID, entry); err != nil {
		t.Fatal(err)
	}
	project := &pebblestore.ProjectRecord{ID: "proj-steps", Name: "Site", Workspaces: []pebblestore.ProjectWorkspaceRef{{WorkspaceID: added.WorkspaceID, Path: added.WorkspacePath, Role: "primary_code"}}}
	if err := sessions.Store().PutProject(principal.AccountScopeID, project); err != nil {
		t.Fatal(err)
	}
	req := requestWithScopedToken(withTestPrincipal(httptest.NewRequest(http.MethodPost, "/mcp", nil)),
		&pebblestore.ScopedTokenRecord{Scopes: []string{"swarm:read", "sessions:read", "sessions:write", "automations:read"}})
	call := &controlMCPCall{request: req, next: server.internalRouteHandler(), server: server}
	server.jobAutomationCallFn = func(context.Context) (*controlMCPCall, error) { return call, nil }

	triggering := jobautomation.Result{V: 1, JobID: "task/proj-steps/task_first", Kind: jobautomation.KindTask, Role: "coder", ProjectID: project.ID, Folder: added.WorkspacePath,
		Status: jobautomation.StatusSucceeded, Report: "Added the limit.", Git: &jobautomation.ResultGit{Branch: "agent/first", Head: head, BaseCommit: head}}
	step := jobautomation.AgentRequest{Account: principal.AccountScopeID, Folder: added.WorkspacePath, Worker: "coder", Brief: "Add a test for the limit.",
		Result: triggering, Head: head, IdempotencyKey: "jdel_steps.test.0"}
	agents := server.JobAutomationAgents()
	jobID, err := agents.Start(context.Background(), step)
	if err != nil {
		t.Fatal(err)
	}
	projectID, taskID, ok := parseTaskJobID(jobID)
	if !ok || projectID != project.ID {
		t.Fatalf("job id = %q", jobID)
	}
	task, found, err := sessions.Store().GetProjectTask(principal.AccountScopeID, project.ID, taskID)
	if err != nil || !found {
		t.Fatalf("task %s: %v %v", taskID, found, err)
	}
	if task.Agent != "coder" || task.AutomationID != "workflow:jdel_steps.test.0" || task.StartCommit != head || task.OriginSessionID != "" ||
		!strings.Contains(task.Description, "Add a test for the limit.") || !strings.Contains(task.Description, "task/proj-steps/task_first") {
		t.Fatalf("step task = agent %q automation %q start %q origin %q\n%s", task.Agent, task.AutomationID, task.StartCommit, task.OriginSessionID, task.Description)
	}
	// The coder's worktree starts at the delivery head, not the folder's branch.
	if got := outboxPinGit(t, task.WorkspacePath, "rev-parse", "HEAD"); got != head || task.BaseCommit != head {
		t.Fatalf("coder worktree at %s (base %s), want the delivery head %s", got, task.BaseCommit, head)
	}
	if again, err := agents.Start(context.Background(), step); err != nil || again != jobID {
		t.Fatalf("restarted step = %q %v, want the same task %q", again, err, jobID)
	}
	workers, err := sessions.Store().WorkerStore().ListWorkers(principal.AccountScopeID, pebblestore.ListWorkersQuery{Limit: 10})
	if err != nil || len(workers.Workers) != 0 {
		t.Fatalf("a built-in agent step deployed workers: %v %v", workers, err)
	}

	for name, bad := range map[string]jobautomation.AgentRequest{
		"fixes":      {Account: step.Account, Folder: step.Folder, Worker: "coder", Brief: "Fix it.", Result: triggering, Fixes: jobautomation.FixesSecurity, IdempotencyKey: "jdel_steps.bad.0"},
		"no project": {Account: step.Account, Folder: step.Folder, Worker: "finder", Brief: "Look.", Result: jobautomation.Result{JobID: "worker/w/r"}, IdempotencyKey: "jdel_steps.bad.1"},
	} {
		_, err := agents.Start(context.Background(), bad)
		var refused *jobautomation.RefusedError
		if !errors.As(err, &refused) {
			t.Fatalf("%s: err = %v, want refused", name, err)
		}
	}
	tasks, err := sessions.Store().ListProjectTasks(principal.AccountScopeID, project.ID, 10)
	if err != nil || len(tasks) != 1 {
		t.Fatalf("tasks = %d %v, want only the step's task", len(tasks), err)
	}

	// Native reviewer admission uses the real execution host and Git target,
	// but never invokes a model. Explicit sandbox-off is the existing harness
	// pattern; read runtime mode, detached checkout and exact head are asserted below.
	// This does not qualify sandbox-on enforcement.
	previous := sandbox.Default()
	manager, err := sandbox.NewManager(context.Background(), sandbox.Config{Mode: sandbox.ModeOff, Backend: sandbox.BackendBubblewrap}, nil)
	if err != nil {
		t.Fatal(err)
	}
	sandbox.SetDefault(manager)
	t.Cleanup(func() { sandbox.SetDefault(previous) })
	runner := server.runner.(*runruntime.Service)
	runner.SetWorkspaceService(workspaces)
	runner.SetSessionDeployCanonicalizer(server.CanonicalizeSessionDeploy)
	execution, err := runruntime.NewWorkerExecutionService(runner, sessions.Store(), trees, server.applySessionV3PrimaryMutation, func(identity.Principal, pebblestore.V3SessionRunIntent) bool { return true })
	if err != nil {
		t.Fatal(err)
	}
	runner.SetWorkerExecutionService(execution)
	outboxPinGit(t, task.WorkspacePath, "commit", "-q", "--allow-empty", "-m", "delivery")
	reviewHead := outboxPinGit(t, task.WorkspacePath, "rev-parse", "HEAD")
	for i, report := range []string{"VERDICT: OK\nSUMMARY: Checked.\nFINDINGS: None.\nTESTS RUN: None.", "VERDICT: BLOCK\nSUMMARY: Unsafe.\nFINDINGS: Fix the unchecked input.\nTESTS RUN: None.", "not a review", "", "VERDICT: OK\nSUMMARY: Checked.\nFINDINGS: None.\nTESTS RUN: None."} {
		checkout := "same"
		if i == 3 {
			checkout = "new"
		}
		reviewStep := jobautomation.AgentRequest{Account: step.Account, Folder: step.Folder, Worker: "reviewer", Brief: "Review the limit.", Checkout: checkout, Head: reviewHead, IdempotencyKey: fmt.Sprintf("review-%d", i),
			Result: jobautomation.Result{V: 1, JobID: jobID, ProjectID: project.ID, Brief: task.Description, Report: "Implemented the limit."}}
		reviewID, err := agents.Start(context.Background(), reviewStep)
		if err != nil {
			t.Fatal(err)
		}
		_, reviewTaskID, _ := parseTaskJobID(reviewID)
		reviewTask, found, err := sessions.Store().GetProjectTask(principal.AccountScopeID, project.ID, reviewTaskID)
		if err != nil || !found {
			t.Fatalf("review task: %v %v", found, err)
		}
		snapshot, found, err := sessions.Store().GetSession(reviewTask.SessionID)
		if err != nil || !found || snapshot.Metadata["agent_name"] != "system-reviewer" || reviewTask.WorkerID != "" {
			t.Fatalf("native reviewer: %+v %v", snapshot, err)
		}
		assertWorkflowReviewConsentRefusal(t, sessions.Store(), snapshot, "bash", `{"command":"git log -1; git diff --stat","category":"read","explanation":["Inspect repository history and changes"],"critical":false}`)
		if snapshot.Metadata["runtime_mode"] != "read" {
			t.Fatalf("review runtime mode = %v", snapshot.Metadata["runtime_mode"])
		}
		if checkout == "new" && outboxPinGit(t, reviewTask.WorkspacePath, "rev-parse", "--abbrev-ref", "HEAD") != "HEAD" {
			t.Fatal("review checkout is not detached")
		}
		if checkout == "same" && reviewTask.WorkspacePath != task.WorkspacePath {
			t.Fatal("review did not borrow the job's worktree")
		}
		if checkout == "new" && reviewTask.WorkspacePath == task.WorkspacePath {
			t.Fatal("review did not allocate its own checkout")
		}
		if got := outboxPinGit(t, reviewTask.WorkspacePath, "rev-parse", "HEAD"); got != reviewHead {
			t.Fatalf("review head = %s", got)
		}
		if again, err := agents.Start(context.Background(), reviewStep); err != nil || again != reviewID {
			t.Fatalf("review replay: %s %v", again, err)
		}
		intent, found, err := sessions.Store().GetV3SessionActiveRunIntent(reviewTask.SessionID)
		if err != nil || !found {
			t.Fatalf("review intent: %v %v", found, err)
		}
		if checkout == "same" {
			key := fmt.Sprintf("review-report-%d", i)
			_, err := server.applySessionV3PrimaryMutation(sessionruntime.SessionMutationInput{SessionID: reviewTask.SessionID, UserID: principal.UserID, AccountScopeID: principal.AccountScopeID,
				Kind: sessionruntime.SessionMutationAppendMessage, ClientRequestID: key, IdempotencyKey: key, PayloadHash: key, RequestHash: key,
				Message: &pebblestore.MessageSnapshot{ID: key, SessionID: reviewTask.SessionID, UserID: principal.UserID, AccountScopeID: principal.AccountScopeID, Role: "assistant", Content: report, CreatedAt: int64(100 + i)}, NowUnixMs: int64(100 + i)})
			if err != nil {
				t.Fatal(err)
			}
		}
		// Purpose: CleanupTaskReview must persist retryable failures without handing
		// the verdict to waitTaskJob. Temporarily unavailable source storage exercises
		// real cleanup validation, backoff marking and the API waiter together.
		if i == 3 {
			if err := os.Rename(folder, folder+"-unavailable"); err != nil {
				t.Fatal(err)
			}
			t.Cleanup(func() { _ = os.Rename(folder+"-unavailable", folder) })
		}
		// Exercise the real terminal hook, including unsuccessful review runs.
		terminal := sessionruntime.RunIntentCompleted
		if i == 1 {
			terminal = sessionruntime.RunIntentFailed
		}
		if i == 2 {
			terminal = sessionruntime.RunIntentCancelled
		}
		if _, err := newSessionV3Executor(server).recordRunStatus(sessionV3ExecutorJob{Principal: principal, SessionID: reviewTask.SessionID, RunID: intent.RunID, EpochID: intent.EpochID}, terminal, "", "session.run."+terminal); err != nil {
			t.Fatal(err)
		}
		if i == 3 {
			failed, _, err := sessions.Store().GetProjectTask(principal.AccountScopeID, project.ID, reviewTaskID)
			if err != nil || failed.Review == nil || failed.ReviewCleanup == nil {
				t.Fatalf("missing retry state %+v %v", failed, err)
			}
			c := failed.ReviewCleanup
			if c.State != "failed" || c.Attempts != 1 || c.NextAttemptAt <= time.Now().UnixMilli() || c.FailureReported || c.LastError == "" {
				t.Fatalf("temporary cleanup failure not marked retryable %+v", c)
			}
			if err := execution.CleanupTaskReview(failed); !errors.Is(err, runruntime.ErrTaskReviewCleanupPending) {
				t.Fatalf("backoff not pending %v", err)
			}
			// Force the next attempt due without waiting through production backoff.
			if _, err := sessions.Store().UpdateProjectTask(principal.AccountScopeID, project.ID, reviewTaskID, func(current *pebblestore.ProjectTaskRecord) error {
				current.ReviewCleanup.NextAttemptAt = 1
				return nil
			}); err != nil {
				t.Fatal(err)
			}
			server.SetSignalStore(pebblestore.NewSignalStore(sessions.Store().Underlying()))
			ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
			defer cancel()
			type waitResult struct {
				outcome jobautomation.AgentOutcome
				err     error
			}
			finished := make(chan waitResult, 1)
			go func() {
				out, err := server.waitTaskJob(ctx, principal.AccountScopeID, reviewID)
				finished <- waitResult{out, err}
			}()
			// Observe the waiter's own failed attempt, not merely a pre-seeded state.
			deadline := time.NewTimer(3 * time.Second)
			defer deadline.Stop()
			tick := time.NewTicker(10 * time.Millisecond)
			defer tick.Stop()
		waiting:
			for {
				select {
				case result := <-finished:
					t.Fatalf("retryable cleanup handed off %+v %v", result.outcome, result.err)
				case <-deadline.C:
					t.Fatal("waiter did not attempt cleanup")
				case <-tick.C:
					current, _, err := sessions.Store().GetProjectTask(principal.AccountScopeID, project.ID, reviewTaskID)
					if err != nil {
						t.Fatal(err)
					}
					if c := current.ReviewCleanup; c.State == "failed" && c.Attempts == 2 {
						if c.NextAttemptAt <= time.Now().UnixMilli() || c.FailureReported {
							t.Fatalf("retry not scheduled %+v", c)
						}
						break waiting
					}
				}
			}
			select {
			case result := <-finished:
				t.Fatalf("waiter returned during backoff %+v %v", result.outcome, result.err)
			case <-time.After(50 * time.Millisecond):
			}
			if err := os.Rename(folder+"-unavailable", folder); err != nil {
				t.Fatal(err)
			}
			if _, err := sessions.Store().UpdateProjectTask(principal.AccountScopeID, project.ID, reviewTaskID, func(current *pebblestore.ProjectTaskRecord) error {
				current.ReviewCleanup.NextAttemptAt = 1
				return nil
			}); err != nil {
				t.Fatal(err)
			}
			if _, err := server.signals.Append(pebblestore.Signal{Account: principal.AccountScopeID, Source: "test", Kind: "task.run.completed", Summary: "Cleanup retry due"}); err != nil {
				t.Fatal(err)
			}
			select {
			case result := <-finished:
				if result.err != nil || result.outcome.Verdict != failed.Review.Verdict {
					t.Fatalf("retry outcome %+v %v", result.outcome, result.err)
				}
			case <-ctx.Done():
				t.Fatal("waiter did not return after cleanup recovered")
			}
			current, _, err := sessions.Store().GetProjectTask(principal.AccountScopeID, project.ID, reviewTaskID)
			if err != nil || current.ReviewCleanup.State != "done" || current.ReviewCleanup.Attempts != 3 || *current.Review != *failed.Review {
				t.Fatalf("recovery changed verdict or missed cleanup %+v %v", current, err)
			}
		}
		settledTask, _, err := sessions.Store().GetProjectTask(principal.AccountScopeID, project.ID, reviewTaskID)
		if err != nil || settledTask.ReviewCleanup == nil || settledTask.ReviewCleanup.State != "done" {
			t.Fatalf("terminal hook did not release review before workflow handoff %+v %v", settledTask, err)
		}
		if i == 1 || i == 2 {
			if settledTask.Review == nil || settledTask.Review.Verdict != "block" {
				t.Fatal("unsuccessful terminal hook did not block review")
			}
		}
		if i == 4 {
			// Recreate interrupted persistence without overwriting a saved verdict:
			// use an independent task pointing at the still-borrowed session.
			repair := *reviewTask
			repair.ID = "moved-repair"
			repair.Review = nil
			repair.Status = "needs_review"
			if err := sessions.Store().PutProjectTask(principal.AccountScopeID, &repair); err != nil {
				t.Fatal(err)
			}
			outboxPinGit(t, task.WorkspacePath, "commit", "-q", "--allow-empty", "-m", "moved")
			checked, err := server.repairTaskReview(&repair)
			if err != nil || checked == nil || checked.Verdict != "block" {
				t.Fatalf("moved repair %+v %v", checked, err)
			}
			outboxPinGit(t, task.WorkspacePath, "reset", "--hard", reviewHead)
			continue
		}
		if checkout == "new" {
			// An unread reviewer settles and removes its disposable checkout at
			// run termination, rather than relying on an MCP result read.
			settled, _, err := sessions.Store().GetProjectTask(principal.AccountScopeID, project.ID, reviewTaskID)
			if err != nil || settled.Review == nil || settled.Review.Verdict != "block" {
				t.Fatalf("unread review settlement: %+v %v", settled, err)
			}
			if _, err := os.Stat(reviewTask.WorkspacePath); !os.IsNotExist(err) {
				t.Fatalf("review checkout retained: %v", err)
			}
			if _, err := server.settleTaskReview(settled); err != nil {
				t.Fatalf("settlement retry: %v", err)
			}
			continue
		}
		{
			outcome, err := server.waitTaskJob(context.Background(), principal.AccountScopeID, reviewID)
			if err != nil {
				t.Fatal(err)
			}
			want := "block"
			if i == 0 {
				want = "ok"
				savedOKTaskID = reviewTaskID
			}
			if outcome.Verdict != want || outcome.ReviewUnreadable != (i == 2) || (i == 1 && outcome.Findings != "Fix the unchecked input.") || (i == 2 && outcome.Findings != "") {
				t.Fatalf("review outcome: %+v", outcome)
			}
			call, err := server.jobAutomationCall(context.Background())
			if err != nil {
				t.Fatal(err)
			}
			view, err := controlMCPTaskResult(call, map[string]any{"project_id": project.ID, "task_id": reviewTaskID})
			if err != nil {
				t.Fatal(err)
			}
			if controlMCPMap(view)["unintegrated_commits"] != 0 {
				t.Fatalf("review claims commits: %+v", view)
			}
			visible, ok := controlMCPMap(view)["review"].(*pebblestore.JobReview)
			if !ok || visible.Verdict != outcome.Verdict || visible.Findings != pebblestore.ParseJobReview(report, reviewHead, reviewID, "", 0).Findings {
				t.Fatalf("task result review = %+v", visible)
			}
			stored, _, _ := sessions.Store().GetProjectTask(principal.AccountScopeID, project.ID, reviewTaskID)
			// Repeated observation must preserve the stored review.
			rederived, err := server.taskReview(stored)
			if err != nil || rederived.Verdict != outcome.Verdict || rederived.Findings != visible.Findings {
				t.Fatalf("rederived: %+v %v", rederived, err)
			}
		}
	}
	workers, err = sessions.Store().WorkerStore().ListWorkers(principal.AccountScopeID, pebblestore.ListWorkersQuery{Limit: 10})
	if err != nil || len(workers.Workers) != 0 {
		t.Fatalf("reviewer created workers: %+v %v", workers, err)
	}
	// The Runner uses the real API agent adapter; only provider output and run
	// completion are supplied by this harness. Conditions and reopen dispatch
	// cross their production boundaries against zero reviewer workers.
	for i, report := range []string{"VERDICT: OK\nSUMMARY: Checked.\nFINDINGS: None.\nTESTS RUN: None.", "VERDICT: BLOCK\nSUMMARY: Unsafe.\nFINDINGS: Fix input.\nTESTS RUN: None.", "not a review", "VERDICT: OK\nSUMMARY: Fixed.\nFINDINGS: None.\nTESTS RUN: None.", "VERDICT: OK\nSUMMARY: Fixed.\nFINDINGS: None.\nTESTS RUN: None.", "VERDICT: BLOCK\nSUMMARY: Unsafe.\nFINDINGS: Fix input.\nTESTS RUN: None.", "VERDICT: OK\nSUMMARY: Fixed.\nFINDINGS: None.\nTESTS RUN: None."} {
		fixes, fixHead := "", ""
		if i >= 3 {
			fixes = jobautomation.FixesSecurity
		}
		feed := pebblestore.NewSignalStore(store)
		server.SetSignalStore(feed)
		automationStore := jobautomation.NewStore(store)
		result := &jobautomation.Result{V: 1, JobID: jobID, Kind: jobautomation.KindTask, Role: "coder", ProjectID: project.ID, Folder: folder, Status: jobautomation.StatusSucceeded, Brief: task.Description, Report: "Delivered", Git: &jobautomation.ResultGit{Head: reviewHead, BaseCommit: head, Commits: []jobautomation.ResultCommit{{SHA: reviewHead}}}}
		workflow := jobautomation.NewRunner(jobautomation.Options{Store: automationStore, Feed: feed, Account: func() string { return principal.AccountScopeID }, Results: reviewLoopResults{result: result}, Agent: agents, Reopen: server.JobAutomationReopener(), Head: ImportJobRef, Verify: VerifyJobHead, Lines: JobHeadChangedLines})
		if err := workflow.ProcessOnce(context.Background()); err != nil {
			t.Fatal(err)
		}
		name := fmt.Sprintf("native-loop-%d", i)
		_, _, err := automationStore.Put(principal.AccountScopeID, jobautomation.Automation{Name: name, Scope: jobautomation.Scope{Folder: folder, Role: "coder"}, When: jobautomation.WhenSucceeded, Enabled: true, Steps: []jobautomation.Step{
			{ID: "review", Type: jobautomation.StepAgent, Worker: "reviewer", Brief: "Review delivery.", Fixes: fixes},
			{ID: "fix", Type: jobautomation.StepReopen, From: "review", MaxRounds: 2, Message: "Fix these", If: []jobautomation.Condition{{Step: "review", Verdict: jobautomation.VerdictBlock}}},
			{ID: "after", Type: jobautomation.StepAgent, Worker: "finder", Brief: "Check delivery.", If: []jobautomation.Condition{{Step: "review", Verdict: jobautomation.VerdictOK}}},
		}})
		if err != nil {
			t.Fatal(err)
		}
		before, _ := sessions.ListMessages(task.SessionID, 0, 100)
		_, err = feed.Append(pebblestore.Signal{Kind: "task.run.succeeded", Severity: pebblestore.SignalSeverityInfo, Source: pebblestore.SignalSourceSwarmd, Account: principal.AccountScopeID, Summary: "Task done", DedupKey: name, Refs: map[string]string{"project_id": project.ID, "task_id": task.ID}, Attrs: map[string]string{"source": "direct", "role": "coder"}})
		if err != nil {
			t.Fatal(err)
		}
		ctx, cancel := context.WithTimeout(context.Background(), 180*time.Second)
		done := make(chan error, 1)
		go func() { done <- workflow.ProcessOnce(ctx) }()
		completed := false
		for !completed {
			select {
			case err := <-done:
				if err != nil {
					t.Fatal(err)
				}
				completed = true
			case <-ctx.Done():
				ds, _ := automationStore.JobDeliveries(principal.AccountScopeID, jobID)
				t.Fatalf("native workflow %d timed out %+v", i, ds)
			case <-time.After(10 * time.Millisecond):
				ready := map[string]bool{}
				ds, _ := automationStore.JobDeliveries(principal.AccountScopeID, jobID)
				for _, d := range ds {
					for _, step := range d.Steps {
						if id, ok := step.Outcome["job_id"].(string); ok {
							ready[id] = true
						}
					}
				}
				records, err := sessions.Store().ListProjectTasks(principal.AccountScopeID, project.ID, 100)
				if err != nil {
					t.Fatal(err)
				}
				for _, record := range records {
					if record.ID == task.ID {
						continue
					}
					if !ready[taskRunJobID(project.ID, record.ID)] {
						continue
					}
					intent, found, err := sessions.Store().GetV3SessionActiveRunIntent(record.SessionID)
					if err != nil || !found {
						continue
					}
					if record.Agent == "reviewer" {
						if fixes != "" {
							snap, _, _ := sessions.Store().GetSession(record.SessionID)
							if snap.Metadata["agent_name"] != "system-reviewer-fixer" || snap.Metadata["runtime_mode"] != "readwrite" {
								t.Fatalf("fix profile %+v", snap.Metadata)
							}
							assertWorkflowReviewConsentRefusal(t, sessions.Store(), snap, "git_add", `{"pathspec":["security.txt"]}`)
							assertWorkflowReviewConsentRefusal(t, sessions.Store(), snap, "git_commit", `{"message":"security fix"}`)
							lines := 2
							if i == 4 {
								lines = 81
							}
							if err := os.WriteFile(filepath.Join(record.WorkspacePath, "security.txt"), []byte(strings.Repeat("checked\n", lines)), 0600); err != nil {
								t.Fatal(err)
							}
							outboxPinGit(t, record.WorkspacePath, "add", "security.txt")
							outboxPinGit(t, record.WorkspacePath, "commit", "-qm", "security fix")
							fixHead = outboxPinGit(t, record.WorkspacePath, "rev-parse", "HEAD")
						}
						key := "native-report-" + record.ID
						_, err := server.applySessionV3PrimaryMutation(sessionruntime.SessionMutationInput{SessionID: record.SessionID, UserID: principal.UserID, AccountScopeID: principal.AccountScopeID, Kind: sessionruntime.SessionMutationAppendMessage, ClientRequestID: key, IdempotencyKey: key, PayloadHash: key, RequestHash: key, Message: &pebblestore.MessageSnapshot{ID: key, SessionID: record.SessionID, UserID: principal.UserID, AccountScopeID: principal.AccountScopeID, Role: "assistant", Content: report, CreatedAt: 200}, NowUnixMs: 200})
						if err != nil {
							t.Fatal(err)
						}
					}
					if i == 6 && record.Agent == "reviewer" {
						// Persist the OK binding, then lose the readable branch before
						// the waiter consumes it, matching worker fix-result failure.
						if _, err := execution.FinishTaskReview(&record, pebblestore.ParseJobReview(report, reviewHead, taskRunJobID(project.ID, record.ID), "", 200)); err != nil {
							t.Fatal(err)
						}
						outboxPinGit(t, record.WorkspacePath, "checkout", "--detach")
					}
					if _, err := newSessionV3Executor(server).recordRunStatus(sessionV3ExecutorJob{Principal: principal, SessionID: record.SessionID, RunID: intent.RunID, EpochID: intent.EpochID}, sessionruntime.RunIntentCompleted, "", "session.assistant.completed"); err != nil {
						t.Fatal(err)
					}
					// Normal settlement through FinishTaskReview must persist the
					// executing session model, not empty task submission overrides.
					if record.Agent == "reviewer" {
						snap, _, err := sessions.GetSession(record.SessionID)
						if err != nil || snap.Preference.Provider == "" || snap.Preference.Model == "" {
							t.Fatalf("review session preference %+v %v", snap.Preference, err)
						}
						saved, _, err := sessions.Store().GetProjectTask(principal.AccountScopeID, project.ID, record.ID)
						if err != nil || saved.Review == nil || saved.Review.Model != snap.Preference.Provider+"/"+snap.Preference.Model {
							t.Fatalf("settled review model %+v %v", saved, err)
						}
					}
				}
			}
		}
		cancel()
		deliveries, err := automationStore.JobDeliveries(principal.AccountScopeID, jobID)
		if err != nil {
			t.Fatal(err)
		}
		var delivery *jobautomation.Delivery
		for j := range deliveries {
			if deliveries[j].AutomationName == name {
				delivery = &deliveries[j]
			}
		}
		if delivery == nil {
			t.Fatalf("missing native delivery: %+v", deliveries)
		}
		if fixes != "" {
			wantHead := reviewHead
			if i == 3 {
				wantHead = fixHead
			}
			if delivery.Head != wantHead {
				t.Fatalf("fix case %d head %s want %s", i, delivery.Head, wantHead)
			}
			if i == 4 && delivery.Steps[0].Reason != jobautomation.ReasonFixesTooLarge {
				t.Fatalf("oversized %+v", delivery.Steps[0])
			}
			if i == 6 && delivery.Steps[0].Reason != jobautomation.ReasonFixesUnreadable {
				t.Fatalf("unreadable %+v", delivery.Steps[0])
			}
			id, _ := delivery.Steps[0].Outcome["job_id"].(string)
			_, tid, _ := parseTaskJobID(id)
			saved, _, _ := sessions.Store().GetProjectTask(principal.AccountScopeID, project.ID, tid)
			if i != 6 && (saved.Review == nil || saved.Review.FixHead != fixHead) {
				t.Fatalf("fix binding %+v", saved.Review)
			}
			// Purpose: taskJobResult must expose the saved fix binding to later
			// workflow steps, not misrepresent OK as applying to the original head.
			// The real API result boundary is the narrowest serialization proof.
			if i == 3 || i == 4 {
				result, err := server.taskJobResult(context.WithoutCancel(ctx), principal.AccountScopeID, project.ID, tid, jobautomation.StatusSucceeded)
				if err != nil || result == nil || result.Review["fix_head"] != fixHead || result.Review["head"] != reviewHead {
					t.Fatalf("task result lost fix binding: %+v %v", result, err)
				}
			}
		}
		want := "block"
		if i == 0 || i == 3 || i == 4 || i == 6 {
			want = "ok"
		}
		if delivery.Steps[0].Outcome["verdict"] != want {
			t.Fatalf("native outcome %+v", delivery)
		}
		after, _ := sessions.ListMessages(task.SessionID, 0, 100)
		if i == 1 || i == 5 {
			if len(after) != len(before)+1 || after[len(after)-1].Content != "Fix these\n\nFix input.\n\n"+jobautomation.ReopenClosing {
				t.Fatalf("native reopen messages %+v", after)
			}
		} else if len(after) != len(before) {
			t.Fatal("unexpected reopen")
		}
		if i == 0 && delivery.Steps[2].State == jobautomation.StateSkipped {
			t.Fatal("ok condition skipped")
		}
		if i == 2 && delivery.Steps[0].Outcome["review_unreadable"] != true {
			t.Fatal("malformed review readable")
		}
		spec, err := automationStore.FindByName(principal.AccountScopeID, name)
		if err != nil {
			t.Fatal(err)
		}
		spec.Enabled = false
		if _, _, err := automationStore.Put(principal.AccountScopeID, spec); err != nil {
			t.Fatal(err)
		}
		if intent, found, _ := sessions.Store().GetV3SessionActiveRunIntent(task.SessionID); found {
			if _, err := newSessionV3Executor(server).recordRunStatus(sessionV3ExecutorJob{Principal: principal, SessionID: task.SessionID, RunID: intent.RunID, EpochID: intent.EpochID}, sessionruntime.RunIntentCompleted, "", "session.assistant.completed"); err != nil {
				t.Fatal(err)
			}
		}
	}
	workers, err = sessions.Store().WorkerStore().ListWorkers(principal.AccountScopeID, pebblestore.ListWorkersQuery{Limit: 10})
	if err != nil || len(workers.Workers) != 0 {
		t.Fatal("native workflows created workers")
	}
	// Naming a custom reviewer by ID must use Dispatch, not task admission.
	if err := pebblestore.NewModelCatalogStore(store).SetRecord(pebblestore.ModelCatalogRecord{Provider: "test-provider", Model: "test-model", ContextWindow: 32000, MaxOutputTokens: 4096}); err != nil {
		t.Fatal(err)
	}
	custom, err := sessions.CreateWorker(context.Background(), principal.AccountScopeID, principal.UserID, pebblestore.CreateWorkerRequest{Role: pebblestore.WorkerRoleReviewer, Name: "Custom review", IdempotencyKey: "custom-review"})
	if err != nil {
		t.Fatal(err)
	}
	custom, err = sessions.Store().WorkerStore().InitializeWorkerModelProfile(principal.AccountScopeID, principal.UserID, custom.ID, custom.Revision, &pebblestore.SessionModelProfileSnapshot{Source: pebblestore.SessionModelProfileSourceSwarmSettings, Action: pebblestore.ModelProfileSelection{Provider: "test-provider", Model: "test-model"}, Plan: &pebblestore.ModelProfileSelection{Provider: "test-provider", Model: "test-model"}})
	if err != nil {
		t.Fatal(err)
	}
	custom, err = sessions.Store().WorkerStore().ActivateWorker(principal.AccountScopeID, principal.UserID, custom.ID, custom.Revision, nil)
	if err != nil {
		t.Fatal(err)
	}
	beforeTasks, err := sessions.Store().ListProjectTasks(principal.AccountScopeID, project.ID, 100)
	if err != nil {
		t.Fatal(err)
	}
	customJob, err := agents.Start(context.Background(), jobautomation.AgentRequest{Account: principal.AccountScopeID, Folder: folder, Worker: custom.ID, Brief: "Review", Checkout: "new", Head: reviewHead, IdempotencyKey: "custom-review-step", Result: jobautomation.Result{V: 1, JobID: jobID}})
	if err != nil {
		t.Fatal(err)
	}
	if !strings.HasPrefix(customJob, "worker/"+custom.ID+"/") {
		t.Fatalf("custom reviewer job %s", customJob)
	}
	afterTasks, err := sessions.Store().ListProjectTasks(principal.AccountScopeID, project.ID, 100)
	if err != nil {
		t.Fatal(err)
	}
	// Worker dispatch retains its existing worker-owned project projection;
	// it must not create a native agent task through startBuiltinAgentStep.
	nativeCount := func(records []pebblestore.ProjectTaskRecord) int {
		n := 0
		for _, record := range records {
			if record.WorkerID == "" {
				n++
			}
		}
		return n
	}
	if nativeCount(afterTasks) != nativeCount(beforeTasks) {
		t.Fatal("custom reviewer created native task")
	}
	// Purpose: executor completion must preserve the verdict and run-end signal while cleanup
	// fails permanently. The executor boundary proves cleanup cannot fail the
	// completed run, while the waiter still rejects an unsafe handoff.
	server.CancelInFlightRuns()
	if !server.WaitForInFlightRuns(2 * time.Second) {
		t.Fatal("executor did not stop")
	}
	server.SetSignalStore(pebblestore.NewSignalStore(store))
	cleanupSnapshot, foundCleanup, err := sessions.GetSession(task.SessionID)
	if err != nil || !foundCleanup {
		t.Fatalf("cleanup session %v", err)
	}
	cleanupTask := taskSessionFixture(t, server, cleanupSnapshot, pebblestore.ProjectTaskRecord{ID: "cleanup-signal", ProjectID: project.ID, Agent: "reviewer", Status: "in_progress", StartCommit: reviewHead})
	cleanupJob := sessionV3ExecutorJob{Principal: principal, SessionID: cleanupSnapshot.ID, RunID: "cleanup-followup", EpochID: "epoch-00000000000000000001"}
	for _, status := range []string{sessionruntime.RunIntentPendingExecutor, sessionruntime.RunIntentRunning, sessionruntime.RunIntentCompleted} {
		var err error
		if status == sessionruntime.RunIntentCompleted {
			_, err = newSessionV3Executor(server).completeRun(cleanupJob, sessionV3AssistantResponse{Content: "Review finished"})
		} else {
			_, err = newSessionV3Executor(server).recordRunStatus(cleanupJob, status, "", "session.run."+status)
		}
		if err != nil {
			t.Fatal(err)
		}
	}
	waitCtx, cancelWait := context.WithTimeout(context.Background(), 100*time.Millisecond)
	defer cancelWait()
	cleanupOutcome, err := server.waitTaskJob(waitCtx, principal.AccountScopeID, taskRunJobID(project.ID, cleanupTask.ID))
	// A permanent cleanup failure ends the wait without losing the verdict.
	if err == nil || !strings.Contains(err.Error(), "review cleanup missing captured head") {
		t.Fatalf("permanent cleanup did not end wait %+v %v", cleanupOutcome, err)
	}
	cleanupSaved, _, err := sessions.Store().GetProjectTask(principal.AccountScopeID, project.ID, cleanupTask.ID)
	if err != nil || cleanupSaved.ReviewCleanup == nil || cleanupSaved.ReviewCleanup.State != "failed" {
		t.Fatalf("cleanup failure not recorded %+v %v", cleanupSaved, err)
	}
	signalPage, err := server.signals.ListAfter(0, 100, pebblestore.SignalFilter{Account: principal.AccountScopeID, KindPrefixes: []string{"task.run"}})
	if err != nil {
		t.Fatal(err)
	}
	signalled := false
	for _, signal := range signalPage.Signals {
		if signal.Refs["task_id"] == cleanupTask.ID {
			signalled = true
		}
	}
	if !signalled {
		t.Fatal("cleanup failure suppressed run-end signal")
	}
	// A verdict persistence conflict must reach settlement and end a waiter,
	// rather than being downgraded to cleanup backoff merely because a parsed
	// review was returned. Use the real FinishTaskReview identity guard.
	staleTask := *cleanupSaved
	staleTask.StartCommit = strings.Repeat("b", 40)
	if _, err := sessions.Store().UpdateProjectTask(principal.AccountScopeID, project.ID, cleanupTask.ID, func(current *pebblestore.ProjectTaskRecord) error {
		current.Review = nil
		return nil
	}); err != nil {
		t.Fatal(err)
	}
	if _, err := server.settleTaskReview(&staleTask); err == nil || !strings.Contains(err.Error(), "review task changed") {
		t.Fatalf("persistence conflict hidden %v", err)
	}
	// Removing the captured head makes cleanup fail immediately, not back off.
	if _, err := sessions.Store().UpdateProjectTask(principal.AccountScopeID, project.ID, cleanupTask.ID, func(current *pebblestore.ProjectTaskRecord) error {
		current.ReviewCleanup = nil
		return nil
	}); err != nil {
		t.Fatal(err)
	}
	failureCtx, cancelFailure := context.WithTimeout(context.Background(), time.Second)
	defer cancelFailure()
	if _, err := server.waitTaskJob(failureCtx, principal.AccountScopeID, taskRunJobID(project.ID, cleanupTask.ID)); err == nil || !strings.Contains(err.Error(), "review cleanup missing captured head") {
		t.Fatalf("non-pending error did not end wait %v", err)
	}
	// Missing session authority is not a cleanup retry and must end the wait.
	missingTask := *cleanupSaved
	missingTask.ID, missingTask.SessionID = "missing-review-session", "absent-review-session"
	missingTask.Review, missingTask.ReviewCleanup = nil, nil
	if err := sessions.Store().PutProjectTask(principal.AccountScopeID, &missingTask); err != nil {
		t.Fatal(err)
	}
	if _, err := server.waitTaskJob(failureCtx, principal.AccountScopeID, taskRunJobID(project.ID, missingTask.ID)); err == nil || err == context.DeadlineExceeded {
		t.Fatalf("missing session failure did not end wait %v", err)
	}
	// Close the actual store after normal settlement, then use a fresh server.
	closeStore.(func())()
	restarted, _, _, _, _ := newRoutedSessionTestServerAt(t, storePath, false)
	recovered, err := restarted.waitTaskJob(context.Background(), principal.AccountScopeID, taskRunJobID(project.ID, savedOKTaskID))
	if err != nil || recovered.Verdict != "ok" || recovered.ReviewUnreadable {
		t.Fatalf("saved approval after restart %+v %v", recovered, err)
	}
}

// Purpose: waitTaskJob must repair a settled native review lacking its durable
// verdict after an interrupted settlement. The real store/message boundary is
// the narrowest layer proving first-write-wins and fail-closed parsing.
func TestNativeReviewWaitRepairsSettlementGap(t *testing.T) {
	for _, report := range []string{"VERDICT: OK\nSUMMARY: Checked.\nFINDINGS: None.\nTESTS RUN: None.", "VERDICT: BLOCK\nSUMMARY: Unsafe.\nFINDINGS: Fix input.\nTESTS RUN: None.", "", "not a review"} {
		t.Run(fmt.Sprintf("report-%d", len(report)), func(t *testing.T) {
			f := newReopenFixture(t)
			p := testPrincipal()
			task := taskSessionFixture(t, f.server, f.session, pebblestore.ProjectTaskRecord{ID: "review-gap", ProjectID: "project-gap", Agent: "reviewer", StartCommit: strings.Repeat("a", 40), Status: "running"})
			if report != "" {
				_, err := f.server.applySessionV3PrimaryMutation(sessionruntime.SessionMutationInput{SessionID: task.SessionID, UserID: p.UserID, AccountScopeID: p.AccountScopeID, Kind: sessionruntime.SessionMutationAppendMessage, ClientRequestID: "report", IdempotencyKey: "report", PayloadHash: "report", RequestHash: "report", Message: &pebblestore.MessageSnapshot{ID: "report", SessionID: task.SessionID, UserID: p.UserID, AccountScopeID: p.AccountScopeID, Role: "assistant", Content: report, CreatedAt: 100}, NowUnixMs: 100})
				if err != nil {
					t.Fatal(err)
				}
			}
			_, err := f.server.sessions.Store().UpdateProjectTask(p.AccountScopeID, task.ProjectID, task.ID, func(current *pebblestore.ProjectTaskRecord) error { current.Status = "needs_review"; return nil })
			if err != nil {
				t.Fatal(err)
			}
			task.Status = "needs_review"
			outcome, err := f.server.waitTaskJob(context.Background(), p.AccountScopeID, taskRunJobID(task.ProjectID, task.ID))
			if err != nil {
				t.Fatal(err)
			}
			want := "block"
			// This fixture has no execution authority, so repair must fail closed.
			if outcome.Verdict != want || outcome.ReviewUnreadable != (report == "" || report == "not a review") {
				messages, _ := f.server.sessions.ListMessages(task.SessionID, 0, 100)
				t.Fatalf("outcome %+v messages %+v", outcome, messages)
			}
			stored, _, err := f.server.sessions.Store().GetProjectTask(p.AccountScopeID, task.ProjectID, task.ID)
			if err != nil || stored.Review == nil || stored.Review.Verdict != want {
				t.Fatalf("saved review %+v %v", stored, err)
			}
			again, err := f.server.taskReview(stored)
			if err != nil || *again != *stored.Review {
				t.Fatalf("replay %+v %v", again, err)
			}
		})
	}
}

// Purpose: a process stopping between task settlement and review persistence
// must not lose the condition outcome. Reopen the real Pebble store and API
// service; waitTaskJob reconstructs the review without an execution host.
// repairTaskReview must also retain the session's resolved model after restart,
// rather than formatting the task's absent submission overrides.
func TestNativeReviewWaitAfterStoreReopen(t *testing.T) {
	path := filepath.Join(t.TempDir(), "restart.pebble")
	p := testPrincipal()
	var task pebblestore.ProjectTaskRecord
	var wantModel string
	t.Run("write", func(t *testing.T) {
		server, _, _, _, _ := newRoutedSessionTestServerAt(t, path, false)
		snapshot := createSessionsV3PrimaryTestSessionWithWorkspaceAndPreference(t, server, "restart-review", "Review", t.TempDir(), pebblestore.ModelPreference{Provider: "test-provider", Model: "test-model"})
		if snapshot.Preference.Provider == "" || snapshot.Preference.Model == "" {
			t.Fatalf("missing resolved preference %+v", snapshot.Preference)
		}
		wantModel = snapshot.Preference.Provider + "/" + snapshot.Preference.Model
		task = taskSessionFixture(t, server, snapshot, pebblestore.ProjectTaskRecord{ID: "restart-review", ProjectID: "restart-project", Agent: "reviewer", Status: "running", StartCommit: strings.Repeat("a", 40)})
		_, err := server.applySessionV3PrimaryMutation(sessionruntime.SessionMutationInput{SessionID: task.SessionID, UserID: p.UserID, AccountScopeID: p.AccountScopeID, Kind: sessionruntime.SessionMutationAppendMessage, ClientRequestID: "report", IdempotencyKey: "report", PayloadHash: "report", RequestHash: "report", Message: &pebblestore.MessageSnapshot{ID: "report", SessionID: task.SessionID, UserID: p.UserID, AccountScopeID: p.AccountScopeID, Role: "assistant", Content: "VERDICT: OK\nSUMMARY: Checked.\nFINDINGS: None.\nTESTS RUN: None.", CreatedAt: 100}, NowUnixMs: 100})
		if err != nil {
			t.Fatal(err)
		}
		_, err = server.sessions.Store().UpdateProjectTask(p.AccountScopeID, task.ProjectID, task.ID, func(current *pebblestore.ProjectTaskRecord) error { current.Status = "needs_review"; return nil })
		if err != nil {
			t.Fatal(err)
		}
	})
	t.Run("recover", func(t *testing.T) {
		server, _, _, _, _ := newRoutedSessionTestServerAt(t, path, false)
		outcome, err := server.waitTaskJob(context.Background(), p.AccountScopeID, taskRunJobID(task.ProjectID, task.ID))
		if err != nil || outcome.Verdict != "block" || outcome.ReviewUnreadable {
			t.Fatalf("recovered outcome %+v %v", outcome, err)
		}
		stored, _, err := server.sessions.Store().GetProjectTask(p.AccountScopeID, task.ProjectID, task.ID)
		if err != nil || stored.Review == nil || stored.Review.Model != wantModel {
			t.Fatalf("repaired review model %+v %v", stored, err)
		}
	})
	t.Run("replay", func(t *testing.T) {
		server, _, _, _, _ := newRoutedSessionTestServerAt(t, path, false)
		outcome, err := server.waitTaskJob(context.Background(), p.AccountScopeID, taskRunJobID(task.ProjectID, task.ID))
		if err != nil || outcome.Verdict != "block" || outcome.ReviewUnreadable {
			t.Fatalf("replayed outcome %+v %v", outcome, err)
		}
	})
}

// Purpose: native outcomes must use the worker normalization boundary, never
// reopen with a placeholder or hide failed execution behind a readable approval.
// The store-backed waiter is the narrowest layer proving parsing and dispatch data.
func TestNativeReviewOutcomeNormalization(t *testing.T) {
	for _, status := range []string{"needs_review", "failed"} {
		t.Run(status, func(t *testing.T) {
			f := newReopenFixture(t)
			p := testPrincipal()
			task := taskSessionFixture(t, f.server, f.session, pebblestore.ProjectTaskRecord{ID: "normalize", ProjectID: "normalize-project", Agent: "reviewer", StartCommit: strings.Repeat("a", 40), Status: "running"})
			report := "VERDICT: BLOCK\nSUMMARY: Unsafe input.\nFINDINGS: None.\nTESTS RUN: None."
			if status == "failed" {
				report = "VERDICT: OK\nSUMMARY: Checked.\nFINDINGS: None.\nTESTS RUN: None."
			}
			_, err := f.server.applySessionV3PrimaryMutation(sessionruntime.SessionMutationInput{SessionID: task.SessionID, UserID: p.UserID, AccountScopeID: p.AccountScopeID, Kind: sessionruntime.SessionMutationAppendMessage, ClientRequestID: "report", IdempotencyKey: "report", PayloadHash: "report", RequestHash: "report", Message: &pebblestore.MessageSnapshot{ID: "report", SessionID: task.SessionID, UserID: p.UserID, AccountScopeID: p.AccountScopeID, Role: "assistant", Content: report, CreatedAt: 100}, NowUnixMs: 100})
			if err != nil {
				t.Fatal(err)
			}
			settled, err := f.server.sessions.Store().UpdateProjectTask(p.AccountScopeID, task.ProjectID, task.ID, func(current *pebblestore.ProjectTaskRecord) error { current.Status = status; return nil })
			if err != nil {
				t.Fatal(err)
			}
			if review, err := f.server.taskReview(settled); err != nil || review != nil {
				t.Fatalf("read repaired review %+v %v", review, err)
			}
			unchanged, _, _ := f.server.sessions.Store().GetProjectTask(p.AccountScopeID, task.ProjectID, task.ID)
			if unchanged.Review != nil {
				t.Fatal("read mutated task")
			}
			outcome, err := f.server.waitTaskJob(context.Background(), p.AccountScopeID, taskRunJobID(task.ProjectID, task.ID))
			if err != nil {
				t.Fatal(err)
			}
			stored, _, _ := f.server.sessions.Store().GetProjectTask(p.AccountScopeID, task.ProjectID, task.ID)
			if outcome.Verdict != "block" || outcome.ReviewUnreadable || outcome.Findings == "None." || outcome.Findings == "" || outcome.Findings != stored.Review.Reason {
				t.Fatalf("outcome %+v review %+v", outcome, stored.Review)
			}
			if status == "failed" && outcome.Findings != "reviewer task failed" {
				t.Fatalf("failed reason %q", outcome.Findings)
			}
		})
	}
}

// Purpose: role alone must not hide a reviewer worker's delivery. The real MCP
// projection over a fixed HTTP task response isolates the native/worker boundary.
func TestReviewerWorkerResultPreservesDelivery(t *testing.T) {
	req := requestWithScopedToken(withTestPrincipal(httptest.NewRequest(http.MethodPost, "/mcp", nil)), &pebblestore.ScopedTokenRecord{Scopes: []string{"sessions:read"}})
	call := &controlMCPCall{request: req, next: http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		fmt.Fprint(w, `{"task":{"id":"review","agent":"reviewer","worker_id":"custom-reviewer","status":"needs_review","unintegrated_commits":2,"diff_summary":"security fix","delivery_assessment":{"source_oid":"abc","state":"ready"}}}`)
	})}
	result, err := controlMCPTaskResult(call, map[string]any{"project_id": "project", "task_id": "review"})
	if err != nil {
		t.Fatal(err)
	}
	view := controlMCPMap(result)
	if view["head"] != "abc" || view["delivery_state"] != "ready" || view["diff_summary"] != "security fix" || fmt.Sprint(view["unintegrated_commits"]) != "2" {
		t.Fatalf("worker delivery changed %+v", view)
	}
}

// Purpose: a transcript read failure must not become a permanent verdict.
// Corrupt only the message record in an isolated real store to exercise the
// service error boundary while leaving task persistence available.
func TestNativeReviewMessageReadFailureDoesNotSave(t *testing.T) {
	f := newReopenFixture(t)
	p := testPrincipal()
	task := taskSessionFixture(t, f.server, f.session, pebblestore.ProjectTaskRecord{ID: "read-error", ProjectID: "read-error-project", Agent: "reviewer", Status: "needs_review"})
	key := pebblestore.MessagePrefix(task.SessionID) + "00000000000000000001"
	if err := f.server.sessions.Store().Underlying().PutBytes(key, []byte("invalid json")); err != nil {
		t.Fatal(err)
	}
	if _, err := f.server.repairTaskReview(&task); err == nil {
		t.Fatal("message read failure was hidden")
	}
	stored, _, err := f.server.sessions.Store().GetProjectTask(p.AccountScopeID, task.ProjectID, task.ID)
	if err != nil || stored.Review != nil {
		t.Fatalf("read failure saved review %+v %v", stored, err)
	}
}

func assertWorkflowReviewConsentRefusal(t *testing.T, sessions *pebblestore.SessionStore, snapshot pebblestore.SessionSnapshot, tool, args string) {
	t.Helper()
	svc := permission.NewService(pebblestore.NewPermissionStore(sessions.Underlying()), nil, nil)
	svc.SetSessionResolver(sessions)
	svc.SetBypassGate(func() (bool, string) { return true, "" })
	svc.SetBypassPermissions(false)
	result, err := svc.AuthorizeToolCall(permission.AuthorizationInput{SessionID: snapshot.ID, AccountScopeID: snapshot.AccountScopeID, RunID: "permission-check", CallID: tool, Mode: snapshot.Mode, ToolName: tool, ToolArguments: args, Overlay: &permission.Policy{Rules: []permission.PolicyRule{{Kind: permission.PolicyRuleKindTool, Tool: tool, Decision: permission.PolicyDecisionAsk}}}})
	if err != nil || result.Decision != permission.AuthorizationDeny || result.Source != "worker_noninteractive" {
		t.Fatalf("workflow consent refusal %+v err %v", result, err)
	}
	count, err := svc.PendingCount(snapshot.ID)
	if err != nil || count != 0 {
		t.Fatalf("pending permissions %d err %v", count, err)
	}
}
