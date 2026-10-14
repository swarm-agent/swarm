package sandbox

import "context"

// CleanupReview retains restrictive registration on any terminal failure.
func (m *Manager) CleanupReview(ctx context.Context, checkout string, remove func() error) error {
	return m.CleanupReviewValidated(ctx, checkout, func() error { return nil }, remove)
}

// CleanupReviewValidated validates while Git can still use the review scope,
// then permanently blocks acquisition before shutdown/removal. Retry skips
// checkout Git: the lane was already validated and cannot execute new commands.
func (m *Manager) CleanupReviewValidated(ctx context.Context, checkout string, validate, remove func() error) error {
	m.mu.Lock()
	closing := m.reviewClosing[checkout]
	root := m.reviews[checkout]
	m.mu.Unlock()
	if !closing {
		if err := validate(); err != nil {
			return err
		}
	}
	if err := m.EndReview(ctx, checkout); err != nil {
		return err
	}
	if err := remove(); err != nil {
		return err
	}
	if m.runner != nil {
		name := scopeContainerName(Scope{Root: root, ReviewCheckout: checkout})
		// Inspect first to make missing volumes (including retries) idempotent.
		out, err := m.runner.Run(ctx, m.cfg.Engine, "volume", "ls", "--filter", "name=^"+name+"-home$", "--format", "{{.Name}}")
		if err != nil {
			return err
		}
		if len(out) > 0 {
			if _, err := m.runner.Run(ctx, m.cfg.Engine, "volume", "rm", name+"-home"); err != nil {
				return err
			}
		}
	}
	m.ForgetReview(checkout)
	return nil
}
