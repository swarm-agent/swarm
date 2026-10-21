package run

import (
	"time"

	store "swarm/packages/swarmd/internal/store/pebble"
)

// restoreReviewWithBackoff persists the deadline before touching the checkout.
// Missing or broken admission checkouts must not hammer Git every sweep, even
// after restart. Keep recovery pending (not successful) until it really works.
func restoreReviewWithBackoff(r *store.WorkerRunRecord, now time.Time, save func(store.WorkerRunRecord) error, restore func() error) (bool, error) {
	if r.ReviewRestore != nil && r.ReviewRestore.NextAttemptAt > now.UnixMilli() {
		return false, nil
	}
	attempt := store.WorkerRunCleanup{}
	if r.ReviewRestore != nil {
		attempt = *r.ReviewRestore
	}
	attempt.Attempts++
	attempt.State = "pending"
	attempt.NextAttemptAt = now.Add(30 * time.Second << min(attempt.Attempts-1, 3)).UnixMilli()
	r.ReviewRestore = &attempt
	if err := save(*r); err != nil {
		return false, err
	}
	if err := restore(); err != nil {
		attempt.State, attempt.LastError = "failed", err.Error()
		if saveErr := save(*r); saveErr != nil {
			return false, saveErr
		}
		return false, err
	}
	r.ReviewRestore = nil
	if err := save(*r); err != nil {
		return false, err
	}
	return true, nil
}
