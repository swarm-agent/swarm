package api

import (
	"path/filepath"
	"strings"
	"testing"
	"time"

	"swarm/packages/swarmd/internal/jobautomation"
	sessionruntime "swarm/packages/swarmd/internal/session"
	pebblestore "swarm/packages/swarmd/internal/store/pebble"
)

// Purpose: a task whose newest workflow delivery ended sent_back, with no run
// on the task's session for the stall window, gets its reopen re-sent once
// (the same follow-up text the reopen step sent) as a run on the task's own
// session, before anything is reported stalled; the delivery's reopen then
// names the new follow-up, and the retry uses no round. If that run also
// leaves no next step, the next stall window raises exactly one task.stalled
// naming the workflow, the step and the round, and no second retry is sent.
// Threat (seen live): review ok, push refused "rebase needed", the workflow
// recorded a rebase reopen (sent_back, round 3), but no run started; the task
// read "running" with no run until "stalled: no run and no next step" and
// nothing recovered it, while a plain message to the session worked at once.
// The task's status still said in_progress, which the reopen path answered
// busy, so the retry must not wait on it.
// Boundary: Server.CheckStalledTasks -> taskStallVerdict (taskStallSentBack)
// -> recoverTaskStall -> Server.reopenTask (Recovery) over the real session
// messages route, durable run intents, recovery receipts, the signal store
// and jobautomation.Store.RecordReopenRetry/ReopenRounds. No provider runs
// (the executor is off); the retry's run is settled explicitly. This is the
// narrowest layer where the stall check, the task's session and the workflow
// delivery meet.
func TestTaskStallSentBackWithNoRunRetriesTheReopenOnceThenAlerts(t *testing.T) {
	f := newStallFixture(t, filepath.Join(t.TempDir(), "sentback.pebble"))
	work := f.session(t, "sentback-task-session")
	origin := f.session(t, "sentback-orchestrator")
	task := taskSessionFixture(t, f.server, work, pebblestore.ProjectTaskRecord{ID: "task_sentback", ProjectID: "proj-sentback", Agent: "coder", Status: "in_progress", OriginSessionID: origin.ID})
	f.settleRun(t, work.ID, pebblestore.InitialTaskRunID(task.ID), sessionruntime.RunIntentCompleted)
	steps := []jobautomation.Step{
		{ID: "review", Type: jobautomation.StepAgent, Worker: "reviewer"},
		{ID: "push", Type: jobautomation.StepRun, Command: "push-dev", ContinueOnFailure: true, If: []jobautomation.Condition{{Step: "review", Verdict: jobautomation.VerdictOK}}},
		{ID: "rebase", Type: jobautomation.StepReopen, Message: "Rebase onto dev: {{steps.push.output}}", MaxRounds: 2, If: []jobautomation.Condition{{Step: "push", Status: jobautomation.StateFailed}}},
	}
	const refused = "NOT PUSHED: rebase needed: dev moved and changed files this job also changed"
	const lost = "run-never-started"
	d := f.delivery(t, task.ProjectID, task.ID, "wf-ship", "run-round-2", steps, func(d *jobautomation.Delivery) {
		at := time.Now().UnixMilli()
		d.State, d.StartedAt, d.FinishedAt, d.Round = jobautomation.StateSentBack, at, at, 2
		d.Steps[0].State, d.Steps[0].Outcome = jobautomation.StateSucceeded, map[string]any{"verdict": jobautomation.VerdictOK}
		d.Steps[1].State, d.Steps[1].Reason, d.Steps[1].Output = jobautomation.StateFailed, "exited with code 1: "+refused, refused
		d.Steps[2].State, d.Steps[2].Outcome = jobautomation.StateSucceeded, map[string]any{"followup": lost, "round": 3}
		d.SentBack = &jobautomation.SentBack{Step: "rebase", Round: 3, Reason: "push failed: exited with code 1: " + refused}
	})
	store, err := f.server.jobAutomationStore()
	if err != nil {
		t.Fatal(err)
	}
	start := time.Now()

	f.check(start.Add(5 * time.Minute)) // within the stall window: nothing yet
	if n := len(f.userMessages(t, work.ID, "Rebase onto dev")); n != 0 {
		t.Fatalf("reopen re-sent within the stall window: %d", n)
	}
	f.check(start.Add(21 * time.Minute))
	retried := f.userMessages(t, work.ID, "Rebase onto dev")
	if len(retried) != 1 || !strings.Contains(retried[0].Content, refused) || !strings.Contains(retried[0].Content, jobautomation.ReopenClosing) {
		t.Fatalf("reopen retry = %+v, want the rebase follow-up once", retried)
	}
	if got := f.signals(t, "task.stalled", task.ID); len(got) != 0 {
		t.Fatalf("the retry raised an alert: %+v", got)
	}
	active, found, err := f.server.sessions.GetSessionActiveRunIntent(work.ID)
	if err != nil || !found || active.RunID == "" || active.RunID == pebblestore.InitialTaskRunID(task.ID) {
		t.Fatalf("active run after the retry = %+v %v %v, want the retry's run", active, found, err)
	}
	saved, err := store.GetDelivery(f.account, d.ID)
	if err != nil {
		t.Fatal(err)
	}
	if got, _ := saved.Steps[2].Outcome["followup"].(string); got != active.RunID || saved.Steps[2].Outcome["retried_followup"] != lost || saved.State != jobautomation.StateSentBack {
		t.Fatalf("delivery reopen after the retry = %+v (state %s), want follow-up %s replacing %s", saved.Steps[2].Outcome, saved.State, active.RunID, lost)
	}
	if rounds, err := store.ReopenRounds(f.account, d.JobID, "wf-ship"); err != nil || rounds.Total != 0 {
		t.Fatalf("rounds after the retry = %+v %v, want the retry to count no round", rounds, err)
	}

	// The retry's run ends and leaves no next step (no new delivery).
	exec := newSessionV3Executor(f.server)
	state, _, err := f.server.sessions.Store().GetV3SessionRunState(work.ID)
	if err != nil {
		t.Fatal(err)
	}
	job := sessionV3ExecutorJob{Principal: testPrincipal(), SessionID: work.ID, RunID: active.RunID, EpochID: firstNonEmptyString(state.EpochID, "epoch-00000000000000000001")}
	for _, step := range []struct{ status, event string }{{sessionruntime.RunIntentRunning, "session.assistant.started"}, {sessionruntime.RunIntentCompleted, "session.run.completed"}} {
		if _, err := exec.recordRunStatus(job, step.status, "", step.event); err != nil {
			t.Fatalf("settle the retry's run %s: %v", step.status, err)
		}
	}
	later := time.Now()
	f.check(later.Add(21 * time.Minute))
	f.check(later.Add(22 * time.Minute))
	got := f.signals(t, "task.stalled", task.ID)
	if len(got) != 1 || got[0].Attrs["stall"] != pebblestore.ProjectTaskStallNoNextStep ||
		!strings.Contains(got[0].Summary, "sent back by workflow review and fix (step rebase, round 3)") || !strings.Contains(got[0].Summary, "after one retry of the reopen") {
		t.Fatalf("stall after the retry = %+v", got)
	}
	if n := len(f.userMessages(t, work.ID, "Rebase onto dev")); n != 1 {
		t.Fatalf("reopen retries = %d, want 1", n)
	}
}
