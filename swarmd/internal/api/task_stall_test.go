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
// its reset plus STALL_AFTER), a task watch (a run asleep on wait_tasks), a
// running review step and a pending background command raise nothing however
// long they last; each has a positive control showing the same task stalls
// once the wait is gone (the limit's reset passed, the background command
// ended and the run is silent).
// Threat: alerts on healthy long work (a weekly limit, a long review), which
// would teach people to ignore task.stalled.
// Boundary: Server.CheckStalledTasks over real deliveries in the workflow
// store, real V3 run intents (including a registered task wait) and the
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
		{SessionID: "watcher", Kind: pebblestore.V3SessionMutationRecordRunIntent, EventType: "session.run.waiting_tasks", ClientRequestID: "task-wait:run-w", TaskWait: &pebblestore.V3ProjectTaskWaitMutation{RunID: "run-w", ProjectID: "proj-watch", TaskIDs: []string{"task_watched"}}},
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
	if state, _, _ := db.GetV3SessionRunState("watcher"); state.Status != pebblestore.V3RunIntentWaitingTasks {
		t.Fatalf("watch not registered: %+v", state)
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
