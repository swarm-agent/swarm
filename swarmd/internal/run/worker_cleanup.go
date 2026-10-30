package run

import (
	"log"
	"strings"
	store "swarm/packages/swarmd/internal/store/pebble"
	"time"
)

// reviewCleanupMaxAttempts bounds a terminal reviewer run's checkout cleanup:
// after this many failed attempts the failure is reported once
// (job.cleanup_failed) and the run is never retried again by itself.
const reviewCleanupMaxAttempts = 5

func markReviewCleanupPending(r *store.WorkerRunRecord) {
	if r.Role == store.WorkerRoleReviewer && !reviewFixesAllowed(r.Input) && r.Cleanup == nil {
		r.Cleanup = &store.WorkerRunCleanup{State: "pending"}
	}
}

func (s *WorkerExecutionService) retryReviewCleanup(r store.WorkerRunRecord) error {
	// A reviewer that ran with fixes keeps its checkout (a coder-style worktree).
	if r.Role != store.WorkerRoleReviewer || reviewFixesAllowed(r.Input) || !store.AutomationV2Terminal(r.Status) || strings.HasPrefix(r.Error, "run stalled:") {
		return nil
	}
	s.cleanupMu.Lock()
	if s.cleanupClosed {
		s.cleanupMu.Unlock()
		return nil
	}
	if s.cleanupQueue == nil {
		s.cleanupQueue = make(map[string]store.WorkerRunRecord)
	}
	s.cleanupQueue[r.AccountScopeID+":"+r.ID] = r
	if !s.cleanupRunning {
		s.cleanupRunning = true
		s.cleanupWG.Add(1)
		go s.drainReviewCleanup()
	}
	s.cleanupMu.Unlock()
	return nil
}

// CloseCleanup must run before closing the backing store.
func (s *WorkerExecutionService) CloseCleanup() {
	s.cleanupMu.Lock()
	s.cleanupClosed = true
	s.cleanupMu.Unlock()
	s.cleanupWG.Wait()
}

func (s *WorkerExecutionService) drainReviewCleanup() {
	s.drainReviewCleanupWith(s.performReviewCleanup)
}

func (s *WorkerExecutionService) drainReviewCleanupWith(clean func(store.WorkerRunRecord) error) {
	defer s.cleanupWG.Done()
	for {
		s.cleanupMu.Lock()
		var next store.WorkerRunRecord
		for key, r := range s.cleanupQueue {
			next = r
			delete(s.cleanupQueue, key)
			break
		}
		if next.ID == "" {
			s.cleanupRunning = false
			s.cleanupMu.Unlock()
			return
		}
		s.cleanupMu.Unlock()
		if err := clean(next); err != nil {
			log.Printf("record review cleanup: %v", err)
		}
	}
}

func (s *WorkerExecutionService) performReviewCleanup(r store.WorkerRunRecord) error {
	ws, err := s.workerStore()
	if err != nil {
		return err
	}
	current, found, err := ws.GetWorkerRun(r.AccountScopeID, r.WorkerID, r.ID)
	if err != nil {
		return err
	}
	if !found || !store.AutomationV2Terminal(current.Status) || reviewFixesAllowed(current.Input) {
		return nil
	}
	r = current
	markReviewCleanupPending(&r)
	if r.Cleanup == nil {
		return nil
	}
	c := *r.Cleanup
	now := time.Now()
	if c.State == "done" || c.NextAttemptAt > now.UnixMilli() {
		return nil
	}
	if c.State == "failed" && (c.FailureReported || c.Attempts >= reviewCleanupMaxAttempts) {
		// Exhausted: the one warning was raised when the last attempt failed.
		// Re-saving the run every backoff period forever re-announced the old
		// failed run to every activity reader and retried with no end.
		return nil
	}
	previous := c.State
	c.State = "pending"
	c.Attempts++
	c.NextAttemptAt = now.Add(time.Minute << min(c.Attempts-1, 6)).UnixMilli()
	r.Cleanup = &c
	// Persist the attempt and retry deadline before any filesystem side effect.
	if _, err := ws.RecordWorkerRun(r.AccountScopeID, r); err != nil {
		return err
	}
	cleanupErr := s.cleanupReview(r)
	if cleanupErr != nil {
		c.State, c.LastError = "failed", cleanupErr.Error()
		if c.Attempts >= reviewCleanupMaxAttempts {
			c.FailureReported, c.NextAttemptAt = true, 0
		}
	} else {
		c.State, c.LastError, c.NextAttemptAt = "done", "", 0
	}
	if _, err := ws.RecordWorkerRun(r.AccountScopeID, r); err != nil {
		return err
	}
	if previous != c.State && cleanupErr != nil {
		log.Printf("review cleanup failed for run %s: %v", r.ID, cleanupErr)
	}
	return nil // Filesystem failure is durable job state, not a sweep failure.
}
