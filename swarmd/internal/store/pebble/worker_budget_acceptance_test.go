package pebblestore

import (
	"fmt"
	"path/filepath"
	"testing"
	"time"
)

// Purpose: AcceptWorker must follow AdmitWorkerRun's workersMu/account lock
// order, and budgetless acceptance must not wait on account budget mutations.
// Real concurrent store operations are the narrowest layer exercising these
// mutexes and proving both durable acceptance and admission complete.
func TestWorkerBudgetAcceptanceConcurrentAdmission(t *testing.T) {
	db, err := Open(filepath.Join(t.TempDir(), "workers.pebble"))
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	ws := NewWorkerStore(db)
	active, err := ws.CreateWorker("acct", "user", CreateWorkerRequest{Name: "Runner", Role: WorkerRoleCoder, Instructions: "Review", InitialLifecycleState: WorkerLifecycleStatePending}, nil)
	if err != nil {
		t.Fatal(err)
	}
	// Holding the account lock must not obstruct acceptance without a budget.
	unlock := db.sessionMutations.lockSessions("account:acct")
	done := make(chan error, 2)
	go func() { _, err := ws.AcceptWorker("acct", "user", active.ID, active.Revision, nil); done <- err }()
	select {
	case err := <-done:
		unlock()
		if err != nil {
			t.Fatal(err)
		}
	case <-time.After(5 * time.Second):
		unlock()
		<-done
		t.Fatal("budgetless acceptance waited on account lock")
	}
	for i := 0; i < 40; i++ {
		pending, err := ws.CreateWorker("acct", "user", CreateWorkerRequest{Name: fmt.Sprintf("Proposal %d", i), Role: WorkerRoleCoder, Instructions: "Review", InitialLifecycleState: WorkerLifecycleStatePending, Budget: &WorkerBudgetRequest{DailyUSD: 10}}, nil)
		if err != nil {
			t.Fatal(err)
		}
		start := make(chan struct{})
		go func() {
			<-start
			_, err := ws.AcceptWorker("acct", "user", pending.ID, pending.Revision, nil)
			done <- err
		}()
		go func() {
			<-start
			_, err := ws.AdmitWorkerRun("acct", WorkerRunAdmission{WorkerID: active.ID, WorkspaceID: "folder", UserID: "user", RequestSource: "direct", IdempotencyKey: fmt.Sprintf("admit-%d", i), Input: map[string]any{"prompt": "Review"}})
			done <- err
		}()
		close(start)
		for j := 0; j < 2; j++ {
			select {
			case err := <-done:
				if err != nil {
					t.Fatal(err)
				}
			case <-time.After(5 * time.Second):
				t.Fatal("acceptance/admission deadlocked")
			}
		}
		policy, err := NewSessionStore(db).GetWorkerBudget("acct", pending.ID)
		if err != nil || policy.DailyCostLimitUSD != 10 {
			t.Fatalf("policy not committed: %+v %v", policy, err)
		}
	}
}
