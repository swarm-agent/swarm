package automation

import (
	"context"
	"errors"
	"fmt"
	"time"

	store "swarm/packages/swarmd/internal/store/pebble"
)

var ErrRecoveryCursor = errors.New("invalid automation recovery cursor")

type ScheduleRepository interface {
	GetAutomationCursor(store.AutomationScope, string, uint64) (int64, error)
	AdvanceAutomationCursor(store.AutomationScope, string, uint64, int64, int64) error
}

// Tick processes one definition and at most one occurrence. Daemon composition
// owns bounded workspace enumeration and wakeups; receipts, not timers, dedupe.
func (e *ExecutionService) Tick(ctx context.Context, p Principal, scope store.AutomationScope, id string, revision uint64) error {
	return e.TickAt(ctx, p, scope, id, revision, e.domain.now())
}

// TickAt evaluates a durable catalog sweep window, not the page's arrival time.
// Authorization and admission still use the live clock. Only trusted daemon
// scheduling supplies this timestamp; it is not a user trigger override.
func (e *ExecutionService) TickAt(ctx context.Context, p Principal, scope store.AutomationScope, id string, revision uint64, at time.Time) error {
	if p.Role != "system" { return ErrDenied }
	if at.After(e.domain.now()) { return ErrInvalid }
	def, err := e.domain.CheckRun(ctx, p, scope, id, revision)
	if err != nil { return err }
	s, err := NormalizeSchedule(def.Definition.Schedule)
	if err != nil { return err }
	if s.Kind != "cron" && s.Kind != "interval" { return nil }
	cursor, ok := e.domain.repo.(ScheduleRepository)
	if !ok { return ErrInvalid }
	previous, err := cursor.GetAutomationCursor(scope, id, revision)
	if err != nil { return err }
	now := at.UnixMilli()
	if now <= previous || now < def.WrittenAt { return nil }
	var candidate int64
	if s.Kind == "interval" {
		step := s.IntervalSeconds*1000
		candidate = def.WrittenAt + (now-def.WrittenAt)/step*step
	} else {
		// At most one day of civil-minute lookback; older downtime is deliberately
		// not backfilled. The cursor still advances, preventing unbounded recovery.
		at := time.UnixMilli(now).Truncate(time.Minute)
		for i := 0; i < 1440 && at.UnixMilli() > previous && at.UnixMilli() >= def.WrittenAt; i++ {
			due, err := Due(s, at, time.UnixMilli(def.WrittenAt))
			if err != nil { return err }
			if due { candidate = at.UnixMilli(); break }
			at = at.Add(-time.Minute)
		}
	}
	if candidate > previous && candidate >= def.WrittenAt && (s.MissedPolicy == "coalesce" || now-candidate < 60000) {
		_, err := e.Admit(ctx, p, scope, id, revision, Trigger{Kind: "schedule", Identity: fmt.Sprint(candidate), ScheduledAt: candidate})
		if err != nil { return err }
	}
	return cursor.AdvanceAutomationCursor(scope, id, revision, previous, now)
}

// RecoverPage resumes pending admissions and durable cancellation fences. Blocked/failed runs require an
// explicit user decision; transient Ensure failures keep their original identity.
func (e *ExecutionService) RecoverPage(ctx context.Context, p Principal, scope store.AutomationScope, id, cursor string) (string, error) {
	if err := e.domain.authorize(ctx, p, scope, "run"); err != nil { return cursor, err }
	records, next, err := e.domain.repo.SearchAutomationRecords(store.AutomationSearch{Scope: scope, AutomationID: id, Kind: "occurrence", Cursor: cursor, Limit: 50})
	if errors.Is(err, store.ErrAutomationInvalid) && cursor != "" { return "", ErrRecoveryCursor }
	if err != nil { return cursor, err }
	var failures []error
	for _, r := range records {
		if err := ctx.Err(); err != nil { return cursor, err }
		if r.Occurrence == nil { continue }
		switch r.Occurrence.State {
		case "pending":
			_, err := e.Dispatch(ctx, p, scope, id, r.ID)
			failures = append(failures, err)
		case "cancelling":
			if r.SubjectID != p.SubjectID { failures = append(failures, ErrDenied); continue }
			// The durable stop was already authorized. Resume its runtime fence,
			// not a new cancellation admission against a different revision.
			canceller, ok := e.runtime.(interface { Cancel(context.Context, Principal, store.AutomationRecord) error })
			repo, stored := e.domain.repo.(interface { FinishAutomationCancellation(store.AutomationRecord, string, int64) (store.AutomationRecord, error) })
			if !ok || !stored { failures = append(failures, ErrInvalid); continue }
			if err := canceller.Cancel(ctx, p, r); err != nil { failures = append(failures, err); continue }
			_, err := repo.FinishAutomationCancellation(r, p.SubjectID, e.domain.now().UnixMilli())
			failures = append(failures, err)
		}
	}
	// Individual failures are revisited on wrap, never starve later pages.
	return next, errors.Join(failures...)
}
