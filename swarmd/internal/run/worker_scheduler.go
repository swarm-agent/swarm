package run

import (
	"context"
	"errors"
	"fmt"
	"time"

	store "swarm/packages/swarmd/internal/store/pebble"
)

// NextWorkerScheduledAt reports the earliest eligible schedule slot strictly
// after after. It is an estimate, not a dispatch guarantee: TickWorker skips
// missed slots and still checks ownership, budget and admission at dispatch.
// An automation whose next slot cannot be computed is skipped and its error
// returned alongside the earliest slot of the others, so one bad schedule
// never stops a worker's other automations.
func NextWorkerScheduledAt(w store.WorkerRecord, after time.Time) (int64, error) {
	if w.LifecycleState != store.WorkerLifecycleStateActive {
		return 0, nil
	}
	var earliest int64
	var failures []error
	for _, a := range w.Automations {
		if !workerAutomationScheduled(a) {
			continue
		}
		next, err := workerAutomationNextDue(a, after)
		if err != nil {
			failures = append(failures, fmt.Errorf("worker %s automation %s: %w", w.ID, a.ID, err))
			continue
		}
		if next > 0 && (earliest == 0 || next < earliest) {
			earliest = next
		}
	}
	return earliest, errors.Join(failures...)
}

func workerAutomationScheduled(a store.WorkerAutomationDefinition) bool {
	return a.Enabled && a.Schedule != nil && (a.ActivationMode == "interval" || a.ActivationMode == "cron")
}

func workerAutomationNextDue(a store.WorkerAutomationDefinition, after time.Time) (int64, error) {
	return store.AutomationV2NextDue(store.AutomationV2Settings{SchemaVersion: 2, Schedule: *a.Schedule, Missed: "skip", Overlap: "independent", ActivateOnAccept: true}, a.CreatedAt, after.UnixMilli())
}

// TickWorker admits at most one slot per automation that fell due in
// (after, now]. The caller owns the cursor: a restarted daemon starts it at
// startup, so slots missed while offline are skipped, never replayed. Worker
// admission owns deduplication. The budget is read only when a slot is due,
// immediately before admission; a held budget skips the slot.
func (s *WorkerExecutionService) TickWorker(ctx context.Context, account, id string, after, now time.Time) error {
	if after.IsZero() || !now.After(after) {
		return nil
	}
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
	type dueSlot struct {
		automation store.WorkerAutomationDefinition
		at         int64
	}
	var failures []error
	var due []dueSlot
	for _, a := range w.Automations {
		if !workerAutomationScheduled(a) {
			continue
		}
		if err = ctx.Err(); err != nil {
			return err
		}
		next, err := workerAutomationNextDue(a, after)
		if err != nil {
			failures = append(failures, err)
			continue
		}
		if next > now.UnixMilli() || next <= 0 {
			continue
		}
		due = append(due, dueSlot{automation: a, at: next})
	}
	if len(due) == 0 {
		return errors.Join(failures...)
	}
	budget, err := s.host.runs.sessions.Store().GetWorkerBudgetStatus(account, id)
	if err != nil {
		return errors.Join(append(failures, err)...)
	}
	if budget.Hold != nil {
		return errors.Join(failures...)
	}
	owner, err := s.scheduleOwner(w)
	if err != nil {
		return errors.Join(append(failures, err)...)
	}
	for _, slot := range due {
		if err = ctx.Err(); err != nil {
			return err
		}
		a := slot.automation
		occurrence := fmt.Sprintf("wocc_%s_%d_%d", a.ID, a.Revision, slot.at)
		req := store.WorkerRunAdmission{WorkerID: id, AutomationID: a.ID, OccurrenceID: occurrence, RequestSource: "schedule", Input: map[string]any{"due_at": slot.at}}
		if s.scheduledDispatchForTest != nil {
			err = s.scheduledDispatchForTest(account, owner, req)
		} else {
			_, err = s.Dispatch(ctx, account, owner, req)
		}
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
