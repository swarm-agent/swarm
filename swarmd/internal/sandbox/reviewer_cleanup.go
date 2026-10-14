package sandbox

import "context"

// CleanupReview is a daemon-only terminal operation. Removal runs while agent
// registration remains restrictive; failure leaves it intact for retry.
func (m *Manager) CleanupReview(ctx context.Context, checkout string, remove func() error) error {
	if err := m.EndReview(ctx, checkout); err != nil {
		return err
	}
	if err := remove(); err != nil {
		return err
	}
	m.ForgetReview(checkout)
	return nil
}
