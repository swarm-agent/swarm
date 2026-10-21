package runtime

import (
	"context"
	"errors"
	"log"
	"swarm/packages/swarmd/internal/sandbox"
	pebblestore "swarm/packages/swarmd/internal/store/pebble"
)

// terminalSandboxEnder is the sandbox side of the terminal sweep
// (*sandbox.Manager in the daemon).
type terminalSandboxEnder interface {
	EndTerminalReview(ctx context.Context, root, checkout string) error
	EndJob(ctx context.Context, checkout string, terminal func() (bool, error)) error
}

// terminalSandboxIsReview reports whether a terminal run's container is a
// read-only review container. A reviewer an automation step allowed to commit
// security fixes (input review_fixes "security", admitted only from an
// automation) runs in an unregistered job-checkout scope, so its container is
// a job container and is ended like a coder's.
func terminalSandboxIsReview(r pebblestore.WorkerRunRecord) bool {
	if r.Role != pebblestore.WorkerRoleReviewer {
		return false
	}
	fixes, _ := r.Input["review_fixes"].(string)
	return fixes != "security"
}

// Sweep terminal receipts independently of reviewer checkout cleanup. Each retry
// rechecks both receipt and intent; it never stops an executing follow-up.
func cleanupTerminalSandboxes(ctx context.Context, m terminalSandboxEnder, db *pebblestore.Store) {
	ws := pebblestore.NewWorkerStore(db)
	runs, err := ws.WorkerSandboxRuns()
	if err != nil {
		log.Printf("enumerate sandbox cleanup: %v", err)
		return
	}
	ss := pebblestore.NewSessionStore(db)
	for _, r := range runs {
		if ctx.Err() != nil {
			return
		}
		if !pebblestore.AutomationV2Terminal(r.Status) || r.FollowupActive() {
			continue
		}
		snap, found, err := ss.GetSession(r.SessionID)
		if err != nil || !found || snap.WorktreeRootPath == "" {
			continue
		}
		terminal := func() (bool, error) {
			current, found, err := ws.GetWorkerRun(r.AccountScopeID, r.WorkerID, r.ID)
			if err != nil || !found {
				return false, err
			}
			if !pebblestore.AutomationV2Terminal(current.Status) || current.FollowupActive() {
				return false, nil
			}
			_, active, err := ss.GetV3SessionActiveRunIntent(r.SessionID)
			return !active, err
		}
		done, err := terminal()
		if err == nil && !done {
			continue
		}
		if err == nil && done {
			if terminalSandboxIsReview(r) {
				err = m.EndTerminalReview(ctx, snap.WorkspacePath, snap.WorktreeRootPath)
			} else {
				err = m.EndJob(ctx, snap.WorktreeRootPath, terminal)
			}
		}
		// A deferral confirms nothing either way: the next sweep retries it.
		if errors.Is(err, sandbox.ErrCleanupDeferred) {
			continue
		}
		message := ""
		if err != nil {
			message = err.Error()
		}
		if err := ws.SetSandboxCleanupError(r, message); err != nil {
			log.Printf("record sandbox cleanup: %v", err)
		}
	}
}
