package run

import (
	"context"
	"errors"
	"strings"
	"testing"

	sessions "swarm/packages/swarmd/internal/session"
	store "swarm/packages/swarmd/internal/store/pebble"
)

// Purpose:
//   - Requirement: stopping a worker job's active run in its session
//     (swarm_stop_run, which cancels the run's V3 intent without the job's
//     cancel mark) leaves a Coder/Designer/Finder job open: its receipt settles
//     as a stopped failure (pebblestore.WorkerRunStopped) on the same session,
//     so a follow-up is admitted, reserved and executed there. Cancelling the
//     job stays possible: swarm_stop_job (CancelRun) on a running job settles
//     it cancelled, and on a stopped job sets the permanent cancel mark that
//     every follow-up gate refuses.
//   - Threat/regression: swarm_stop_run on worker-execution-<run> cancelled the
//     job, and the next swarm_message_job failed with "the job was cancelled;
//     give the worker a new job instead", so a caller could not pause and
//     correct a job.
//   - Boundary/authority: WorkerExecutionService.observeRun (the receipt
//     settlement from the run intent), CancelRun / cancelStoppedJob,
//     workerFollowupBusy, workerRunExecutable (the run gate) and
//     WorkerStore.ReserveWorkerFollowup over a real Pebble store and session
//     service (limitRunFixture); only the model provider is absent.
//   - Narrowest layer: the settlement and gates are run-package functions; the
//     HTTP/MCP path over them, including PrepareFollowup with the worker
//     owner's membership, is covered in the api package
//     (worker_job_correction_test.go).

const stoppedByCaller = "the caller stopped the run"

func TestStoppedWorkerJobStaysOpenForAFollowupOnTheSameSession(t *testing.T) {
	f := newLimitRunFixture(t, store.WorkerRoleCoder)
	f.intent("execution", sessions.RunIntentRunning, "")
	// swarm_stop_run: the executor records the run cancelled with its reason;
	// the job's cancel mark is not set.
	f.intent("execution", sessions.RunIntentCancelled, stoppedByCaller)

	stopped := f.observe()
	if stopped.Status != "failed" || stopped.CancelRequested || !store.WorkerRunStopped(stopped) || !strings.Contains(stopped.Error, stoppedByCaller) {
		t.Fatalf("a stopped run did not leave the job open: %+v", stopped)
	}
	if stopped.SessionID != f.run.SessionID || stopped.CompletedAt == 0 {
		t.Fatalf("stopped job lost its session or settlement: %+v", stopped)
	}
	runs, _, err := f.svc.Store().WorkerStore().ListWorkerRuns("account-a", f.worker.ID, 10, "")
	if err != nil || len(runs) != 1 {
		t.Fatalf("stopping created or lost a job: %d %v", len(runs), err)
	}

	// The follow-up a message starts is reserved and passes the run gate in
	// the job's own session.
	admitted, err := f.svc.Store().WorkerStore().ReserveWorkerFollowup("account-a", f.worker.ID, f.run.ID, "followup-1", 0)
	if err != nil || !admitted {
		t.Fatalf("follow-up of a stopped job not admitted: %v %v", admitted, err)
	}
	f.intent("followup-1", sessions.RunIntentPendingExecutor, "")
	intent, found, err := f.svc.GetSessionActiveRunIntent(f.run.SessionID)
	if err != nil || !found || intent.RunID != "followup-1" {
		t.Fatalf("follow-up intent = %+v %v %v", intent, found, err)
	}
	if !workerRunExecutable(f.current(), intent, true) {
		t.Fatalf("the run gate refused the stopped job's follow-up: %+v", f.current())
	}
	// While it is queued to start, another follow-up is refused, naming it.
	err = workerFollowupBusy(f.current(), intent)
	if !errors.Is(err, store.ErrWorkerConflict) || !strings.Contains(err.Error(), "a follow-up is already running in this job") || !strings.Contains(err.Error(), "followup-1") {
		t.Fatalf("busy refusal = %v, want it to name followup-1", err)
	}
}

func TestStopJobStillCancelsARunningAndAStoppedJob(t *testing.T) {
	// swarm_stop_job on a running job: the job is cancelled, as before.
	running := newLimitRunFixture(t, store.WorkerRoleCoder)
	running.intent("execution", sessions.RunIntentRunning, "")
	if _, err := running.execution.CancelRun(context.Background(), "account-a", running.worker.ID, running.run.ID); err != nil {
		t.Fatal(err)
	}
	cancelled := running.current()
	if cancelled.Status != "cancelled" || !cancelled.CancelRequested || store.WorkerRunStopped(cancelled) {
		t.Fatalf("stop_job did not cancel the running job: %+v", cancelled)
	}
	if _, err := running.svc.Store().WorkerStore().ReserveWorkerFollowup("account-a", running.worker.ID, running.run.ID, "followup-1", 0); !errors.Is(err, store.ErrWorkerConflict) {
		t.Fatalf("a cancelled job reserved a follow-up: %v", err)
	}

	// swarm_stop_job on a stopped job: it is closed for good.
	stopped := newLimitRunFixture(t, store.WorkerRoleCoder)
	stopped.intent("execution", sessions.RunIntentRunning, "")
	stopped.intent("execution", sessions.RunIntentCancelled, stoppedByCaller)
	if got := stopped.observe(); !store.WorkerRunStopped(got) {
		t.Fatalf("fixture job is not stopped: %+v", got)
	}
	closed, err := stopped.execution.CancelRun(context.Background(), "account-a", stopped.worker.ID, stopped.run.ID)
	if err != nil {
		t.Fatal(err)
	}
	if !closed.CancelRequested || store.WorkerRunStopped(closed) || stopped.current().CancelRequested != true {
		t.Fatalf("stop_job did not close the stopped job: %+v", closed)
	}
	if _, err := stopped.svc.Store().WorkerStore().ReserveWorkerFollowup("account-a", stopped.worker.ID, stopped.run.ID, "followup-1", 0); !errors.Is(err, store.ErrWorkerConflict) {
		t.Fatalf("a cancelled stopped job reserved a follow-up: %v", err)
	}
	// A second stop_job has nothing left to cancel.
	if _, err := stopped.execution.CancelRun(context.Background(), "account-a", stopped.worker.ID, stopped.run.ID); !errors.Is(err, store.ErrWorkerConflict) {
		t.Fatalf("second stop_job = %v, want a conflict", err)
	}
}

func TestStopRunStillCancelsPlanAndReviewerJobs(t *testing.T) {
	// A reviewer records its verdict and releases its checkout when it
	// settles; a plan-run job's paused plan is never resumed by a settled
	// receipt. Their stopped run still settles them cancelled.
	r := store.WorkerRunRecord{Role: store.WorkerRoleReviewer}
	if workerRunStopKeepsJobOpen(r) {
		t.Fatal("a reviewer job stays open after a stop")
	}
	if workerRunStopKeepsJobOpen(store.WorkerRunRecord{Role: store.WorkerRoleSwarm}) {
		t.Fatal("a plan-run job stays open after a stop")
	}
	if workerRunStopKeepsJobOpen(store.WorkerRunRecord{Role: store.WorkerRoleCoder, CancelRequested: true}) {
		t.Fatal("a cancelled job stays open")
	}
	for _, role := range []string{store.WorkerRoleCoder, store.WorkerRoleDesigner, store.WorkerRoleFinder} {
		if !workerRunStopKeepsJobOpen(store.WorkerRunRecord{Role: role}) {
			t.Fatalf("a %s job is cancelled by a stop", role)
		}
	}
}
