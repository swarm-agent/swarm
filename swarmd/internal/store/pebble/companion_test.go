package pebblestore

import (
	"errors"
	"path/filepath"
	"sync"
	"testing"
)

// Purpose: an account user has exactly one Companion identity. Requirement:
// EnsureCompanion is idempotent under concurrency and across a store reopen,
// is isolated per account and per user, and RetireCompanion (the explicit
// delete) advances to a fresh deterministic session only for the exact current
// session, refusing a stale or foreign retire without changing the record.
// Threat: duplicate Companions from racing clients or restarts, one user
// reaching another's Companion, or a retried delete wiping the replacement.
// Boundary: SessionStore.EnsureCompanion/GetCompanion/RetireCompanion and
// CompanionSessionID. Narrowest layer: real Pebble, reopened at the same path.
func TestCompanionIdentityIsSingleIsolatedAndDurable(t *testing.T) {
	path := filepath.Join(t.TempDir(), "db")
	db, err := Open(path)
	if err != nil {
		t.Fatal(err)
	}
	store := NewSessionStore(db)

	const callers = 16
	results := make([]CompanionIdentity, callers)
	createdCount := 0
	var mu sync.Mutex
	var wg sync.WaitGroup
	for i := 0; i < callers; i++ {
		wg.Add(1)
		go func(i int) {
			defer wg.Done()
			record, created, err := store.EnsureCompanion("acct-a", "user-1", 100)
			if err != nil {
				t.Error(err)
				return
			}
			mu.Lock()
			defer mu.Unlock()
			results[i] = record
			if created {
				createdCount++
			}
		}(i)
	}
	wg.Wait()
	if createdCount != 1 {
		t.Fatalf("created %d identities, want exactly 1", createdCount)
	}
	first := results[0]
	for _, record := range results {
		if record != first {
			t.Fatalf("concurrent ensure diverged: %+v vs %+v", record, first)
		}
	}
	if first.Generation != 1 || first.SessionID != CompanionSessionID("acct-a", "user-1", 1) || first.SessionID == "" {
		t.Fatalf("first identity %+v", first)
	}

	// Isolation: another user and another account get their own identity.
	otherUser, _, err := store.EnsureCompanion("acct-a", "user-2", 100)
	if err != nil {
		t.Fatal(err)
	}
	otherAccount, _, err := store.EnsureCompanion("acct-b", "user-1", 100)
	if err != nil {
		t.Fatal(err)
	}
	if otherUser.SessionID == first.SessionID || otherAccount.SessionID == first.SessionID || otherUser.SessionID == otherAccount.SessionID {
		t.Fatalf("identities are not isolated: %s %s %s", first.SessionID, otherUser.SessionID, otherAccount.SessionID)
	}
	if _, _, err := store.EnsureCompanion("acct-a", " ", 100); err == nil {
		t.Fatal("blank user accepted")
	}

	// Restart: the same identity is read back and ensure does not recreate it.
	if err := db.Close(); err != nil {
		t.Fatal(err)
	}
	db, err = Open(path)
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	store = NewSessionStore(db)
	again, created, err := store.EnsureCompanion("acct-a", "user-1", 200)
	if err != nil || created || again != first {
		t.Fatalf("after reopen: %+v created=%v err=%v, want %+v", again, created, err, first)
	}

	// A stale or foreign retire is refused and changes nothing.
	if _, err := store.RetireCompanion("acct-a", "user-1", otherUser.SessionID, 300); !errors.Is(err, ErrCompanionConflict) {
		t.Fatalf("foreign retire: %v", err)
	}
	if current, _, _ := store.GetCompanion("acct-a", "user-1"); current != first {
		t.Fatalf("refused retire changed the identity: %+v", current)
	}
	next, err := store.RetireCompanion("acct-a", "user-1", first.SessionID, 300)
	if err != nil {
		t.Fatal(err)
	}
	if next.Generation != 2 || next.SessionID != CompanionSessionID("acct-a", "user-1", 2) || next.SessionID == first.SessionID || next.CreatedAt != first.CreatedAt {
		t.Fatalf("retired identity %+v", next)
	}
	if _, err := store.RetireCompanion("acct-a", "user-1", first.SessionID, 400); !errors.Is(err, ErrCompanionConflict) {
		t.Fatalf("retried retire: %v", err)
	}
	if current, _, _ := store.GetCompanion("acct-a", "user-1"); current != next {
		t.Fatalf("retried retire changed the identity: %+v", current)
	}
	if untouched, _, _ := store.GetCompanion("acct-a", "user-2"); untouched != otherUser {
		t.Fatalf("another user's identity changed: %+v", untouched)
	}
}
