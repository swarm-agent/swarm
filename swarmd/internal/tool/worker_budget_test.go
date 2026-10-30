package tool

import (
	"errors"
	"testing"

	pebblestore "swarm/packages/swarmd/internal/store/pebble"
)

// budgetOnlySessions exposes only the session store; checkMediaWorkerBudget
// reaches no other session method.
type budgetOnlySessions struct {
	manageSessionService
	store *pebblestore.SessionStore
}

func (s budgetOnlySessions) Store() *pebblestore.SessionStore { return s.store }

// Purpose: manage_artifact image/video/audio generation has no pre-charge
// quote, so checkMediaWorkerBudget must reserve the unpriced ceiling of the
// requested kind under a capped worker (image $0.50, audio $1.00, video
// $10.00) and refuse only when that ceiling would cross the cap. Previously it
// passed a hard-coded unknown price and refused every such generation under an
// account cap. Calling the Runtime method over a real temporary store is the
// narrowest layer that proves the kind reaches CheckWorkerMediaAdmission and
// that refusals and releases leave no reservation behind.
func TestCheckMediaWorkerBudgetUnpricedCeiling(t *testing.T) {
	db, err := pebblestore.Open(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	store := pebblestore.NewSessionStore(db)
	if err := store.CreateSessionForAccount(pebblestore.SessionSnapshot{ID: "session"}, "user", "account"); err != nil {
		t.Fatal(err)
	}
	if err := db.PutJSON(pebblestore.KeyWorker("account", "worker"), pebblestore.WorkerRecord{ID: "worker", AccountScopeID: "account"}); err != nil {
		t.Fatal(err)
	}
	if err := db.PutJSON("usage_scope_binding/account/session", []pebblestore.UsageScopeTotal{{Kind: "worker", ID: "worker"}}); err != nil {
		t.Fatal(err)
	}
	if err := store.PutUsageLimit(pebblestore.UsageLimitRecord{AccountScopeID: "account", Enabled: true, DailyCostLimitUSD: 1.5}); err != nil {
		t.Fatal(err)
	}
	r := &Runtime{sessions: budgetOnlySessions{store: store}}
	if err := r.checkMediaWorkerBudget("account", "session", "audio", "audio-op"); err != nil {
		t.Fatalf("unpriced audio refused under cap: %v", err)
	}
	if err := r.checkMediaWorkerBudget("account", "session", "video", "video-op"); !errors.Is(err, pebblestore.ErrWorkerBudget) {
		t.Fatalf("video ceiling over the cap admitted: %v", err)
	}
	if err := r.checkMediaWorkerBudget("account", "session", "image", "image-op"); err != nil {
		t.Fatalf("image ceiling at the cap refused: %v", err)
	}
	// $1.50 is now reserved; any further unpriced media crosses the cap.
	if err := r.checkMediaWorkerBudget("account", "session", "image", "image-op-2"); !errors.Is(err, pebblestore.ErrWorkerBudget) {
		t.Fatalf("reservations ignored: %v", err)
	}
	for _, op := range []string{"audio-op", "image-op"} {
		if err := r.releaseMediaWorkerBudget("account", "session", op); err != nil {
			t.Fatal(err)
		}
	}
	reserved := 0
	if err := db.IteratePrefix("worker_budget_account/account/reservation/", 0, func(string, []byte) error {
		reserved++
		return nil
	}); err != nil {
		t.Fatal(err)
	}
	if reserved != 0 {
		t.Fatalf("%d reservations left after refusals and releases", reserved)
	}
	if err := r.checkMediaWorkerBudget("account", "session", "video", "video-op"); !errors.Is(err, pebblestore.ErrWorkerBudget) {
		t.Fatalf("video ceiling alone over the cap admitted: %v", err)
	}
}
