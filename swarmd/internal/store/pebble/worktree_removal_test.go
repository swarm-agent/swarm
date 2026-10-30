package pebblestore

import (
	"fmt"
	"path/filepath"
	"testing"
	"time"
)

// Requirement: writing a run's worktree removal record never overwrites a
// follow-up status written concurrently (ReserveWorkerFollowup writes
// FollowupStatus under workersMu). SetWorkerRunWorktreeRemoval reads and
// writes the run under that same lock.
// Threat (review finding): the run package read the run (GetWorkerRun) and
// later wrote the whole stale copy back (RecordWorkerRun); a reservation in
// between was lost, so a queued follow-up read as not active and its checkout
// could be removed. The test reproduces that interleaving: it holds
// workersMu (as a reservation does), commits FollowupStatus queued the way
// ReserveWorkerFollowup does, then releases. The old read-then-record
// pattern is run through the same interleaving to show the hazard.
// Owner: WorkerStore.SetWorkerRunWorktreeRemoval over a real Pebble store.
func TestSetWorkerRunWorktreeRemovalKeepsAConcurrentFollowupReservation(t *testing.T) {
	store, err := Open(filepath.Join(t.TempDir(), "runs.pebble"))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = store.Close() })
	ws := NewSessionStore(store).WorkerStore()
	const account, user = "acct", "user"
	w, err := ws.CreateWorker(account, user, CreateWorkerRequest{Name: "Builder", Instructions: "Build.", IdempotencyKey: "builder"}, nil)
	if err != nil {
		t.Fatal(err)
	}
	record := WorktreeRemoval{State: WorktreeRemovalRestored, Path: "/checkout", Branch: "agent/x", Head: "abc", At: 1}
	interleave := func(runID string, write func() error) WorkerRunRecord {
		t.Helper()
		if _, err := ws.RecordWorkerRun(account, WorkerRunRecord{ID: runID, WorkerID: w.ID, UserID: user, AccountScopeID: account, SessionID: "s-" + runID, RequestSource: "direct", Status: "succeeded", CompletedAt: 1}); err != nil {
			t.Fatal(err)
		}
		store.workersMu.Lock() // a follow-up reservation is in progress
		done := make(chan error, 1)
		go func() { done <- write() }()
		time.Sleep(200 * time.Millisecond)
		r, _, err := ws.GetWorkerRun(account, w.ID, runID)
		if err != nil {
			store.workersMu.Unlock()
			t.Fatal(err)
		}
		r.FollowupStatus = "queued"
		m := &workerRealtimeMutation{accountScopeID: account, userID: user, workerID: w.ID}
		if err := m.put(KeyWorkerRun(account, w.ID, runID), r); err != nil {
			store.workersMu.Unlock()
			t.Fatal(err)
		}
		if err := store.commitWorkerRealtime(m); err != nil {
			store.workersMu.Unlock()
			t.Fatal(err)
		}
		store.workersMu.Unlock()
		if err := <-done; err != nil {
			t.Fatal(err)
		}
		got, _, err := ws.GetWorkerRun(account, w.ID, runID)
		if err != nil {
			t.Fatal(err)
		}
		return got
	}

	// The old pattern loses the reservation: proof of the hazard.
	old := interleave("run-old", func() error {
		current, _, err := ws.GetWorkerRun(account, w.ID, "run-old")
		if err != nil {
			return err
		}
		copy := record
		current.WorktreeRemoval = &copy
		_, err = ws.RecordWorkerRun(account, current)
		return err
	})
	if old.FollowupStatus == "queued" {
		t.Fatalf("fixture: the old read-then-record pattern kept the reservation; the interleaving is not reproduced")
	}

	got := interleave("run-new", func() error {
		return ws.SetWorkerRunWorktreeRemoval(account, w.ID, "run-new", record)
	})
	if got.FollowupStatus != "queued" || !got.FollowupActive() {
		t.Fatalf("follow-up reservation lost: %+v", got)
	}
	if got.WorktreeRemoval == nil || *got.WorktreeRemoval != record {
		t.Fatalf("removal record = %+v, want %+v", got.WorktreeRemoval, record)
	}
	if err := ws.SetWorkerRunWorktreeRemoval(account, w.ID, "missing", record); err != ErrWorkerNotFound {
		t.Fatalf("missing run = %v, want ErrWorkerNotFound", err)
	}
}

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
