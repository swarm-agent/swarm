package api

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"sync"
	"testing"
	"time"

	provideriface "swarm/packages/swarmd/internal/provider/interfaces"
	"swarm/packages/swarmd/internal/providerlimit"
	sessionruntime "swarm/packages/swarmd/internal/session"
	pebblestore "swarm/packages/swarmd/internal/store/pebble"
	"swarm/packages/swarmd/internal/tool"
)

// claudeSessionLimit is the failure text the Claude Code CLI ended the
// forge's task runs with (2026-10-29).
const claudeSessionLimit = "claude code turn failed: You've hit your session limit · resets 1:50pm (UTC)"

// limitClock is the limit wait's clock in these tests (Server.taskLimitClock).
type limitClock struct {
	mu  sync.Mutex
	now time.Time
}

func (c *limitClock) Now() time.Time  { c.mu.Lock(); defer c.mu.Unlock(); return c.now }
func (c *limitClock) Set(t time.Time) { c.mu.Lock(); c.now = t; c.mu.Unlock() }

// limitWaitFixture is a project task in its own V3 session on a real store,
// run by the real V3 executor against a fake provider whose first `limits`
// turns end with Claude Code's session-limit failure.
type limitWaitFixture struct {
	*confirmationFixture
	clock   *limitClock
	task    pebblestore.ProjectTaskRecord
	session pebblestore.SessionSnapshot
}

func newLimitWaitFixture(t *testing.T, path string, limits int) *limitWaitFixture {
	t.Helper()
	f := newConfirmationFixture(t, path, nil)
	t.Cleanup(func() { stopProjectTaskLimitTimers(f.server) })
	clock := &limitClock{now: time.Date(2026, 10, 29, 12, 0, 0, 0, time.UTC)}
	f.server.taskLimitClock = clock.Now
	if f.server.signals == nil {
		f.server.SetSignalStore(pebblestore.NewSignalStore(f.sessions.Store().Underlying()))
	}
	var mu sync.Mutex
	calls := 0
	f.runner.handler = func(_ context.Context, req provideriface.Request, _ func(provideriface.StreamEvent)) (provideriface.Response, error) {
		mu.Lock()
		calls++
		n := calls
		mu.Unlock()
		if n <= limits {
			return provideriface.Response{}, errors.New(claudeSessionLimit)
		}
		return provideriface.Response{Text: "committed the remaining edits", StopReason: "stop"}, nil
	}
	return &limitWaitFixture{confirmationFixture: f, clock: clock}
}

// bindTask creates the task's session and its swarm-role task, in_progress.
func (f *limitWaitFixture) bindTask(id string) {
	f.t.Helper()
	sid := f.createSession("task " + id)
	session, ok, err := f.sessions.GetSession(sid)
	if err != nil || !ok {
		f.t.Fatalf("session: %v %v", ok, err)
	}
	f.task = taskSessionFixture(f.t, f.server, session, pebblestore.ProjectTaskRecord{ID: id, ProjectID: "limit-project", Title: "Address the review findings", Agent: "swarm", Status: "in_progress", WorkspacePath: session.WorkspacePath})
	f.session, _, _ = f.sessions.GetSession(sid)
}

func (f *limitWaitFixture) current() pebblestore.ProjectTaskRecord {
	f.t.Helper()
	task, found, err := f.sessions.Store().GetProjectTask(testPrincipal().AccountScopeID, f.task.ProjectID, f.task.ID)
	if err != nil || !found {
		f.t.Fatalf("task: %v %v", found, err)
	}
	return *task
}

func (f *limitWaitFixture) waitStatus(status string) pebblestore.ProjectTaskRecord {
	f.t.Helper()
	deadline := time.Now().Add(5 * time.Second)
	for time.Now().Before(deadline) {
		if task := f.current(); task.Status == status {
			return task
		}
		time.Sleep(10 * time.Millisecond)
	}
	f.t.Fatalf("task never reached %s: %+v", status, f.current())
	return pebblestore.ProjectTaskRecord{}
}

// startTaskRun records the task's first run (desktop-v3-run:task-<id>) with
// its request message through the canonical session mutation, as task
// launch does, and hands it to the V3 executor, which runs it against the
// fake provider.
func (f *limitWaitFixture) startTaskRun() string {
	f.t.Helper()
	p := testPrincipal()
	runID := pebblestore.InitialTaskRunID(f.task.ID)
	message := pebblestore.MessageSnapshot{ID: "msg-" + f.task.ID, Role: "user", Content: "Address the review findings"}
	result, err := f.sessions.ApplySessionMutation(sessionruntime.SessionMutationInput{
		SessionID: f.session.ID, AccountScopeID: p.AccountScopeID, UserID: p.UserID, ClientRequestID: runID, PayloadHash: runID,
		Kind: sessionruntime.SessionMutationAppendMessage, Message: &message,
		RunIntent: &pebblestore.V3SessionRunIntent{RunID: runID, SourceMessageID: message.ID, Status: sessionruntime.RunIntentPendingExecutor},
	})
	if err != nil || result.RunIntent == nil {
		f.t.Fatalf("task run: %+v %v", result, err)
	}
	if !f.server.v3SessionExecutor.EnqueueRun(sessionV3ExecutorJob{Principal: p, SessionID: f.session.ID, RunID: runID, EpochID: result.RunIntent.EpochID, SourceMessageID: message.ID}) {
		f.t.Fatal("task run not enqueued")
	}
	return runID
}

// failRunDirect records run runID failed with reason in the task's session and
// settles it into the task, as the executor does, without a provider turn.
func (f *limitWaitFixture) failRunDirect(runID, reason string) {
	f.t.Helper()
	p := testPrincipal()
	for _, in := range []sessionruntime.SessionMutationInput{
		{Kind: sessionruntime.SessionMutationRecordRunIntent, ClientRequestID: runID, RunIntent: &pebblestore.V3SessionRunIntent{RunID: runID, Status: sessionruntime.RunIntentPendingExecutor}},
		{Kind: sessionruntime.SessionMutationRecordRunIntent, ClientRequestID: runID + "-failed", RunIntent: &pebblestore.V3SessionRunIntent{RunID: runID, Status: sessionruntime.RunIntentFailed, BlockedReason: reason}},
	} {
		in.SessionID, in.AccountScopeID, in.UserID, in.PayloadHash = f.session.ID, p.AccountScopeID, p.UserID, in.ClientRequestID
		if _, err := f.sessions.ApplySessionMutation(in); err != nil {
			f.t.Fatal(err)
		}
	}
	if err := f.server.reconcileProjectTaskRunLifecycle(sessionV3ExecutorJob{Principal: p, SessionID: f.session.ID, RunID: runID}, sessionruntime.RunIntentFailed, reason); err != nil {
		f.t.Fatal(err)
	}
}

func (f *limitWaitFixture) limitSignals() []pebblestore.Signal {
	f.t.Helper()
	page, err := f.server.signals.ListAfter(0, 100, pebblestore.SignalFilter{Account: testPrincipal().AccountScopeID, KindPrefixes: []string{"task.limit_wait"}})
	if err != nil {
		f.t.Fatal(err)
	}
	return page.Signals
}

func (f *limitWaitFixture) armed() bool {
	f.server.taskLimitMu.Lock()
	defer f.server.taskLimitMu.Unlock()
	return f.server.taskLimitTimers[taskLimitKey(testPrincipal().AccountScopeID, f.task.ProjectID, f.task.ID)] != nil
}

func (f *limitWaitFixture) eventually(what string, ok func() bool) {
	f.t.Helper()
	deadline := time.Now().Add(5 * time.Second)
	for time.Now().Before(deadline) {
		if ok() {
			return
		}
		time.Sleep(10 * time.Millisecond)
	}
	f.t.Fatalf("never: %s", what)
}

func (f *limitWaitFixture) intent(runID string) (pebblestore.V3SessionRunIntent, bool) {
	f.t.Helper()
	intent, found, err := f.sessions.GetSessionRunIntent(f.session.ID, runID)
	if err != nil {
		f.t.Fatal(err)
	}
	return intent, found
}

func stopProjectTaskLimitTimers(s *Server) {
	s.taskLimitMu.Lock()
	defer s.taskLimitMu.Unlock()
	for key, timer := range s.taskLimitTimers {
		timer.Stop()
		delete(s.taskLimitTimers, key)
	}
}

// Requirement: a project task's run (swarm role, here in its own session)
// that ends on Claude Code's session limit does not fail the task. The task
// waits with a reason naming the provider and the reset parsed from the
// message ("resets 1:50pm (UTC)"), shows task_state waiting, raises one
// task.limit_wait signal, and at the reset its same session continues (same
// session, same workspace, uncommitted edits untouched) with a note telling
// the agent to check git status, finish and commit. A continuation that hits
// the limit again waits again without a second signal, and the task finishes
// (needs_review) when a continuation completes.
// Threat (forge, 2026-10-29): two tasks failed on "You've hit your session
// limit · resets 1:50pm (UTC)" mid-edit and nothing resumed them.
// Owners: sessionV3Executor.recordRunStatus -> settleTaskRunEnd ->
// reconcileProjectTaskRunLifecycle -> projectTaskLimitWait, and
// Server.continueProjectTaskAfterLimit, over a real Pebble store, the real V3
// executor and a fake provider: the narrowest layer where a provider turn's
// failure reaches the task record and a continuation reaches the provider.
func TestProjectTaskProviderLimitWaitsAndContinuesInItsSession(t *testing.T) {
	f := newLimitWaitFixture(t, filepath.Join(t.TempDir(), "limit"), 2)
	defer f.close()
	f.bindTask("limited-task")
	account := testPrincipal().AccountScopeID
	dirty := filepath.Join(f.session.WorkspacePath, "half_done.go")
	if err := os.WriteFile(dirty, []byte("package half // uncommitted edit\n"), 0o600); err != nil {
		t.Fatal(err)
	}

	runID := f.startTaskRun()
	f.waitRun(f.session.ID, runID, sessionruntime.RunIntentFailed)
	waiting := f.waitStatus(pebblestore.ProjectTaskStatusLimitWaiting)
	reset := time.Date(2026, 10, 29, 13, 51, 0, 0, time.UTC) // 1:50pm UTC plus the margin
	if waiting.LimitRetryAt != reset.UnixMilli() || waiting.LimitWaits != 1 || waiting.LimitRunID != runID || waiting.LastError != "" {
		t.Fatalf("limit wait not recorded with the parsed reset: %+v", waiting)
	}
	if !strings.Contains(waiting.LimitReason, "until 2026-10-29T13:51:00Z") || !strings.Contains(waiting.LimitReason, "provider test-provider") || !strings.Contains(waiting.LimitReason, "session limit") {
		t.Fatalf("wait reason does not name the provider and the reset: %q", waiting.LimitReason)
	}
	progress := f.server.taskProgressFor(account, waiting, f.clock.Now().UnixMilli())
	if progress.TaskState != pebblestore.ProjectTaskStateWaiting || !strings.Contains(progress.TaskStateReason, "2026-10-29T13:51:00Z") || progress.NeedsAttention {
		t.Fatalf("task_state does not show the wait: %+v", progress)
	}
	// The executor arms the wake and signals right after it records the wait.
	f.eventually("the wake armed and the wait signalled", func() bool { return f.armed() && len(f.limitSignals()) == 1 })
	if got := f.limitSignals(); len(got) != 1 || got[0].Refs["task_id"] != f.task.ID || got[0].Attrs["retry_at"] != "2026-10-29T13:51:00Z" {
		t.Fatalf("want one task.limit_wait signal, got %+v", got)
	}

	// Before the reset nothing continues.
	f.clock.Set(reset.Add(-time.Minute))
	if err := f.server.continueProjectTaskAfterLimit(account, f.task.ProjectID, f.task.ID); err != nil {
		t.Fatal(err)
	}
	rerun := pebblestore.ProjectTaskLimitRerunID(runID, 1)
	if _, found := f.intent(rerun); found || f.current().Status != pebblestore.ProjectTaskStatusLimitWaiting || f.callCount() != 1 {
		t.Fatalf("continued before the reset: task %+v calls %d", f.current(), f.callCount())
	}

	// At the reset the same session continues; this continuation hits the
	// limit again and waits again, with no second signal.
	f.clock.Set(reset)
	if err := f.server.continueProjectTaskAfterLimit(account, f.task.ProjectID, f.task.ID); err != nil {
		t.Fatal(err)
	}
	f.waitRun(f.session.ID, rerun, sessionruntime.RunIntentFailed)
	again := f.waitStatus(pebblestore.ProjectTaskStatusLimitWaiting)
	nextReset := reset.Add(24 * time.Hour)
	if again.LimitWaits != 2 || again.LimitRunID != rerun || again.LimitRetryAt != nextReset.UnixMilli() || again.SessionID != f.session.ID {
		t.Fatalf("second wait: %+v", again)
	}
	f.eventually("the second wake armed", f.armed)
	if got := f.limitSignals(); len(got) != 1 {
		t.Fatalf("a retry raised another task.limit_wait signal: %+v", got)
	}

	f.clock.Set(nextReset)
	if err := f.server.continueProjectTaskAfterLimit(account, f.task.ProjectID, f.task.ID); err != nil {
		t.Fatal(err)
	}
	final := pebblestore.ProjectTaskLimitRerunID(runID, 2)
	f.waitRun(f.session.ID, final, sessionruntime.RunIntentCompleted)
	done := f.waitStatus("needs_review")
	if done.SessionID != f.session.ID || done.LimitRetryAt != 0 || done.LimitReason != "" {
		t.Fatalf("continuation did not finish the same task session: %+v", done)
	}
	f.runner.mu.Lock()
	requests := append([]provideriface.Request(nil), f.runner.requests...)
	f.runner.mu.Unlock()
	if len(requests) != 3 {
		t.Fatalf("want 3 provider turns, got %d", len(requests))
	}
	note := lastConfirmationTestUserText(requests[2].Input)
	if !strings.Contains(note, "usage limit at 2026-10-29T13:51:00Z") || !strings.Contains(note, "git status") || !strings.Contains(note, "commit") {
		t.Fatalf("continuation note: %s", note)
	}
	if raw, err := os.ReadFile(dirty); err != nil || string(raw) != "package half // uncommitted edit\n" {
		t.Fatalf("uncommitted edit not kept: %q %v", raw, err)
	}
	if session, _, _ := f.sessions.GetSession(f.session.ID); session.WorkspacePath != f.session.WorkspacePath {
		t.Fatalf("continuation moved the workspace: %q", session.WorkspacePath)
	}
	if refs, err := f.sessions.Store().ListProjectTaskLimitWaits(); err != nil || len(refs) != 0 {
		t.Fatalf("finished wait left in the index: %+v %v", refs, err)
	}
}

// Requirement: waits are bounded by the same bound the workflow and worker
// paths use (providerlimit.MaxWaits): a task that already waited that often
// fails with providerlimit.Exhausted, arms no wake, records no wait, and an
// ordinary failure still fails the task as before.
// Threat: a provider whose limit never lifts keeps a task waiting forever.
// Owners: reconcileProjectTaskRunLifecycle -> projectTaskLimitWait on a real
// store; the terminal bridge is the narrowest layer that writes the task.
func TestProjectTaskProviderLimitWaitIsBounded(t *testing.T) {
	f := newLimitWaitFixture(t, filepath.Join(t.TempDir(), "bound"), 0)
	defer f.close()
	f.bindTask("bounded-task")
	account := testPrincipal().AccountScopeID
	if _, err := f.sessions.Store().UpdateProjectTask(account, f.task.ProjectID, f.task.ID, func(t *pebblestore.ProjectTaskRecord) error {
		t.LimitWaits, t.LimitRunID = providerlimit.MaxWaits, "earlier.limit"+strconv.Itoa(providerlimit.MaxWaits-1)
		return nil
	}); err != nil {
		t.Fatal(err)
	}
	f.failRunDirect("bounded-run", claudeSessionLimit)
	failed := f.current()
	if failed.Status != "failed" || !providerlimit.IsExhausted(failed.LastError) || failed.LimitRetryAt != 0 || failed.LimitWaits != providerlimit.MaxWaits {
		t.Fatalf("exhausted waits did not fail the task: %+v", failed)
	}
	if f.armed() || len(f.limitSignals()) != 0 {
		t.Fatal("an exhausted task armed a wait or signalled one")
	}
	state, reason := pebblestore.ProjectTaskState(failed, "", false, false)
	if state != pebblestore.ProjectTaskStateNeedsYou || !strings.Contains(reason, "still in effect after 12 waits") {
		t.Fatalf("exhausted task state: %s %q", state, reason)
	}

	other := newLimitWaitFixture(t, filepath.Join(t.TempDir(), "ordinary"), 0)
	defer other.close()
	other.bindTask("ordinary-task")
	other.failRunDirect("ordinary-run", "the checkout could not be read")
	if got := other.current(); got.Status != "failed" || got.LastError != "the checkout could not be read" || got.LimitWaits != 0 || other.armed() {
		t.Fatalf("ordinary failure: %+v", got)
	}
}

// Requirement: the person's stop of a waiting task (stop_task) and archiving
// it cancel the wait: the task is stopped, no wake stays armed, the index is
// empty and a later wake continues nothing. The person's own message in the
// task's session continues it at once, and the wake then does nothing.
// Threat: a task the person stopped or put away starts again by itself.
// Owners: Server.StopProjectTask, the task archive route,
// cancelProjectTaskLimitWait and prepareTaskExecutionContinuation (the
// session message route) on a real store, session and executor.
func TestProjectTaskProviderLimitWaitStopAndArchiveCancel(t *testing.T) {
	for _, how := range []string{"stop", "archive", "message"} {
		t.Run(how, func(t *testing.T) {
			f := newLimitWaitFixture(t, filepath.Join(t.TempDir(), how), 0)
			defer f.close()
			f.bindTask(how + "-task")
			account := testPrincipal().AccountScopeID
			f.failRunDirect(how+"-run", claudeSessionLimit)
			if got := f.current(); got.Status != pebblestore.ProjectTaskStatusLimitWaiting || !f.armed() {
				t.Fatalf("not waiting: %+v", got)
			}
			if how == "stop" {
				result, err := f.server.StopProjectTask(context.Background(), testPrincipal(), f.task.ProjectID, f.task.ID)
				if err != nil || result.Status != "stopped" {
					t.Fatalf("stop: %+v %v", result, err)
				}
			} else if how == "message" {
				// The person's own message in the task's session continues it
				// at once and ends the wait in the same commit.
				body := `{"client_request_id":"person-continues","role":"user","content":"carry on"}`
				if code, out := f.do(http.MethodPost, "/v3/sessions/"+f.session.ID+"/messages", body); code != http.StatusOK {
					t.Fatalf("message: %d %s", code, out)
				}
				f.waitRun(f.session.ID, stableSessionsV3PrimaryRunID(f.session.ID, "person-continues"), sessionruntime.RunIntentCompleted)
				done := f.waitStatus("needs_review")
				if done.LimitRetryAt != 0 || done.LimitReason != "" {
					t.Fatalf("message left the wait: %+v", done)
				}
				if refs, err := f.sessions.Store().ListProjectTaskLimitWaits(); err != nil || len(refs) != 0 {
					t.Fatalf("continued wait left in the index: %+v %v", refs, err)
				}
				f.clock.Set(f.clock.Now().Add(48 * time.Hour))
				if err := f.server.continueProjectTaskAfterLimit(account, f.task.ProjectID, f.task.ID); err != nil {
					t.Fatal(err)
				}
				if _, found := f.intent(pebblestore.ProjectTaskLimitRerunID(how+"-run", 1)); found || f.callCount() != 1 {
					t.Fatal("the wake continued a task the person already continued")
				}
				return
			} else {
				body := `{"revision":` + strconv.Itoa(f.current().Revision) + `}`
				if code, out := f.do(http.MethodPost, "/v3/projects/"+f.task.ProjectID+"/tasks/"+f.task.ID+"/archive", body); code != http.StatusOK {
					t.Fatalf("archive: %d %s", code, out)
				}
			}
			stopped := f.current()
			if stopped.Status != pebblestore.ProjectTaskStatusStopped || stopped.LimitRetryAt != 0 || f.armed() {
				t.Fatalf("wait not cancelled: %+v armed=%v", stopped, f.armed())
			}
			if refs, err := f.sessions.Store().ListProjectTaskLimitWaits(); err != nil || len(refs) != 0 {
				t.Fatalf("cancelled wait left in the index: %+v %v", refs, err)
			}
			f.clock.Set(f.clock.Now().Add(48 * time.Hour))
			if err := f.server.continueProjectTaskAfterLimit(account, f.task.ProjectID, f.task.ID); err != nil {
				t.Fatal(err)
			}
			if _, found := f.intent(pebblestore.ProjectTaskLimitRerunID(how+"-run", 1)); found || f.callCount() != 0 {
				t.Fatal("a cancelled wait continued")
			}
		})
	}
}

// Requirement: archiving a waiting task through the Orchestrator's native
// manage_projects archive_task cancels the wait exactly as the HTTP archive
// route does: the archive commit stops the task and drops the wait index
// entry, so unarchiving it before the reset leaves it stopped and the wake
// at the reset continues nothing.
// Threat: the native archive left the task limit_waiting; unarchived before
// the reset it continued by itself although the person archived it (one
// action behaving differently by path).
// Owners: tool.Runtime manage_projects archive_task/unarchive_task ->
// SessionStore.ArchiveProjectTaskIfRevision -> CancelProjectTaskLimitWait,
// and Server.continueProjectTaskAfterLimit, on a real store and session: the
// native tool entry is the narrowest layer that proves the reported path.
func TestProjectTaskProviderLimitWaitNativeArchiveCancels(t *testing.T) {
	f := newLimitWaitFixture(t, filepath.Join(t.TempDir(), "native-archive"), 0)
	defer f.close()
	f.bindTask("native-archive-task")
	p := testPrincipal()
	f.failRunDirect("native-archive-run", claudeSessionLimit)
	if got := f.current(); got.Status != pebblestore.ProjectTaskStatusLimitWaiting || !f.armed() {
		t.Fatalf("not waiting: %+v", got)
	}
	runtime := tool.NewRuntime(1)
	defer runtime.Close()
	runtime.SetManageProjectStore(f.sessions.Store())
	runtime.SetProjectTaskLifecycleService(f.server)
	runtime.SetManageSessionService(f.server.sessions)
	// The Orchestrator acts from its own session, not the task's.
	orchestrator := f.createSession("orchestrator")
	native := func(action string) {
		t.Helper()
		args, _ := json.Marshal(map[string]any{"action": action, "project_id": f.task.ProjectID, "task_id": f.task.ID, "expected_revision": f.current().Revision})
		if out, err := runtime.ExecuteForWorkspaceScopeWithRuntime(context.Background(), tool.WorkspaceScope{SessionID: orchestrator, Principal: p}, tool.Call{Name: "manage_projects", Arguments: string(args)}); err != nil {
			t.Fatalf("%s: %v %s", action, err, out)
		}
	}
	native("archive_task")
	archived := f.current()
	if !archived.Archived || archived.Status != pebblestore.ProjectTaskStatusStopped || archived.LimitRetryAt != 0 || archived.LimitReason != "" {
		t.Fatalf("native archive left the wait: %+v", archived)
	}
	if refs, err := f.sessions.Store().ListProjectTaskLimitWaits(); err != nil || len(refs) != 0 {
		t.Fatalf("native archive left the wait indexed: %+v %v", refs, err)
	}
	native("unarchive_task")
	if got := f.current(); got.Archived || got.Status != pebblestore.ProjectTaskStatusStopped {
		t.Fatalf("unarchive: %+v", got)
	}
	// The wake (armed before the archive) fires at the reset: nothing continues.
	f.clock.Set(time.Date(2026, 10, 29, 13, 51, 0, 0, time.UTC))
	if err := f.server.continueProjectTaskAfterLimit(p.AccountScopeID, f.task.ProjectID, f.task.ID); err != nil {
		t.Fatal(err)
	}
	if _, found := f.intent(pebblestore.ProjectTaskLimitRerunID("native-archive-run", 1)); found || f.callCount() != 0 || f.current().Status != pebblestore.ProjectTaskStatusStopped {
		t.Fatalf("an archived-then-unarchived task continued: %+v", f.current())
	}
}

// Requirement: the wait is durable. After a restart of the daemon (a new
// server over the same store) the wait is re-armed from the store at its
// reset time and does not continue at startup; at the reset it continues the
// same session.
// Threat: a restart drops the wait (the task never resumes) or resumes it at
// startup before the provider's limit reset.
// Owners: the project task limit wait index (setTaskLimitWaitIndex,
// ListProjectTaskLimitWaits) and Server.ResumeProjectTaskLimitWaits over a
// reopened Pebble store.
func TestProjectTaskProviderLimitWaitSurvivesRestart(t *testing.T) {
	path := filepath.Join(t.TempDir(), "restart")
	f := newLimitWaitFixture(t, path, 0)
	f.bindTask("restart-task")
	f.failRunDirect("restart-run", claudeSessionLimit)
	task, session := f.task, f.session
	if got := f.current(); got.Status != pebblestore.ProjectTaskStatusLimitWaiting {
		t.Fatalf("not waiting: %+v", got)
	}
	stopProjectTaskLimitTimers(f.server)
	f.close()

	g := newLimitWaitFixture(t, path, 0)
	defer g.close()
	g.task, g.session = task, session
	g.clock.Set(time.Date(2026, 10, 29, 12, 5, 0, 0, time.UTC))
	g.server.ResumeProjectTaskLimitWaits()
	if !g.armed() {
		t.Fatal("restart did not re-arm the wait")
	}
	rerun := pebblestore.ProjectTaskLimitRerunID("restart-run", 1)
	if got := g.current(); got.Status != pebblestore.ProjectTaskStatusLimitWaiting || got.LimitRetryAt != time.Date(2026, 10, 29, 13, 51, 0, 0, time.UTC).UnixMilli() {
		t.Fatalf("wait changed by the restart: %+v", got)
	}
	if _, found := g.intent(rerun); found || g.callCount() != 0 {
		t.Fatal("the wait continued at startup, before the reset")
	}
	g.clock.Set(time.Date(2026, 10, 29, 13, 51, 0, 0, time.UTC))
	if err := g.server.continueProjectTaskAfterLimit(testPrincipal().AccountScopeID, task.ProjectID, task.ID); err != nil {
		t.Fatal(err)
	}
	g.waitRun(session.ID, rerun, sessionruntime.RunIntentCompleted)
	if got := g.waitStatus("needs_review"); got.SessionID != session.ID {
		t.Fatalf("restart continuation: %+v", got)
	}
}
