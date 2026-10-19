package run

import (
	"context"
	"errors"
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
// workers.
type AutomationSweeper interface {
	Sweep(ctx context.Context, now time.Time) error
}

type WorkerSchedulerOptions struct {
	Clock  SchedulerClock
	V2     AutomationSweeper
	Report func(error)
}

// WorkerScheduler owns the daemon's scheduled automation admission loop.
type WorkerScheduler struct {
	service *WorkerExecutionService
	clock   SchedulerClock
	v2      AutomationSweeper
	report  func(error)
	wake    chan struct{}
	last    time.Time
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
	return &WorkerScheduler{service: service, clock: clock, v2: opts.V2, report: report, wake: make(chan struct{}, 1)}
}

// Notify accepts a committed store change.
func (sch *WorkerScheduler) Notify(store.SchedulingSignal) {}

// Run blocks until ctx is cancelled.
func (sch *WorkerScheduler) Run(ctx context.Context) {
	for ctx.Err() == nil {
		now := sch.clock.Now()
		bounded, stop := context.WithTimeout(ctx, 30*time.Second)
		if err := sch.process(bounded, now); err != nil && ctx.Err() == nil {
			sch.report(err)
		}
		stop()
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
	}
}

func (sch *WorkerScheduler) nextWake() time.Time { return sch.last.Add(time.Second) }

// process is the once-a-second sweep: every worker is reconciled and every
// active worker ticked.
func (sch *WorkerScheduler) process(ctx context.Context, now time.Time) error {
	sch.last = now
	var failures []error
	if sch.v2 != nil {
		failures = append(failures, sch.v2.Sweep(ctx, now))
	}
	ws, err := sch.service.WorkerStoreForScheduling()
	if err != nil {
		return err
	}
	cursor := ""
	completed := false
	for page := 0; page < 10; page++ {
		rows, err := ws.ListWorkersForScheduling(cursor, 25)
		if err != nil {
			return err
		}
		for _, w := range rows.Workers {
			if err = ctx.Err(); err != nil {
				return err
			}
			if err = sch.service.ReconcileWorker(ctx, w.AccountScopeID, w.ID); err != nil {
				failures = append(failures, err)
			}
			if w.LifecycleState == store.WorkerLifecycleStateActive {
				if err = sch.service.TickWorker(ctx, w.AccountScopeID, w.ID, now); err != nil {
					failures = append(failures, err)
				}
			}
		}
		if rows.NextCursor == "" {
			completed = true
			break
		}
		cursor = rows.NextCursor
	}
	if !completed {
		failures = append(failures, errors.New("worker sweep exceeded 250-worker budget"))
	}
	sch.service.MarkWorkerSweep(now)
	return errors.Join(failures...)
}
