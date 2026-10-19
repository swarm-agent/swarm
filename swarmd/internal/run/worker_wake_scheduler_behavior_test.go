package run

import (
	"context"
	"sync"
	"testing"
	"time"

	pebblestore "swarm/packages/swarmd/internal/store/pebble"
)

// dispatchRecorder replaces only the Dispatch boundary of a due scheduled
// slot, so these tests observe exactly which occurrences were admitted.
type dispatchRecorder struct {
	mu   sync.Mutex
	reqs []pebblestore.WorkerRunAdmission
}

func (d *dispatchRecorder) record(_, _ string, req pebblestore.WorkerRunAdmission) error {
	d.mu.Lock()
	defer d.mu.Unlock()
	d.reqs = append(d.reqs, req)
	return nil
}

func (d *dispatchRecorder) dueAts() []int64 {
	d.mu.Lock()
	defer d.mu.Unlock()
	out := make([]int64, 0, len(d.reqs))
	for _, r := range d.reqs {
		out = append(out, r.Input["due_at"].(int64))
	}
	return out
}

func (d *dispatchRecorder) occurrences() map[string]int {
	d.mu.Lock()
	defer d.mu.Unlock()
	out := map[string]int{}
	for _, r := range d.reqs {
		out[r.OccurrenceID]++
	}
	return out
}

type schedulerHarness struct {
	fx       *schedulerFixture
	clock    *fakeSchedulerClock
	sch      *WorkerScheduler
	dispatch *dispatchRecorder
	ctx      context.Context
}

func newSchedulerHarness(t *testing.T, fx *schedulerFixture, start time.Time) *schedulerHarness {
	t.Helper()
	h := &schedulerHarness{fx: fx, clock: newFakeSchedulerClock(start), dispatch: &dispatchRecorder{}, ctx: context.Background()}
	fx.service.scheduledDispatchForTest = h.dispatch.record
	h.sch = NewWorkerScheduler(fx.service, WorkerSchedulerOptions{Clock: h.clock, Report: func(err error) { t.Errorf("scheduler report: %v", err) }})
	fx.db.SetSchedulingObserver(h.sch.Notify)
	t.Cleanup(func() { fx.db.SetSchedulingObserver(nil) })
	if err := h.sch.process(h.ctx, start); err != nil {
		t.Fatalf("startup pass: %v", err)
	}
	return h
}

// pass processes pending signals at the current simulated time, as a wake
// from Notify does.
func (h *schedulerHarness) pass(t *testing.T) {
	t.Helper()
	if err := h.sch.process(h.ctx, h.clock.Now()); err != nil {
		t.Fatalf("scheduler pass: %v", err)
	}
}

func (h *schedulerHarness) run(t *testing.T, end time.Time) int {
	t.Helper()
	return simulate(h.ctx, h.sch, h.clock, end)
}

func firstSlotAfter(t *testing.T, w pebblestore.WorkerRecord, after time.Time) time.Time {
	t.Helper()
	next, err := NextWorkerScheduledAt(w, after)
	if err != nil || next <= 0 {
		t.Fatalf("next slot after %v: %d, %v", after, next, err)
	}
	return time.UnixMilli(next)
}

// housekeepingWakes counts the safety rescans and UTC midnights the scheduler
// is expected to wake for in (start, end], besides due slots.
func housekeepingWakes(start, end time.Time) int {
	n := 0
	for at := start.Add(workerSchedulerSafetyInterval); !at.After(end); at = at.Add(workerSchedulerSafetyInterval) {
		n++
	}
	y, m, d := start.UTC().Date()
	if midnight := time.Date(y, m, d+1, 0, 0, 0, 0, time.UTC); !midnight.After(end) {
		n++
	}
	return n
}

// earliestWake is the first wake the scheduler should set: the due slot or
// the safety rescan, whichever is first.
func earliestWake(slot, start time.Time) time.Time {
	if rescan := start.Add(workerSchedulerSafetyInterval); rescan.Before(slot) {
		return rescan
	}
	return slot
}

func (fx *schedulerFixture) budgetStatusReads() int64 {
	if v, ok := fx.readKinds.Load(pebblestore.BudgetReadWorkerBudgetStatus); ok {
		return v.(interface{ Load() int64 }).Load()
	}
	return 0
}

// Requirement: a due slot is admitted at its due time, exactly once, with the
// budget read only at that admission. Threat: an event-driven scheduler that
// oversleeps, double-fires a slot, or still reads the budget while waiting.
// Owner: WorkerScheduler.process/nextWake and TickWorker over a real store.
func TestWorkerSchedulerFiresDueSlotsOnTimeOnce(t *testing.T) {
	fx := newSchedulerFixture(t, 1, 60)
	start := time.Now()
	h := newSchedulerHarness(t, fx, start)
	first := firstSlotAfter(t, fx.workers[0], start)
	if got := h.sch.nextWake(); !got.Equal(first) {
		t.Fatalf("next wake = %v, want first slot %v", got, first)
	}
	if wakes := h.run(t, first.Add(-time.Millisecond)); wakes != 0 || len(h.dispatch.dueAts()) != 0 || fx.budgetReads.Load() != 0 {
		t.Fatalf("before the slot: wakes=%d dispatches=%v budget reads=%d, want none", wakes, h.dispatch.dueAts(), fx.budgetReads.Load())
	}
	second := first.Add(time.Minute)
	end := second.Add(30 * time.Second)
	if wakes, want := h.run(t, end), 2+housekeepingWakes(start, end); wakes != want {
		t.Fatalf("wakes through two slots = %d, want %d (2 slots + rescans/midnight)", wakes, want)
	}
	if got := h.dispatch.dueAts(); len(got) != 2 || got[0] != first.UnixMilli() || got[1] != second.UnixMilli() {
		t.Fatalf("dispatched due_at = %v, want [%d %d]", got, first.UnixMilli(), second.UnixMilli())
	}
	for occ, n := range h.dispatch.occurrences() {
		if n != 1 {
			t.Fatalf("occurrence %s admitted %d times, want once", occ, n)
		}
	}
	if n := fx.budgetStatusReads(); n != 2 {
		t.Fatalf("budget status reads = %d, want exactly one per admitted slot (2)", n)
	}
	// Reprocessing the same instant must not admit the slot again.
	h.pass(t)
	if got := h.dispatch.dueAts(); len(got) != 2 {
		t.Fatalf("re-pass dispatched again: %v", got)
	}
}

// Requirement: an accepted schedule edit reschedules the worker without
// waiting for the old due time. Threat: the scheduler sleeping on a stale
// heap entry. Owner: commitWorkerRealtime scheduling signal -> Notify ->
// reconcileEntry/reschedule.
func TestWorkerSchedulerEditReschedules(t *testing.T) {
	fx := newSchedulerFixture(t, 1, 3600)
	start := time.Now()
	h := newSchedulerHarness(t, fx, start)
	w := fx.workers[0]
	if got, want := h.sch.nextWake(), earliestWake(firstSlotAfter(t, w, start), start); !got.Equal(want) {
		t.Fatalf("initial next wake = %v, want %v", got, want)
	}
	editAt := start.Add(5 * time.Second)
	h.clock.Set(editAt)
	auto := w.Automations[0]
	auto.Schedule = &pebblestore.AutomationV2Schedule{Kind: "interval", IntervalSeconds: 90}
	proposed, err := fx.ws.UpdateWorkerAutomation(schedulerAccount, schedulerUser, w.ID, auto.ID, w.Revision, auto, nil)
	if err != nil {
		t.Fatal(err)
	}
	if proposed.PendingReview != nil {
		if proposed, err = fx.ws.AcceptWorker(schedulerAccount, schedulerUser, w.ID, proposed.Revision, nil); err != nil {
			t.Fatal(err)
		}
	}
	select {
	case <-h.sch.wake:
	default:
		t.Fatal("accepted edit did not wake the scheduler")
	}
	h.pass(t)
	want := firstSlotAfter(t, proposed, editAt)
	if want.After(editAt.Add(90 * time.Second)) {
		t.Fatalf("edited schedule next slot %v is not within the new interval", want)
	}
	if got := h.sch.nextWake(); !got.Equal(want) {
		t.Fatalf("next wake after edit = %v, want %v", got, want)
	}
	h.run(t, want)
	got := h.dispatch.dueAts()
	if len(got) != 1 || got[0] != want.UnixMilli() {
		t.Fatalf("dispatched after edit = %v, want [%d]", got, want.UnixMilli())
	}
	for _, r := range h.dispatch.reqs {
		if r.AutomationID != auto.ID {
			t.Fatalf("dispatched automation %s, want %s", r.AutomationID, auto.ID)
		}
	}
}

// Requirement: disabling an automation cancels its pending wake. Threat: a
// disabled schedule still firing from the heap. Owner: DisableWorkerAutomation
// signal -> reschedule; TickWorker re-checks the current definition.
func TestWorkerSchedulerDisableCancels(t *testing.T) {
	fx := newSchedulerFixture(t, 1, 60)
	start := time.Now()
	h := newSchedulerHarness(t, fx, start)
	w := fx.workers[0]
	due := firstSlotAfter(t, w, start)
	h.clock.Set(start.Add(time.Second))
	if _, _, err := fx.ws.DisableWorkerAutomation(schedulerAccount, schedulerUser, w.ID, w.Automations[0].ID, w.Revision); err != nil {
		t.Fatal(err)
	}
	h.pass(t)
	if next, want := h.sch.nextWake(), earliestWake(time.Date(9999, 1, 1, 0, 0, 0, 0, time.UTC), start); !next.Equal(want) && next.Before(want) {
		t.Fatalf("next wake after disable = %v, want no slot wake (rescan %v or midnight only)", next, want)
	}
	h.run(t, due.Add(3*time.Minute))
	if got := h.dispatch.dueAts(); len(got) != 0 {
		t.Fatalf("disabled automation dispatched %v", got)
	}
}

// Requirement: a restarted daemon skips slots that fell due while it was
// offline and never replays them (the lastScheduled fence). Threat: catch-up
// storms after restart. Owner: WorkerScheduler startup rescan cursor.
func TestWorkerSchedulerRestartSkipsMissedSlots(t *testing.T) {
	fx := newSchedulerFixture(t, 1, 60)
	start := time.Now()
	w := fx.workers[0]
	first := firstSlotAfter(t, w, start)
	// The daemon was offline across three slots and restarts mid-interval.
	restart := first.Add(2*time.Minute + 20*time.Second)
	h := newSchedulerHarness(t, fx, restart)
	if got := h.dispatch.dueAts(); len(got) != 0 {
		t.Fatalf("startup replayed missed slots: %v", got)
	}
	next := firstSlotAfter(t, w, restart)
	if got := h.sch.nextWake(); !got.Equal(next) {
		t.Fatalf("next wake after restart = %v, want %v", got, next)
	}
	h.run(t, next)
	if got := h.dispatch.dueAts(); len(got) != 1 || got[0] != next.UnixMilli() {
		t.Fatalf("dispatched after restart = %v, want only [%d]", got, next.UnixMilli())
	}
}

// Requirement: a budget hold blocks admission at the due time, and the slot
// is skipped rather than retried. Threat: budget checks moved out of the
// polling path being skipped at admission. Owner: TickWorker budget check.
func TestWorkerSchedulerBudgetHoldBlocksAdmissionAtDueTime(t *testing.T) {
	fx := newSchedulerFixture(t, 1, 60)
	start := time.Now()
	w := fx.workers[0]
	date := time.Now().UTC().Format("2006-01-02")
	hold := pebblestore.WorkerBudgetHold{Date: date, Reason: "daily_budget_exhausted", CapSource: "worker", Dimension: "tokens", Limit: 1, Usage: 1}
	if err := fx.db.PutJSON(pebblestore.KeyWorkerBudgetHold(schedulerAccount, w.ID, date), hold); err != nil {
		t.Fatal(err)
	}
	h := newSchedulerHarness(t, fx, start)
	due := firstSlotAfter(t, w, start)
	h.run(t, due.Add(-time.Millisecond))
	if n := fx.budgetStatusReads(); n != 0 {
		t.Fatalf("budget read %d times before the slot was due, want 0", n)
	}
	h.run(t, due)
	if n := fx.budgetStatusReads(); n != 1 {
		t.Fatalf("budget status reads at the due slot = %d, want 1", n)
	}
	if got := h.dispatch.dueAts(); len(got) != 0 {
		t.Fatalf("held worker dispatched %v", got)
	}
	if next, want := h.sch.nextWake(), earliestWake(due.Add(time.Minute), start); !next.Equal(want) {
		t.Fatalf("next wake after held slot = %v, want the following slot %v", next, want)
	}
}

// Requirement: the running loop waits on a single timer set to the earliest
// due time, wakes for no per-second ticks, and wakes early for a committed
// worker change. Threat: Run re-arming a short polling timer. Owner:
// WorkerScheduler.Run with Notify installed as the store scheduling observer.
func TestWorkerSchedulerRunSleepsUntilDueAndWakesOnChange(t *testing.T) {
	fx := newSchedulerFixture(t, 2, 3600)
	start := time.Now()
	clock := newFakeSchedulerClock(start)
	sch := NewWorkerScheduler(fx.service, WorkerSchedulerOptions{Clock: clock})
	waits := make(chan time.Time, 16)
	sch.waitHook = func(next time.Time) { waits <- next }
	fx.db.SetSchedulingObserver(sch.Notify)
	t.Cleanup(func() { fx.db.SetSchedulingObserver(nil) })
	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan struct{})
	go func() { defer close(done); sch.Run(ctx) }()
	t.Cleanup(func() { cancel(); <-done })
	receive := func() time.Time {
		select {
		case next := <-waits:
			return next
		case <-time.After(10 * time.Second):
			t.Fatal("scheduler did not reach its wait")
			return time.Time{}
		}
	}
	first := receive()
	want := firstSlotAfter(t, fx.workers[0], start)
	if other := firstSlotAfter(t, fx.workers[1], start); other.Before(want) {
		want = other
	}
	want = earliestWake(want, start)
	if !first.Equal(want) {
		t.Fatalf("first wait until %v, want earliest due slot %v", first, want)
	}
	clock.Set(start.Add(10 * time.Second))
	select {
	case next := <-waits:
		t.Fatalf("scheduler woke during 10 idle simulated seconds (next %v)", next)
	default:
	}
	if n := sch.Wakeups(); n != 0 {
		t.Fatalf("idle wakeups = %d, want 0", n)
	}
	if n := fx.budgetReads.Load(); n != 0 {
		t.Fatalf("idle loop budget/usage reads = %d, want 0", n)
	}
	created := fx.createActiveWorker(t, "Late worker", 60)
	deadline := firstSlotAfter(t, created, clock.Now())
	for next := receive(); !next.Equal(deadline); next = receive() {
		if next.After(want) {
			t.Fatalf("after a new worker, next wait %v, want its first slot %v", next, deadline)
		}
	}
}
