package permission

import (
	"fmt"
	"path/filepath"
	"strings"
	pebblestore "swarm/packages/swarmd/internal/store/pebble"
	"testing"
)

// Purpose: real Pebble reservations must refuse helper cap overflow without
// allocating a reservation, across turns, and honor canonical limit edits.
// This is the narrowest admission layer; no provider or process is simulated.
func TestHelperAdmissionCaps(t *testing.T) {
	db, err := pebblestore.Open(filepath.Join(t.TempDir(), "db"))
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	svc := NewService(pebblestore.NewPermissionStore(db), nil, nil)
	reserve := func(n int) error {
		_, err := svc.ReserveSubagentWave(SubagentReservationRequest{AccountScopeID: "account", SessionID: "job", RunID: fmt.Sprint(n), CallID: "call", ManifestHash: "hash", LaunchCount: 1, Helper: true})
		return err
	}
	for i := 1; i <= 4; i++ {
		if err := reserve(i); err != nil {
			t.Fatal(err)
		}
	}
	if err := reserve(5); err == nil || !strings.Contains(err.Error(), "helper_concurrency") {
		t.Fatalf("fifth: %v", err)
	}
	if _, ok, _ := svc.GetSubagentReservation("job", "5", "call"); ok {
		t.Fatal("rejected helper allocated")
	}
	l, _ := db.GetLimits("account")
	l.HelperConcurrency = 5
	if _, err := db.SetLimits("account", l.Revision, l); err != nil {
		t.Fatal(err)
	}
	if err := reserve(5); err != nil {
		t.Fatal(err)
	}
	for i := 1; i <= 20; i++ {
		if i > 5 {
			if err := reserve(i); err != nil {
				t.Fatal(err)
			}
		}
		if err := svc.FinishSubagentWave("job", fmt.Sprint(i), "call", "completed"); err != nil {
			t.Fatal(err)
		}
	}
	if err := reserve(21); err == nil || !strings.Contains(err.Error(), "helpers_per_job") {
		t.Fatalf("21st: %v", err)
	}
	l, _ = db.GetLimits("account")
	l.HelpersPerJob = 21
	if _, err := db.SetLimits("account", l.Revision, l); err != nil {
		t.Fatal(err)
	}
	if err := reserve(21); err != nil {
		t.Fatal(err)
	}
}
