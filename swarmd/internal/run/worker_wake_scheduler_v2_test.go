package run

import (
	"context"
	"testing"
	"time"

	pebblestore "swarm/packages/swarmd/internal/store/pebble"
)

type fakeV2Sweeper struct{ sweeps []time.Time }

func (f *fakeV2Sweeper) Sweep(_ context.Context, now time.Time) error {
	f.sweeps = append(f.sweeps, now)
	return nil
}

type fakeV2Hinter struct {
	hint  pebblestore.AutomationV2ScheduleHint
	calls int
}

func (f *fakeV2Hinter) AutomationV2ScheduleHint(int64) (pebblestore.AutomationV2ScheduleHint, error) {
	f.calls++
	return f.hint, nil
}

// Requirement: the V2 automation sweep, previously run every second, runs
// only when an accepted V2 automation is due or has unfinished occurrences,
// and a due pass still visits every page. Threat: idle V2 polling, or a due
// V2 automation never swept. Owner: WorkerScheduler.processV2; the V2
// scheduler and its store hint are replaced at their interfaces because the
// V2 Tick/admission contract itself is unchanged.
func TestWorkerSchedulerDrivesAutomationV2OnlyWhenDueOrPending(t *testing.T) {
	start := time.Now()
	clock := newFakeSchedulerClock(start)
	sweeper := &fakeV2Sweeper{}
	hinter := &fakeV2Hinter{}
	sch := NewWorkerScheduler(nil, WorkerSchedulerOptions{Clock: clock, V2: sweeper, V2Hints: hinter})
	ctx := context.Background()
	if err := sch.process(ctx, start); err != nil {
		t.Fatal(err)
	}
	if wakes := simulate(ctx, sch, clock, start.Add(10*time.Second)); wakes != 0 || len(sweeper.sweeps) != 0 {
		t.Fatalf("no V2 automations: wakes=%d sweeps=%d, want 0/0", wakes, len(sweeper.sweeps))
	}

	// An accepted automation, 21 index entries (3 pages), due in 30s.
	due := clock.Now().Add(30 * time.Second)
	hinter.hint = pebblestore.AutomationV2ScheduleHint{AcceptedKeys: 21, EarliestDueAt: due.UnixMilli()}
	sch.Notify(pebblestore.SchedulingSignal{Kind: pebblestore.SchedulingSignalAutomationV2})
	if err := sch.process(ctx, clock.Now()); err != nil {
		t.Fatal(err)
	}
	if len(sweeper.sweeps) != 0 {
		t.Fatalf("swept %d times before the V2 automation was due", len(sweeper.sweeps))
	}
	if got := sch.nextWake(); !got.Equal(time.UnixMilli(due.UnixMilli())) {
		t.Fatalf("next wake = %v, want V2 due %v", got, due)
	}
	if wakes := simulate(ctx, sch, clock, due.Add(-time.Second)); wakes != 0 {
		t.Fatalf("woke %d times before the V2 due time", wakes)
	}
	// Admission advances the record; the occurrence is now pending.
	next := due.Add(time.Hour)
	sweepsBefore := len(sweeper.sweeps)
	hinterAfterDue := func() {
		hinter.hint = pebblestore.AutomationV2ScheduleHint{AcceptedKeys: 21, EarliestDueAt: next.UnixMilli(), Pending: true}
	}
	clock.Set(time.UnixMilli(due.UnixMilli()))
	// The first hint read in the pass still sees the due record; the
	// recomputed hint after the sweep sees the admitted state.
	origCalls := hinter.calls
	if err := sch.process(ctx, clock.Now()); err != nil {
		t.Fatal(err)
	}
	if got := len(sweeper.sweeps) - sweepsBefore; got != 21/automationV2SweepPage+2 {
		t.Fatalf("due pass swept %d pages, want %d (every page once)", got, 21/automationV2SweepPage+2)
	}
	if hinter.calls-origCalls != 2 {
		t.Fatalf("due pass read %d hints, want 2", hinter.calls-origCalls)
	}
	hinterAfterDue()
	sch.Notify(pebblestore.SchedulingSignal{Kind: pebblestore.SchedulingSignalAutomationV2})
	if err := sch.process(ctx, clock.Now()); err != nil {
		t.Fatal(err)
	}
	// Pending occurrences keep the prior one page per second.
	sweepsBefore = len(sweeper.sweeps)
	if wakes := simulate(ctx, sch, clock, clock.Now().Add(5*time.Second)); wakes != 5 || len(sweeper.sweeps)-sweepsBefore != 5 {
		t.Fatalf("pending: wakes=%d sweeps=%d over 5s, want 5/5", wakes, len(sweeper.sweeps)-sweepsBefore)
	}
	// Once settled, the scheduler sleeps until the next due time. Settling
	// commits a V2 occurrence mutation, which signals the scheduler.
	hinter.hint.Pending = false
	clock.Set(clock.Now().Add(time.Second))
	sch.Notify(pebblestore.SchedulingSignal{Kind: pebblestore.SchedulingSignalAutomationV2})
	if err := sch.process(ctx, clock.Now()); err != nil {
		t.Fatal(err)
	}
	sweepsBefore = len(sweeper.sweeps)
	end := clock.Now().Add(time.Minute)
	if wakes, want := simulate(ctx, sch, clock, end), housekeepingWakes(start, end)-housekeepingWakes(start, end.Add(-time.Minute)); wakes != want || len(sweeper.sweeps) != sweepsBefore {
		t.Fatalf("settled: wakes=%d sweeps=%d over a minute, want %d/0", wakes, len(sweeper.sweeps)-sweepsBefore, want)
	}
}

// Requirement: a V2 automation that is due but cannot be admitted (serialize
// overlap while the previous occurrence runs, or an admission conflict) must
// not drive full-catalog sweeps every second. While occurrences are pending,
// the scheduler keeps the prior cadence of at most one sweep page and one
// hint read per second; with nothing pending it sleeps until a committed V2
// change retries the record. Threat: processV2 re-arming a one-second timer
// for an un-advanced due record and re-running AcceptedKeys/10+2 sweeps plus
// two hint scans on every wake. Owner: WorkerScheduler.processV2; the V2
// sweeper and hint are replaced at their interfaces, as above.
func TestWorkerSchedulerBacksOffDueV2RecordThatDoesNotAdvance(t *testing.T) {
	// Fixed mid-day start: no UTC midnight or safety rescan falls in a window.
	start := time.Date(2026, 3, 4, 12, 0, 0, 0, time.UTC)
	clock := newFakeSchedulerClock(start)
	sweeper := &fakeV2Sweeper{}
	hinter := &fakeV2Hinter{}
	sch := NewWorkerScheduler(nil, WorkerSchedulerOptions{Clock: clock, V2: sweeper, V2Hints: hinter})
	ctx := context.Background()
	if err := sch.process(ctx, start); err != nil {
		t.Fatal(err)
	}
	// 201 index entries; the record is due in 10s while an earlier serialized
	// occurrence is still running, so admission keeps returning a conflict
	// and NextDueAt never advances.
	due := start.Add(10 * time.Second)
	hinter.hint = pebblestore.AutomationV2ScheduleHint{AcceptedKeys: 201, EarliestDueAt: due.UnixMilli(), Pending: true}
	clock.Set(start.Add(time.Second))
	sch.Notify(pebblestore.SchedulingSignal{Kind: pebblestore.SchedulingSignalAutomationV2})
	if err := sch.process(ctx, clock.Now()); err != nil {
		t.Fatal(err)
	}
	fullPass := 201/automationV2SweepPage + 2
	simulate(ctx, sch, clock, time.UnixMilli(due.UnixMilli()))
	if got := len(sweeper.sweeps); got < fullPass {
		t.Fatalf("sweeps at the due time = %d, want at least one full pass (%d)", got, fullPass)
	}

	sweeps, hints := len(sweeper.sweeps), hinter.calls
	wakes := simulate(ctx, sch, clock, clock.Now().Add(10*time.Second))
	if got := len(sweeper.sweeps) - sweeps; got > 10 {
		t.Errorf("blocked due record with pending work: %d sweep pages in 10s, want at most one per second (10)", got)
	}
	if got := hinter.calls - hints; got > 10 {
		t.Errorf("blocked due record with pending work: %d hint scans in 10s, want at most one per second (10)", got)
	}
	if wakes > 10 {
		t.Errorf("blocked due record with pending work: %d wakes in 10s, want at most 10", wakes)
	}

	// Nothing pending (for example the occurrence finished but admission
	// still conflicts): the committed V2 change retries once, then the
	// scheduler sleeps instead of polling.
	hinter.hint.Pending = false
	clock.Set(clock.Now().Add(time.Second))
	sch.Notify(pebblestore.SchedulingSignal{Kind: pebblestore.SchedulingSignalAutomationV2})
	sweeps = len(sweeper.sweeps)
	if err := sch.process(ctx, clock.Now()); err != nil {
		t.Fatal(err)
	}
	if got := len(sweeper.sweeps) - sweeps; got != fullPass {
		t.Fatalf("V2 change retried the blocked record with %d sweeps, want one full pass (%d)", got, fullPass)
	}
	sweeps, hints = len(sweeper.sweeps), hinter.calls
	if wakes := simulate(ctx, sch, clock, clock.Now().Add(10*time.Second)); wakes != 0 {
		t.Errorf("blocked due record, nothing pending: %d wakes in 10s, want 0", wakes)
	}
	if got := len(sweeper.sweeps) - sweeps; got != 0 {
		t.Errorf("blocked due record, nothing pending: %d sweeps in 10s, want 0", got)
	}
	if got := hinter.calls - hints; got != 0 {
		t.Errorf("blocked due record, nothing pending: %d hint scans in 10s, want 0", got)
	}
}

// Requirement: a blocked V2 record must not hide a different record that
// falls due later; that one still gets a full pass at its due time. Threat:
// the blocked-record backoff suppressing admission of every later slot.
// Owner: WorkerScheduler.processV2 (v2BlockedThrough vs hint LatestDueAt).
func TestWorkerSchedulerBlockedV2RecordDoesNotHideALaterDueRecord(t *testing.T) {
	start := time.Date(2026, 3, 4, 12, 0, 0, 0, time.UTC)
	clock := newFakeSchedulerClock(start)
	sweeper := &fakeV2Sweeper{}
	hinter := &fakeV2Hinter{}
	sch := NewWorkerScheduler(nil, WorkerSchedulerOptions{Clock: clock, V2: sweeper, V2Hints: hinter})
	ctx := context.Background()
	blocked := start.Add(-time.Minute).UnixMilli()
	later := start.Add(30 * time.Second)
	hinter.hint = pebblestore.AutomationV2ScheduleHint{AcceptedKeys: 30, EarliestDueAt: blocked, LatestDueAt: blocked, UpcomingDueAt: later.UnixMilli()}
	if err := sch.process(ctx, start); err != nil {
		t.Fatal(err)
	}
	fullPass := 30/automationV2SweepPage + 2
	if got := len(sweeper.sweeps); got != fullPass {
		t.Fatalf("startup sweeps = %d, want one full pass (%d)", got, fullPass)
	}
	if got := sch.nextWake(); !got.Equal(later) {
		t.Fatalf("next wake with a blocked record = %v, want the later record's due %v", got, later)
	}
	// At the later due time both records are due; the later one is new.
	clock.Set(later)
	hinter.hint = pebblestore.AutomationV2ScheduleHint{AcceptedKeys: 30, EarliestDueAt: blocked, LatestDueAt: later.UnixMilli()}
	if err := sch.process(ctx, later); err != nil {
		t.Fatal(err)
	}
	if got := len(sweeper.sweeps) - fullPass; got != fullPass {
		t.Fatalf("later due record got %d sweeps, want a full pass (%d)", got, fullPass)
	}
}

// Requirement: a V2 catalog above the bounded hint scan is still swept at the
// prior one page per second, and the degradation is reported once rather
// than every second. Threat: the hint bound stopping V2 scheduling entirely.
// Owner: WorkerScheduler.pageV2Overflow.
func TestWorkerSchedulerPagesV2CatalogAboveHintBound(t *testing.T) {
	start := time.Date(2026, 3, 4, 12, 0, 0, 0, time.UTC)
	clock := newFakeSchedulerClock(start)
	sweeper := &fakeV2Sweeper{}
	hinter := &fakeV2Hinter{hint: pebblestore.AutomationV2ScheduleHint{AcceptedKeys: 10000, Overflow: true}}
	var reports int
	sch := NewWorkerScheduler(nil, WorkerSchedulerOptions{Clock: clock, V2: sweeper, V2Hints: hinter})
	ctx := context.Background()
	if err := sch.process(ctx, start); err == nil {
		t.Fatal("overflow was not reported")
	}
	reports++
	end := start.Add(10 * time.Second)
	for next := sch.nextWake(); !next.After(end); next = sch.nextWake() {
		clock.Set(next)
		if err := sch.process(ctx, next); err != nil {
			reports++
		}
	}
	if reports != 1 {
		t.Fatalf("overflow reported %d times, want once", reports)
	}
	if got := len(sweeper.sweeps); got != 11 {
		t.Fatalf("overflow sweeps over 10s = %d, want one page per second (11 including startup)", got)
	}
}
