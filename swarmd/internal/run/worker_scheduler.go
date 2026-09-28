package run

import (
	"context"
	"errors"
	"fmt"
	"time"

	store "swarm/packages/swarmd/internal/store/pebble"
)

// TickWorker admits at most one due slot per automation. Worker admission owns
// deduplication; missed slots are skipped rather than replayed after restart.
func (s *WorkerExecutionService) TickWorker(ctx context.Context, account, id string, now time.Time) error {
	ws, err := s.workerStore()
	if err != nil {
		return err
	}
	w, found, err := ws.GetWorker(account, id)
	if err != nil {
		return err
	}
	if !found {
		return store.ErrWorkerNotFound
	}
	if w.LifecycleState != store.WorkerLifecycleStateActive {
		return nil
	}
	// The first sweep sets a process-local cursor; restart never catches up
	// slots that fell due while this process was offline.
	s.scheduleMu.Lock()
	after := s.lastScheduled
	s.scheduleMu.Unlock()
	if after.IsZero() || !now.After(after) {
		return nil
	}
	var failures []error
	owner, err := s.scheduleOwner(w)
	if err != nil {
		return err
	}
	for _, a := range w.Automations {
		if !a.Enabled || a.Schedule == nil || (a.ActivationMode != "interval" && a.ActivationMode != "cron") {
			continue
		}
		if err = ctx.Err(); err != nil {
			return err
		}
		next, err := store.AutomationV2NextDue(store.AutomationV2Settings{SchemaVersion: 2, Schedule: *a.Schedule, Missed: "skip", Overlap: "independent", ActivateOnAccept: true}, a.CreatedAt, after.UnixMilli())
		if err != nil {
			failures = append(failures, err)
			continue
		}
		if next > now.UnixMilli() || next <= 0 {
			continue
		}
		occurrence := fmt.Sprintf("wocc_%s_%d_%d", a.ID, a.Revision, next)
		_, err = s.Dispatch(ctx, account, owner, store.WorkerRunAdmission{WorkerID: id, AutomationID: a.ID, OccurrenceID: occurrence, RequestSource: "schedule", Input: map[string]any{"due_at": next}})
		if err != nil {
			failures = append(failures, err)
		}
	}
	return errors.Join(failures...)
}

func (s *WorkerExecutionService) scheduleOwner(w store.WorkerRecord) (string, error) {
	ws, err := s.workerStore()
	if err != nil {
		return "", err
	}
	history, found, err := ws.GetWorkerRevision(w.AccountScopeID, w.ID, 1)
	if err != nil {
		return "", err
	}
	if !found || history.CommittedBy == "" {
		return "", store.ErrWorkerConflict
	}
	return history.CommittedBy, nil
}
