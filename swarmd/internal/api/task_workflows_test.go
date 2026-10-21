package api

import (
	"context"
	"errors"
	"net/http"
	"net/http/httptest"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"

	"swarm/packages/swarmd/internal/jobautomation"
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
// project store; the task's run is not executed (no provider).
func TestBuiltinAgentStepStartsATaskAtTheDeliveryHead(t *testing.T) {
	server, sessions, _, _, _ := newRoutedSessionTestServerWithSwarmStore(t)
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
}
