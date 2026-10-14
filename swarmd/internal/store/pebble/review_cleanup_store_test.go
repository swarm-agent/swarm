package pebblestore

import (
	"path/filepath"
	"swarm/packages/swarmd/internal/sandbox"
	"testing"
)

// Purpose: cleanup identity and phase must survive real store reopen; immutable
// identity and monotonic phase reject stale writes without changing evidence.
// Pebble reopen is the narrowest durability boundary, without an engine.
func TestReviewCleanupDurability(t *testing.T) {
	path := filepath.Join(t.TempDir(), "db")
	db, err := Open(path)
	if err != nil {
		t.Fatal(err)
	}
	ss := NewSessionStore(db)
	state := sandbox.ReviewCleanupState{Role: "reviewer", Session: "review", Root: "/source", Checkout: "/checkout", Container: "container", Volume: "volume", Phase: "active"}
	if err := ss.PutReviewCleanup("account", state); err != nil {
		t.Fatal(err)
	}
	state.Phase = "container_removed"
	if err := ss.PutReviewCleanup("account", state); err != nil {
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
	ss = NewSessionStore(db)
	got, found, err := ss.GetReviewCleanup("account", "review")
	if err != nil || !found || got != state {
		t.Fatal(got, found, err)
	}
	stale := state
	stale.Phase = "active"
	if err := ss.PutReviewCleanup("account", stale); err == nil {
		t.Fatal("phase regressed")
	}
	stale = state
	stale.Root = "/foreign"
	if err := ss.PutReviewCleanup("account", stale); err == nil {
		t.Fatal("identity changed")
	}
	got, _, _ = ss.GetReviewCleanup("account", "review")
	if got != state {
		t.Fatal("rejection changed evidence")
	}
	if _, found, err := ss.GetReviewCleanup("foreign", "review"); err != nil || found {
		t.Fatal("cross-account read", err)
	}
}
