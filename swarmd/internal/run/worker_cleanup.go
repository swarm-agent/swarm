package run

import (
	"log"
	"strings"
	store "swarm/packages/swarmd/internal/store/pebble"
	"time"
)

func markReviewCleanupPending(r *store.WorkerRunRecord) {
	if r.Role == store.WorkerRoleReviewer && r.Cleanup == nil {
		r.Cleanup = &store.WorkerRunCleanup{State: "pending"}
	}
}

func (s *WorkerExecutionService) retryReviewCleanup(r store.WorkerRunRecord) error {
	if r.Role != store.WorkerRoleReviewer || !store.AutomationV2Terminal(r.Status) || strings.HasPrefix(r.Error, "run stalled:") {
		return nil
	}
	s.cleanupMu.Lock()
	defer s.cleanupMu.Unlock()
	ws, err := s.workerStore()
	if err != nil {
		return err
	}
	current, found, err := ws.GetWorkerRun(r.AccountScopeID, r.WorkerID, r.ID)
	if err != nil {
		return err
	}
	if !found || !store.AutomationV2Terminal(current.Status) {
		return nil
	}
	r = current
	markReviewCleanupPending(&r)
	c := *r.Cleanup
	now := time.Now()
	if c.State == "done" || c.NextAttemptAt > now.UnixMilli() {
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
		if c.Attempts >= 5 {
			c.FailureReported = true
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
