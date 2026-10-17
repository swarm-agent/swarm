package pebblestore

import (
	"reflect"
	"testing"
	"time"
)

// Purpose: ClaimRunnerLanding is the atomic lease admission boundary. Temporary
// Pebble and an injected clock prove capacity rejection without state mutation,
// expiry, stale-lease rejection and legacy capacity without HTTP or Docker.
func TestRunnerLandingSlots(t *testing.T) {
	db, err := Open(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	now := time.Unix(1000, 0)
	if err := db.mutateRunners("account", func(st *runnerState) error {
		st.Landings = []RunnerLanding{{ID: "one", State: "pending"}, {ID: "two", State: "pending"}, {ID: "three", State: "pending"}}
		return nil
	}); err != nil {
		t.Fatal(err)
	}
	snapshot := func() runnerState {
		t.Helper()
		var st runnerState
		if _, err := db.GetJSON("runners:v1:"+runnerHash("account"), &st); err != nil {
			t.Fatal(err)
		}
		return st
	}
	first, err := db.ClaimRunnerLanding("account", "runner", now, 2)
	if err != nil || first == nil {
		t.Fatalf("first: %v %v", first, err)
	}
	second, err := db.ClaimRunnerLanding("account", "runner", now.Add(30*time.Second), 2)
	if err != nil || second == nil || second.ID == first.ID || second.LeaseID == first.LeaseID {
		t.Fatalf("second: %v %v", second, err)
	}
	before := snapshot()
	for _, slots := range [][]int{nil, {2}, {0}, {17}, {1, 2}} {
		l, err := db.ClaimRunnerLanding("account", "runner", now.Add(30*time.Second), slots...)
		if err == nil || l != nil {
			t.Fatalf("capacity %v accepted: %v %v", slots, l, err)
		}
		if !reflect.DeepEqual(before, snapshot()) {
			t.Fatal("rejection mutated persisted state")
		}
	}
	renewed, err := db.ClaimRunnerLanding("account", "runner", now.Add(time.Minute), 2)
	if err != nil || renewed == nil || renewed.LeaseID == first.LeaseID {
		t.Fatalf("expired slot not freed: %v %v", renewed, err)
	}
	if err := db.UpdateRunnerLanding("account", "runner", first.ID, first.LeaseID, "heartbeat", "", nil, 0, now.Add(time.Minute)); err == nil {
		t.Fatal("expired lease usable")
	}
	if err := db.UpdateRunnerLanding("account", "runner", second.ID, second.LeaseID, "heartbeat", "", nil, 0, now.Add(time.Minute)); err != nil {
		t.Fatal(err)
	}
	legacy, err := db.ClaimRunnerLanding("account", "legacy", now.Add(time.Minute))
	if err != nil || legacy == nil {
		t.Fatalf("legacy first: %v %v", legacy, err)
	}
	before = snapshot()
	if l, err := db.ClaimRunnerLanding("account", "legacy", now.Add(time.Minute)); err == nil || l != nil {
		t.Fatal("legacy exceeded one slot")
	}
	if !reflect.DeepEqual(before, snapshot()) {
		t.Fatal("legacy rejection mutated state")
	}
}
