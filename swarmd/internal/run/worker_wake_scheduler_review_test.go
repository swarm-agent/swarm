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
	start := schedulerTestStart(time.Now())
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
	start := schedulerTestStart(time.Now())
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
	start := schedulerTestStart(time.Now()).Add(time.Minute)
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
	// End at the sibling's slot: it is admitted at the pass after the slow
	// first Dispatch (already past it). A later end would, for gaps over
	// ~29s, also reach the first automation's next slot and admit it
	// legitimately, making the exact-once assertion depend on the wall clock.
	simulate(ctx, sch, clock, second)
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
// hide the spin. The fake hint stays fixed, so by the deferred pass its
// upcoming time is also in the past; that must not become a past wake either
// (v2DueTimes).
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

// Requirement: budget and usage are read only just before a due slot is
// admitted, or when a reconcile is driven by an event (budget, worker or run
// signal, UTC midnight, the slow safety rescan). A worker with runs in flight
// is re-observed on the busy interval for the stall watchdog and settlement,
// but that timer must reuse the hold state from the last event-driven read
// instead of re-reading the ledger. Threat: reconcileWorker calling
// GetWorkerBudgetStatus (GetUsageScopeDay, GetDailyUsageAccumulator) on every
// 5 s busy re-check, a short poll outside admission. Owner:
// WorkerScheduler.reconcileDue/reconcileEntry and
// WorkerExecutionService.reconcileWorker over a real Pebble store; the run in
// flight is a real WorkerStore.AdmitWorkerRun record. The scheduler is the
// narrowest layer that owns which reconciles are event-driven.
func TestWorkerSchedulerBusyRecheckDoesNotReadBudget(t *testing.T) {
	fx := newSchedulerFixture(t, 0, 3600)
	w := fx.createActiveWorkerWith(t, "Busy worker", []pebblestore.WorkerAutomationDefinition{
		{Name: "hourly", ActivationMode: "interval", Enabled: true, WorkspaceID: "ws-sched", Schedule: &pebblestore.AutomationV2Schedule{Kind: "interval", IntervalSeconds: 3600}},
	})
	start := schedulerTestStart(time.Now())
	clock := newFakeSchedulerClock(start)
	sch := NewWorkerScheduler(fx.service, WorkerSchedulerOptions{Clock: clock})
	ctx := context.Background()
	admitted, err := fx.ws.AdmitWorkerRun(schedulerAccount, pebblestore.WorkerRunAdmission{WorkerID: w.ID, AutomationID: w.Automations[0].ID, OccurrenceID: "wocc_busy", RequestSource: "schedule", UserID: schedulerUser})
	if err != nil {
		t.Fatal(err)
	}
	// The run is past preparation and executing elsewhere: reconcile only
	// observes it (watchdog, settlement) and it stays in flight.
	admitted.Status = "running"
	if _, err := fx.ws.RecordWorkerRun(schedulerAccount, admitted); err != nil {
		t.Fatal(err)
	}
	// Startup is a rescan: the busy worker's budget is read once there.
	_ = sch.process(ctx, start)
	if e := sch.entries[workerKey{schedulerAccount, w.ID}]; e == nil || !e.busy {
		t.Fatalf("worker with a run in flight is not tracked as busy: %+v", e)
	}
	startupReads := fx.budgetReads.Load()
	if startupReads == 0 {
		t.Fatal("startup rescan did not read the busy worker's budget")
	}
	// Stay well inside the safety rescan and the next slot; no signals.
	end := start.Add(workerSchedulerSafetyInterval - 10*time.Second)
	fx.db.SetSchedulingObserver(nil)
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
	if min := int((end.Sub(start))/workerSchedulerBusyInterval) - 1; wakes < min {
		t.Fatalf("busy worker re-checked %d times over %v, want at least %d busy re-checks", wakes, end.Sub(start), min)
	}
	if n := fx.budgetReads.Load() - startupReads; n != 0 {
		kinds := map[string]int64{}
		fx.readKinds.Range(func(k, v any) bool { kinds[k.(string)] = v.(interface{ Load() int64 }).Load(); return true })
		t.Fatalf("%d busy re-checks with no signals performed %d budget/usage reads (totals %v), want 0", wakes, n, kinds)
	}
	// A budget signal for the account is an event: the next reconcile reads.
	sch.busyWorkers.Store(1)
	sch.Notify(pebblestore.SchedulingSignal{Kind: pebblestore.SchedulingSignalBudget, AccountScopeID: schedulerAccount})
	clock.Set(clock.Now().Add(workerSchedulerMinSpacing))
	_ = sch.process(ctx, clock.Now())
	if n := fx.budgetStatusReads(); n == 0 || fx.budgetReads.Load() == startupReads {
		t.Fatal("budget signal for a busy worker did not re-read the budget")
	}
}

// Requirement: a busy worker under a budget hold is not re-read every second.
// With Notify installed as the store's scheduling observer, the reconcile's own
// budget read of an existing hold commits nothing and must not wake the
// scheduler; after the event-driven startup read and any reconcile the hold
// itself triggers, busy re-checks reuse the cached hold state. Threat:
// GetWorkerBudgetStatus -> publishWorkerBudgetHolds sending
// SchedulingSignalBudget on every read that finds a hold, so applySignals marks
// the busy worker stale, reconciles it a second later, reads again and signals
// again for as long as runs are in flight. Owner: WorkerScheduler (Notify,
// applySignals, reconcileEntry) over the real store read and signal paths; the
// in-flight run is a real AdmitWorkerRun record and the hold a real durable
// hold record for the wall-clock UTC day (GetWorkerBudgetStatus dates by the
// wall clock) and the next one.
func TestWorkerSchedulerBusyHeldWorkerIsNotReadEverySecond(t *testing.T) {
	fx := newSchedulerFixture(t, 0, 3600)
	w := fx.createActiveWorkerWith(t, "Busy held worker", []pebblestore.WorkerAutomationDefinition{
		{Name: "hourly", ActivationMode: "interval", Enabled: true, WorkspaceID: "ws-sched", Schedule: &pebblestore.AutomationV2Schedule{Kind: "interval", IntervalSeconds: 3600}},
	})
	start := schedulerTestStart(time.Now())
	clock := newFakeSchedulerClock(start)
	sch := NewWorkerScheduler(fx.service, WorkerSchedulerOptions{Clock: clock})
	ctx := context.Background()
	admitted, err := fx.ws.AdmitWorkerRun(schedulerAccount, pebblestore.WorkerRunAdmission{WorkerID: w.ID, AutomationID: w.Automations[0].ID, OccurrenceID: "wocc_busy_held", RequestSource: "schedule", UserID: schedulerUser})
	if err != nil {
		t.Fatal(err)
	}
	// Cancelled but not yet acknowledged by its executor: the run's session
	// is live (active lifecycle) and has no settled intent, so reconcile
	// keeps it in flight until the executor stops.
	admitted.Status = "running"
	admitted.CancelRequested = true
	if _, err := fx.ws.RecordWorkerRun(schedulerAccount, admitted); err != nil {
		t.Fatal(err)
	}
	sessionStore := pebblestore.NewSessionStore(fx.db)
	if err := sessionStore.CreateSessionForAccount(pebblestore.SessionSnapshot{ID: admitted.SessionID, Title: "Busy held run", Metadata: map[string]any{"worker_execution_run_id": admitted.ID}}, schedulerUser, schedulerAccount); err != nil {
		t.Fatal(err)
	}
	if err := sessionStore.UpsertSessionLifecycle(pebblestore.SessionLifecycleSnapshot{SessionID: admitted.SessionID, UserID: schedulerUser, AccountScopeID: schedulerAccount, Active: true, Phase: "running"}); err != nil {
		t.Fatal(err)
	}
	wall := time.Now().UTC()
	for _, date := range []string{wall.Format("2006-01-02"), wall.AddDate(0, 0, 1).Format("2006-01-02")} {
		hold := pebblestore.WorkerBudgetHold{Date: date, Reason: "daily_budget_exhausted", CapSource: "worker", Dimension: "tokens", Limit: 1, Usage: 1}
		if err := fx.db.PutJSON(pebblestore.KeyWorkerBudgetHold(schedulerAccount, w.ID, date), hold); err != nil {
			t.Fatal(err)
		}
	}
	fx.db.SetSchedulingObserver(sch.Notify)
	t.Cleanup(func() { fx.db.SetSchedulingObserver(nil) })
	_ = sch.process(ctx, start)
	if e := sch.entries[workerKey{schedulerAccount, w.ID}]; e == nil || !e.busy {
		t.Fatalf("worker with a run in flight is not tracked as busy: %+v", e)
	}
	if fx.budgetStatusReads() == 0 {
		t.Fatal("startup rescan did not read the busy worker's budget")
	}
	drive := func(end time.Time) int {
		wakes := 0
		for i := 0; i < 400; i++ {
			next := sch.nextWake()
			select {
			case <-sch.wake:
				next = clock.Now()
			default:
			}
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
		return wakes
	}
	// A real committed worker write while the worker is busy is an event: the
	// next reconcile reads the budget once. Let that reconcile settle.
	clock.Set(start.Add(2 * time.Second))
	policy, err := sessionStore.GetWorkerBudget(schedulerAccount, w.ID)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := sessionStore.SetWorkerBudget(schedulerAccount, w.ID, policy.Revision, policy.DailyCostLimitUSD, policy.DailyTokensLimit); err != nil {
		t.Fatal(err)
	}
	beforeEvent := fx.budgetStatusReads()
	drive(start.Add(5 * time.Second))
	if fx.budgetStatusReads() == beforeEvent {
		t.Fatal("committed worker write did not cause an event-driven budget read")
	}
	settled := fx.budgetStatusReads()
	end := start.Add(workerSchedulerSafetyInterval - 10*time.Second)
	wakes := drive(end)
	if n := fx.budgetStatusReads() - settled; n != 0 {
		t.Fatalf("busy held worker: %d budget status reads over %v (%d wakes) with no new writes, want 0", n, end.Sub(start.Add(5*time.Second)), wakes)
	}
	if e := sch.entries[workerKey{schedulerAccount, w.ID}]; e == nil || !e.busy || !e.budgetHeld {
		t.Fatalf("worker is not tracked as busy and held at the end: %+v", e)
	}
	if max := int(end.Sub(start)/workerSchedulerBusyInterval) + 1; wakes > max {
		t.Fatalf("busy held worker woke %d times, want at most %d busy re-checks", wakes, max)
	}
}
