package sandbox

import (
	"context"
	"fmt"
	"path/filepath"
)

// ReviewCleanupState is daemon-owned persisted identity, never agent input.
type ReviewCleanupState struct {
	Role      string `json:"role"`
	Session   string `json:"session"`
	Checkout  string `json:"checkout"`
	Root      string `json:"root"`
	Container string `json:"container"`
	Volume    string `json:"volume"`
	Phase     string `json:"phase"`
	// Borrowed: the checkout is another session's worktree read in place
	// (RegisterBorrowedReviewCheckout); cleanup never removes it.
	Borrowed bool `json:"borrowed,omitempty"`
}

func (m *Manager) CaptureReviewIdentity(session, checkout string) (ReviewCleanupState, error) {
	m.mu.Lock()
	root := m.reviews[checkout]
	m.mu.Unlock()
	if root == "" {
		return ReviewCleanupState{}, fmt.Errorf("review identity missing")
	}
	name := scopeContainerName(Scope{Root: root, ReviewCheckout: checkout})
	return ReviewCleanupState{"reviewer", session, checkout, root, name, name + "-home", "active", borrowedReviewSession(checkout) == session}, nil
}

// RestoreReviewIdentity does not inspect the checkout: it can already be gone.
func (m *Manager) RestoreReviewIdentity(s ReviewCleanupState) error {
	if s.Role != "reviewer" || s.Session == "" || !filepath.IsAbs(s.Root) || !filepath.IsAbs(s.Checkout) || s.Root == s.Checkout || s.Container != scopeContainerName(Scope{Root: s.Root, ReviewCheckout: s.Checkout}) || s.Volume != s.Container+"-home" {
		return fmt.Errorf("invalid durable review identity")
	}
	switch s.Phase {
	case "volume_removed":
		return nil // A completed identity must never reserve the checkout again.
	case "active", "closing", "container_removed", "checkout_removed":
	default:
		return fmt.Errorf("invalid review cleanup phase")
	}
	m.mu.Lock()
	if m.reviews == nil {
		m.reviews = map[string]string{}
	}
	if m.reviewClosing == nil {
		m.reviewClosing = map[string]bool{}
	}
	if s.Borrowed {
		if current := borrowedReviewSession(s.Checkout); current != "" && current != s.Session {
			m.mu.Unlock()
			return fmt.Errorf("the worktree is being reviewed by another session")
		}
		borrowedReviews.Store(s.Checkout, s.Session)
	}
	m.reviews[s.Checkout] = s.Root
	if s.Phase != "active" {
		m.reviewClosing[s.Checkout] = true
	}
	m.mu.Unlock()
	return m.BindReviewSession(s.Session, s.Checkout)
}

// CleanupReviewDurable persists each phase after its idempotent side effect.
// A crash before a write repeats the effect, never validation in a closing lane.
func (m *Manager) CleanupReviewDurable(ctx context.Context, s *ReviewCleanupState, save func(ReviewCleanupState) error, validate, remove func() error) error {
	if err := m.RestoreReviewIdentity(*s); err != nil {
		return err
	}
	if s.Phase == "volume_removed" {
		return nil
	}
	advance := func(phase string) error {
		next := *s
		next.Phase = phase
		if err := save(next); err != nil {
			return err
		}
		*s = next
		return nil
	}
	if s.Phase == "active" {
		if err := validate(); err != nil {
			return err
		}
		if err := advance("closing"); err != nil {
			return err
		}
		if err := m.RestoreReviewIdentity(*s); err != nil {
			return err
		}
	}
	if s.Phase == "closing" {
		if err := m.EndReview(ctx, s.Checkout); err != nil {
			return err
		}
		if err := advance("container_removed"); err != nil {
			return err
		}
	}
	if s.Phase == "container_removed" {
		if err := remove(); err != nil {
			return err
		}
		if err := advance("checkout_removed"); err != nil {
			return err
		}
	}
	if s.Phase == "checkout_removed" {
		if m.runner != nil {
			out, err := m.runner.Run(ctx, m.cfg.Engine, "volume", "ls", "--filter", "name=^"+s.Volume+"$", "--format", "{{.Name}}")
			if err != nil {
				return err
			}
			if len(out) > 0 {
				if _, err := m.runner.Run(ctx, m.cfg.Engine, "volume", "rm", s.Volume); err != nil {
					return err
				}
			}
		}
		if err := advance("volume_removed"); err != nil {
			return err
		}
	}
	m.ForgetReview(s.Checkout)
	return nil
}
