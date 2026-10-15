package run

import (
	"context"
	"errors"
	"fmt"
	"swarm/packages/swarmd/internal/sandbox"
	"time"

	store "swarm/packages/swarmd/internal/store/pebble"
)

// SweepReviewAdmissions runs before admission resumes at daemon startup. Durable
// reviewer receipts, not directory names, establish ownership of legacy lanes.
func (s *WorkerExecutionService) SweepReviewAdmissions(ctx context.Context) error {
	s.dispatchMu.Lock()
	defer s.dispatchMu.Unlock()
	ws, err := s.workerStore()
	if err != nil {
		return err
	}
	rows, err := ws.ReviewAdmissionRecoveryRuns()
	if err != nil {
		return err
	}
	var failures []error
	for _, r := range rows {
		if err := ctx.Err(); err != nil {
			return errors.Join(err, errors.Join(failures...))
		}
		if !store.AutomationV2Terminal(r.Status) {
			continue
		}
		if err := s.cleanupReviewAdmissionMode(r, true); err != nil {
			failures = append(failures, fmt.Errorf("review admission cleanup: %w", err))
		}
	}
	return errors.Join(failures...)
}

func (s *WorkerExecutionService) cleanupReviewAdmission(r store.WorkerRunRecord) error {
	return s.cleanupReviewAdmissionMode(r, false)
}

func (s *WorkerExecutionService) cleanupReviewAdmissionMode(r store.WorkerRunRecord, startup bool) error {
	cleanup := s.host.trees.CleanupReviewAdmission
	if startup {
		cleanup = s.host.trees.RecoverReviewAdmission
	}
	if r.Role != store.WorkerRoleReviewer {
		return nil
	}
	snapshot, found, err := s.host.runs.sessions.GetSession(r.SessionID)
	if err != nil {
		return err
	}
	if found {
		if snapshot.AccountScopeID != r.AccountScopeID || snapshot.Metadata["worker_execution_run_id"] != r.ID {
			return store.ErrWorkerConflict
		}
		// A session is persisted before admission. Without a run intent there
		// cannot have been provider execution; do not require successful Git
		// identity validation of an interrupted checkout to discard it.
		_, admitted, err := s.host.runs.sessions.GetSessionRunIntent(r.SessionID, r.ID)
		if err != nil {
			return err
		}
		if admitted {
			if err := s.cleanupReview(r); err != nil {
				return err
			}
		}
		remove := func() error { return cleanup(snapshot.WorkspacePath, r.SessionID) }
		state, registered, err := s.host.runs.sessions.Store().GetReviewCleanup(snapshot.AccountScopeID, snapshot.ID)
		if err != nil {
			return err
		}
		if registered {
			m := sandbox.Default()
			if m == nil {
				return sandbox.ErrSandboxRequired
			}
			ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
			defer cancel()
			return m.CleanupReviewDurable(ctx, &state, func(next sandbox.ReviewCleanupState) error {
				return s.host.runs.sessions.Store().PutReviewCleanup(snapshot.AccountScopeID, next)
			}, func() error { return nil }, remove)
		}
		return remove()
	}
	// No session means no execution or review sandbox could have started.
	entry, err := s.activeWorkspace(r.AccountScopeID, r.UserID, r.WorkspaceID)
	if err != nil {
		return err
	}
	return cleanup(entry.Path, r.SessionID)
}
