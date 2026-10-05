package environments

import "time"

// ManagedReviewRetention is the finite default review policy, not a lease TTL.
const ManagedReviewRetention = 24 * time.Hour

func (d Deployment) ReviewExpiresAt() int64 {
	if d.ReviewDeadline > 0 { return d.ReviewDeadline }
	if d.Build != nil && d.CreatedAt > 0 { return d.CreatedAt + ManagedReviewRetention.Milliseconds() }
	return 0
}

func (d Deployment) ReviewExpired(now int64) bool {
	deadline := d.ReviewExpiresAt()
	return deadline > 0 && now >= deadline
}
