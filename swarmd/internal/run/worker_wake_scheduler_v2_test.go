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
	// Once settled, the scheduler sleeps until the next due time.
	hinter.hint.Pending = false
	clock.Set(clock.Now().Add(time.Second))
	if err := sch.process(ctx, clock.Now()); err != nil {
		t.Fatal(err)
	}
	sweepsBefore = len(sweeper.sweeps)
	end := clock.Now().Add(time.Minute)
	if wakes, want := simulate(ctx, sch, clock, end), housekeepingWakes(start, end)-housekeepingWakes(start, end.Add(-time.Minute)); wakes != want || len(sweeper.sweeps) != sweepsBefore {
		t.Fatalf("settled: wakes=%d sweeps=%d over a minute, want %d/0", wakes, len(sweeper.sweeps)-sweepsBefore, want)
	}
}
