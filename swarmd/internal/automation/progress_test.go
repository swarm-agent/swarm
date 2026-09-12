package automation

import (
	"context"
	"errors"
	"testing"
	"time"

	store "swarm/packages/swarmd/internal/store/pebble"
)

// Purpose: preview must share Due/TickAt semantics rather than a UI cron engine.
// These pure domain tests prove civil-day DST and anchored elapsed-time behavior
// with fixed clocks; no daemon, provider or ambient local timezone is involved.
func TestProgressScheduleSlots(t *testing.T) {
	for _, tc := range []struct{ date, expression string; want int }{
		{"2026-03-08", "0 * * * *", 23},
		{"2026-11-01", "0 * * * *", 24}, // first fold instant only
		{"2026-03-09", "0 9 * * *", 1},
		{"2026-03-09", "0 */6 * * *", 4},
	} {
		loc, _ := time.LoadLocation("America/New_York")
		start, err := time.ParseInLocation("2006-01-02", tc.date, loc)
		if err != nil { t.Fatal(err) }
		r := store.AutomationRecord{Revision: 2, WrittenAt: start.Add(-time.Hour).UnixMilli(), Definition: &store.AutomationDefinition{Schedule: store.AutomationSchedulePolicy{Kind: "cron", Expression: tc.expression, Timezone: "America/New_York"}}}
		slots, err := scheduleSlots(context.Background(), r, start.UnixMilli(), start.AddDate(0, 0, 1).UnixMilli())
		if err != nil || len(slots) != tc.want { t.Fatalf("%s %s: %d %v", tc.date, tc.expression, len(slots), err) }
	}
	anchor := time.Date(2026, 3, 9, 0, 0, 17, 123000000, time.UTC).UnixMilli()
	r := store.AutomationRecord{Revision: 3, WrittenAt: anchor, Definition: &store.AutomationDefinition{Schedule: store.AutomationSchedulePolicy{Kind: "interval", IntervalSeconds: 3601}}}
	slots, err := scheduleSlots(context.Background(), r, anchor+1, anchor+7202001)
	if err != nil || len(slots) != 2 || slots[0].ScheduledAt != anchor+3601000 || slots[1].ScheduledAt != anchor+7202000 { t.Fatalf("anchor parity: %+v %v", slots, err) }
}

type progressRepo struct {
	*fakeRepo
	pages int
	incomplete bool
	occurrences []store.AutomationRecord
	revisions map[uint64]store.AutomationRecord
}
func (r *progressRepo) SearchAutomationRecords(store.AutomationSearch) ([]store.AutomationRecord, string, error) {
	r.pages++
	if r.incomplete { return r.occurrences, "continuation", nil }
	return r.occurrences, "", nil
}
func (r *progressRepo) GetAutomationRecord(scope store.AutomationScope, id, kind, record string, rev uint64) (store.AutomationRecord, bool, error) {
	if rev != 0 && r.revisions != nil { v, ok := r.revisions[rev]; return v, ok, nil }
	return r.fakeRepo.GetAutomationRecord(scope, id, kind, record, rev)
}

// Purpose: ScheduleProgress counts scheduled-day cohorts, not titles, retries or
// manual requests. Domain repository fakes exercise bounded empty/duplicate pages,
// pinned edits, incomplete totals and account rejection before any storage access.
func TestProgressCohortAndBounds(t *testing.T) {
	s, base, _, _, p, scope, d := fixture(t)
	now := time.Date(2026, 3, 9, 12, 0, 0, 0, time.UTC)
	s.now = func() time.Time { return now }
	d.Schedule = store.AutomationSchedulePolicy{Kind: "interval", IntervalSeconds: 3600}
	d.Enabled = true
	old := store.AutomationRecord{Revision: 1, WrittenAt: now.Add(-12*time.Hour).UnixMilli(), Definition: &d}
	d2 := d
	d2.Schedule.IntervalSeconds = 7200
	head := store.AutomationRecord{Revision: 2, WrittenAt: now.UnixMilli(), Definition: &d2}
	base.rows["definitionauto"] = head
	r := &progressRepo{fakeRepo: base, revisions: map[uint64]store.AutomationRecord{1: old}, incomplete: true}
	for i, state := range []string{"completed", "failed", "skipped", "pending", "running", "blocked", "cancelled", "cancelling"} {
		id := string(rune('a'+i))
		r.occurrences = append(r.occurrences, store.AutomationRecord{ID: id, Revision: 4, WrittenAt: now.UnixMilli(), Occurrence: &store.AutomationOccurrence{DefinitionRevision: 1, ScheduledAt: now.Add(-time.Hour).UnixMilli(), TriggerKind: "schedule", State: state}})
	}
	r.occurrences = append(r.occurrences,
		store.AutomationRecord{ID: "manual", Occurrence: &store.AutomationOccurrence{ScheduledAt: now.UnixMilli(), TriggerKind: "manual", State: "completed"}},
		store.AutomationRecord{ID: "yesterday", Occurrence: &store.AutomationOccurrence{ScheduledAt: now.Add(-24*time.Hour).UnixMilli(), TriggerKind: "schedule", State: "completed"}},
		store.AutomationRecord{ID: "unknown", Occurrence: &store.AutomationOccurrence{ScheduledAt: now.UnixMilli(), State: "completed"}})
	s.repo = r
	out, err := s.ScheduleProgress(context.Background(), p, scope, "auto", "UTC")
	if err != nil { t.Fatal(err) }
	if r.pages != 8 || out.HistoryComplete || out.Counts["completed"] != 1 || out.ManualCounts["completed"] != 1 || out.UnknownTriggerCount != 1 { t.Fatalf("bounded cohort: %+v pages=%d", out, r.pages) }
	if len(out.PlannedSlots) != 18 || out.PlannedSlots[0].DefinitionRevision != 1 || out.PlannedSlots[12].DefinitionRevision != 2 { t.Fatalf("revision windows: %+v", out.PlannedSlots) }
	if out.NextEligible != nil || out.NoNextReason != "approval_required" { t.Fatalf("approval: %+v", out) }
	r.incomplete = false
	out, err = s.ScheduleProgress(context.Background(), p, scope, "auto", "UTC")
	if err != nil || !out.HistoryComplete { t.Fatalf("completion: %+v %v", out, err) }
	reads, pages := base.reads, r.pages
	p.AccountID = "other"
	_, err = s.ScheduleProgress(context.Background(), p, scope, "auto", "UTC")
	if !errors.Is(err, ErrDenied) || base.reads != reads || r.pages != pages || base.writes != 0 { t.Fatal("cross-account read/write") }
}

// Purpose: no eligible run is distinct from zero daily quota. Read-only projection
// exposes paused/expired/manual states and refuses implicit local timezone input.
func TestProgressUnavailableStates(t *testing.T) {
	for _, reason := range []string{"paused", "expired", "not_time_scheduled"} {
		s, repo, _, _, p, scope, d := fixture(t)
		d.Schedule = store.AutomationSchedulePolicy{Kind: "interval", IntervalSeconds: 3600}
		d.Enabled = reason != "paused"
		d.Authorization.Mode = "approved_policy"
		d.Authorization.ExpiresAt = 1
		if reason == "not_time_scheduled" { d.Schedule.Kind = "manual"; d.Schedule.IntervalSeconds = 0 }
		repo.rows["definitionauto"] = store.AutomationRecord{Revision: 1, WrittenAt: 1000, Definition: &d}
		out, err := s.ScheduleProgress(context.Background(), p, scope, "auto", "UTC")
		if err != nil || out.NoNextReason != reason || out.NextEligible != nil { t.Fatalf("%s: %+v %v", reason, out, err) }
		if reason == "not_time_scheduled" && len(out.PlannedSlots) != 0 { t.Fatal("manual quota") }
		if _, err := s.ScheduleProgress(context.Background(), p, scope, "auto", "Local"); !errors.Is(err, ErrInvalid) { t.Fatal("implicit timezone accepted") }
	}
}
