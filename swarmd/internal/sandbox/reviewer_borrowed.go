package sandbox

import (
	"fmt"
	"sync"
)

// borrowedReviews holds the review checkouts that are another session's
// worktree, read in place by one reviewer session: checkout -> that
// session. A borrowed checkout is mounted read-only (not only its Git
// metadata), only its reviewer session runs in it, and releasing it leaves
// nothing behind that would refuse its owner's own commands.
var borrowedReviews sync.Map

// RegisterBorrowedReviewCheckout is RegisterReviewCheckout for a reviewer
// session that reads another session's worktree in place. It is
// daemon-owned, never an agent tool, and refuses a worktree another
// reviewer session is reading.
func (m *Manager) RegisterBorrowedReviewCheckout(checkout, session string) error {
	if session == "" {
		return fmt.Errorf("borrowed review checkout requires its reviewer session")
	}
	checkout = resolveExisting(checkout)
	m.mu.Lock()
	_, registered := m.reviews[checkout]
	m.mu.Unlock()
	if current, ok := borrowedReviews.Load(checkout); ok && current != session {
		return fmt.Errorf("the worktree is being reviewed by another session")
	} else if !ok && registered {
		return fmt.Errorf("the worktree is registered as a disposable review checkout")
	}
	if err := m.RegisterReviewCheckout(checkout); err != nil {
		return err
	}
	borrowedReviews.Store(checkout, session)
	if !registered {
		// An earlier reviewer's closed registration of this worktree is over:
		// its session stays bound to it as a deny identity (enforced below).
		m.mu.Lock()
		delete(m.reviewClosing, checkout)
		m.mu.Unlock()
	}
	return nil
}

// borrowedReviewSession is the reviewer session a borrowed review checkout
// belongs to ("" for a disposable review checkout).
func borrowedReviewSession(checkout string) string {
	value, ok := borrowedReviews.Load(checkout)
	if !ok {
		return ""
	}
	return value.(string)
}

// forgetBorrowedReview releases a borrowed checkout: with its registration
// gone, the owner's commands in it run in the owner's own scope again.
func (m *Manager) forgetBorrowedReview(checkout string) {
	if _, ok := borrowedReviews.LoadAndDelete(checkout); !ok {
		return
	}
	m.mu.Lock()
	delete(m.reviewClosing, checkout)
	m.mu.Unlock()
}

// OwnerTurnedOff reports whether this machine's owner turned the agent
// sandbox off (mode off, or no sandbox configured): agents then run on the
// machine, and a reviewer runs like them, without a review sandbox. A
// sandbox that is on but unavailable is not off: reviewers still refuse.
func OwnerTurnedOff() bool {
	return Default().Status().Mode == ModeOff
}
