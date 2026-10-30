package api

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"swarm/packages/swarmd/internal/jobautomation"
	sessionruntime "swarm/packages/swarmd/internal/session"
	pebblestore "swarm/packages/swarmd/internal/store/pebble"
)

// Stall detection tests (task_stall.go, docs/task-progress.md "Stalled
// tasks"). Every fixture is an in-process daemon Server over a real Pebble
// store at a temporary path: real V3 sessions, project tasks, run intents,
// workflow deliveries, the signal feed and the session messages route. The
// V3 executor is off (newRoutedSessionTestServerAt), so no provider is ever
// reached: a turn the check sends stays a durable run intent the test settles
// explicitly. Time is passed to CheckStalledTasks, never slept.

type stallFixture struct {
	server  *Server
	close   func()
	account string
}

func newStallFixture(t *testing.T, path string) stallFixture {
	t.Helper()
	server, sessions, _, closer, _ := newRoutedSessionTestServerAt(t, path, false)
	p := testPrincipal()
	ids := pebblestore.NewIdentityStore(sessions.Store().Underlying())
	if _, err := ids.PutUser(p.User); err != nil {
		t.Fatal(err)
	}
	if _, err := ids.PutAccountScope(p.AccountScope); err != nil {
		t.Fatal(err)
	}
	if _, err := ids.PutAccountUser(p.AccountUser); err != nil {
		t.Fatal(err)
	}
	req := requestWithScopedToken(withTestPrincipal(httptest.NewRequest(http.MethodPost, "/mcp", nil)),
		&pebblestore.ScopedTokenRecord{Scopes: []string{"swarm:read", "sessions:read", "sessions:write", "automations:read"}})
	call := &controlMCPCall{request: req, next: server.internalRouteHandler(), server: server}
	server.jobAutomationCallFn = func(context.Context) (*controlMCPCall, error) { return call, nil }
	underlying := sessions.Store().Underlying()
	server.SetSignalStore(pebblestore.NewSignalStore(underlying))
	server.SetJobAutomations(jobautomation.NewRunner(jobautomation.Options{Store: jobautomation.NewStore(underlying)}))
	return stallFixture{server: server, close: closer.(func()), account: p.AccountScopeID}
}

func (f stallFixture) session(t *testing.T, id string) pebblestore.SessionSnapshot {
	t.Helper()
	return createSessionsV3PrimaryTestSessionWithWorkspaceAndPreference(t, f.server, id, id, t.TempDir(),
		pebblestore.ModelPreference{Provider: "test-provider", Model: "test-model", Thinking: "medium"})
}

func (f stallFixture) check(at time.Time) {
	f.server.CheckStalledTasks(context.Background(), f.account, at)
}

// signals lists the task.stalled / task.unstalled signals of taskID.
func (f stallFixture) signals(t *testing.T, kind, taskID string) []pebblestore.Signal {
	t.Helper()
	page, err := f.server.signals.ListAfter(0, 500, pebblestore.SignalFilter{Account: f.account, KindPrefixes: []string{"task"}})
	if err != nil {
		t.Fatal(err)
	}
	var out []pebblestore.Signal
	for _, sig := range page.Signals {
		if sig.Kind == kind && sig.Refs["task_id"] == taskID {
			out = append(out, sig)
		}
	}
	return out
}

// userMessages lists the user messages of sessionID whose content contains
// marker.
func (f stallFixture) userMessages(t *testing.T, sessionID, marker string) []pebblestore.MessageSnapshot {
	t.Helper()
	messages, err := f.server.sessions.ListMessages(sessionID, 0, 500)
	if err != nil {
		t.Fatal(err)
	}
	var out []pebblestore.MessageSnapshot
	for _, m := range messages {
		if m.Role == "user" && strings.Contains(m.Content, marker) {
			out = append(out, m)
		}
	}
	return out
}

func (f stallFixture) task(t *testing.T, projectID, taskID string) pebblestore.ProjectTaskRecord {
	t.Helper()
	task, found, err := f.server.sessions.Store().GetProjectTask(f.account, projectID, taskID)
	if err != nil || !found {
		t.Fatalf("task %s: %v %v", taskID, found, err)
	}
	return *task
}

// settleRun records a run of sessionID through to terminal, as the executor
// does.
func (f stallFixture) settleRun(t *testing.T, sessionID, runID, terminal string) {
	t.Helper()
	exec := newSessionV3Executor(f.server)
	epoch := "epoch-00000000000000000001"
	if intent, found, err := f.server.sessions.Store().GetV3SessionRunIntent(sessionID, runID); err == nil && found && intent.EpochID != "" {
		epoch = intent.EpochID
	}
	job := sessionV3ExecutorJob{Principal: testPrincipal(), SessionID: sessionID, RunID: runID, EpochID: epoch}
	steps := []struct{ status, event string }{{sessionruntime.RunIntentPendingExecutor, "session.assistant.queued"}, {sessionruntime.RunIntentRunning, "session.assistant.started"}}
	switch terminal {
	case sessionruntime.RunIntentCompleted:
		steps = append(steps, struct{ status, event string }{terminal, "session.run.completed"})
	case "":
	default:
		steps = append(steps, struct{ status, event string }{terminal, "session.run." + terminal})
	}
	for _, step := range steps {
		if _, err := exec.recordRunStatus(job, step.status, "", step.event); err != nil {
			t.Fatalf("record %s of %s: %v", step.status, runID, err)
		}
	}
}

// delivery records a workflow delivery for the task's job and lets mutate set
// its state, as the workflow runner does.
func (f stallFixture) delivery(t *testing.T, projectID, taskID, automationID, followup string, steps []jobautomation.Step, mutate func(*jobautomation.Delivery)) jobautomation.Delivery {
	t.Helper()
	store, err := f.server.jobAutomationStore()
	if err != nil {
		t.Fatal(err)
	}
	d, created, err := store.CreateDelivery(jobautomation.Automation{ID: automationID, Account: f.account, Name: "review and fix", Revision: 1, Enabled: true, Steps: steps},
		jobautomation.Result{V: jobautomation.ResultVersion, JobID: taskRunJobID(projectID, taskID), Kind: jobautomation.KindTask, Followup: followup, ProjectID: projectID})
	if err != nil || !created {
		t.Fatalf("create delivery: %v (created %v)", err, created)
	}
	mutate(&d)
	if err := store.SaveDelivery(d); err != nil {
		t.Fatal(err)
	}
	return d
}

// Purpose: the invariant's alert half. A task that is not finished and has no
// run, no queued run, no workflow step and no armed wait raises nothing
// before STALL_AFTER (20 minutes by default, from limits), then exactly one
// task.stalled warning per stall episode naming the state, the task title and
// the machine; it wakes the delegating Orchestrator conversation once with the
// same text, and task_state_reason reads "stalled: …" on the task routes and
// the list index. Repeated checks, a daemon restart and a crash between the
// signal and the episode record send nothing again. When the task moves:
// exactly one task.unstalled and the marker is gone.
// Threat: the forge incident (work silently stuck while every surface read
// "running"), an alert storm every minute, or a duplicate alert after restart.
// Boundary: Server.CheckStalledTasks over durable project tasks, run state,
// SignalStore.AppendOnce, the session messages route (delegator wake) and
// SessionStore.SetProjectTaskStall; restart reopens the same Pebble path.
func TestTaskStallNoNextStepAlertsOnceWakesDelegatorAndUnstalls(t *testing.T) {
	path := filepath.Join(t.TempDir(), "stall.pebble")
	f := newStallFixture(t, path)
	work := f.session(t, "stall-task-session")
	origin := f.session(t, "stall-orchestrator")
	task := taskSessionFixture(t, f.server, work, pebblestore.ProjectTaskRecord{ID: "task_stall", ProjectID: "proj-stall", Agent: "coder", Status: "in_progress", OriginSessionID: origin.ID})
	start := time.UnixMilli(task.UpdatedAt)

	f.check(start.Add(5 * time.Minute))
	if got := f.signals(t, "task.stalled", task.ID); len(got) != 0 {
		t.Fatalf("stalled before STALL_AFTER: %+v", got)
	}

	f.check(start.Add(25 * time.Minute))
	got := f.signals(t, "task.stalled", task.ID)
	if len(got) != 1 {
		t.Fatalf("task.stalled signals = %d, want exactly 1", len(got))
	}
	sig := got[0]
	if sig.Severity != pebblestore.SignalSeverityWarning || sig.Attrs["stall"] != pebblestore.ProjectTaskStallNoNextStep ||
		!strings.Contains(sig.Summary, "Add a rate limit") || !strings.Contains(sig.Summary, "this machine") || !strings.Contains(sig.Summary, "no run and no next step") ||
		!strings.HasPrefix(sig.DedupKey, "task.stalled:"+task.ID+":") || sig.Refs["project_id"] != task.ProjectID {
		t.Fatalf("task.stalled = %+v", sig)
	}
	wakes := f.userMessages(t, origin.ID, sig.Summary)
	if len(wakes) != 1 || wakes[0].Metadata["source"] != "task_stall" {
		t.Fatalf("delegator wakes = %+v, want one carrying the signal's text", wakes)
	}
	if n := len(f.userMessages(t, work.ID, "stalled")); n != 0 {
		t.Fatalf("the stalled task's own session got %d alert messages", n)
	}
	stored := f.task(t, task.ProjectID, task.ID)
	if stored.UpdatedAt != task.UpdatedAt {
		t.Fatal("the stall marker changed the task's UpdatedAt (a marker is not movement)")
	}
	progress := f.server.taskProgressFor(f.account, stored, start.Add(25*time.Minute).UnixMilli())
	if progress.TaskState != pebblestore.ProjectTaskStateRunning || !strings.HasPrefix(progress.TaskStateReason, "stalled: no run and no next step") {
		t.Fatalf("task state %q reason %q", progress.TaskState, progress.TaskStateReason)
	}
	rows, _, err := f.server.sessions.Store().ListProjectTaskSummaries(f.account, task.ProjectID, false)
	if err != nil || len(rows) != 1 {
		t.Fatalf("list rows %d %v", len(rows), err)
	}
	if _, reason := pebblestore.ProjectTaskState(rows[0], "", false, false); !strings.HasPrefix(reason, "stalled: ") {
		t.Fatalf("list index reason = %q", reason)
	}

	// Repeated checks in the same episode send nothing again.
	f.check(start.Add(40 * time.Minute))
	f.check(start.Add(41 * time.Minute))
	if n := len(f.signals(t, "task.stalled", task.ID)); n != 1 {
		t.Fatalf("repeated checks: %d task.stalled", n)
	}

	// Restart: the same store, a new daemon Server and signal store.
	f.close()
	f = newStallFixture(t, path)
	f.check(start.Add(45 * time.Minute))
	// A crash after the signal but before the episode record was written.
	if err := f.server.sessions.Store().DeleteTaskStall(f.account, task.ID); err != nil {
		t.Fatal(err)
	}
	f.check(start.Add(46 * time.Minute))
	if n := len(f.signals(t, "task.stalled", task.ID)); n != 1 {
		t.Fatalf("after restart: %d task.stalled, want still 1", n)
	}
	if n := len(f.userMessages(t, origin.ID, sig.Summary)); n != 1 {
		t.Fatalf("after restart: %d delegator wakes, want still 1", n)
	}

	// The task moves: one task.unstalled, the marker is gone.
	moved, err := f.server.sessions.Store().UpdateProjectTask(f.account, task.ProjectID, task.ID, func(t *pebblestore.ProjectTaskRecord) error {
		t.CurrentStageIndex++
		return nil
	})
	if err != nil {
		t.Fatal(err)
	}
	after := time.UnixMilli(moved.UpdatedAt).Add(time.Minute)
	f.check(after)
	f.check(after.Add(time.Minute))
	unstalled := f.signals(t, "task.unstalled", task.ID)
	if len(unstalled) != 1 || unstalled[0].Severity != pebblestore.SignalSeverityInfo {
		t.Fatalf("task.unstalled = %+v, want exactly one info", unstalled)
	}
	if _, found, err := f.server.sessions.Store().GetTaskStall(f.account, task.ID); err != nil || found {
		t.Fatalf("stall episode after movement: found=%v err=%v", found, err)
	}
	stored = f.task(t, task.ProjectID, task.ID)
	if _, reason := pebblestore.ProjectTaskState(stored, "", false, false); stored.Stall != nil || strings.HasPrefix(reason, "stalled") {
		t.Fatalf("after movement: stall %+v reason %q", stored.Stall, reason)
	}
	if n := len(f.signals(t, "task.stalled", task.ID)); n != 1 {
		t.Fatalf("movement raised another task.stalled: %d", n)
	}
}

type stallFakeBackground struct{ running map[string]bool }

func (b stallFakeBackground) StopSessionBackgroundCommands(string, string) int { return 0 }
func (b stallFakeBackground) SessionRunEnded(string, string)                   {}
func (b stallFakeBackground) SessionBackgroundCommandsRunning(id string) bool  { return b.running[id] }

// Purpose: armed waits are not stalls. A task in a provider-limit wait (until
// its reset plus STALL_AFTER), a task watch (an idle conversation watching
// its delegated task with wait_tasks), a
// running review step and a pending background command raise nothing however
// long they last; each has a positive control showing the same task stalls
// once the wait is gone (the limit's reset passed, the background command
// ended and the run is silent).
// Threat: alerts on healthy long work (a weekly limit, a long review), which
// would teach people to ignore task.stalled.
// Boundary: Server.CheckStalledTasks over real deliveries in the workflow
// store, real V3 run intents and a registered task watch on an idle run, and the
// background-command registry interface the tool runtime implements.
func TestTaskStallArmedWaitsAreNotStalled(t *testing.T) {
	f := newStallFixture(t, filepath.Join(t.TempDir(), "armed.pebble"))
	bg := stallFakeBackground{running: map[string]bool{}}
	f.server.SetBackgroundCommandLifecycle(bg)
	now := time.Now()
	agentStep := []jobautomation.Step{{ID: "review", Type: jobautomation.StepAgent, Worker: "reviewer"}}

	// A provider-limit wait: an agent step waiting for the stated reset.
	limitSession := f.session(t, "armed-limit")
	limit := taskSessionFixture(t, f.server, limitSession, pebblestore.ProjectTaskRecord{ID: "task_limit", ProjectID: "proj-armed", Agent: "coder", Status: "in_progress"})
	f.settleRun(t, limitSession.ID, pebblestore.InitialTaskRunID(limit.ID), sessionruntime.RunIntentCompleted)
	reset := now.Add(3 * time.Hour)
	f.delivery(t, limit.ProjectID, limit.ID, "wf-limit", "", agentStep, func(d *jobautomation.Delivery) {
		d.State, d.StartedAt = jobautomation.StateRunning, now.UnixMilli()
		d.Steps[0].State, d.Steps[0].StartedAt, d.Steps[0].NextAttemptAt = jobautomation.StateWaiting, now.UnixMilli(), reset.UnixMilli()
		d.Steps[0].Reason = "waiting for the provider's limit to reset"
	})

	// A running review step.
	reviewSession := f.session(t, "armed-review")
	review := taskSessionFixture(t, f.server, reviewSession, pebblestore.ProjectTaskRecord{ID: "task_review", ProjectID: "proj-armed", Agent: "coder", Status: "in_progress"})
	f.settleRun(t, reviewSession.ID, pebblestore.InitialTaskRunID(review.ID), sessionruntime.RunIntentCompleted)
	f.delivery(t, review.ProjectID, review.ID, "wf-review", "", agentStep, func(d *jobautomation.Delivery) {
		d.State, d.StartedAt = jobautomation.StateRunning, now.UnixMilli()
		d.Steps[0].State, d.Steps[0].StartedAt = jobautomation.StateRunning, now.UnixMilli()
	})

	// A pending background command beside a silent active run.
	bgSession := f.session(t, "armed-background")
	background := taskSessionFixture(t, f.server, bgSession, pebblestore.ProjectTaskRecord{ID: "task_background", ProjectID: "proj-armed", Agent: "coder", Status: "in_progress"})
	f.settleRun(t, bgSession.ID, pebblestore.InitialTaskRunID(background.ID), "")
	bg.running[bgSession.ID] = true

	// A task watch: a Swarm task's run asleep on wait_tasks for its
	// delegated task.
	db := f.server.sessions.Store()
	watched := taskSessionFixture(t, f.server, f.session(t, "armed-watched"), pebblestore.ProjectTaskRecord{ID: "task_watched", ProjectID: "proj-watch", Agent: "coder", Status: "in_progress"})
	_ = watched
	watchMeta := map[string]any{"swarm_v3_project_id": "proj-watch", "project_id": "proj-watch", "agent_name": "system-orchestrator", "resolved_agent_name": "system-orchestrator"}
	p := testPrincipal()
	for _, in := range []pebblestore.V3SessionMutationInput{
		{SessionID: "watcher", Kind: pebblestore.V3SessionMutationCreateSession, ClientRequestID: "create-watcher", Session: &pebblestore.SessionSnapshot{ID: "watcher", Mode: "auto", Metadata: watchMeta}},
		{SessionID: "watcher", Kind: pebblestore.V3SessionMutationRecordRunIntent, ClientRequestID: "pending-watcher", RunIntent: &pebblestore.V3SessionRunIntent{RunID: "run-w", Status: pebblestore.V3RunIntentPendingExecutor}},
		{SessionID: "watcher", Kind: pebblestore.V3SessionMutationRecordRunIntent, ClientRequestID: "run-watcher", RunIntent: &pebblestore.V3SessionRunIntent{RunID: "run-w", Status: pebblestore.V3RunIntentRunning}},
		// run-w watches its delegated task (wait_tasks) and ends normally:
		// watching never pends the run, so the conversation is idle with a watch.
		{SessionID: "watcher", Kind: pebblestore.V3SessionMutationAppendMessage, EventType: "session.task_watch.started", ClientRequestID: "task-watch:run-w", TaskWait: &pebblestore.V3ProjectTaskWaitMutation{RunID: "run-w", ProjectID: "proj-watch", TaskIDs: []string{"task_watched"}}},
		{SessionID: "watcher", Kind: pebblestore.V3SessionMutationRecordRunIntent, EventType: "session.run.completed", ClientRequestID: "done-watcher", RunIntent: &pebblestore.V3SessionRunIntent{RunID: "run-w", Status: pebblestore.V3RunIntentCompleted}},
	} {
		in.AccountScopeID, in.UserID, in.PayloadHash = p.AccountScopeID, p.UserID, in.ClientRequestID
		if _, err := db.ApplyV3SessionMutation(in); err != nil {
			t.Fatal(err)
		}
	}
	watcher := pebblestore.ProjectTaskRecord{ID: "task_watcher", ProjectID: "proj-watch", Title: "Coordinate", Agent: "swarm", Status: "in_progress", SessionID: "watcher"}
	if err := db.PutProjectTask(f.account, &watcher); err != nil {
		t.Fatal(err)
	}
	if state, _, _ := db.GetV3SessionRunState("watcher"); state.Active || state.Status == pebblestore.V3RunIntentWaitingTasks {
		t.Fatalf("watching left the run pending: %+v", state)
	}
	if watches, err := db.ListV3SessionTaskWatches("watcher"); err != nil || len(watches) != 1 {
		t.Fatalf("watch not registered: %+v err=%v", watches, err)
	}

	later := now.Add(2 * time.Hour)
	f.check(later)
	f.check(later.Add(time.Minute))
	for _, id := range []string{limit.ID, review.ID, background.ID, watcher.ID} {
		if got := f.signals(t, "task.stalled", id); len(got) != 0 {
			t.Fatalf("%s in an armed wait raised %+v", id, got)
		}
	}

	// Positive controls: the limit's reset passed long ago, and the
	// background command ended while the run stayed silent.
	bg.running[bgSession.ID] = false
	f.check(reset.Add(25 * time.Minute))
	if got := f.signals(t, "task.stalled", limit.ID); len(got) != 1 || got[0].Attrs["stall"] != pebblestore.ProjectTaskStallProviderLimit || !strings.Contains(got[0].Summary, "provider limit until") {
		t.Fatalf("passed limit wait = %+v", got)
	}
	if got := f.signals(t, "task.stalled", background.ID); len(got) != 1 || got[0].Attrs["stall"] != pebblestore.ProjectTaskStallRunSilent || !strings.Contains(got[0].Summary, "no events for") {
		t.Fatalf("silent run without its background command = %+v", got)
	}
	for _, id := range []string{review.ID, watcher.ID} {
		if got := f.signals(t, "task.stalled", id); len(got) != 0 {
			t.Fatalf("%s still armed but raised %+v", id, got)
		}
	}
}

// Purpose: a project task waiting for its provider's limit (status
// limit_waiting, project_task_limit_wait.go) is an armed wait: its run has
// ended and nothing runs until its reset, often hours away, yet the check
// raises nothing and wakes nobody before LimitRetryAt plus STALL_AFTER. Once
// that has passed without the task continuing it stalls as provider_limit
// (positive control).
// Threat: every limit wait reported as "no run and no next step" after 20
// minutes, alerting and waking the Orchestrator on healthy waits.
// Boundary: Server.CheckStalledTasks over a real task record in the wait and
// its session's ended run, on a real Pebble store.
func TestTaskStallProjectTaskLimitWaitIsArmed(t *testing.T) {
	f := newStallFixture(t, filepath.Join(t.TempDir(), "task-limit.pebble"))
	work := f.session(t, "limit-wait-session")
	origin := f.session(t, "limit-wait-orchestrator")
	task := taskSessionFixture(t, f.server, work, pebblestore.ProjectTaskRecord{ID: "task_limit_wait", ProjectID: "proj-limit-wait", Agent: "swarm", Status: "in_progress", OriginSessionID: origin.ID})
	f.settleRun(t, work.ID, pebblestore.InitialTaskRunID(task.ID), sessionruntime.RunIntentFailed)
	start := time.Now()
	reset := start.Add(3 * time.Hour)
	waiting, err := f.server.sessions.Store().UpdateProjectTask(f.account, task.ProjectID, task.ID, func(t *pebblestore.ProjectTaskRecord) error {
		t.Status, t.LimitWaits, t.LimitAt, t.LimitRetryAt = pebblestore.ProjectTaskStatusLimitWaiting, 1, start.UnixMilli(), reset.UnixMilli()
		t.LimitRunID, t.LimitReason = pebblestore.InitialTaskRunID(task.ID), "waiting for the provider's limit to reset"
		return nil
	})
	if err != nil {
		t.Fatal(err)
	}
	if waiting.Status != pebblestore.ProjectTaskStatusLimitWaiting {
		t.Fatalf("task not waiting: %+v", waiting)
	}
	f.check(start.Add(2 * time.Hour))
	f.check(reset.Add(10 * time.Minute))
	if got := f.signals(t, "task.stalled", task.ID); len(got) != 0 {
		t.Fatalf("a task in its provider-limit wait raised %+v", got)
	}
	if n := len(f.userMessages(t, origin.ID, "stalled")); n != 0 {
		t.Fatalf("the delegator was woken %d times for a healthy wait", n)
	}
	f.check(reset.Add(25 * time.Minute))
	if got := f.signals(t, "task.stalled", task.ID); len(got) != 1 || got[0].Attrs["stall"] != pebblestore.ProjectTaskStallProviderLimit || !strings.Contains(got[0].Summary, "has not continued") {
		t.Fatalf("passed task limit wait = %+v", got)
	}
}

// Purpose: the review-exhausted shape recovers by itself before anyone is
// alerted. When the review loop used its automatic rounds and the reviewer
// still blocks, the check sends the last findings into the task's own
// session as one extra turn (no signal); if that round also ends blocked it
// hands the findings to the delegating Orchestrator (one task.stalled
// review_exhausted and one wake stating review is exhausted, findings
// included), and never sends a second extra round.
// Threat: the loop going idle with findings nobody reads, or an endless loop
// of extra rounds.
// Boundary: Server.CheckStalledTasks -> Server.reopenTask (the review loop's
// own reopen path, over the session messages route) with real workflow
// deliveries, run intents and recovery receipts; no provider is reached.
func TestTaskStallReviewExhaustedExtraRoundThenHandoff(t *testing.T) {
	f := newStallFixture(t, filepath.Join(t.TempDir(), "review.pebble"))
	work := f.session(t, "review-task-session")
	origin := f.session(t, "review-orchestrator")
	task := taskSessionFixture(t, f.server, work, pebblestore.ProjectTaskRecord{ID: "task_reviewed", ProjectID: "proj-review", Agent: "coder", Status: "in_progress", OriginSessionID: origin.ID})
	f.settleRun(t, work.ID, pebblestore.InitialTaskRunID(task.ID), sessionruntime.RunIntentCompleted)
	steps := []jobautomation.Step{{ID: "review", Type: jobautomation.StepAgent, Worker: "reviewer"}, {ID: "fix", Type: jobautomation.StepReopen, From: "review", MaxRounds: 1}}
	blocked := func(findings string) func(*jobautomation.Delivery) {
		return func(d *jobautomation.Delivery) {
			at := time.Now().UnixMilli()
			d.State, d.StartedAt, d.FinishedAt = jobautomation.StateBlocked, at, at
			d.BlockedReason = jobautomation.RoundsExhaustedReason("fix")
			d.Steps[0].State, d.Steps[0].FinishedAt = jobautomation.StateSucceeded, at
			d.Steps[0].Outcome = map[string]any{"verdict": "block", "findings": findings}
			d.Steps[1].State, d.Steps[1].FinishedAt, d.Steps[1].Reason = jobautomation.StateFailed, at, jobautomation.RoundsExhaustedReason("fix")
		}
	}
	first := "1. high api/upload.go:40 the limit is never checked"
	f.delivery(t, task.ProjectID, task.ID, "wf-review", "", steps, blocked(first))
	start := time.Now()

	f.check(start.Add(time.Minute)) // within the recovery grace: nothing yet
	if n := len(f.userMessages(t, work.ID, "One extra round")); n != 0 {
		t.Fatalf("extra round sent within the grace: %d", n)
	}
	f.check(start.Add(3 * time.Minute))
	extra := f.userMessages(t, work.ID, "One extra round")
	if len(extra) != 1 || !strings.Contains(extra[0].Content, first) || !strings.Contains(extra[0].Content, jobautomation.ReopenClosing) {
		t.Fatalf("extra round = %+v", extra)
	}
	if got := f.signals(t, "task.stalled", task.ID); len(got) != 0 {
		t.Fatalf("a recovery raised an alert: %+v", got)
	}
	f.check(start.Add(4 * time.Minute))
	if n := len(f.userMessages(t, work.ID, "One extra round")); n != 1 {
		t.Fatalf("extra rounds = %d, want 1", n)
	}

	// The extra round ends, the workflow reviews again and still blocks.
	state, _, err := f.server.sessions.Store().GetV3SessionRunState(work.ID)
	if err != nil || state.RunID == "" || state.RunID == pebblestore.InitialTaskRunID(task.ID) {
		t.Fatalf("extra round run state %+v %v", state, err)
	}
	exec := newSessionV3Executor(f.server)
	job := sessionV3ExecutorJob{Principal: testPrincipal(), SessionID: work.ID, RunID: state.RunID, EpochID: firstNonEmptyString(state.EpochID, "epoch-00000000000000000001")}
	for _, step := range []struct{ status, event string }{{sessionruntime.RunIntentRunning, "session.assistant.started"}, {sessionruntime.RunIntentCompleted, "session.run.completed"}} {
		if _, err := exec.recordRunStatus(job, step.status, "", step.event); err != nil {
			t.Fatalf("settle extra round %s: %v", step.status, err)
		}
	}
	second := "1. high api/upload.go:40 still unchecked after the fix"
	f.delivery(t, task.ProjectID, task.ID, "wf-review", state.RunID, steps, blocked(second))
	handoffAt := time.Now().Add(3 * time.Minute)
	f.check(handoffAt)
	f.check(handoffAt.Add(time.Minute))

	got := f.signals(t, "task.stalled", task.ID)
	if len(got) != 1 || got[0].Attrs["stall"] != pebblestore.ProjectTaskStallReviewExhausted || !strings.Contains(got[0].Summary, "review exhausted") {
		t.Fatalf("handoff signal = %+v", got)
	}
	wakes := f.userMessages(t, origin.ID, "Review is exhausted")
	if len(wakes) != 1 || !strings.Contains(wakes[0].Content, second) || !strings.Contains(wakes[0].Content, got[0].Summary) {
		t.Fatalf("delegator handoff = %+v", wakes)
	}
	if n := len(f.userMessages(t, work.ID, "One extra round")); n != 1 {
		t.Fatalf("a second extra round was sent: %d", n)
	}
	stored := f.task(t, task.ProjectID, task.ID)
	if _, reason := pebblestore.ProjectTaskState(stored, "", false, false); !strings.HasPrefix(reason, "stalled: review exhausted") {
		t.Fatalf("task_state_reason = %q", reason)
	}
}

// Purpose: the ended-uncommitted shape recovers by itself, once per head. A
// run that ended with uncommitted changes and nothing committed gets one
// automatic turn asking to finish, test and commit (no signal). If the task
// still has nothing committed at the same head after that turn, it raises one
// task.stalled ended_uncommitted, and no second nudge is ever sent for that
// head, also after a restart. The worktree is read at most once per episode
// and never for a task that is still working: the check stays cheap.
// Threat: finished-looking work that never lands because nothing was
// committed, a nudge loop, or a Git scan of every task every minute.
// Boundary: Server.CheckStalledTasks -> Server.reopenTask with the worktree
// read replaced by taskStallGitFn (the production read is the delivery
// assessment task reads already use); durable recovery receipts.
func TestTaskStallEndedUncommittedNudgesOncePerHead(t *testing.T) {
	path := filepath.Join(t.TempDir(), "uncommitted.pebble")
	f := newStallFixture(t, path)
	work := f.session(t, "uncommitted-task-session")
	task := taskSessionFixture(t, f.server, work, pebblestore.ProjectTaskRecord{ID: "task_dirty", ProjectID: "proj-dirty", Agent: "coder", Status: "in_progress", WorkspacePath: t.TempDir()})
	busy := taskSessionFixture(t, f.server, f.session(t, "uncommitted-busy"), pebblestore.ProjectTaskRecord{ID: "task_busy", ProjectID: "proj-dirty", Agent: "coder", Status: "in_progress", WorkspacePath: t.TempDir()})
	f.settleRun(t, work.ID, pebblestore.InitialTaskRunID(task.ID), sessionruntime.RunIntentCompleted)
	reads := map[string]int{}
	gitFn := func(_ context.Context, read pebblestore.ProjectTaskRecord) (taskStallGit, bool) {
		reads[read.ID]++
		return taskStallGit{Head: strings.Repeat("a", 40), Dirty: true}, true
	}
	f.server.taskStallGitFn = gitFn
	if status := f.task(t, task.ProjectID, task.ID).Status; taskStatusClass(status) != "finished" {
		t.Fatalf("task status after its run = %q", status)
	}
	start := time.Now()

	f.check(start.Add(time.Minute)) // within the grace
	if reads[task.ID] != 0 {
		t.Fatal("the worktree was read within the grace")
	}
	f.check(start.Add(3 * time.Minute))
	f.check(start.Add(4 * time.Minute))
	const marker = "uncommitted changes in your worktree"
	if n := len(f.userMessages(t, work.ID, marker)); n != 1 {
		t.Fatalf("commit nudges = %d, want 1", n)
	}
	if got := f.signals(t, "task.stalled", task.ID); len(got) != 0 {
		t.Fatalf("the nudge raised an alert: %+v", got)
	}

	// The nudge turn ends with the same head, still uncommitted.
	state, _, err := f.server.sessions.Store().GetV3SessionRunState(work.ID)
	if err != nil || state.RunID == pebblestore.InitialTaskRunID(task.ID) {
		t.Fatalf("nudge run %+v %v", state, err)
	}
	exec := newSessionV3Executor(f.server)
	job := sessionV3ExecutorJob{Principal: testPrincipal(), SessionID: work.ID, RunID: state.RunID, EpochID: firstNonEmptyString(state.EpochID, "epoch-00000000000000000001")}
	for _, step := range []struct{ status, event string }{{sessionruntime.RunIntentRunning, "session.assistant.started"}, {sessionruntime.RunIntentCompleted, "session.run.completed"}} {
		if _, err := exec.recordRunStatus(job, step.status, "", step.event); err != nil {
			t.Fatalf("settle nudge %s: %v", step.status, err)
		}
	}
	after := time.Now().Add(3 * time.Minute)
	f.check(after)
	f.check(after.Add(time.Minute))
	got := f.signals(t, "task.stalled", task.ID)
	if len(got) != 1 || got[0].Attrs["stall"] != pebblestore.ProjectTaskStallEndedUncommitted || !strings.Contains(got[0].Summary, "ended uncommitted") {
		t.Fatalf("after the nudge: %+v", got)
	}
	if n := len(f.userMessages(t, work.ID, marker)); n != 1 {
		t.Fatalf("a second nudge for the same head: %d", n)
	}
	// One read per episode (before and after the nudge), none for the task
	// that is still working.
	if reads[task.ID] != 2 || reads[busy.ID] != 0 {
		t.Fatalf("worktree reads = %v, want 2 for %s and none for %s", reads, task.ID, busy.ID)
	}

	// Restart: no second nudge and no second alert for that head.
	f.close()
	f = newStallFixture(t, path)
	f.server.taskStallGitFn = gitFn
	f.check(after.Add(2 * time.Minute))
	if n := len(f.userMessages(t, work.ID, marker)); n != 1 {
		t.Fatalf("after restart: %d nudges", n)
	}
	if n := len(f.signals(t, "task.stalled", task.ID)); n != 1 {
		t.Fatalf("after restart: %d task.stalled", n)
	}
}

// Purpose: linked reads show the stall. A linked machine's task list row
// carries the stall reason the linked machine's own stall check decided
// (progress.task_state_reason "stalled: …"), and an ordinary reason is not
// passed through as one.
// Threat: a stalled task on another machine reading only "running" here.
// Boundary: compactFleetTaskList, the linked list projection manage_projects
// returns for a machine; a pure transform, so a unit test is the narrowest.
func TestTaskStallReasonReachesLinkedTaskList(t *testing.T) {
	raw := []byte(`{"tasks":[` +
		`{"id":"task_a","title":"A","status":"in_progress","progress":{"task_state_reason":"stalled: no run and no next step for 25 min"}},` +
		`{"id":"task_b","title":"B","status":"in_progress","progress":{"task_state_reason":"working"}}]}`)
	out, err := compactFleetTaskList(raw)
	if err != nil {
		t.Fatal(err)
	}
	var got struct {
		Tasks []map[string]any `json:"tasks"`
	}
	if err := json.Unmarshal(out, &got); err != nil || len(got.Tasks) != 2 {
		t.Fatalf("decode %s: %v", out, err)
	}
	if got.Tasks[0]["task_state_reason"] != "stalled: no run and no next step for 25 min" {
		t.Fatalf("stalled row = %+v", got.Tasks[0])
	}
	if _, ok := got.Tasks[1]["task_state_reason"]; ok {
		t.Fatalf("an ordinary reason was passed through: %+v", got.Tasks[1])
	}
}

// kindSignals lists the account's task signals of kind (whatever the task).
func (f stallFixture) kindSignals(t *testing.T, kind string) []pebblestore.Signal {
	t.Helper()
	page, err := f.server.signals.ListAfter(0, 500, pebblestore.SignalFilter{Account: f.account, KindPrefixes: []string{"task"}})
	if err != nil {
		t.Fatal(err)
	}
	var out []pebblestore.Signal
	for _, sig := range page.Signals {
		if sig.Kind == kind {
			out = append(out, sig)
		}
	}
	return out
}

// Purpose: the check follows only work that can still move. An unfinished
// task whose records last changed longer ago than the horizon (24 h by
// default) is neither recovered nor alerted: an exhausted review gets no
// extra round, a task with no next step raises no task.stalled, nobody is
// woken and no stall marker is written. The same shapes still recover and
// alert when fresh (positive control in a second store, checked within the
// horizon), so the horizon is not a blanket off switch.
// Threat: the forge's first sweep, which re-ran weeks-old work, failed every
// recovery and sent about 25 task.stalled warnings in 3 minutes.
// Boundary: Server.CheckStalledTasks over real project tasks, sessions,
// workflow deliveries, the signal store and the session messages route on a
// temporary Pebble store; time is passed in, never slept.
func TestTaskStallOldWorkPastHorizonIsLeftAloneFreshStillActs(t *testing.T) {
	steps := []jobautomation.Step{{ID: "review", Type: jobautomation.StepAgent, Worker: "reviewer"}, {ID: "fix", Type: jobautomation.StepReopen, From: "review", MaxRounds: 1}}
	exhausted := func(d *jobautomation.Delivery) {
		at := time.Now().UnixMilli()
		d.State, d.StartedAt, d.FinishedAt = jobautomation.StateBlocked, at, at
		d.BlockedReason = jobautomation.RoundsExhaustedReason("fix")
		d.Steps[0].State, d.Steps[0].FinishedAt = jobautomation.StateSucceeded, at
		d.Steps[0].Outcome = map[string]any{"verdict": "block", "findings": "1. high a.go:1 unchecked"}
		d.Steps[1].State, d.Steps[1].FinishedAt, d.Steps[1].Reason = jobautomation.StateFailed, at, jobautomation.RoundsExhaustedReason("fix")
	}
	type shapes struct {
		f                     stallFixture
		origin, review, quiet string
		reviewTask, quietTask pebblestore.ProjectTaskRecord
	}
	setup := func(name string) shapes {
		f := newStallFixture(t, filepath.Join(t.TempDir(), name+".pebble"))
		origin := f.session(t, name+"-orchestrator")
		review := f.session(t, name+"-review")
		reviewTask := taskSessionFixture(t, f.server, review, pebblestore.ProjectTaskRecord{ID: "task_" + name + "_review", ProjectID: "proj-" + name, Agent: "coder", Status: "in_progress", OriginSessionID: origin.ID})
		f.settleRun(t, review.ID, pebblestore.InitialTaskRunID(reviewTask.ID), sessionruntime.RunIntentCompleted)
		f.delivery(t, reviewTask.ProjectID, reviewTask.ID, "wf-"+name, "", steps, exhausted)
		quiet := f.session(t, name+"-quiet")
		quietTask := taskSessionFixture(t, f.server, quiet, pebblestore.ProjectTaskRecord{ID: "task_" + name + "_quiet", ProjectID: "proj-" + name, Agent: "coder", Status: "in_progress", OriginSessionID: origin.ID})
		return shapes{f: f, origin: origin.ID, review: review.ID, quiet: quiet.ID, reviewTask: reviewTask, quietTask: quietTask}
	}

	old := setup("old")
	defer old.f.close()
	later := time.Now().Add(25 * time.Hour)
	old.f.check(later)
	old.f.check(later.Add(time.Minute))
	for _, id := range []string{old.reviewTask.ID, old.quietTask.ID} {
		if got := old.f.signals(t, "task.stalled", id); len(got) != 0 {
			t.Fatalf("old task %s alerted: %+v", id, got)
		}
		if stored := old.f.task(t, old.reviewTask.ProjectID, id); stored.Stall != nil {
			t.Fatalf("old task %s was marked stalled: %+v", id, stored.Stall)
		}
		if _, found, err := old.f.server.sessions.Store().GetTaskStall(old.f.account, id); err != nil || found {
			t.Fatalf("old task %s has a stall episode: found=%v err=%v", id, found, err)
		}
	}
	if n := len(old.f.userMessages(t, old.review, "One extra round")); n != 0 {
		t.Fatalf("old exhausted review was recovered: %d extra rounds", n)
	}
	if done, err := old.f.server.sessions.Store().HasTaskStallRecovery(old.f.account, old.reviewTask.ID, taskStallRecoveryReviewExtra); err != nil || done {
		t.Fatalf("old task recovery receipt: %v %v", done, err)
	}
	if n := len(old.f.userMessages(t, old.origin, "stalled")); n != 0 {
		t.Fatalf("the delegator was woken %d times for old work", n)
	}
	if state, _, _ := old.f.server.sessions.Store().GetV3SessionRunState(old.review); state.RunID != pebblestore.InitialTaskRunID(old.reviewTask.ID) {
		t.Fatalf("old task got a new run: %+v", state)
	}

	fresh := setup("fresh")
	defer fresh.f.close()
	soon := time.Now().Add(25 * time.Minute)
	fresh.f.check(soon)
	fresh.f.check(soon.Add(time.Minute))
	if n := len(fresh.f.userMessages(t, fresh.review, "One extra round")); n != 1 {
		t.Fatalf("fresh exhausted review: %d extra rounds, want 1", n)
	}
	if got := fresh.f.signals(t, "task.stalled", fresh.quietTask.ID); len(got) != 1 || got[0].Attrs["stall"] != pebblestore.ProjectTaskStallNoNextStep {
		t.Fatalf("fresh quiet task = %+v, want one no_next_step", got)
	}
}

// Purpose: a project task mirroring a worker run whose worker no longer runs
// jobs (gone, or not active) belongs to the retired worker pipeline: the
// stall check never re-runs it as recovery and never alerts on it, even
// within the horizon; a stall marker an earlier check left on it is cleared
// without a task.unstalled announcement.
// Threat: the forge's task_wrun_* mirrors of the retired Coder worker, whose
// "recovery" re-ran old worker runs that failed at once, each then raising
// task.stalled and reading needs_you.
// Boundary: Server.CheckStalledTasks with the worker read from the real
// WorkerStore on the same temporary Pebble store.
func TestTaskStallLegacyWorkerRunMirrorIsNeverRecoveredOrAlerted(t *testing.T) {
	f := newStallFixture(t, filepath.Join(t.TempDir(), "legacy.pebble"))
	defer f.close()
	p := testPrincipal()
	ws := pebblestore.NewWorkerStore(f.server.sessions.Store().Underlying())
	idle, err := ws.CreateWorker(f.account, p.UserID, pebblestore.CreateWorkerRequest{Role: pebblestore.WorkerRoleCoder, Name: "Coder", IdempotencyKey: "legacy-coder"}, nil)
	if err != nil {
		t.Fatal(err)
	}
	retired, err := ws.CreateWorker(f.account, p.UserID, pebblestore.CreateWorkerRequest{Role: pebblestore.WorkerRoleCoder, Name: "Old coder", IdempotencyKey: "retired-coder"}, nil)
	if err != nil {
		t.Fatal(err)
	}
	if idle.LifecycleState == pebblestore.WorkerLifecycleStateActive {
		t.Fatalf("fixture worker is active: %s", idle.LifecycleState)
	}
	steps := []jobautomation.Step{{ID: "review", Type: jobautomation.StepAgent, Worker: "reviewer"}, {ID: "fix", Type: jobautomation.StepReopen, From: "review", MaxRounds: 1}}
	mirror := func(id, workerID string) (pebblestore.ProjectTaskRecord, string) {
		s := f.session(t, "worker-execution-"+id)
		if _, err := ws.RecordWorkerRun(f.account, pebblestore.WorkerRunRecord{ID: id, WorkerID: workerID, UserID: p.UserID, Role: pebblestore.WorkerRoleCoder, SessionID: s.ID, Status: "failed", Error: "old run", CreatedAt: 1, StartedAt: 1, CompletedAt: 2}); err != nil {
			t.Fatal(err)
		}
		task := taskSessionFixture(t, f.server, s, pebblestore.ProjectTaskRecord{ID: "task_" + id, Title: "Coder", ProjectID: "proj-legacy", Agent: "coder", Status: "in_progress", WorkerID: workerID, WorkerRunID: id})
		f.settleRun(t, s.ID, pebblestore.InitialTaskRunID(task.ID), sessionruntime.RunIntentCompleted)
		f.delivery(t, task.ProjectID, task.ID, "wf-"+id, "", steps, func(d *jobautomation.Delivery) {
			at := time.Now().UnixMilli()
			d.State, d.StartedAt, d.FinishedAt = jobautomation.StateBlocked, at, at
			d.BlockedReason = jobautomation.RoundsExhaustedReason("fix")
			d.Steps[0].State, d.Steps[0].FinishedAt = jobautomation.StateSucceeded, at
			d.Steps[0].Outcome = map[string]any{"verdict": "block", "findings": "1. high a.go:1 unchecked"}
			d.Steps[1].State, d.Steps[1].FinishedAt, d.Steps[1].Reason = jobautomation.StateFailed, at, jobautomation.RoundsExhaustedReason("fix")
		})
		return task, s.ID
	}
	gone, goneSession := mirror("wrun_gone", retired.ID)
	notActive, notActiveSession := mirror("wrun_idle", idle.ID)
	if err := ws.DeleteWorker(f.account, p.UserID, retired.ID, retired.Revision); err != nil {
		t.Fatal(err)
	}
	// An earlier check (before this rule) marked one of them.
	if ok, err := f.server.sessions.Store().SetProjectTaskStall(f.account, gone.ProjectID, gone.ID, f.task(t, gone.ProjectID, gone.ID).UpdatedAt, &pebblestore.ProjectTaskStall{Kind: pebblestore.ProjectTaskStallReviewExhausted, Reason: "review exhausted", Since: 1, TaskUpdatedAt: f.task(t, gone.ProjectID, gone.ID).UpdatedAt}); err != nil || !ok {
		t.Fatalf("seed marker: %v %v", ok, err)
	}
	if err := f.server.sessions.Store().PutTaskStall(f.account, pebblestore.TaskStallRecord{ProjectID: gone.ProjectID, TaskID: gone.ID, Episode: "old", Kind: pebblestore.ProjectTaskStallReviewExhausted, Reason: "review exhausted", Alerted: true}); err != nil {
		t.Fatal(err)
	}

	at := time.Now().Add(30 * time.Minute)
	f.check(at)
	f.check(at.Add(time.Minute))
	for _, c := range []struct {
		task    pebblestore.ProjectTaskRecord
		session string
	}{{gone, goneSession}, {notActive, notActiveSession}} {
		if n := len(f.userMessages(t, c.session, "One extra round")); n != 0 {
			t.Fatalf("legacy mirror %s was re-run as recovery (%d)", c.task.ID, n)
		}
		if state, _, _ := f.server.sessions.Store().GetV3SessionRunState(c.session); state.RunID != pebblestore.InitialTaskRunID(c.task.ID) {
			t.Fatalf("legacy mirror %s got a new run: %+v", c.task.ID, state)
		}
		for _, kind := range []string{"task.stalled", "task.unstalled"} {
			if got := f.signals(t, kind, c.task.ID); len(got) != 0 {
				t.Fatalf("legacy mirror %s raised %s: %+v", c.task.ID, kind, got)
			}
		}
		stored := f.task(t, c.task.ProjectID, c.task.ID)
		if _, reason := pebblestore.ProjectTaskState(stored, "", false, false); stored.Stall != nil || strings.HasPrefix(reason, "stalled") {
			t.Fatalf("legacy mirror %s still reads stalled: %+v %q", c.task.ID, stored.Stall, reason)
		}
		if _, found, err := f.server.sessions.Store().GetTaskStall(f.account, c.task.ID); err != nil || found {
			t.Fatalf("legacy mirror %s keeps a stall episode: %v %v", c.task.ID, found, err)
		}
	}
}

// Purpose: no backfill of alerts for old work. The first check of a daemon
// process over many old unfinished tasks sends at most one summary signal
// ("N old unfinished tasks were not checked ...") and no task.stalled; later
// checks and a restart over the same old work send nothing more.
// Threat: one Telegram message per old task (about 25 in 3 minutes on the
// forge) when the stall feature first ran.
// Boundary: Server.CheckStalledTasks and SignalStore.AppendOnce; the restart
// reopens the same Pebble path with a new Server.
func TestTaskStallFirstSweepOverOldWorkSendsOneSummary(t *testing.T) {
	path := filepath.Join(t.TempDir(), "summary.pebble")
	f := newStallFixture(t, path)
	const n = 6
	for i := 0; i < n; i++ {
		id := "old-" + string(rune('a'+i))
		taskSessionFixture(t, f.server, f.session(t, id), pebblestore.ProjectTaskRecord{ID: "task_" + id, ProjectID: "proj-summary", Agent: "coder", Status: "in_progress"})
	}
	later := time.Now().Add(48 * time.Hour)
	f.check(later)
	f.check(later.Add(time.Minute))
	f.check(later.Add(2 * time.Minute))
	summaries := f.kindSignals(t, "task.stall_check_skipped")
	if len(summaries) != 1 || summaries[0].Attrs["count"] != "6" || summaries[0].Attrs["horizon_hours"] != "24" ||
		!strings.Contains(summaries[0].Summary, "6 old unfinished tasks were not checked") || summaries[0].Severity != pebblestore.SignalSeverityInfo {
		t.Fatalf("summaries = %+v, want exactly one for 6 tasks", summaries)
	}
	if got := f.kindSignals(t, "task.stalled"); len(got) != 0 {
		t.Fatalf("old work raised %d task.stalled", len(got))
	}
	f.close()
	f = newStallFixture(t, path)
	defer f.close()
	f.check(later.Add(3 * time.Minute))
	if got := f.kindSignals(t, "task.stall_check_skipped"); len(got) != 1 {
		t.Fatalf("after restart: %d summaries, want still 1", len(got))
	}
	if got := f.kindSignals(t, "task.stalled"); len(got) != 0 {
		t.Fatalf("after restart: %d task.stalled", len(got))
	}
}
