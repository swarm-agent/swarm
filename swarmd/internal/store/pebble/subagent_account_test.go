package pebblestore

import (
	"path/filepath"
	"testing"
)

// Purpose: account Swarm occupancy must survive restart and release atomically
// with its canonical reservation. The real Pebble layer proves the index is not
// a second transient authority and excludes unrelated accounts/regular children.
func TestAccountSwarmReservationIndexRestart(t *testing.T) {
	path := filepath.Join(t.TempDir(), "reservations")
	db, err := Open(path)
	if err != nil {
		t.Fatal(err)
	}
	p := NewPermissionStore(db)
	record := SubagentWaveReservation{AccountScopeID: "account", SessionID: "parent", RunID: "run", CallID: "call", ManifestHash: "exact", LaunchCount: 3, ActiveCount: 3, SwarmMode: true, Status: "approve"}
	if err := p.PutSubagentWaveReservation(record); err != nil {
		t.Fatal(err)
	}
	if err := db.Close(); err != nil {
		t.Fatal(err)
	}
	db, err = Open(path)
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	p = NewPermissionStore(db)
	if n, err := p.CountAccountSwarmChildren("account"); err != nil || n != 3 {
		t.Fatalf("restart occupancy=%d error=%v", n, err)
	}
	if n, err := p.CountAccountSwarmChildren("foreign"); err != nil || n != 0 {
		t.Fatalf("foreign occupancy=%d error=%v", n, err)
	}
	record.ActiveCount = 0
	record.Status = "completed"
	if err := p.PutSubagentWaveReservation(record); err != nil {
		t.Fatal(err)
	}
	if n, err := p.CountAccountSwarmChildren("account"); err != nil || n != 0 {
		t.Fatalf("released occupancy=%d error=%v", n, err)
	}
	record.CallID = "regular"
	record.SwarmMode = false
	record.ActiveCount = 3
	if err := p.PutSubagentWaveReservation(record); err != nil {
		t.Fatal(err)
	}
	if n, err := p.CountAccountSwarmChildren("account"); err != nil || n != 0 {
		t.Fatalf("regular leaked into Swarm occupancy=%d error=%v", n, err)
	}
}
