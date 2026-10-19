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

// Requirement: a worker change committed while a scheduler pass is running
// (above all, the pass's own admission) must not move the worker's admission
// cursor past the pass time; a sibling automation's slot that falls due while
// Dispatch runs must still be admitted, as the old per-second sweep did by
// setting its cursor to the sweep start. Threat: Notify stamps the change with
// the wall clock, which is later than the pass time once Dispatch takes a
// while; applySignals and reconcileEntry then advance the cursor to that later
// time and the sibling's slot is never admitted. Owner: WorkerScheduler
// (Notify, applySignals, reconcileEntry, admitDue) with the real
// WorkerStore.AdmitWorkerRun commit, whose commitWorkerRealtime emits the
// worker scheduling signal. Only the service-level Dispatch preparation
// (workspace, model, project resolution) is replaced; the hook advances
// simulated time by the dispatch duration and then performs the real store
// admission, which is the narrowest layer that produces the in-pass signal.
func TestWorkerSchedulerAdmissionSignalDoesNotSkipSiblingSlot(t *testing.T) {
	fx := newSchedulerFixture(t, 0, 60)
	w := fx.createActiveWorkerWith(t, "Close slots", []pebblestore.WorkerAutomationDefinition{
		{Name: "every minute cron", ActivationMode: "cron", Enabled: true, WorkspaceID: "ws-sched", Schedule: &pebblestore.AutomationV2Schedule{Kind: "cron", Cron: "* * * * *", Timezone: "UTC"}},
		{Name: "every minute interval", ActivationMode: "interval", Enabled: true, WorkspaceID: "ws-sched", Schedule: &pebblestore.AutomationV2Schedule{Kind: "interval", IntervalSeconds: 60}},
	})
	// Start a full minute ahead so the startup reconcile is well clear of the
	// first slot's per-worker reconcile spacing.
	start := time.Now().Add(time.Minute)
	slots := map[string]time.Time{}
	for _, a := range w.Automations {
		next, err := workerAutomationNextDue(a, start)
		if err != nil || next <= 0 {
			t.Fatalf("automation %s next due: %d, %v", a.ID, next, err)
		}
		slots[a.ID] = time.UnixMilli(next)
	}
	var firstID, secondID string
	for id, at := range slots {
		if firstID == "" || at.Before(slots[firstID]) {
			firstID = id
		}
	}
	for id := range slots {
		if id != firstID {
			secondID = id
		}
	}
	first, second := slots[firstID], slots[secondID]
	gap := second.Sub(first)
	if gap <= 0 {
		t.Skipf("cron and interval slots coincide (worker created on a whole minute); no sibling window to test")
	}
	// The first admission's Dispatch outlasts the gap to the sibling's slot.
	dispatchTime := gap + time.Second

	clock := newFakeSchedulerClock(start)
	sch := NewWorkerScheduler(fx.service, WorkerSchedulerOptions{Clock: clock})
	fx.db.SetSchedulingObserver(sch.Notify)
	t.Cleanup(func() { fx.db.SetSchedulingObserver(nil) })
	dispatch := &dispatchRecorder{}
	var admitErrs []error
	fx.service.scheduledDispatchForTest = func(account, owner string, req pebblestore.WorkerRunAdmission) error {
		clock.Set(clock.Now().Add(dispatchTime))
		req.UserID = owner
		if _, err := fx.ws.AdmitWorkerRun(account, req); err != nil {
			admitErrs = append(admitErrs, err)
			return err
		}
		return dispatch.record(account, owner, req)
	}
	ctx := context.Background()
	_ = sch.process(ctx, start)
	simulate(ctx, sch, clock, second.Add(dispatchTime+time.Second))
	if len(admitErrs) != 0 {
		t.Fatalf("real store admission failed: %v", admitErrs)
	}
	got := map[string][]int64{}
	for _, r := range dispatch.reqs {
		got[r.AutomationID] = append(got[r.AutomationID], r.Input["due_at"].(int64))
	}
	if len(got[firstID]) != 1 || got[firstID][0] != first.UnixMilli() {
		t.Fatalf("first automation admitted %v, want [%d]", got[firstID], first.UnixMilli())
	}
	if len(got[secondID]) != 1 || got[secondID][0] != second.UnixMilli() {
		t.Fatalf("sibling automation admitted %v, want its slot [%d] that fell due %v after the first, while Dispatch ran", got[secondID], second.UnixMilli(), gap)
	}
	for occ, n := range dispatch.occurrences() {
		if n != 1 {
			t.Fatalf("occurrence %s admitted %d times, want once", occ, n)
		}
	}
}

// Requirement: after every scheduler pass the next wake must be in the
// future; a deferred change-driven V2 pass must never leave a past wake time.
// Threat: a V2 change arriving inside the one-second change spacing, while
// v2Next holds an upcoming V2 slot earlier than the deferred time, kept that
// slot; once it passed, nextWake returned a past time and Run armed zero
// timers back to back until the spacing ended (a busy spin). Owner:
// WorkerScheduler.processV2 deferral. Each process call is driven by hand at
// the wake it asked for, because simulate clamps past wakes to now and would
// hide the spin.
func TestWorkerSchedulerDeferredV2ChangeNeverLeavesPastWake(t *testing.T) {
	start := time.Date(2026, 3, 4, 12, 0, 0, 0, time.UTC)
	clock := newFakeSchedulerClock(start)
	sweeper := &fakeV2Sweeper{}
	upcoming := start.Add(300 * time.Millisecond)
	hinter := &fakeV2Hinter{hint: pebblestore.AutomationV2ScheduleHint{AcceptedKeys: 4, EarliestDueAt: upcoming.UnixMilli(), UpcomingDueAt: upcoming.UnixMilli()}}
	sch := NewWorkerScheduler(nil, WorkerSchedulerOptions{Clock: clock, V2: sweeper, V2Hints: hinter})
	ctx := context.Background()
	check := func(now time.Time) {
		t.Helper()
		if next := sch.nextWake(); !next.After(now) {
			t.Fatalf("after the pass at %v, next wake %v is not in the future", now.Sub(start), next.Sub(start))
		}
	}
	// A dirty pass sees an upcoming V2 slot 300ms away.
	if err := sch.process(ctx, start); err != nil {
		t.Fatal(err)
	}
	check(start)
	if got := sch.nextWake(); !got.Equal(upcoming) {
		t.Fatalf("next wake = %v, want the upcoming slot %v", got, upcoming)
	}
	// A V2 change commits before the slot, inside the change spacing.
	signalAt := start.Add(100 * time.Millisecond)
	clock.Set(signalAt)
	sch.Notify(pebblestore.SchedulingSignal{Kind: pebblestore.SchedulingSignalAutomationV2})
	if err := sch.process(ctx, signalAt); err != nil {
		t.Fatal(err)
	}
	check(signalAt)
	// Follow every wake the scheduler asks for until the spacing has ended.
	passes := 0
	for now := sch.nextWake(); !now.After(start.Add(2 * time.Second)); now = sch.nextWake() {
		if passes++; passes > 20 {
			t.Fatalf("more than 20 passes within 2s of simulated time")
		}
		clock.Set(now)
		if err := sch.process(ctx, now); err != nil {
			t.Fatal(err)
		}
		check(now)
	}
	if len(sweeper.sweeps) == 0 {
		t.Fatal("the deferred V2 change was never processed")
	}
}
