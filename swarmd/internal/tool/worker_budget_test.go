package tool

import (
	"errors"
	"path/filepath"
	"testing"

	sessionruntime "swarm/packages/swarmd/internal/session"
	store "swarm/packages/swarmd/internal/store/pebble"
)

// Purpose: media must not bypass worker budgets or treat an unavailable quote
// as free. Owners checkMediaWorkerBudget and canonical worker lineage; direct
// runtime helper with real temp storage is the narrowest pre-generation gate.
func TestWorkerBudgetMediaUnknownQuote(t *testing.T) {
	db, err := store.Open(filepath.Join(t.TempDir(), "budget.pebble"))
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	s := store.NewSessionStore(db)
	account, session := "account", "budget-media"
	if err := db.PutJSON(store.KeyWorker(account, "worker"), store.WorkerRecord{ID: "worker", AccountScopeID: account}); err != nil {
		t.Fatal(err)
	}
	if err := db.PutJSON(store.KeySession(session), store.SessionSnapshot{ID: session, AccountScopeID: account, UserID: "user", Metadata: map[string]any{"worker_id": "worker", "worker_run_id": "run"}}); err != nil {
		t.Fatal(err)
	}
	if err := db.PutJSON(store.KeyWorkerRun(account, "worker", "run"), store.WorkerRunRecord{ID: "run", WorkerID: "worker", AccountScopeID: account, SessionID: session}); err != nil {
		t.Fatal(err)
	}
	if _, err := s.SetWorkerBudget(account, "worker", 0, 1, 0); err != nil {
		t.Fatal(err)
	}
	r := &Runtime{sessions: sessionruntime.NewService(s, nil)}
	if err := r.checkMediaWorkerBudget(account, session); !errors.Is(err, store.ErrWorkerBudget) {
		t.Fatalf("unknown quote: %v", err)
	}
	if _, err := s.SetWorkerBudget(account, "worker", 1, 0, 100); err != nil {
		t.Fatal(err)
	}
	if err := r.checkMediaWorkerBudget(account, session); err != nil {
		t.Fatalf("token-only: %v", err)
	}
	if err := r.checkMediaWorkerBudget(account, session); !errors.Is(err, store.ErrWorkerBudget) {
		t.Fatalf("concurrent generation bypass: %v", err)
	}
}
