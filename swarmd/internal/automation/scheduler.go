package automation

import (
	"context"
	"fmt"
	"time"

	store "swarm/packages/swarmd/internal/store/pebble"
)

type ScheduleRepository interface {
	GetAutomationCursor(store.AutomationScope, string, uint64) (int64, error)
	AdvanceAutomationCursor(store.AutomationScope, string, uint64, int64, int64) error
}

// Tick processes one definition and at most one occurrence. Daemon composition
// owns bounded workspace enumeration and wakeups; receipts, not timers, dedupe.
func (e *ExecutionService) Tick(ctx context.Context, p Principal, scope store.AutomationScope, id string, revision uint64) error {
	if p.Role != "system" { return ErrDenied }
	def, err := e.domain.CheckRun(ctx, p, scope, id, revision)
	if err != nil { return err }
	s := def.Definition.Schedule
	if s.Kind != "cron" && s.Kind != "interval" { return nil }
	cursor, ok := e.domain.repo.(ScheduleRepository)
	if !ok { return ErrInvalid }
	previous, err := cursor.GetAutomationCursor(scope, id, revision)
	if err != nil { return err }
	now := e.domain.now().UnixMilli()
	if now <= previous { return nil }
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

// RecoverPage resumes only pending admissions. Blocked/failed runs require an
// explicit user decision; transient Ensure failures keep their original identity.
func (e *ExecutionService) RecoverPage(ctx context.Context, p Principal, scope store.AutomationScope, id, cursor string) (string, error) {
	if err := e.domain.authorize(ctx, p, scope, "run"); err != nil { return "", err }
	records, next, err := e.domain.repo.SearchAutomationRecords(store.AutomationSearch{Scope: scope, AutomationID: id, Kind: "occurrence", Cursor: cursor, Limit: 50})
	if err != nil { return "", err }
	for _, r := range records {
		if err := ctx.Err(); err != nil { return cursor, err }
		if r.Occurrence != nil && r.Occurrence.State == "pending" {
			if _, err := e.Dispatch(ctx, p, scope, id, r.ID); err != nil { return cursor, err }
		}
	}
	return next, nil
}
