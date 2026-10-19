package pebblestore

// SchedulingSignalKind names a committed change that can move an automation
// scheduler's next wake. Signals are accelerators only: durable records stay
// authoritative and a scheduler must still recover from a missed signal.
type SchedulingSignalKind string

const (
	// SchedulingSignalWorker follows a committed worker, automation, worker
	// run or worker budget policy write for one worker.
	SchedulingSignalWorker SchedulingSignalKind = "worker"
	// SchedulingSignalRun follows a committed V3 run intent or lifecycle
	// change, including a run reaching its terminal state.
	SchedulingSignalRun SchedulingSignalKind = "run"
	// SchedulingSignalAutomationV2 follows a committed V2 automation record or
	// occurrence mutation.
	SchedulingSignalAutomationV2 SchedulingSignalKind = "automation_v2"
	// SchedulingSignalBudget follows a committed usage receipt that may have
	// set an account's worker budget holds.
	SchedulingSignalBudget SchedulingSignalKind = "budget"
)

// SchedulingSignal identifies what changed, never record content.
type SchedulingSignal struct {
	Kind           SchedulingSignalKind
	AccountScopeID string
	WorkerID       string
	SessionID      string
}

// SetSchedulingObserver installs the instance-owned scheduler wakeup. fn runs
// synchronously after the commit, possibly while store locks are held, so it
// must not block or call back into the store.
func (s *Store) SetSchedulingObserver(fn func(SchedulingSignal)) {
	if s == nil {
		return
	}
	if fn == nil {
		s.schedulingObserver.Store(nil)
		return
	}
	s.schedulingObserver.Store(&fn)
}

func (s *Store) signalScheduling(signal SchedulingSignal) {
	if s == nil {
		return
	}
	if fn := s.schedulingObserver.Load(); fn != nil {
		(*fn)(signal)
	}
}

// Budget read kinds reported to a budget read observer.
const (
	BudgetReadWorkerBudgetStatus    = "worker_budget_status"
	BudgetReadDailyUsageAccumulator = "daily_usage_accumulator"
	BudgetReadUsageScopeDay         = "usage_scope_day"
)

// SetBudgetReadObserver installs a counting seam for budget and usage reads
// (GetWorkerBudgetStatus, GetDailyUsageAccumulator, GetUsageScopeDay). It
// lets scheduler tests prove an idle daemon performs none. fn must not block.
func (s *Store) SetBudgetReadObserver(fn func(kind string)) {
	if s == nil {
		return
	}
	if fn == nil {
		s.budgetReadObserver.Store(nil)
		return
	}
	s.budgetReadObserver.Store(&fn)
}

func (s *Store) observeBudgetRead(kind string) {
	if s == nil {
		return
	}
	if fn := s.budgetReadObserver.Load(); fn != nil {
		(*fn)(kind)
	}
}

// KeyWorkerBudgetHold is the durable UTC-day budget hold record for a worker.
func KeyWorkerBudgetHold(account, worker, date string) string {
	return workerBudgetHoldKey(account, worker, date)
}
