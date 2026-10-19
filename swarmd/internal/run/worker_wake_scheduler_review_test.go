package run

import (
	"context"
	"strings"
	"testing"
	"time"

	pebblestore "swarm/packages/swarmd/internal/store/pebble"
)

func (fx *schedulerFixture) createActiveWorkerWith(t *testing.T, name string, automations []pebblestore.WorkerAutomationDefinition) pebblestore.WorkerRecord {
	t.Helper()
	w, err := fx.ws.CreateWorker(schedulerAccount, schedulerUser, pebblestore.CreateWorkerRequest{Name: name, Instructions: "Scheduled work.", IdempotencyKey: name, Automations: automations}, nil)
	if err != nil {
		t.Fatal(err)
	}
	w, err = fx.ws.ConfigureWorkerBindings(schedulerAccount, schedulerUser, w.ID, w.Revision, nil, true)
	if err != nil {
		t.Fatal(err)
	}
	if w.LifecycleState != pebblestore.WorkerLifecycleStateActive {
		t.Fatalf("worker %s state = %s, want active", w.ID, w.LifecycleState)
	}
	return w
}

// Requirement: one automation whose next due time cannot be computed must not
// stop the same worker's other scheduled automations; its error is still
// reported. ValidateSchedule accepts a cron such as "0 0 31 2 *" (31 February)
// that AutomationV2NextDue rejects. Threat: NextWorkerScheduledAt returning
// 0 on the first error, so reschedule clears the worker's due time and a valid
// 60s interval automation on the same worker never fires again. Owner:
// NextWorkerScheduledAt and WorkerScheduler.reschedule/admitDue over a real
// Pebble store; only the final Dispatch is recorded.
func TestWorkerSchedulerFailingAutomationDoesNotStopSiblings(t *testing.T) {
	fx := newSchedulerFixture(t, 0, 60)
	w := fx.createActiveWorkerWith(t, "Mixed schedules", []pebblestore.WorkerAutomationDefinition{
		{Name: "never", ActivationMode: "cron", Enabled: true, Schedule: &pebblestore.AutomationV2Schedule{Kind: "cron", Cron: "0 0 31 2 *", Timezone: "UTC"}},
		{Name: "every minute", ActivationMode: "interval", Enabled: true, Schedule: &pebblestore.AutomationV2Schedule{Kind: "interval", IntervalSeconds: 60}},
	})
	var interval pebblestore.WorkerAutomationDefinition
	for _, a := range w.Automations {
		if a.ActivationMode == "interval" {
			interval = a
		}
	}
	if interval.ID == "" {
		t.Fatal("interval automation missing")
	}
	start := time.Now()
	clock := newFakeSchedulerClock(start)
	dispatch := &dispatchRecorder{}
	fx.service.scheduledDispatchForTest = dispatch.record
	sch := NewWorkerScheduler(fx.service, WorkerSchedulerOptions{Clock: clock})
	ctx := context.Background()
	err := sch.process(ctx, start)
	if err == nil || !strings.Contains(err.Error(), "occurrence") {
		t.Fatalf("startup pass error = %v, want the failing cron automation reported", err)
	}
	firstMs, nerr := workerAutomationNextDue(interval, start)
	if nerr != nil || firstMs <= 0 {
		t.Fatalf("interval next due: %d, %v", firstMs, nerr)
	}
	first := time.UnixMilli(firstMs)
	if got := sch.nextWake(); !got.Equal(first) {
		t.Fatalf("next wake = %v, want the valid interval slot %v", got, first)
	}
	second := first.Add(time.Minute)
	simulate(ctx, sch, clock, second)
	got := dispatch.dueAts()
	if len(got) != 2 || got[0] != first.UnixMilli() || got[1] != second.UnixMilli() {
		t.Fatalf("dispatched due_at = %v, want the interval's [%d %d]", got, first.UnixMilli(), second.UnixMilli())
	}
	for _, r := range dispatch.reqs {
		if r.AutomationID != interval.ID {
			t.Fatalf("dispatched automation %s, want only the interval %s", r.AutomationID, interval.ID)
		}
	}
}

// Requirement: a persistent worker read error must not make the scheduler
// loop spin. Threat: reschedule returning on a GetWorker error with the
// past-due e.due left in place, so nextWake is in the past, Run arms a
// negative timer and pins a core logging every iteration (the old loop ran at
// most once a second). Owner: WorkerScheduler.reschedule after admitDue; the
// unreadable record is a real undecodable value at the worker's store key.
func TestWorkerSchedulerBacksOffAfterWorkerReadError(t *testing.T) {
	fx := newSchedulerFixture(t, 1, 60)
	w := fx.workers[0]
	start := time.Now()
	clock := newFakeSchedulerClock(start)
	sch := NewWorkerScheduler(fx.service, WorkerSchedulerOptions{Clock: clock})
	ctx := context.Background()
	if err := sch.process(ctx, start); err != nil {
		t.Fatalf("startup pass: %v", err)
	}
	due := firstSlotAfter(t, w, start)
	if err := fx.db.PutJSON(pebblestore.KeyWorker(schedulerAccount, w.ID), "unreadable worker record"); err != nil {
		t.Fatal(err)
	}
	clock.Set(due)
	if err := sch.process(ctx, due); err == nil {
		t.Fatal("unreadable worker was not reported")
	}
	if next := sch.nextWake(); next.Before(due.Add(workerSchedulerBusyInterval)) {
		t.Fatalf("next wake after a read error = %v (%v after due), want a back-off of at least %v", next, next.Sub(due), workerSchedulerBusyInterval)
	}
	// Bounded drive: a spinning scheduler is caught by the iteration cap
	// instead of hanging the test.
	end := due.Add(30 * time.Second)
	wakes := 0
	for i := 0; i < 200; i++ {
		next := sch.nextWake()
		if next.After(end) {
			break
		}
		if next.Before(clock.Now()) {
			next = clock.Now()
		}
		clock.Set(next)
		_ = sch.process(ctx, next)
		wakes++
	}
	if max := int(30*time.Second/workerSchedulerBusyInterval) + 1; wakes > max {
		t.Fatalf("scheduler woke %d times in 30s with an unreadable worker, want at most %d", wakes, max)
	}
}

// Requirement: while a due V2 record stays blocked (serialize overlap with
// its previous occurrence still pending), the scheduler keeps the old loop's
// cost: one sweep page per second and no full-catalog work. Threat: every
// pending wake re-reading the schedule hint, which decodes every accepted
// record (both index entries per automation), so the per-second cost grows
// with the catalog. The same applies to paging a catalog above the hint
// bound. Owner: WorkerScheduler.processV2; a settling occurrence commits a V2
// mutation and signals, which is when the hint is re-read.
func TestWorkerSchedulerPendingV2WakesPageWithoutCatalogScans(t *testing.T) {
	start := time.Date(2026, 3, 4, 12, 0, 0, 0, time.UTC)
	clock := newFakeSchedulerClock(start)
	sweeper := &fakeV2Sweeper{}
	hinter := &fakeV2Hinter{}
	sch := NewWorkerScheduler(nil, WorkerSchedulerOptions{Clock: clock, V2: sweeper, V2Hints: hinter})
	ctx := context.Background()
	if err := sch.process(ctx, start); err != nil {
		t.Fatal(err)
	}
	blocked := start.Add(-time.Minute).UnixMilli()
	hinter.hint = pebblestore.AutomationV2ScheduleHint{AcceptedKeys: 201, EarliestDueAt: blocked, LatestDueAt: blocked, UpcomingDueAt: start.Add(time.Hour).UnixMilli(), Pending: true}
	clock.Set(start.Add(time.Second))
	sch.Notify(pebblestore.SchedulingSignal{Kind: pebblestore.SchedulingSignalAutomationV2})
	if err := sch.process(ctx, clock.Now()); err != nil {
		t.Fatal(err)
	}
	sweeps, hints := len(sweeper.sweeps), hinter.calls
	wakes := simulate(ctx, sch, clock, clock.Now().Add(10*time.Second))
	if got := len(sweeper.sweeps) - sweeps; got != 10 || wakes != 10 {
		t.Errorf("blocked record with a pending occurrence: wakes=%d sweep pages=%d in 10s, want 10/10", wakes, got)
	}
	if got := hinter.calls - hints; got != 0 {
		t.Errorf("blocked record with a pending occurrence: %d full catalog hint scans in 10s, want 0", got)
	}

	// The occurrence settles: its commit signals, the hint is read once and
	// the scheduler goes back to sleep.
	hinter.hint.Pending = false
	clock.Set(clock.Now().Add(time.Second))
	sch.Notify(pebblestore.SchedulingSignal{Kind: pebblestore.SchedulingSignalAutomationV2})
	if err := sch.process(ctx, clock.Now()); err != nil {
		t.Fatal(err)
	}
	sweeps, hints = len(sweeper.sweeps), hinter.calls
	if wakes := simulate(ctx, sch, clock, clock.Now().Add(10*time.Second)); wakes != 0 || len(sweeper.sweeps) != sweeps || hinter.calls != hints {
		t.Errorf("settled: wakes=%d sweeps=%d hints=%d in 10s, want 0/0/0", wakes, len(sweeper.sweeps)-sweeps, hinter.calls-hints)
	}

	// Above the hint bound the catalog is paged without rescanning it.
	hinter.hint = pebblestore.AutomationV2ScheduleHint{AcceptedKeys: 10000, Overflow: true}
	clock.Set(clock.Now().Add(time.Second))
	sch.Notify(pebblestore.SchedulingSignal{Kind: pebblestore.SchedulingSignalAutomationV2})
	_ = sch.process(ctx, clock.Now())
	sweeps, hints = len(sweeper.sweeps), hinter.calls
	wakes = simulate(ctx, sch, clock, clock.Now().Add(10*time.Second))
	if got := len(sweeper.sweeps) - sweeps; got != 10 || wakes != 10 {
		t.Errorf("overflow: wakes=%d sweep pages=%d in 10s, want 10/10", wakes, got)
	}
	if got := hinter.calls - hints; got != 0 {
		t.Errorf("overflow: %d bounded catalog hint scans in 10s, want 0", got)
	}
}
