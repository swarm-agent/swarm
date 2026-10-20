package run

import (
	"container/heap"
	"context"
	"errors"
	"fmt"
	"sync"
	"sync/atomic"
	"time"

	store "swarm/packages/swarmd/internal/store/pebble"
)

// SchedulerClock is the scheduler's only time source so tests can simulate
// time without real sleeps.
type SchedulerClock interface {
	Now() time.Time
	NewTimer(d time.Duration) SchedulerTimer
}

// SchedulerTimer is the subset of time.Timer the scheduler uses.
type SchedulerTimer interface {
	C() <-chan time.Time
	Stop() bool
}

type systemSchedulerClock struct{}

type systemSchedulerTimer struct{ t *time.Timer }

func (systemSchedulerClock) Now() time.Time { return time.Now() }
func (systemSchedulerClock) NewTimer(d time.Duration) SchedulerTimer {
	return systemSchedulerTimer{t: time.NewTimer(d)}
}
func (t systemSchedulerTimer) C() <-chan time.Time { return t.t.C }
func (t systemSchedulerTimer) Stop() bool          { return t.t.Stop() }

// AutomationSweeper is the V2 automation scheduler surface driven alongside
// workers. One Sweep call visits one bounded page of accepted automations.
type AutomationSweeper interface {
	Sweep(ctx context.Context, now time.Time) error
}

// AutomationV2Hinter summarizes V2 automations for the next wake.
type AutomationV2Hinter interface {
	AutomationV2ScheduleHint(now int64) (store.AutomationV2ScheduleHint, error)
}

type WorkerSchedulerOptions struct {
	Clock SchedulerClock
	// V2 and V2Hints drive V2 session automations; both or neither.
	V2      AutomationSweeper
	V2Hints AutomationV2Hinter
	Report  func(error)
}

const (
	// workerSchedulerSafetyInterval bounds drift from a missed signal: every
	// listed worker is reconciled and rescheduled this often.
	workerSchedulerSafetyInterval = 2 * time.Minute
	// workerSchedulerBusyInterval re-observes workers with unfinished runs
	// (watchdog, settlement) between run-finished signals, and retries a
	// failed reconcile.
	workerSchedulerBusyInterval = 5 * time.Second
	// workerSchedulerMinSpacing caps signal-driven work per worker, so a
	// reconcile that writes cannot spin faster than the old sweep did.
	workerSchedulerMinSpacing = time.Second
	// workerSchedulerWorkerBudget is the bounded worker sweep budget (10
	// pages of 25).
	workerSchedulerWorkerBudget = 250
	// automationV2PendingInterval keeps the prior one-page-per-second
	// cadence while durable V2 occurrences are unfinished.
	automationV2PendingInterval = time.Second
	automationV2SweepPage       = 10
	automationV2MaxSweepPasses  = 1002
	// workerSchedulerPassTimeout bounds the worker admission and reconcile
	// work of one wake. V2 sweeping does not share it: a slow worker phase
	// must not leave the V2 pass an expired deadline.
	workerSchedulerPassTimeout = 30 * time.Second
	// automationV2PageTimeout bounds one V2 sweep page; every page gets its
	// own deadline, so a long pass is never failed by an accumulated one.
	automationV2PageTimeout = 30 * time.Second
	// automationV2PassBudget is how long one wake keeps paging a V2 pass
	// before yielding to a busy store. The pass resumes from the sweeper's
	// cursor on a later wake with only its remaining pages.
	automationV2PassBudget = 10 * time.Second
)

type workerKey struct{ account, id string }

// workerSchedEntry is one worker's schedule state. after is the admission
// cursor: TickWorker admits slots in (after, now]. It starts when the daemon
// first sees the worker (so restart skips missed slots) and advances on every
// admission pass or definition change.
type workerSchedEntry struct {
	key           workerKey
	after         time.Time
	due           time.Time
	reconcileAt   time.Time
	lastReconcile time.Time
	changedSince  time.Time
	busy          bool
	// budgetHeld is the hold state from the last budget read. budgetStale
	// asks the next reconcile to read the budget again; only events (budget,
	// worker or run signals, UTC midnight) and the safety rescan set it, never
	// the busy re-check timer.
	budgetHeld  bool
	budgetStale bool
	index       int
}

func (e *workerSchedEntry) at() time.Time {
	switch {
	case e.due.IsZero():
		return e.reconcileAt
	case e.reconcileAt.IsZero() || e.due.Before(e.reconcileAt):
		return e.due
	default:
		return e.reconcileAt
	}
}

// workerSchedHeap is a min-heap of entries by their next action time.
type workerSchedHeap []*workerSchedEntry

func (h workerSchedHeap) Len() int           { return len(h) }
func (h workerSchedHeap) Less(i, j int) bool { return h[i].at().Before(h[j].at()) }
func (h workerSchedHeap) Swap(i, j int) {
	h[i], h[j] = h[j], h[i]
	h[i].index, h[j].index = i, j
}
func (h *workerSchedHeap) Push(x any) {
	e := x.(*workerSchedEntry)
	e.index = len(*h)
	*h = append(*h, e)
}
func (h *workerSchedHeap) Pop() any {
	old := *h
	e := old[len(old)-1]
	old[len(old)-1] = nil
	*h = old[:len(old)-1]
	e.index = -1
	return e
}

// WorkerScheduler owns scheduled automation admission. It sleeps on one timer
// until the earliest worker slot, reconcile, V2 due time, UTC midnight or
// safety rescan, and is woken early by committed store changes. It never
// polls: an idle daemon with nothing due performs no store reads.
type WorkerScheduler struct {
	service *WorkerExecutionService
	clock   SchedulerClock
	v2      AutomationSweeper
	v2Hints AutomationV2Hinter
	report  func(error)
	wake    chan struct{}

	mu             sync.Mutex
	changed        map[workerKey]time.Time
	runSignal      bool
	budgetAccounts map[string]struct{}
	v2Signal       bool
	busyWorkers    atomic.Int64

	// Loop-owned state.
	entries      map[workerKey]*workerSchedEntry
	queue        workerSchedHeap
	nextRescan   time.Time
	nextMidnight time.Time
	v2Dirty      bool
	v2Next       time.Time
	// v2LastDirty is when the last change-driven V2 pass ran.
	v2LastDirty time.Time
	// v2BlockedThrough is the latest due time a full pass left unadmitted;
	// only a record due later triggers another full pass.
	v2BlockedThrough int64
	v2Overflow       bool
	// v2Pending and v2Upcoming are from the last hint. Pending observation
	// wakes page the catalog without re-reading the hint; a settling
	// occurrence commits a V2 mutation, which signals and refreshes them.
	v2Pending  bool
	v2Upcoming time.Time
	// v2PassLeft is the number of pages a started full pass still has to
	// sweep; v2PassTarget is the latest due time the pass was started for.
	// A pass that yields or times out resumes with only these pages.
	v2PassLeft   int
	v2PassTarget int64
	wakeups      atomic.Int64
	// waitHook observes each wait (test seam).
	waitHook func(next time.Time)
}

func NewWorkerScheduler(service *WorkerExecutionService, opts WorkerSchedulerOptions) *WorkerScheduler {
	clock := opts.Clock
	if clock == nil {
		clock = systemSchedulerClock{}
	}
	report := opts.Report
	if report == nil {
		report = func(error) {}
	}
	sch := &WorkerScheduler{service: service, clock: clock, report: report, wake: make(chan struct{}, 1),
		changed: map[workerKey]time.Time{}, budgetAccounts: map[string]struct{}{}, entries: map[workerKey]*workerSchedEntry{}, v2Dirty: true}
	if opts.V2 != nil && opts.V2Hints != nil {
		sch.v2, sch.v2Hints = opts.V2, opts.V2Hints
	}
	return sch
}

// Wakeups reports how many times Run woke from its wait.
func (sch *WorkerScheduler) Wakeups() int64 { return sch.wakeups.Load() }

// Notify accepts a committed store change. It never blocks or reads the
// store; irrelevant signals do not wake the loop.
func (sch *WorkerScheduler) Notify(signal store.SchedulingSignal) {
	if sch == nil {
		return
	}
	now := sch.clock.Now()
	sch.mu.Lock()
	switch signal.Kind {
	case store.SchedulingSignalWorker:
		if signal.AccountScopeID == "" || signal.WorkerID == "" {
			sch.mu.Unlock()
			return
		}
		k := workerKey{signal.AccountScopeID, signal.WorkerID}
		if _, ok := sch.changed[k]; !ok {
			sch.changed[k] = now
		}
	case store.SchedulingSignalRun:
		if sch.busyWorkers.Load() == 0 {
			sch.mu.Unlock()
			return
		}
		sch.runSignal = true
	case store.SchedulingSignalBudget:
		if sch.busyWorkers.Load() == 0 {
			sch.mu.Unlock()
			return
		}
		sch.budgetAccounts[signal.AccountScopeID] = struct{}{}
	case store.SchedulingSignalAutomationV2:
		if sch.v2 == nil {
			sch.mu.Unlock()
			return
		}
		sch.v2Signal = true
	default:
		sch.mu.Unlock()
		return
	}
	sch.mu.Unlock()
	select {
	case sch.wake <- struct{}{}:
	default:
	}
}

// Run blocks until ctx is cancelled.
func (sch *WorkerScheduler) Run(ctx context.Context) {
	for ctx.Err() == nil {
		now := sch.clock.Now()
		// process bounds each phase itself: worker work per wake, V2 work
		// per sweep page and per-wake budget.
		if err := sch.process(ctx, now); err != nil && ctx.Err() == nil {
			sch.report(err)
		}
		next := sch.nextWake()
		if sch.waitHook != nil {
			sch.waitHook(next)
		}
		timer := sch.clock.NewTimer(next.Sub(sch.clock.Now()))
		select {
		case <-ctx.Done():
		case <-timer.C():
		case <-sch.wake:
		}
		timer.Stop()
		sch.wakeups.Add(1)
	}
}

// nextWake is the earliest time the loop has work without a new signal.
func (sch *WorkerScheduler) nextWake() time.Time {
	next := sch.nextRescan
	earlier := func(t time.Time) {
		if !t.IsZero() && (next.IsZero() || t.Before(next)) {
			next = t
		}
	}
	if len(sch.queue) > 0 {
		earlier(sch.queue[0].at())
	}
	earlier(sch.nextMidnight)
	earlier(sch.v2Next)
	return next
}

// process performs every action due at now. Due slots are admitted before
// signals are applied, so a change never moves a cursor past an unadmitted
// slot of the definition that was current when the slot fell due.
func (sch *WorkerScheduler) process(ctx context.Context, now time.Time) error {
	workerCtx, stop := context.WithTimeout(ctx, workerSchedulerPassTimeout)
	defer stop()
	var failures []error
	failures = append(failures, sch.admitDue(workerCtx, now))
	sch.applySignals(now)
	if sch.nextRescan.IsZero() || !now.Before(sch.nextRescan) {
		failures = append(failures, sch.rescan(workerCtx, now))
		sch.nextRescan = now.Add(workerSchedulerSafetyInterval)
	}
	if !sch.nextMidnight.IsZero() && !now.Before(sch.nextMidnight) {
		// The UTC day rolled: budget holds lift and daily caps reset.
		for _, e := range sch.entries {
			sch.requestReconcile(e, now)
		}
		sch.v2Dirty = true
	}
	if sch.nextMidnight.IsZero() || !now.Before(sch.nextMidnight) {
		y, m, d := now.UTC().Date()
		sch.nextMidnight = time.Date(y, m, d+1, 0, 0, 0, 0, time.UTC)
	}
	failures = append(failures, sch.reconcileDue(workerCtx, now))
	failures = append(failures, sch.admitDue(workerCtx, now))
	workerErr := errors.Join(failures...)
	v2Err := sch.processV2(ctx, now)
	sch.busyWorkers.Store(int64(sch.countBusy()))
	return errors.Join(labelSchedulerError("worker scheduling", workerErr), labelSchedulerError("automation v2 sweep", v2Err))
}

// labelSchedulerError names the phase that failed, so a worker reconcile
// deadline is not reported as a V2 sweep failure.
func labelSchedulerError(phase string, err error) error {
	if err == nil {
		return nil
	}
	return fmt.Errorf("%s: %w", phase, err)
}

func (sch *WorkerScheduler) countBusy() int {
	n := 0
	for _, e := range sch.entries {
		if e.busy {
			n++
		}
	}
	return n
}

func (sch *WorkerScheduler) fix(e *workerSchedEntry) {
	switch {
	case e.at().IsZero() && e.index >= 0:
		heap.Remove(&sch.queue, e.index)
	case e.at().IsZero():
	case e.index >= 0:
		heap.Fix(&sch.queue, e.index)
	default:
		heap.Push(&sch.queue, e)
	}
}

func (sch *WorkerScheduler) remove(e *workerSchedEntry) {
	if e.index >= 0 {
		heap.Remove(&sch.queue, e.index)
	}
	delete(sch.entries, e.key)
}

func (sch *WorkerScheduler) entry(k workerKey, after time.Time) *workerSchedEntry {
	if e := sch.entries[k]; e != nil {
		return e
	}
	if len(sch.entries) >= workerSchedulerWorkerBudget {
		return nil
	}
	e := &workerSchedEntry{key: k, after: after, budgetStale: true, index: -1}
	sch.entries[k] = e
	return e
}

// requestReconcile schedules an event-driven reconcile no sooner than the
// per-worker spacing allows. Every caller is an event, so it also re-reads
// the budget.
func (sch *WorkerScheduler) requestReconcile(e *workerSchedEntry, now time.Time) {
	e.budgetStale = true
	at := now
	if !e.lastReconcile.IsZero() {
		if spaced := e.lastReconcile.Add(workerSchedulerMinSpacing); spaced.After(at) {
			at = spaced
		}
	}
	if e.reconcileAt.IsZero() || at.Before(e.reconcileAt) {
		e.reconcileAt = at
	}
	sch.fix(e)
}

func (sch *WorkerScheduler) applySignals(now time.Time) {
	sch.mu.Lock()
	changed := sch.changed
	sch.changed = map[workerKey]time.Time{}
	run := sch.runSignal
	sch.runSignal = false
	budget := sch.budgetAccounts
	sch.budgetAccounts = map[string]struct{}{}
	v2 := sch.v2Signal
	sch.v2Signal = false
	sch.mu.Unlock()
	for k, at := range changed {
		// Notify stamps the wall clock. A change committed during this pass
		// (above all, the pass's own admission) is later than now; moving the
		// cursor past now would skip a sibling slot that fell due while
		// Dispatch ran. The old sweep's cursor was its start time, so clamp.
		if at.After(now) {
			at = now
		}
		e := sch.entry(k, at)
		if e == nil {
			sch.report(errors.New("worker scheduler exceeded 250-worker budget"))
			continue
		}
		if e.changedSince.IsZero() || at.Before(e.changedSince) {
			e.changedSince = at
		}
		sch.requestReconcile(e, now)
	}
	if run || len(budget) > 0 {
		for _, e := range sch.entries {
			if !e.busy {
				continue
			}
			if _, held := budget[e.key.account]; run || held {
				sch.requestReconcile(e, now)
			}
		}
	}
	if v2 {
		sch.v2Dirty = true
	}
}

// rescan is the startup pass and the slow drift safety net: it lists the
// bounded worker catalog, reconciles each worker and recomputes its due time.
func (sch *WorkerScheduler) rescan(ctx context.Context, now time.Time) error {
	sch.v2Dirty = true
	if sch.service == nil {
		return nil
	}
	ws, err := sch.service.WorkerStoreForScheduling()
	if err != nil {
		return err
	}
	var failures []error
	seen := map[workerKey]bool{}
	cursor := ""
	completed := false
	for page := 0; page < 10; page++ {
		rows, err := ws.ListWorkersForScheduling(cursor, 25)
		if err != nil {
			return errors.Join(append(failures, err)...)
		}
		for _, w := range rows.Workers {
			if err = ctx.Err(); err != nil {
				return err
			}
			k := workerKey{w.AccountScopeID, w.ID}
			seen[k] = true
			e := sch.entry(k, now)
			if e == nil {
				continue
			}
			e.budgetStale = true
			failures = append(failures, sch.reconcileEntry(ctx, ws, e, now))
		}
		if rows.NextCursor == "" {
			completed = true
			break
		}
		cursor = rows.NextCursor
	}
	if !completed {
		failures = append(failures, errors.New("worker sweep exceeded 250-worker budget"))
		return errors.Join(failures...)
	}
	for k, e := range sch.entries {
		if !seen[k] {
			sch.remove(e)
		}
	}
	return errors.Join(failures...)
}

func (sch *WorkerScheduler) reconcileDue(ctx context.Context, now time.Time) error {
	if sch.service == nil {
		return nil
	}
	var due []*workerSchedEntry
	for _, e := range sch.entries {
		if !e.reconcileAt.IsZero() && !e.reconcileAt.After(now) {
			due = append(due, e)
		}
	}
	if len(due) == 0 {
		return nil
	}
	ws, err := sch.service.WorkerStoreForScheduling()
	if err != nil {
		return err
	}
	var failures []error
	for _, e := range due {
		if err := ctx.Err(); err != nil {
			return err
		}
		failures = append(failures, sch.reconcileEntry(ctx, ws, e, now))
	}
	return errors.Join(failures...)
}

// reconcileEntry runs ReconcileWorker and recomputes the next due slot from
// the entry's cursor, moved to the first unapplied change. The budget is read
// only when an event or the rescan marked it stale; a busy re-check applies
// the hold state from that read. A failed read stays stale and is retried.
func (sch *WorkerScheduler) reconcileEntry(ctx context.Context, ws *store.WorkerStore, e *workerSchedEntry, now time.Time) error {
	busy, held, known, err := sch.service.reconcileWorker(ctx, e.key.account, e.key.id, e.budgetStale, e.budgetHeld)
	e.lastReconcile, e.reconcileAt = now, time.Time{}
	if errors.Is(err, store.ErrWorkerNotFound) {
		sch.remove(e)
		return nil
	}
	e.busy = busy
	if known {
		e.budgetHeld, e.budgetStale = held, false
	}
	if busy || err != nil {
		e.reconcileAt = now.Add(workerSchedulerBusyInterval)
	}
	if e.changedSince.After(e.after) {
		e.after = e.changedSince
	}
	e.changedSince = time.Time{}
	return errors.Join(err, sch.reschedule(ws, e, now))
}

// reschedule recomputes the entry's next due slot. When the worker cannot be
// read, a due time that is not in the future backs off by the busy interval:
// a past due time would make nextWake return the past and spin the loop.
func (sch *WorkerScheduler) reschedule(ws *store.WorkerStore, e *workerSchedEntry, now time.Time) error {
	w, found, err := ws.GetWorker(e.key.account, e.key.id)
	if err != nil {
		if !e.due.After(now) {
			e.due = now.Add(workerSchedulerBusyInterval)
		}
		sch.fix(e)
		return err
	}
	if !found {
		sch.remove(e)
		return nil
	}
	next, err := NextWorkerScheduledAt(w, e.after)
	e.due = time.Time{}
	if next > 0 {
		e.due = time.UnixMilli(next)
	}
	sch.fix(e)
	return err
}

// admitDue ticks every worker whose slot fell due by now, then advances its
// cursor to now: a failed admission never replays a stale slot later.
func (sch *WorkerScheduler) admitDue(ctx context.Context, now time.Time) error {
	if sch.service == nil {
		return nil
	}
	var due []*workerSchedEntry
	for _, e := range sch.entries {
		if !e.due.IsZero() && !e.due.After(now) {
			due = append(due, e)
		}
	}
	if len(due) == 0 {
		return nil
	}
	ws, err := sch.service.WorkerStoreForScheduling()
	if err != nil {
		return err
	}
	var failures []error
	for _, e := range due {
		if err := ctx.Err(); err != nil {
			return err
		}
		err := sch.service.TickWorker(ctx, e.key.account, e.key.id, e.after, now)
		if errors.Is(err, store.ErrWorkerNotFound) {
			sch.remove(e)
			continue
		}
		failures = append(failures, err)
		if now.After(e.after) {
			e.after = now
		}
		failures = append(failures, sch.reschedule(ws, e, now))
	}
	return errors.Join(failures...)
}

// v2DueTimes derives, from a hint, the latest already-due and the earliest
// upcoming V2 due times in unix milliseconds (zero when none).
func v2DueTimes(hint store.AutomationV2ScheduleHint, nowMs int64) (latestDue, upcoming int64) {
	latestDue, upcoming = hint.LatestDueAt, hint.UpcomingDueAt
	if upcoming > 0 && upcoming <= nowMs {
		// An upcoming time that is no longer in the future is due; as a
		// wake time it would be in the past and spin the loop.
		if upcoming > latestDue {
			latestDue = upcoming
		}
		upcoming = 0
	}
	if e := hint.EarliestDueAt; e > 0 {
		if e <= nowMs && e > latestDue {
			latestDue = e
		}
		if e > nowMs && upcoming == 0 {
			upcoming = e
		}
	}
	return latestDue, upcoming
}

// processV2 sweeps V2 automations only when one is newly due or occurrences
// are unfinished.
//   - A record that fell due since the last full pass gets one full pass:
//     every page once, then one hint read to see what advanced.
//   - A record still due after that pass could not be admitted (serialize
//     overlap, a conflict or an admission error). It is blocked: no further
//     full passes until a committed V2 change, the safety rescan or UTC
//     midnight clears the block.
//   - While occurrences are pending, the prior cadence holds: one sweep page
//     per second, without re-reading the hint, which also re-ticks blocked
//     records as their page comes round. The hint is re-read only on a
//     committed V2 change, a newly due time, the rescan or UTC midnight.
//   - Committed changes are coalesced to at most one pass per second.
//   - Above the bounded hint the catalog is paged one page per second.
//   - A full pass is bounded per page (automationV2PageTimeout) and per wake
//     (automationV2PassBudget), never by one deadline for the whole pass.
//     On a busy store it yields and resumes a second later with only its
//     remaining pages, from the sweeper's cursor; a timed-out page is
//     retried after the busy back-off. Neither restarts the pass.
func (sch *WorkerScheduler) processV2(ctx context.Context, now time.Time) error {
	if sch.v2 == nil {
		return nil
	}
	if sch.v2Dirty {
		if !sch.v2LastDirty.IsZero() {
			if at := sch.v2LastDirty.Add(workerSchedulerMinSpacing); now.Before(at) {
				// A v2Next that is not after now has already been reached
				// (for example an upcoming slot earlier than at); keeping
				// it would leave a past wake and spin the loop.
				if sch.v2Next.IsZero() || !sch.v2Next.After(now) || at.Before(sch.v2Next) {
					sch.v2Next = at
				}
				return nil
			}
		}
		sch.v2Dirty = false
		sch.v2LastDirty = now
		sch.v2BlockedThrough = 0
	} else if sch.v2Next.IsZero() || now.Before(sch.v2Next) {
		return nil
	} else if sch.v2PassLeft == 0 && (sch.v2Pending || sch.v2Overflow) && (sch.v2Upcoming.IsZero() || now.Before(sch.v2Upcoming)) {
		// Timed observation wake: one page, the old loop's cost. The hint
		// decodes every accepted record, so it is re-read only on a
		// committed V2 change, a newly due time, the rescan or midnight.
		timedOut, err := sch.sweepV2Page(ctx, now)
		if timedOut {
			sch.v2Next = sch.after(now, workerSchedulerBusyInterval)
			return err
		}
		sch.v2Next = sch.after(now, automationV2PendingInterval)
		if !sch.v2Upcoming.IsZero() && sch.v2Upcoming.Before(sch.v2Next) {
			sch.v2Next = sch.v2Upcoming
		}
		return err
	}
	if sch.v2PassLeft > 0 {
		// A started pass continues from the sweeper's cursor. A V2 change
		// committed meanwhile is covered by the hint read that ends it.
		return sch.continueV2Pass(ctx, now)
	}
	nowMs := now.UnixMilli()
	hint, err := sch.v2Hints.AutomationV2ScheduleHint(nowMs)
	if err != nil {
		sch.v2Next = sch.after(now, workerSchedulerBusyInterval)
		sch.v2Pending, sch.v2Overflow = false, false
		return err
	}
	if hint.Overflow {
		return sch.pageV2Overflow(ctx, now)
	}
	sch.v2Overflow = false
	latestDue, upcoming := v2DueTimes(hint, nowMs)
	if latestDue > sch.v2BlockedThrough {
		sch.v2PassLeft, sch.v2PassTarget = v2FullPassPages(hint), latestDue
		return sch.continueV2Pass(ctx, now)
	}
	if !hint.Pending {
		sch.setV2Wake(false, upcoming, now)
		return nil
	}
	timedOut, err := sch.sweepV2Page(ctx, now)
	sch.setV2Wake(true, upcoming, now)
	if timedOut {
		sch.v2Next = sch.after(now, workerSchedulerBusyInterval)
	}
	return err
}

// v2FullPassPages is the page count that visits every accepted entry once.
func v2FullPassPages(hint store.AutomationV2ScheduleHint) int {
	passes := hint.AcceptedKeys/automationV2SweepPage + 2
	if passes > automationV2MaxSweepPasses {
		passes = automationV2MaxSweepPasses
	}
	return passes
}

// continueV2Pass sweeps the remaining pages of the current full pass until it
// is done or this wake's budget is spent, then ends a finished pass with one
// hint read. A record that fell due after the pass started gets a new pass;
// one already due when it started and still due is blocked as before.
func (sch *WorkerScheduler) continueV2Pass(ctx context.Context, now time.Time) error {
	var failures []error
	for sch.v2PassLeft > 0 {
		if err := ctx.Err(); err != nil {
			return errors.Join(append(failures, err)...)
		}
		if sch.clock.Now().Sub(now) >= automationV2PassBudget {
			// The store is busy: yield and resume the remaining pages after
			// the usual spacing. Bounded by v2PassLeft, so not a retry loop.
			sch.v2Next = sch.after(now, workerSchedulerMinSpacing)
			return errors.Join(failures...)
		}
		timedOut, err := sch.sweepV2Page(ctx, now)
		if timedOut {
			// The page is retried, from the same cursor, after the busy
			// back-off; the pages already swept are not repeated.
			sch.v2Next = sch.after(now, workerSchedulerBusyInterval)
			return errors.Join(append(failures, err)...)
		}
		failures = append(failures, err)
		sch.v2PassLeft--
	}
	nowMs := now.UnixMilli()
	hint, err := sch.v2Hints.AutomationV2ScheduleHint(nowMs)
	if err != nil {
		sch.v2Next = sch.after(now, workerSchedulerBusyInterval)
		sch.v2Pending, sch.v2Overflow = false, false
		return errors.Join(append(failures, err)...)
	}
	if hint.Overflow {
		return errors.Join(append(failures, sch.pageV2Overflow(ctx, now))...)
	}
	latestDue, upcoming := v2DueTimes(hint, nowMs)
	if latestDue > sch.v2PassTarget {
		// Fell due while the pass ran, possibly behind the cursor.
		sch.v2PassLeft, sch.v2PassTarget = v2FullPassPages(hint), latestDue
		sch.v2Pending, sch.v2Upcoming = hint.Pending, time.Time{}
		sch.v2Next = sch.after(now, workerSchedulerMinSpacing)
		return errors.Join(failures...)
	}
	sch.v2BlockedThrough = latestDue
	sch.setV2Wake(hint.Pending, upcoming, now)
	return errors.Join(failures...)
}

// sweepV2Page runs one sweep page under its own deadline. timedOut reports
// that the page, not the scheduler, ran out of time.
func (sch *WorkerScheduler) sweepV2Page(ctx context.Context, now time.Time) (timedOut bool, err error) {
	pageCtx, stop := context.WithTimeout(ctx, automationV2PageTimeout)
	defer stop()
	err = sch.v2.Sweep(pageCtx, now)
	return err != nil && ctx.Err() == nil && errors.Is(err, context.DeadlineExceeded), err
}

// after is d past the later of now and the clock: spacing that work took
// longer than d to do must not leave a wake in the past.
func (sch *WorkerScheduler) after(now time.Time, d time.Duration) time.Time {
	if c := sch.clock.Now(); c.After(now) {
		now = c
	}
	return now.Add(d)
}

// setV2Wake records the hint's pending/upcoming state and the next V2 wake.
func (sch *WorkerScheduler) setV2Wake(pending bool, upcoming int64, now time.Time) {
	sch.v2Pending, sch.v2Upcoming = pending, time.Time{}
	sch.v2Next = time.Time{}
	if upcoming > 0 {
		sch.v2Upcoming = time.UnixMilli(upcoming)
		sch.v2Next = sch.v2Upcoming
	}
	if pending {
		if p := sch.after(now, automationV2PendingInterval); sch.v2Next.IsZero() || p.Before(sch.v2Next) {
			sch.v2Next = p
		}
	}
}

// pageV2Overflow keeps a catalog above the bounded hint swept at the prior
// one-page-per-second cadence and reports the degradation once.
func (sch *WorkerScheduler) pageV2Overflow(ctx context.Context, now time.Time) error {
	var failures []error
	if !sch.v2Overflow {
		failures = append(failures, errors.New("automation v2 catalog exceeds the bounded schedule hint; sweeping one page per second"))
	}
	sch.v2Overflow = true
	sch.v2BlockedThrough = 0
	sch.v2PassLeft = 0
	sch.v2Pending, sch.v2Upcoming = false, time.Time{}
	timedOut, err := sch.sweepV2Page(ctx, now)
	failures = append(failures, err)
	sch.v2Next = sch.after(now, automationV2PendingInterval)
	if timedOut {
		sch.v2Next = sch.after(now, workerSchedulerBusyInterval)
	}
	return errors.Join(failures...)
}
