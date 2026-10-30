package pebblestore

import (
	"fmt"
	"path/filepath"
	"testing"
)

// Requirement: the landed-worktree removal decides that a checkout is unused
// only from the complete set of active sessions. ListEveryActiveSessionLifecycle
// pages through every lifecycle record, so an active session stored after more
// than one page of finished ones is still returned, and an unreadable record
// fails the whole list instead of yielding a partial one.
// Threat (review finding): ListActiveSessionLifecycles(0) stops after the
// first 1000 records (active or not), so on a machine with many finished
// sessions an active one was silently missed and its checkout treated as
// unused. Owner: SessionStore.ListEveryActiveSessionLifecycle over a real
// Pebble store, the narrowest layer holding the records.
func TestListEveryActiveSessionLifecyclePagesPastTheFirstThousand(t *testing.T) {
	store, err := Open(filepath.Join(t.TempDir(), "lifecycles.pebble"))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = store.Close() })
	sessions := NewSessionStore(store)
	for i := 0; i < 2100; i++ {
		id := fmt.Sprintf("s%05d", i)
		active := i == 1500 || i == 2099
		if err := sessions.UpsertSessionLifecycle(SessionLifecycleSnapshot{SessionID: id, AccountScopeID: "acct", Active: active, UpdatedAt: int64(i)}); err != nil {
			t.Fatal(err)
		}
	}
	paged, err := sessions.ListActiveSessionLifecycles(0)
	if err != nil {
		t.Fatal(err)
	}
	if len(paged) != 0 {
		t.Fatalf("fixture: the first page already holds the active sessions: %+v", paged)
	}
	every, err := sessions.ListEveryActiveSessionLifecycle()
	if err != nil {
		t.Fatal(err)
	}
	if len(every) != 2 || every[0].SessionID != "s01500" || every[1].SessionID != "s02099" {
		t.Fatalf("active sessions = %+v, want s01500 and s02099", every)
	}

	if err := store.PutBytes(KeySessionLifecycle("s09999"), []byte("{not json")); err != nil {
		t.Fatal(err)
	}
	if got, err := sessions.ListEveryActiveSessionLifecycle(); err == nil {
		t.Fatalf("an unreadable lifecycle record gave a partial list: %+v", got)
	}
}
