package pebblestore

import (
	"testing"
	"time"
)

// Purpose: RunnerStatuses must derive current landing from account-scoped live
// claims, never a runner's self-reported value or an expired/completed lease.
// Real temporary Pebble storage is the narrow persistence boundary for this read.
func TestRunnerCurrentLanding(t *testing.T) {
	db, err := Open(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	for _, state := range []string{"claimed", "completed", "expired"} {
		err := db.mutateRunners("account", func(st *runnerState) error {
			st.Statuses["worker"] = RunnerStatus{Name: "worker", State: "busy", CurrentLanding: "spoofed"}
			until := time.Now().Add(time.Hour).UnixMilli()
			landingState := state
			if state == "expired" {
				landingState = "claimed"
				until = 1
			}
			st.Landings = []RunnerLanding{{ID: "current", Runner: "worker", State: landingState, LeaseUntil: until}}
			return nil
		})
		if err != nil {
			t.Fatal(err)
		}
		rows, err := db.RunnerStatuses("account")
		if err != nil || len(rows) != 1 {
			t.Fatalf("%v %v", rows, err)
		}
		want := ""
		if state == "claimed" {
			want = "current"
		}
		if rows[0].CurrentLanding != want {
			t.Fatalf("%s: %+v", state, rows)
		}
		other, err := db.RunnerStatuses("other")
		if err != nil || len(other) != 0 {
			t.Fatalf("cross-account: %v %v", other, err)
		}
	}
}
