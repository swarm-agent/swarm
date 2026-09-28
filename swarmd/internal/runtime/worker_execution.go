package runtime

import (
	"context"
	"errors"
	"time"

	store "swarm/packages/swarmd/internal/store/pebble"
)

// sweepWorkers shares the existing bounded automation loop. Only newly due
// slots are admitted: a restarted daemon does not catch up missed intervals.
func (d *Daemon) sweepWorkers(ctx context.Context, now time.Time) error {
	if d.workerExecution == nil {
		return nil
	}
	ws, err := d.workerExecution.WorkerStoreForScheduling()
	if err != nil {
		return err
	}
	var failures []error
	cursor := ""
	completed := false
	for page := 0; page < 10; page++ {
		rows, err := ws.ListWorkersForScheduling(cursor, 25)
		if err != nil {
			return err
		}
		for _, w := range rows.Workers {
			if err = ctx.Err(); err != nil {
				return err
			}
			if err = d.workerExecution.ReconcileWorker(ctx, w.AccountScopeID, w.ID); err != nil {
				failures = append(failures, err)
			}
			if w.LifecycleState == store.WorkerLifecycleStateActive {
				if err = d.workerExecution.TickWorker(ctx, w.AccountScopeID, w.ID, now); err != nil {
					failures = append(failures, err)
				}
			}
		}
		if rows.NextCursor == "" {
			completed = true
			break
		}
		cursor = rows.NextCursor
	}
	if !completed {
		failures = append(failures, errors.New("worker sweep exceeded 250-worker budget"))
	}
	// Always advance the time fence: a failed worker must not cause stale slots
	// to be replayed by the next sweep.
	d.workerExecution.MarkWorkerSweep(now)
	return errors.Join(failures...)
}
