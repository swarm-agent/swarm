package runtime

import (
	"context"
	"log"
	"swarm/packages/swarmd/internal/sandbox"
	pebblestore "swarm/packages/swarmd/internal/store/pebble"
)

// Sweep terminal receipts independently of reviewer checkout cleanup. Each retry
// rechecks both receipt and intent; it never stops an executing follow-up.
func cleanupTerminalSandboxes(ctx context.Context, m *sandbox.Manager, db *pebblestore.Store) {
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
		if err == nil && done {
			if r.Role == pebblestore.WorkerRoleReviewer {
				err = m.EndTerminalReview(ctx, snap.WorkspacePath, snap.WorktreeRootPath)
			} else {
				err = m.EndJob(ctx, snap.WorktreeRootPath, terminal)
			}
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
