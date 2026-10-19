package pebblestore

import (
	"path/filepath"
	"testing"
	"time"
)

func openBudgetSignalStore(t *testing.T) (*SessionStore, *[]SchedulingSignal) {
	t.Helper()
	s, err := Open(filepath.Join(t.TempDir(), "budget-signal.pebble"))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = s.Close() })
	var signals []SchedulingSignal
	s.SetSchedulingObserver(func(sig SchedulingSignal) {
		if sig.Kind == SchedulingSignalBudget {
			signals = append(signals, sig)
		}
	})
	return NewSessionStore(s), &signals
}

func createBudgetSignalWorker(t *testing.T, ss *SessionStore, account string, tokens int64) WorkerRecord {
	t.Helper()
	w, err := NewWorkerStore(ss.store).CreateWorker(account, "user-budget", CreateWorkerRequest{Name: "Budgeted", Instructions: "Work.", IdempotencyKey: "budgeted"}, nil)
	if err != nil {
		t.Fatal(err)
	}
	policy, err := ss.GetWorkerBudget(account, w.ID)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := ss.SetWorkerBudget(account, w.ID, policy.Revision, 0, tokens); err != nil {
		t.Fatal(err)
	}
	return w
}

// Requirement: the budget scheduling signal follows committed hold, usage or
// limit writes only (scheduling_signal.go). Reading a worker's budget status,
// or re-checking admission, while a hold already exists commits nothing and
// must not signal. Threat: GetWorkerBudgetStatus -> publishWorkerBudgetHolds
// signalling on every read that finds a hold; the scheduler then reconciles
// every busy worker of the account a second later, which reads again and
// signals again, re-reading the ledger every second for as long as runs are in
// flight. Owner: SessionStore.GetWorkerBudgetStatus, CheckWorkerBudgetAdmission,
// holdWorkerBudgetLocked and publishWorkerBudgetHolds. The store is the
// narrowest layer that owns both the read and the signal.
func TestWorkerBudgetReadsOfAnExistingHoldDoNotSignal(t *testing.T) {
	ss, signals := openBudgetSignalStore(t)
	const account = "acct-budget-read"
	w := createBudgetSignalWorker(t, ss, account, 1)
	date := time.Now().UTC().Format("2006-01-02")
	hold := WorkerBudgetHold{Date: date, Reason: "daily_budget_exhausted", CapSource: "worker", Dimension: "tokens", Limit: 1, Usage: 1}
	for _, d := range []string{date, time.Now().UTC().AddDate(0, 0, 1).Format("2006-01-02")} {
		hold.Date = d
		if err := ss.store.PutJSON(workerBudgetHoldKey(account, w.ID, d), hold); err != nil {
			t.Fatal(err)
		}
	}
	*signals = nil
	for i := 0; i < 3; i++ {
		status, err := ss.GetWorkerBudgetStatus(account, w.ID)
		if err != nil {
			t.Fatal(err)
		}
		if status.Hold == nil || !status.Blocked {
			t.Fatalf("status read %d did not report the hold: %+v", i, status)
		}
		if err := ss.CheckWorkerBudgetAdmission(account, w.ID); err == nil {
			t.Fatal("admission check passed under a hold")
		}
	}
	if len(*signals) != 0 {
		t.Fatalf("reads of an existing hold sent %d budget scheduling signals, want 0", len(*signals))
	}
}

// Requirement: a hold newly committed by an admission check is a committed
// write and still wakes the scheduler, exactly once; a later read does not.
// Threat: removing the read-path signal also silencing a real new hold.
// Owner: holdWorkerBudgetLocked via checkWorkerBudgetLocked.
func TestWorkerBudgetNewlyCommittedHoldSignalsOnce(t *testing.T) {
	ss, signals := openBudgetSignalStore(t)
	const account = "acct-budget-new"
	w := createBudgetSignalWorker(t, ss, account, 1)
	date := time.Now().UTC().Format("2006-01-02")
	day := UsageScopeTotal{Kind: "worker", ID: w.ID, TotalTokens: 5}
	if err := ss.store.PutJSON(usageScopeDayKey(account, day, date), day); err != nil {
		t.Fatal(err)
	}
	*signals = nil
	if err := ss.CheckWorkerBudgetAdmission(account, w.ID); err == nil {
		t.Fatal("exhausted worker passed admission")
	}
	if found, err := ss.store.GetJSON(workerBudgetHoldKey(account, w.ID, date), &WorkerBudgetHold{}); err != nil || !found {
		t.Fatalf("admission check did not commit a hold: found=%v err=%v", found, err)
	}
	if len(*signals) != 1 || (*signals)[0].AccountScopeID != account {
		t.Fatalf("new hold sent signals %+v, want one budget signal for %s", *signals, account)
	}
	if _, err := ss.GetWorkerBudgetStatus(account, w.ID); err != nil {
		t.Fatal(err)
	}
	if len(*signals) != 1 {
		t.Fatalf("status read after the hold sent %d signals in total, want still 1", len(*signals))
	}
}

// Requirement: usage-scope repair that commits worker usage (and possibly a
// worker budget hold through setUsageDays -> setWorkerBudgetHold) wakes the
// scheduler for the account, as a settled receipt does. Threat: repair
// committing a hold silently, so a busy worker's runs are not closed until the
// 2-minute safety rescan. Owner: SessionStore.RepairUsageScopes /
// repairUsageScopeReceipt over a real bound session, worker and receipt.
func TestRepairUsageScopesSignalsBudgetAfterCommit(t *testing.T) {
	ss, signals := openBudgetSignalStore(t)
	const account = "acct-budget-repair"
	w := createBudgetSignalWorker(t, ss, account, 1)
	const session, run = "session_budget_repair", "run_budget_repair"
	if err := ss.CreateSessionForAccount(SessionSnapshot{ID: session, Title: "Repair"}, "user-budget", account); err != nil {
		t.Fatal(err)
	}
	if err := ss.store.PutJSON(usageBindingKey(account, session), []UsageScopeTotal{{Kind: "worker", ID: w.ID}}); err != nil {
		t.Fatal(err)
	}
	receipt := SessionTurnUsageSnapshot{SessionID: session, AccountScopeID: account, RunID: run, Provider: "fake", Model: "fake", Source: "test", InputTokens: 5, TotalTokens: 5, CreatedAt: time.Now().UnixMilli()}
	if err := ss.store.PutJSON(KeySessionTurnUsage(session, run), receipt); err != nil {
		t.Fatal(err)
	}
	if err := ss.store.db.Set([]byte(KeySessionTurnUsageByAccount(account, session, run)), []byte(run), nil); err != nil {
		t.Fatal(err)
	}
	*signals = nil
	result, err := ss.RepairUsageScopes(account, "", 10)
	if err != nil {
		t.Fatal(err)
	}
	if result.Repaired != 1 {
		t.Fatalf("repair result %+v, want one repaired receipt", result)
	}
	date := time.UnixMilli(receipt.CreatedAt).UTC().Format("2006-01-02")
	if found, err := ss.store.GetJSON(workerBudgetHoldKey(account, w.ID, date), &WorkerBudgetHold{}); err != nil || !found {
		t.Fatalf("repair did not commit the worker hold: found=%v err=%v", found, err)
	}
	if len(*signals) == 0 {
		t.Fatal("repair committed a worker budget hold without a budget scheduling signal")
	}
	for _, sig := range *signals {
		if sig.AccountScopeID != account {
			t.Fatalf("repair signalled %+v, want account %s", sig, account)
		}
	}
	// Nothing left to repair: no commit, no signal.
	*signals = nil
	if result, err = ss.RepairUsageScopes(account, "", 10); err != nil || result.Repaired != 0 {
		t.Fatalf("second repair: %+v, %v", result, err)
	}
	if len(*signals) != 0 {
		t.Fatalf("repair with nothing to repair sent %d signals, want 0", len(*signals))
	}
}
