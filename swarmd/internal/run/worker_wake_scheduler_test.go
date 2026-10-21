package run

import (
	"context"
	"fmt"
	"path/filepath"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	sessionruntime "swarm/packages/swarmd/internal/session"
	pebblestore "swarm/packages/swarmd/internal/store/pebble"
)

// fakeSchedulerClock is simulated time: timers fire only when the test
// advances the clock, so no assertion depends on real sleeps.
type fakeSchedulerClock struct {
	mu     sync.Mutex
	now    time.Time
	timers []*fakeSchedulerTimer
}

type fakeSchedulerTimer struct {
	clock    *fakeSchedulerClock
	deadline time.Time
	ch       chan time.Time
	active   bool
}

// schedulerTestStart is the fixed simulated start for scheduler tests: the
// first 12:00 UTC at or after notBefore. Seeding the fake clock from it keeps
// every test window twelve hours from the UTC-midnight wake, whatever the wall
// clock, while never starting before the workers' CreatedAt (pass a time taken
// after the fixture's workers were created).
func schedulerTestStart(notBefore time.Time) time.Time {
	y, m, d := notBefore.UTC().Date()
	noon := time.Date(y, m, d, 12, 0, 0, 0, time.UTC)
	if noon.Before(notBefore) {
		noon = noon.AddDate(0, 0, 1)
	}
	return noon
}

func newFakeSchedulerClock(now time.Time) *fakeSchedulerClock {
	return &fakeSchedulerClock{now: now}
}

func (c *fakeSchedulerClock) Now() time.Time {
	c.mu.Lock()
	defer c.mu.Unlock()
	return c.now
}

func (c *fakeSchedulerClock) NewTimer(d time.Duration) SchedulerTimer {
	c.mu.Lock()
	defer c.mu.Unlock()
	t := &fakeSchedulerTimer{clock: c, deadline: c.now.Add(d), ch: make(chan time.Time, 1), active: true}
	if d <= 0 {
		t.active = false
		t.ch <- c.now
		return t
	}
	c.timers = append(c.timers, t)
	return t
}

func (c *fakeSchedulerClock) Set(now time.Time) {
	c.mu.Lock()
	defer c.mu.Unlock()
	c.now = now
	for _, t := range c.timers {
		if t.active && !t.deadline.After(now) {
			t.active = false
			t.ch <- now
		}
	}
}

func (t *fakeSchedulerTimer) C() <-chan time.Time { return t.ch }
func (t *fakeSchedulerTimer) Stop() bool {
	t.clock.mu.Lock()
	defer t.clock.mu.Unlock()
	was := t.active
	t.active = false
	return was
}

// schedulerFixture is a real Pebble store, session service and worker store
// holding active workers. Only time and the dispatch boundary are replaced.
type schedulerFixture struct {
	db          *pebblestore.Store
	service     *WorkerExecutionService
	ws          *pebblestore.WorkerStore
	workers     []pebblestore.WorkerRecord
	budgetReads atomic.Int64
	readKinds   sync.Map
}

const schedulerAccount, schedulerUser = "account-sched", "user-sched"

func newSchedulerFixture(t *testing.T, count int, intervalSeconds int64) *schedulerFixture {
	t.Helper()
	db, err := pebblestore.Open(filepath.Join(t.TempDir(), "scheduler.pebble"))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = db.Close() })
	events, err := pebblestore.NewEventLog(db)
	if err != nil {
		t.Fatal(err)
	}
	sessions := sessionruntime.NewService(pebblestore.NewSessionStore(db), events)
	fx := &schedulerFixture{db: db, ws: sessions.Store().WorkerStore()}
	fx.service = &WorkerExecutionService{host: &AutomationV2ExecutionHost{runs: &Service{sessions: sessions}}}
	for i := 0; i < count; i++ {
		fx.workers = append(fx.workers, fx.createActiveWorker(t, fmt.Sprintf("Worker %d", i), intervalSeconds))
	}
	db.SetBudgetReadObserver(func(kind string) {
		fx.budgetReads.Add(1)
		v, _ := fx.readKinds.LoadOrStore(kind, new(atomic.Int64))
		v.(*atomic.Int64).Add(1)
	})
	return fx
}

func (fx *schedulerFixture) createActiveWorker(t *testing.T, name string, intervalSeconds int64) pebblestore.WorkerRecord {
	t.Helper()
	w, err := fx.ws.CreateWorker(schedulerAccount, schedulerUser, pebblestore.CreateWorkerRequest{Name: name, Instructions: "Scheduled work.", IdempotencyKey: name,
		Automations: []pebblestore.WorkerAutomationDefinition{{Name: "tick", ActivationMode: "interval", Enabled: true, Schedule: &pebblestore.AutomationV2Schedule{Kind: "interval", IntervalSeconds: intervalSeconds}}}}, nil)
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

// simulate drives the scheduler through simulated time up to end, processing
// every wake the scheduler asks for, and returns the number of wakes.
func simulate(ctx context.Context, sch *WorkerScheduler, clock *fakeSchedulerClock, end time.Time) int {
	wakes := 0
	for {
		next := sch.nextWake()
		if next.IsZero() || next.After(end) {
			clock.Set(end)
			return wakes
		}
		if next.Before(clock.Now()) {
			next = clock.Now()
		}
		clock.Set(next)
		_ = sch.process(ctx, next)
		wakes++
	}
}

// Requirement: an idle daemon (active workers, no runs, nothing due) must not
// poll. Threat: the once-a-second worker sweep read the budget/usage ledger
// (GetWorkerBudgetStatus -> GetUsageScopeDay, GetDailyUsageAccumulator) for
// every worker every second, burning ~40% of a core on a live daemon. Owner:
// WorkerScheduler (process/nextWake), TickWorker and ReconcileWorker over the
// real Pebble store; the store's budget read observer counts the reads. The
// scheduler layer is the narrowest one that owns both the wake cadence and the
// per-worker work. Time is simulated; nothing sleeps.
func TestIdleWorkerSchedulerPerformsNoBudgetReadsOrPerSecondWakeups(t *testing.T) {
	fx := newSchedulerFixture(t, 4, 3600)
	clock := newFakeSchedulerClock(schedulerTestStart(time.Now()))
	sch := NewWorkerScheduler(fx.service, WorkerSchedulerOptions{Clock: clock})
	ctx := context.Background()
	start := clock.Now()
	if err := sch.process(ctx, start); err != nil {
		t.Fatalf("startup pass: %v", err)
	}
	wakes := simulate(ctx, sch, clock, start.Add(10*time.Second))
	if wakes != 0 {
		t.Errorf("idle scheduler woke %d times over 10 simulated seconds, want 0", wakes)
	}
	if n := fx.budgetReads.Load(); n != 0 {
		kinds := map[string]int64{}
		fx.readKinds.Range(func(k, v any) bool { kinds[k.(string)] = v.(*atomic.Int64).Load(); return true })
		t.Errorf("idle scheduler performed %d budget/usage reads (%v), want 0", n, kinds)
	}
	if next := sch.nextWake(); next.Before(start.Add(time.Minute)) {
		t.Errorf("idle scheduler next wake %v after start, want the next due slot or the safety interval", next.Sub(start))
	}
}
