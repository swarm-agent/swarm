package run

import (
	"context"
	"errors"
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

func (s *WorkerExecutionService) trackExecution(sessionID string, cancel context.CancelFunc) func() {
	a := &workerExecutionActivity{last: time.Now(), cancel: cancel}
	s.activityMu.Lock()
	s.activity.Store(sessionID, a)
	s.activityMu.Unlock()
	return func() { s.activityMu.Lock(); defer s.activityMu.Unlock(); s.activity.CompareAndDelete(sessionID, a) }
}
func (s *WorkerExecutionService) touchExecution(sessionID string) {
	if value, ok := s.activity.Load(sessionID); ok {
		a := value.(*workerExecutionActivity)
		a.mu.Lock()
		a.last = time.Now()
		a.mu.Unlock()
	}
}

// TrackV3Execution owns the whole turn, including quiet providers and tools,
// until the executor has persisted its terminal outcome.
func (s *WorkerExecutionService) TrackV3Execution(sessionID string, cancel context.CancelFunc) func() {
	return s.trackExecution(sessionID, cancel)
}
func (s *WorkerExecutionService) RunActive(sessionID string) bool {
	_, ok := s.activity.Load(sessionID)
	return ok
}
func (s *WorkerExecutionService) SetStallTimeout(timeout time.Duration) {
	s.stallTimeout.Store(int64(timeout))
}

func (s *WorkerExecutionService) watchdogRun(r store.WorkerRunRecord, now time.Time) (bool, error) {
	// Serialize ownership changes with inspection and settlement. Session progress
	// is independently fenced by the canonical projection sequence below.
	s.activityMu.Lock()
	defer s.activityMu.Unlock()
	if r.Status != "running" || r.CancelRequested || s.RunActive(r.SessionID) {
		return false, nil
	}
	projection, found, err := s.host.runs.sessions.GetSessionProjection(r.SessionID)
	if err != nil || !found {
		return false, err
	}
	snapshot, found, err := s.host.runs.sessions.GetSession(r.SessionID)
	if err != nil || !found {
		return false, err
	}
	if snapshot.AccountScopeID != r.AccountScopeID || snapshot.Metadata["worker_execution_run_id"] != r.ID {
		return false, store.ErrWorkerConflict
	}
	// Pending admission and running intents belong to executor recovery. This
	// remains true before its startup scan completes; age does not revoke ownership.
	_, exists, err := s.host.runs.sessions.GetSessionActiveRunIntent(r.SessionID)
	if err != nil || exists {
		return false, err
	}
	terminal, exists, err := s.workerRunIntent(r)
	if err != nil {
		return false, err
	}
	if exists && terminal.Status != sessions.RunIntentRunning && terminal.Status != sessions.RunIntentPendingExecutor && terminal.Status != "waiting_tasks" {
		return false, nil
	}
	reason := "run stalled: execution lost"
	timeout := time.Duration(s.stallTimeout.Load())
	if timeout <= 0 {
		timeout = 15 * time.Minute
	}
	idle := now.Sub(time.UnixMilli(snapshot.UpdatedAt))
	if idle >= timeout {
		reason = fmt.Sprintf("run stalled: no progress for %.0f min / execution lost", idle.Minutes())
	}
	ws, err := s.workerStore()
	if err != nil {
		return false, err
	}
	if s.watchdogBeforeSettleForTest != nil {
		s.watchdogBeforeSettleForTest()
	}
	r.Status, r.Error, r.CompletedAt = "failed", reason, now.UnixMilli()
	_, err = ws.RecordWorkerRunIfRunning(r.AccountScopeID, r, func() error {
		lifecycle, exists, err := s.host.runs.sessions.GetLifecycle(r.SessionID)
		if err != nil {
			return err
		}
		if !exists {
			lifecycle = store.SessionLifecycleSnapshot{SessionID: r.SessionID, UserID: r.UserID, AccountScopeID: r.AccountScopeID}
		}
		lifecycle.Active = false
		lifecycle.Phase = lifecyclePhaseErrored
		lifecycle.Error = reason
		lifecycle.EndedAt = now.UnixMilli()
		lifecycle.UpdatedAt = now.UnixMilli()
		key := "worker-watchdog-lifecycle:" + r.ID
		_, err = s.host.apply(sessions.SessionMutationInput{SessionID: r.SessionID, UserID: r.UserID, AccountScopeID: r.AccountScopeID, Kind: sessions.SessionMutationUpsertLifecycle, ExpectedLastEventSeq: &projection.LastEventSeq, Lifecycle: &lifecycle, EventType: "session.lifecycle.updated", ClientRequestID: key, IdempotencyKey: key, PayloadHash: key, RequestHash: key, NowUnixMs: now.UnixMilli()})
		return err
	})
	var conflict *store.V3ProjectionConflictError
	if errors.Is(err, store.ErrWorkerConflict) || errors.As(err, &conflict) {
		return false, nil
	}
	if err != nil {
		return false, err
	}
	// Preserve work and restrictive review registration for follow-up.
	return true, s.reconcileRunTask(r, r.Status, reason)
}
