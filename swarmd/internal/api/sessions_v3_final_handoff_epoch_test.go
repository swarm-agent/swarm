package api

// Purpose:
//   - Requirement: a run that completes a plan's last checkpoint outlives the
//     final plan handoff that seals its execution epoch
//     (run.appendFinalPlanExecutionSystemMessage -> BeginExecutionEpoch with
//     SkipRunIntent). Its own later writes (progress, completion, failure, a
//     stop, the restart's reconciliation) must still commit, in the
//     handoff's successor epoch, so the run ends, the task settles and the
//     task's job end is signalled exactly once. A run with no executor whose
//     work already ended in that persisted handoff (plan waiting for review,
//     every checkpoint completed) is finalized as completed on stop or
//     restart, never as a person's stop or a restart failure, and the plan's
//     review gate is left untouched.
//   - Threat/regression: every run-owned write was fenced by the run's
//     original, now sealed, epoch (applySessionV3PrimaryMutation defaults it
//     from RunIntent.EpochID; recordRunStatus passes job.EpochID): the run
//     stayed the session's active writer, the task stayed in_progress with no
//     job-end signal for workflows, stop_task returned 500 "execution epoch
//     ... is not active" and reopen was refused for an active writer.
//   - The fence must still refuse: an epoch sealed by any other boundary
//     (compaction for another run), a run that is not the session's active
//     run, and a live run is still stopped as a person's stop. A refused stop
//     is a 409 conflict, not a 500.
//   - Boundary/authority: Server.applySessionV3PrimaryMutation and
//     sessionV3RunWriteEpoch, sessionV3Executor.CancelRun /
//     failStaleRunningRunForRecovery / recoverDurableRuns,
//     Server.StopProjectTask, the store's ApplyV3SessionMutation epoch fence
//     (ExecutionEpochNotActiveError), settleTaskRunEnd -> signalTaskRunEnded.
//   - Narrowest layer: real server, executor and Pebble store. The live-run
//     cases stub only the model provider (blockingProvider); the dead-run
//     cases write the run's intents through the executor's own status writer
//     with no executor goroutine, exactly the state a run whose executor
//     ended leaves behind.

import (
	"context"
	"errors"
	"net/http"
	"path/filepath"
	"strings"
	"testing"

	runruntime "swarm/packages/swarmd/internal/run"
	sessionruntime "swarm/packages/swarmd/internal/session"
	pebblestore "swarm/packages/swarmd/internal/store/pebble"
)

const finalHandoffInitialEpoch = "epoch-00000000000000000001"

// sealByFinalPlanHandoff seals the session's active epoch exactly as the
// plan lifecycle's final handoff does: a system handoff message as the
// predecessor's tail, a successor epoch, and no run ownership transferred.
func sealByFinalPlanHandoff(t *testing.T, sessions *sessionruntime.Service, sessionID, planID, key string) pebblestore.BeginExecutionEpochResult {
	t.Helper()
	p := testPrincipal()
	message := pebblestore.MessageSnapshot{
		ID: "v3msg_final_handoff_" + key, SessionID: sessionID, UserID: p.UserID, AccountScopeID: p.AccountScopeID,
		Role: "system", Content: "Final plan handoff: every checkpoint is complete.",
		Metadata: map[string]any{"source": runruntime.PlanExecutionFinalHandoffMessageSource},
	}
	result, err := sessions.BeginExecutionEpoch(pebblestore.BeginExecutionEpochInput{
		SessionID: sessionID, UserID: p.UserID, AccountScopeID: p.AccountScopeID,
		ClientRequestID: "final-handoff-" + key, PayloadHash: "final-handoff-hash-" + key,
		Reason: "final_plan_handoff", PlanID: planID, SourceMessageID: message.ID,
		FinalHandoffMessage: &message, SkipRunIntent: true,
	})
	if err != nil {
		t.Fatalf("final plan handoff: %v", err)
	}
	if result.Epoch.Status != pebblestore.ExecutionEpochStatusActive || result.Predecessor.Status != pebblestore.ExecutionEpochStatusSealed {
		t.Fatalf("final plan handoff did not seal the predecessor: %+v", result)
	}
	return result
}

// saveFinalPlan saves an approved plan whose every checkpoint is completed,
// in executionStatus (waiting_review: the person's final review is due).
func saveFinalPlan(t *testing.T, sessions *sessionruntime.Service, sessionID, planID, executionStatus string) pebblestore.SessionPlanSnapshot {
	t.Helper()
	checkpointStatus := sessionruntime.PlanCheckpointStatusCompleted
	if executionStatus == sessionruntime.PlanExecutionStateInProgress {
		checkpointStatus = sessionruntime.PlanCheckpointStatusInProgress
	}
	doc := pebblestore.SessionPlanDocument{
		ID: planID, Title: "Final plan",
		Checkpoints: []pebblestore.SessionPlanCheckpoint{
			{ID: "cp-1", Title: "First", Status: sessionruntime.PlanCheckpointStatusCompleted},
			{ID: "cp-2", Title: "Last", Status: checkpointStatus},
		},
		ExecutionState: &pebblestore.SessionPlanExecutionState{Status: executionStatus},
	}
	plan, _, err := sessions.SavePlanWithMetadata(sessionID, planID, "Final plan", "Do the work in two checkpoints.", "approved", "approved", true, sessionruntime.PlanSaveMetadata{Document: &doc})
	if err != nil {
		t.Fatalf("save plan: %v", err)
	}
	stored, ok, err := sessions.GetPlan(sessionID, planID)
	if err != nil || !ok || stored.Document == nil || stored.Document.ExecutionState == nil || stored.Document.ExecutionState.Status != executionStatus {
		t.Fatalf("stored plan = %+v ok=%v err=%v (saved %+v)", stored, ok, err, plan)
	}
	return stored
}

func requireActiveEpoch(t *testing.T, sessions *sessionruntime.Service, sessionID, want string) {
	t.Helper()
	active, ok, err := sessions.GetActiveExecutionEpoch(sessionID)
	if err != nil || !ok || active.EpochID != want || active.Status != pebblestore.ExecutionEpochStatusActive {
		t.Fatalf("active epoch = %+v ok=%v err=%v, want %q", active, ok, err, want)
	}
	sealed, ok, err := sessions.GetExecutionEpoch(sessionID, finalHandoffInitialEpoch)
	if err != nil || !ok || sealed.Status != pebblestore.ExecutionEpochStatusSealed {
		t.Fatalf("the run's own epoch was reopened: %+v ok=%v err=%v", sealed, ok, err)
	}
}

func runFailureMessages(t *testing.T, sessions *sessionruntime.Service, sessionID string) []string {
	t.Helper()
	messages, err := sessions.ListSessionMessages(sessionID, 0, 500)
	if err != nil {
		t.Fatal(err)
	}
	var out []string
	for _, message := range messages {
		if kind, _ := message.Metadata["message_kind"].(string); kind == "run_failure" {
			out = append(out, message.Content)
		}
	}
	return out
}

func TestV3RunOutlivingItsFinalPlanHandoffCompletesInTheSuccessorEpoch(t *testing.T) {
	f, p := newRunCancelFixture(t, "final-handoff-live")
	sessionID := f.createSession("plan-run")
	runID := sendFollowup(t, f, p, sessionID, "final-checkpoint")
	before, ok, err := f.sessions.GetSessionRunIntent(sessionID, runID)
	if err != nil || !ok || before.EpochID != finalHandoffInitialEpoch {
		t.Fatalf("run intent before handoff = %+v ok=%v err=%v", before, ok, err)
	}

	// The last checkpoint's completion hands the plan off while the run is
	// still inside its provider step.
	handoff := sealByFinalPlanHandoff(t, f.sessions, sessionID, "plan-live", "live")
	_, release := p.channels(sessionID)
	close(release)

	f.waitRun(sessionID, runID, sessionruntime.RunIntentCompleted)
	intent, ok, err := f.sessions.GetSessionRunIntent(sessionID, runID)
	if err != nil || !ok || intent.Status != sessionruntime.RunIntentCompleted || intent.EpochID != finalHandoffInitialEpoch {
		t.Fatalf("completed intent = %+v ok=%v err=%v (the run keeps its own epoch as provenance)", intent, ok, err)
	}
	if _, active, err := f.sessions.GetSessionActiveRunIntent(sessionID); err != nil || active {
		t.Fatalf("the completed run is still the session's active writer: active=%v err=%v", active, err)
	}
	requireActiveEpoch(t, f.sessions, sessionID, handoff.Epoch.EpochID)
	if got := f.lastReply(sessionID); got != "follow-up done" {
		t.Fatalf("assistant reply after the handoff = %q", got)
	}
	if failures := runFailureMessages(t, f.sessions, sessionID); len(failures) != 0 {
		t.Fatalf("the run recorded failures after its handoff: %q", failures)
	}
}

func TestV3StopOfALiveRunAfterItsFinalPlanHandoffStillStopsIt(t *testing.T) {
	f, p := newRunCancelFixture(t, "final-handoff-live-stop")
	sessionID := f.createSession("plan-run")
	runID := sendFollowup(t, f, p, sessionID, "final-checkpoint")
	handoff := sealByFinalPlanHandoff(t, f.sessions, sessionID, "plan-live-stop", "live-stop")

	// A live executor still holds the run: it is the active writer and a
	// person's stop stops it (no handoff finalization for a live run).
	if owner, active, err := f.sessions.GetSessionActiveRunIntent(sessionID); err != nil || !active || owner.RunID != runID {
		t.Fatalf("live run is not the active writer: %+v %v %v", owner, active, err)
	}
	const reason = "person pressed stop"
	if _, cancelled, err := f.server.v3SessionExecutor.CancelRun(sessionV3ExecutorJob{Principal: testPrincipal(), SessionID: sessionID, RunID: runID, StopCause: sessionruntime.PlanStoppedByUser}, reason); err != nil || !cancelled {
		t.Fatalf("CancelRun after the handoff: cancelled=%v err=%v", cancelled, err)
	}
	if got := runReason(t, f, sessionID, runID, sessionruntime.RunIntentCancelled); got != reason {
		t.Fatalf("cancelled reason = %q, want %q", got, reason)
	}
	intent, _, _ := f.sessions.GetSessionRunIntent(sessionID, runID)
	if !pebblestore.V3RunIntentStoppedByPerson(intent) {
		t.Fatalf("the person's stop was not recorded: %+v", intent)
	}
	requireActiveEpoch(t, f.sessions, sessionID, handoff.Epoch.EpochID)
}

// deadHandedOffTask leaves a project task's run exactly as a run whose
// executor ended after its final plan handoff: the intent running in its
// original epoch, the plan handed off and that epoch sealed.
type deadHandedOffTask struct {
	server  *Server
	exec    *sessionV3Executor
	session pebblestore.SessionSnapshot
	task    pebblestore.ProjectTaskRecord
	job     sessionV3ExecutorJob
	plan    pebblestore.SessionPlanSnapshot
	handoff pebblestore.BeginExecutionEpochResult
}

func newDeadHandedOffTask(t *testing.T, id, planStatus string) *deadHandedOffTask {
	t.Helper()
	server, sessions, _, _, _ := newRoutedSessionTestServerWithSwarmStore(t)
	db, err := pebblestore.Open(filepath.Join(t.TempDir(), "signals.pebble"))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = db.Close() })
	server.SetSignalStore(pebblestore.NewSignalStore(db))
	exec := newSessionV3Executor(server)
	server.v3SessionExecutor = exec
	session := createSessionsV3PrimaryTestSessionWithWorkspaceAndPreference(t, server, "final-handoff-"+id, id, t.TempDir(), pebblestore.ModelPreference{Provider: "test-provider", Model: "test-model", Thinking: "medium"})
	planID := "plan-" + id
	task := taskSessionFixture(t, server, session, pebblestore.ProjectTaskRecord{ID: id, ProjectID: "proj-final", Agent: "coder", Status: "in_progress", PlanBinding: &pebblestore.ProjectTaskPlanBinding{SessionID: session.ID, PlanID: planID}})
	job := sessionV3ExecutorJob{Principal: testPrincipal(), SessionID: session.ID, RunID: task.ExecutionRunID(), EpochID: finalHandoffInitialEpoch}
	for _, step := range []struct{ status, event string }{
		{sessionruntime.RunIntentPendingExecutor, "session.assistant.queued"},
		{sessionruntime.RunIntentRunning, "session.assistant.started"},
	} {
		if _, err := exec.recordRunStatus(job, step.status, "", step.event); err != nil {
			t.Fatalf("record %s: %v", step.status, err)
		}
	}
	plan := saveFinalPlan(t, sessions, session.ID, planID, planStatus)
	handoff := sealByFinalPlanHandoff(t, sessions, session.ID, planID, id)
	return &deadHandedOffTask{server: server, exec: exec, session: session, task: task, job: job, plan: plan, handoff: handoff}
}

func (d *deadHandedOffTask) signals(t *testing.T) []pebblestore.Signal {
	t.Helper()
	page, err := d.server.signals.ListAfter(0, 100, pebblestore.SignalFilter{Account: testPrincipal().AccountScopeID, KindPrefixes: []string{"task.run"}})
	if err != nil {
		t.Fatal(err)
	}
	var out []pebblestore.Signal
	for _, sig := range page.Signals {
		if sig.Refs["task_id"] == d.task.ID {
			out = append(out, sig)
		}
	}
	return out
}

func (d *deadHandedOffTask) taskRecord(t *testing.T) pebblestore.ProjectTaskRecord {
	t.Helper()
	task, ok, err := d.server.sessions.Store().GetProjectTask(testPrincipal().AccountScopeID, d.task.ProjectID, d.task.ID)
	if err != nil || !ok || task == nil {
		t.Fatalf("task: ok=%v err=%v", ok, err)
	}
	return *task
}

// requireFinalized checks the postconditions of a handed-off run's
// finalization: completed in its own epoch, no active writer, the task
// settled as needs_review with exactly one succeeded job-end signal, and
// the plan (the person's review) unchanged.
func (d *deadHandedOffTask) requireFinalized(t *testing.T) {
	t.Helper()
	sessions := d.server.sessions
	intent, ok, err := sessions.GetSessionRunIntent(d.session.ID, d.job.RunID)
	if err != nil || !ok || intent.Status != sessionruntime.RunIntentCompleted || intent.EpochID != finalHandoffInitialEpoch || intent.BlockedReason != "" || intent.StoppedBy != "" {
		t.Fatalf("finalized intent = %+v ok=%v err=%v", intent, ok, err)
	}
	if _, active, err := sessions.GetSessionActiveRunIntent(d.session.ID); err != nil || active {
		t.Fatalf("the finalized run is still the active writer: active=%v err=%v", active, err)
	}
	requireActiveEpoch(t, sessions, d.session.ID, d.handoff.Epoch.EpochID)
	task := d.taskRecord(t)
	// No commits are integrated (the fixture has no repository), so the
	// completed run's task waits for review, exactly.
	if task.Status != "needs_review" || task.ActionNeeded != "Action Needed: Review agent deliverables and verify outcomes" || task.IsIntegrated {
		t.Fatalf("task after finalization = %q (%q) integrated=%v, want needs_review", task.Status, task.ActionNeeded, task.IsIntegrated)
	}
	got := d.signals(t)
	if len(got) != 1 || got[0].Kind != "task.run.succeeded" {
		t.Fatalf("task.run signals = %+v, want exactly one task.run.succeeded", got)
	}
	plan, ok, err := sessions.GetPlan(d.session.ID, d.plan.ID)
	if err != nil || !ok || plan.Version != d.plan.Version || plan.Document == nil || plan.Document.ExecutionState == nil || plan.Document.ExecutionState.Status != d.plan.Document.ExecutionState.Status {
		t.Fatalf("plan changed by the run's finalization: %+v ok=%v err=%v (was version %d)", plan, ok, err, d.plan.Version)
	}
}

func TestV3StopFinalizesADeadRunWhoseFinalPlanHandoffIsPersisted(t *testing.T) {
	d := newDeadHandedOffTask(t, "task_stop_dead", sessionruntime.PlanExecutionStateWaitingReview)
	// Before: the dead run still holds the session, which is what refused
	// the canonical reopen ("retained source still has an active writer").
	if _, active, err := d.server.sessions.GetSessionActiveRunIntent(d.session.ID); err != nil || !active {
		t.Fatalf("fixture run is not the active writer: %v %v", active, err)
	}

	stopped, err := d.server.StopProjectTask(context.Background(), testPrincipal(), d.task.ProjectID, d.task.ID)
	if err != nil {
		t.Fatalf("stop_task on a handed-off run: %v", err)
	}
	if stopped.Status != "completed" || stopped.RunID != d.job.RunID {
		t.Fatalf("stop result = %+v, want the run reported completed", stopped)
	}
	d.requireFinalized(t)

	// Repeats change nothing: a second stop finds no active run, and the
	// terminal write replays without a second job-end signal.
	var conflict *projectTaskFollowupError
	if _, err := d.server.StopProjectTask(context.Background(), testPrincipal(), d.task.ProjectID, d.task.ID); !errors.As(err, &conflict) || conflict.status != http.StatusConflict {
		t.Fatalf("second stop = %v, want 409 no active run", err)
	}
	if result, err := d.exec.recordRunStatus(d.job, sessionruntime.RunIntentCompleted, "", "session.assistant.completed"); err != nil || !result.Replayed {
		t.Fatalf("repeated completion = replayed %v err %v", result.Replayed, err)
	}
	if _, _, err := d.exec.CancelRun(d.job, "late stop"); err == nil {
		t.Fatal("a stop of the finalized run was accepted")
	}
	d.requireFinalized(t)
}

func TestV3RestartFinalizesARunWhoseFinalPlanHandoffIsPersisted(t *testing.T) {
	d := newDeadHandedOffTask(t, "task_restart_dead", sessionruntime.PlanExecutionStateWaitingReview)
	d.exec.recoverDurableRuns(context.Background())
	d.requireFinalized(t)
	// A second recovery pass (another restart) finds nothing to do.
	d.exec.recoverDurableRuns(context.Background())
	d.requireFinalized(t)
}

func TestV3SealedEpochFenceStillRefusesForeignBoundariesAndOtherRuns(t *testing.T) {
	t.Run("handoff of a plan still in progress is stopped, not completed", func(t *testing.T) {
		d := newDeadHandedOffTask(t, "task_not_terminal", sessionruntime.PlanExecutionStateInProgress)
		stopped, err := d.server.StopProjectTask(context.Background(), testPrincipal(), d.task.ProjectID, d.task.ID)
		if err != nil || stopped.Status != "stopped" {
			t.Fatalf("stop = %+v err=%v, want a person's stop", stopped, err)
		}
		intent, _, _ := d.server.sessions.GetSessionRunIntent(d.session.ID, d.job.RunID)
		if intent.Status != sessionruntime.RunIntentCancelled || !pebblestore.V3RunIntentStoppedByPerson(intent) {
			t.Fatalf("intent = %+v, want cancelled by the person", intent)
		}
		// A person's stop leaves the task stopped, not a job end: no workflow
		// signal (taskRunLeftResult), and message_task resumes it.
		if task := d.taskRecord(t); task.Status != pebblestore.ProjectTaskStatusStopped {
			t.Fatalf("task = %q, want stopped", task.Status)
		}
		if got := d.signals(t); len(got) != 0 {
			t.Fatalf("signals = %+v, want none for a person's stop", got)
		}
	})

	t.Run("an epoch sealed by another run's boundary stays fenced", func(t *testing.T) {
		server, sessions, _, _, _ := newRoutedSessionTestServerWithSwarmStore(t)
		exec := newSessionV3Executor(server)
		server.v3SessionExecutor = exec
		session := createSessionsV3PrimaryTestSessionWithWorkspaceAndPreference(t, server, "final-handoff-foreign", "foreign", t.TempDir(), pebblestore.ModelPreference{Provider: "test-provider", Model: "test-model", Thinking: "medium"})
		task := taskSessionFixture(t, server, session, pebblestore.ProjectTaskRecord{ID: "task_foreign", ProjectID: "proj-final", Agent: "coder", Status: "in_progress"})
		job := sessionV3ExecutorJob{Principal: testPrincipal(), SessionID: session.ID, RunID: task.ExecutionRunID(), EpochID: finalHandoffInitialEpoch}
		for _, status := range []string{sessionruntime.RunIntentPendingExecutor, sessionruntime.RunIntentRunning} {
			if _, err := exec.recordRunStatus(job, status, "", "session.run."+status); err != nil {
				t.Fatal(err)
			}
		}
		p := testPrincipal()
		trigger := pebblestore.MessageSnapshot{ID: "v3msg_foreign_compaction", SessionID: session.ID, UserID: p.UserID, AccountScopeID: p.AccountScopeID, Role: "user", Content: "Continue from the recap."}
		if _, err := sessions.BeginExecutionEpoch(pebblestore.BeginExecutionEpochInput{SessionID: session.ID, UserID: p.UserID, AccountScopeID: p.AccountScopeID, ClientRequestID: "foreign-compaction", PayloadHash: "foreign-compaction", Reason: "context_compaction_threshold", RunID: "run-other", SourceMessageID: trigger.ID, TriggerMessage: &trigger, SkipRunIntent: true}); err != nil {
			t.Fatalf("foreign boundary: %v", err)
		}
		_, err := exec.recordRunStatus(job, sessionruntime.RunIntentFailed, "late failure", "session.run.failed")
		var fenced *pebblestore.ExecutionEpochNotActiveError
		if !errors.As(err, &fenced) || fenced.EpochID != finalHandoffInitialEpoch {
			t.Fatalf("write into an epoch sealed by another run's boundary = %v, want the epoch fence", err)
		}
		var conflict *projectTaskFollowupError
		if _, err := server.StopProjectTask(context.Background(), p, task.ProjectID, task.ID); !errors.As(err, &conflict) || conflict.status != http.StatusConflict || !strings.Contains(err.Error(), "superseded") {
			t.Fatalf("stop of a fenced run = %v, want a 409 conflict", err)
		}
		intent, _, _ := sessions.GetSessionRunIntent(session.ID, job.RunID)
		if intent.Status != sessionruntime.RunIntentRunning {
			t.Fatalf("a refused write changed the run: %+v", intent)
		}
		if stored, _, _ := server.sessions.Store().GetProjectTask(p.AccountScopeID, task.ProjectID, task.ID); stored == nil || stored.Status != "in_progress" {
			t.Fatalf("a refused stop changed the task: %+v", stored)
		}
	})

	t.Run("another run cannot write into the handed-off run's sealed epoch", func(t *testing.T) {
		d := newDeadHandedOffTask(t, "task_intruder", sessionruntime.PlanExecutionStateWaitingReview)
		intruder := d.job
		intruder.RunID = "run-intruder"
		_, err := d.exec.recordRunStatus(intruder, sessionruntime.RunIntentPendingExecutor, "", "session.assistant.queued")
		var fenced *pebblestore.ExecutionEpochNotActiveError
		if !errors.As(err, &fenced) {
			t.Fatalf("intruder write into the sealed epoch = %v, want the epoch fence", err)
		}
		if _, ok, _ := d.server.sessions.GetSessionRunIntent(d.session.ID, intruder.RunID); ok {
			t.Fatal("the intruder's run intent was recorded")
		}
		if owner, active, _ := d.server.sessions.GetSessionActiveRunIntent(d.session.ID); !active || owner.RunID != d.job.RunID {
			t.Fatalf("active writer changed: %+v %v", owner, active)
		}
		if got := d.signals(t); len(got) != 0 {
			t.Fatalf("signals = %+v", got)
		}
	})
}
