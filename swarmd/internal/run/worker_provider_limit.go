package run

import (
	"context"
	"errors"
	"strconv"
	"time"

	"swarm/packages/swarmd/internal/identity"
	"swarm/packages/swarmd/internal/providerlimit"
	"swarm/packages/swarmd/internal/sandbox"
	sessions "swarm/packages/swarmd/internal/session"
	store "swarm/packages/swarmd/internal/store/pebble"
)

// maxWorkerLimitWaits bounds how many times one worker run waits for its
// provider's limit to reset before it fails.
const maxWorkerLimitWaits = 12

// workerLimitRerunMessage resumes a job in its own session once the
// provider's limit that stopped it has reset.
const workerLimitRerunMessage = "The provider's usage limit that stopped this job has reset. Continue the job from where it stopped; the job's request is above."

// waitForProviderLimit records a run whose session run (runID) ended on the
// provider's usage, rate or session limit as waiting for the reset the
// provider states, else a bounded backoff, instead of failed. It reports
// whether the run now waits; when it does not, reason is the failure to
// record (Exhausted after the bounded number of waits). A run whose plan the
// executor owns (the swarm role), or whose cancel was requested, is not
// waited on.
func (s *WorkerExecutionService) waitForProviderLimit(ws *store.WorkerStore, current store.WorkerRunRecord, runID, reason string, now time.Time) (bool, string, error) {
	if current.CancelRequested || workerRunPlanID(current) != "" || !providerlimit.Is(reason) {
		return false, reason, nil
	}
	if runID == "" || runID != current.LimitRunID {
		if current.LimitWaits >= maxWorkerLimitWaits {
			return false, providerlimit.Exhausted(current.LimitWaits, reason), nil
		}
		current.LimitWaits++
		until, stated := providerlimit.ResetAt(reason, now)
		if !stated {
			until = now.Add(providerlimit.Backoff(current.LimitWaits))
		}
		current.RetryAt, current.LimitRunID = until.UnixMilli(), runID
		current.QueueReason = providerlimit.Waiting(until, stated, reason)
	} else if current.RetryAt == 0 {
		// The run was marked running for its rerun, but the rerun's message
		// was not recorded before a restart: the limit already reset.
		current.RetryAt = now.UnixMilli()
		current.QueueReason = "the provider's limit has reset; the job runs again"
	}
	current.Status, current.Error = store.WorkerRunStatusWaiting, ""
	if _, err := ws.RecordWorkerRun(current.AccountScopeID, current); err != nil {
		return false, reason, err
	}
	return true, "", nil
}

// limitRerunID is the session run ID of the n-th rerun after a limit.
func limitRerunID(r store.WorkerRunRecord) string {
	return r.ID + ".limit" + strconv.Itoa(r.LimitWaits)
}

// rerunAfterProviderLimit settles a waiting run: cancelled when its
// admission closed or its cancel was requested, unchanged until the reset
// (or while the worker's budget holds it), else running again in its own
// session through one canonical message and run intent. The receipt turns
// running before the message is recorded, so a restart in between finds the
// limited run's intent and resumes the rerun at once (waitForProviderLimit).
func (s *WorkerExecutionService) rerunAfterProviderLimit(ctx context.Context, ws *store.WorkerStore, w store.WorkerRecord, r store.WorkerRunRecord, budgetHeld bool, now time.Time) error {
	if r.CancelRequested || !workerRunAdmissionOpen(w, r) || !workerRunAutomationEnabled(w, r) {
		r.Status, r.CompletedAt, r.RetryAt = "cancelled", now.UnixMilli(), 0
		if err := s.recordReviewResult(r); err != nil {
			return err
		}
		markReviewCleanupPending(&r)
		if _, err := ws.RecordWorkerRun(r.AccountScopeID, r); err != nil {
			return err
		}
		if err := s.reconcileRunTask(r, "cancelled", "worker run cancelled while waiting for the provider's limit to reset"); err != nil {
			return err
		}
		return s.retryReviewCleanup(r)
	}
	if budgetHeld || now.UnixMilli() < r.RetryAt {
		return nil
	}
	if err := ctx.Err(); err != nil {
		return err
	}
	if err := s.host.runs.sessions.Store().CheckWorkerBudgetAdmission(r.AccountScopeID, r.WorkerID); err != nil {
		return err
	}
	if m := sandbox.Default(); m != nil {
		admitted, err := ws.ReserveWorkerCapacity(r, m.MaxWorkerRuns())
		if err != nil || !admitted {
			return err
		}
	} else {
		running := r
		running.Status, running.QueueReason, running.RetryAt = "running", "", 0
		if _, err := ws.RecordWorkerRun(r.AccountScopeID, running); err != nil {
			return err
		}
	}
	return s.startLimitRerun(r)
}

// startLimitRerun records the rerun's message and pending run intent in the
// job's session (idempotent per rerun) and wakes the executor.
func (s *WorkerExecutionService) startLimitRerun(r store.WorkerRunRecord) error {
	h := s.host
	runID := limitRerunID(r)
	intent, exists, err := h.runs.sessions.GetSessionRunIntent(r.SessionID, runID)
	if err != nil {
		return err
	}
	if !exists {
		now := time.Now().UnixMilli()
		message := store.MessageSnapshot{ID: deterministicDeployID(runID, r.SessionID, "worker-limit-rerun"), SessionID: r.SessionID, UserID: r.UserID, AccountScopeID: r.AccountScopeID, Role: "user", Content: workerLimitRerunMessage, Metadata: map[string]any{"source": "worker_job_limit_rerun", "worker_run_id": r.ID}, CreatedAt: now}
		pending := sessionDeployRunIntent(r.SessionID, runID, r.SessionID, r.UserID, r.AccountScopeID)
		key := "worker-limit-rerun:" + runID
		appended, err := h.apply(sessions.SessionMutationInput{SessionID: r.SessionID, UserID: r.UserID, AccountScopeID: r.AccountScopeID, ClientRequestID: key, IdempotencyKey: key, PayloadHash: key, RequestHash: key, Kind: sessions.SessionMutationAppendMessage, Message: &message, RunIntent: &pending, NowUnixMs: now})
		if err != nil {
			return err
		}
		if appended.RunIntent == nil {
			return errors.New("worker job rerun did not persist a run intent")
		}
		intent = *appended.RunIntent
	}
	if intent.SessionID != r.SessionID || intent.RunID != runID || intent.PlanID != "" {
		return store.ErrWorkerConflict
	}
	if intent.Status == sessions.RunIntentPendingExecutor {
		p := identity.Principal{Type: identity.PrincipalTypeUser, UserID: r.UserID, AccountScopeID: r.AccountScopeID, AccountScopeSource: identity.AccountScopeSourceServerState}
		if !h.enqueue(p, intent) {
			return errors.New("worker executor wake unavailable; rerun intent retained")
		}
	}
	return nil
}
