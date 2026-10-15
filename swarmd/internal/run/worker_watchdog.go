package run

import (
	"context"
	"fmt"
	"sync"
	"time"

	sessions "swarm/packages/swarmd/internal/session"
	store "swarm/packages/swarmd/internal/store/pebble"
)

type workerExecutionActivity struct {
	mu     sync.Mutex
	last   time.Time
	cancel context.CancelFunc
}

// Track the actual turn, including V3 turns which do not claim activeRuns.
func (s *WorkerExecutionService) trackExecution(sessionID string, cancel context.CancelFunc) func() {
	a := &workerExecutionActivity{last: time.Now(), cancel: cancel}
	s.activity.Store(sessionID, a)
	return func() { s.activity.CompareAndDelete(sessionID, a) }
}
func (s *WorkerExecutionService) touchExecution(sessionID string) {
	if value, ok := s.activity.Load(sessionID); ok {
		a := value.(*workerExecutionActivity)
		a.mu.Lock()
		a.last = time.Now()
		a.mu.Unlock()
	}
}

// RunActive is live execution evidence, not a durable running receipt.
func (s *WorkerExecutionService) RunActive(sessionID string) bool {
	_, ok := s.activity.Load(sessionID)
	return ok
}

// SetStallTimeout configures the no-progress deadline; nonpositive values restore
// the default. The scheduler invokes the watchdog at startup and on each sweep.
func (s *WorkerExecutionService) SetStallTimeout(timeout time.Duration) {
	s.stallTimeout.Store(int64(timeout))
}

func (s *WorkerExecutionService) watchdogRun(r store.WorkerRunRecord, now time.Time) (bool, error) {
	if r.Status != "running" || r.CancelRequested {
		return false, nil
	}
	snapshot, found, err := s.host.runs.sessions.GetSession(r.SessionID)
	if err != nil {
		return false, err
	}
	if !found {
		return false, nil
	}
	if snapshot.AccountScopeID != r.AccountScopeID || snapshot.Metadata["worker_execution_run_id"] != r.ID {
		return false, store.ErrWorkerConflict
	}
	timeout := time.Duration(s.stallTimeout.Load())
	if timeout <= 0 {
		timeout = 15 * time.Minute
	}
	last := time.UnixMilli(snapshot.UpdatedAt)
	reason := ""
	if value, ok := s.activity.Load(r.SessionID); ok {
		a := value.(*workerExecutionActivity)
		a.mu.Lock()
		if a.last.After(last) {
			last = a.last
		}
		if now.Sub(last) >= timeout {
			reason = fmt.Sprintf("run stalled: no progress for %.0f min", now.Sub(last).Minutes())
			a.cancel()
		}
		a.mu.Unlock()
	} else {
		intent, exists, err := s.host.runs.sessions.GetSessionActiveRunIntent(r.SessionID)
		if err != nil {
			return false, err
		}
		// Allow the executor's claim/return-to-terminal-mutation window. A
		// durable running intent alone is not proof of a live provider.
		if exists && intent.Status == sessions.RunIntentRunning && now.Sub(last) < min(timeout, 30*time.Second) {
			return false, nil
		}
		// Queued admission and checkpoint handoffs are not missing executions.
		if exists && intent.Status == sessions.RunIntentPendingExecutor && now.Sub(last) < timeout {
			return false, nil
		}
		if exists && intent.Status == "waiting_tasks" && now.Sub(last) < timeout {
			return false, nil
		}
		// Let ordinary terminal observation record a completed provider result.
		terminal, exists, err := s.workerRunIntent(r)
		if err != nil {
			return false, err
		}
		if exists && terminal.Status != sessions.RunIntentRunning && terminal.Status != sessions.RunIntentPendingExecutor && terminal.Status != "waiting_tasks" {
			return false, nil
		}
		reason = "run stalled: execution lost"
	}
	if reason == "" {
		return false, nil
	}
	intent, exists, err := s.host.runs.sessions.GetSessionActiveRunIntent(r.SessionID)
	if err != nil {
		return false, err
	}
	if exists {
		intent.Status = sessions.RunIntentFailed
		key := "worker-watchdog:" + r.ID + ":" + intent.RunID
		if _, err := s.host.apply(sessions.SessionMutationInput{SessionID: r.SessionID, UserID: r.UserID, AccountScopeID: r.AccountScopeID, Kind: sessions.SessionMutationRecordRunIntent, RunIntent: &intent, EventType: "session.run_intent.updated", ClientRequestID: key, IdempotencyKey: key, PayloadHash: key, RequestHash: key, NowUnixMs: now.UnixMilli()}); err != nil {
			return false, err
		}
	}
	lifecycle, exists, err := s.host.runs.sessions.GetLifecycle(r.SessionID)
	if err != nil {
		return false, err
	}
	if exists {
		lifecycle.Active = false
		lifecycle.Phase = lifecyclePhaseErrored
		lifecycle.Error = reason
		lifecycle.EndedAt = now.UnixMilli()
		lifecycle.UpdatedAt = now.UnixMilli()
		key := "worker-watchdog-lifecycle:" + r.ID
		if _, err := s.host.apply(sessions.SessionMutationInput{SessionID: r.SessionID, UserID: r.UserID, AccountScopeID: r.AccountScopeID, Kind: sessions.SessionMutationUpsertLifecycle, Lifecycle: &lifecycle, EventType: "session.lifecycle.updated", ClientRequestID: key, IdempotencyKey: key, PayloadHash: key, RequestHash: key, NowUnixMs: now.UnixMilli()}); err != nil {
			return false, err
		}
	}
	// Deliberately do not cleanupReview: failed work remains available for follow-up.
	r.Status, r.Error, r.CompletedAt = "failed", reason, now.UnixMilli()
	ws, err := s.workerStore()
	if err != nil {
		return false, err
	}
	if _, err := ws.RecordWorkerRun(r.AccountScopeID, r); err != nil {
		return false, err
	}
	return true, s.reconcileRunTask(r, r.Status, reason)
}
