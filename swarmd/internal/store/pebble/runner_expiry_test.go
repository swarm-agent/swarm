package pebblestore

import (
	"testing"
	"time"
)

// Purpose: expireRunnerLeases owns the two-attempt terminal transition. A
// supplied clock proves the exact deadline and attribution without sleeping;
// an expired lease must never remain usable or lose either runner's name.
func TestRunnerExpiryReason(t *testing.T) {
	now := time.Unix(1000, 0)
	st := runnerState{Landings: []RunnerLanding{{ID: "job", State: "claimed", Runner: "B", LeaseID: "lease", LeaseUntil: now.UnixMilli(), Attempts: []string{"A", "B"}}}}
	expireRunnerLeases(&st, now.Add(-time.Millisecond).UnixMilli())
	if st.Landings[0].State != "claimed" {
		t.Fatal("early expiry")
	}
	expireRunnerLeases(&st, now.UnixMilli())
	l := st.Landings[0]
	if l.State != "environment_failed" || l.LeaseID != "" || l.DeliveryReason != "environment failed on runners A, B" {
		t.Fatalf("bad expiry: %+v", l)
	}
}

// Purpose: proactive expiry must persist and emit attribution without a claim
// or heartbeat triggering it. Real Pebble plus an injected time is the narrowest
// deterministic layer for the background tick's production operation.
func TestRunnerProactiveExpiry(t *testing.T) {
	db, err := Open(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	now := time.Unix(1000, 0)
	if err := db.mutateRunners("account", func(st *runnerState) error {
		st.Landings = []RunnerLanding{{ID: "job", State: "claimed", Runner: "A", LeaseID: "lease", LeaseUntil: now.UnixMilli(), Attempts: []string{"A"}}}
		return nil
	}); err != nil {
		t.Fatal(err)
	}
	if err := db.ExpireRunnerLeases(now); err != nil {
		t.Fatal(err)
	}
	var st runnerState
	if _, err := db.GetJSON("runners:v1:"+runnerHash("account"), &st); err != nil {
		t.Fatal(err)
	}
	if st.Landings[0].State != "lease_expired" {
		t.Fatal(st)
	}
	l, err := db.ClaimRunnerLanding("account", "B", now)
	if err != nil || l == nil {
		t.Fatal(err)
	}
	if err := db.ExpireRunnerLeases(now.Add(time.Minute)); err != nil {
		t.Fatal(err)
	}
	if _, err := db.GetJSON("runners:v1:"+runnerHash("account"), &st); err != nil {
		t.Fatal(err)
	}
	if st.Landings[0].State != "environment_failed" || st.Landings[0].DeliveryReason != "environment failed on runners A, B" {
		t.Fatal(st)
	}
	page, err := NewSignalStore(db).ListAfter(0, 10, SignalFilter{Account: "account"})
	if err != nil {
		t.Fatal(err)
	}
	if len(page.Signals) != 1 || page.Signals[0].Kind != "job.environment_failed" || page.Signals[0].Refs["runners"] != "A, B" {
		t.Fatal(page)
	}
}
