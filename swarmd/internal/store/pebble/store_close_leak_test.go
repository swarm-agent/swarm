package pebblestore

import (
	"path/filepath"
	"strings"
	"testing"

	"github.com/cockroachdb/pebble"
)

// Purpose: Store.Close is the leak detector the stall-check memory test
// relies on. A Pebble iterator or snapshot left open pins memtables and
// sstables for the life of the process; Close must report it as an error, not
// swallow it, and a second Close (a test cleanup after an explicit close)
// returns nil instead of panicking on a closed database. A store whose reads
// all closed reports nothing.
// Threat: the stall detector's memory blowup ("close store: leaked
// iterators") going unnoticed in tests because the close error was dropped
// or the check could not run before a fixture's own cleanup.
// Boundary: Store.Close over a real Pebble database in a temporary directory;
// the leak is made with the raw iterator and snapshot APIs the store wraps,
// which only this package can reach.
func TestStoreCloseReportsLeakedIteratorsAndSnapshots(t *testing.T) {
	clean, err := Open(filepath.Join(t.TempDir(), "clean.pebble"))
	if err != nil {
		t.Fatal(err)
	}
	if err := clean.PutJSON("leak-test/a", "a"); err != nil {
		t.Fatal(err)
	}
	if err := clean.IteratePrefix("leak-test/", 10, func(string, []byte) error { return nil }); err != nil {
		t.Fatal(err)
	}
	if err := clean.Close(); err != nil {
		t.Fatalf("close after closed reads: %v", err)
	}
	if err := clean.Close(); err != nil {
		t.Fatalf("second close: %v", err)
	}

	leaky, err := Open(filepath.Join(t.TempDir(), "iterator.pebble"))
	if err != nil {
		t.Fatal(err)
	}
	iter, err := leaky.db.NewIter(&pebble.IterOptions{LowerBound: []byte("a"), UpperBound: []byte("b")})
	if err != nil {
		t.Fatal(err)
	}
	_ = iter.First() // never closed
	if err := leaky.Close(); err == nil || !strings.Contains(err.Error(), "leaked iterators") {
		t.Fatalf("close with an open iterator = %v, want a leaked iterators error", err)
	}
	if err := leaky.Close(); err != nil {
		t.Fatalf("second close after a leak: %v", err)
	}

	snap, err := Open(filepath.Join(t.TempDir(), "snapshot.pebble"))
	if err != nil {
		t.Fatal(err)
	}
	_ = snap.db.NewSnapshot() // never closed
	if err := snap.Close(); err == nil || !strings.Contains(err.Error(), "leaked snapshots") {
		t.Fatalf("close with an open snapshot = %v, want a leaked snapshots error", err)
	}
}

// Purpose: GetWorker, read by the stall check for every worker-run task each
// minute, resolves an automation id through the automation index in exactly
// one hop. An index entry that names itself, or two entries naming each
// other, answer "not found" instead of recursing without bound; a real
// automation id still resolves to its worker.
// Threat: unbounded recursion (stack growth until the process dies) on a
// stale or corrupt index entry, reached every minute from the stall check.
// Boundary: WorkerStore.GetWorker over a real Pebble store with index records
// written at their canonical keys.
func TestGetWorkerAutomationLookupIsOneHop(t *testing.T) {
	s, err := Open(filepath.Join(t.TempDir(), "workers.pebble"))
	if err != nil {
		t.Fatal(err)
	}
	defer func() {
		if err := s.Close(); err != nil {
			t.Errorf("close: %v", err)
		}
	}()
	const account = "acct-one-hop"
	if err := s.PutJSON(KeyWorker(account, "w_real"), WorkerRecord{ID: "w_real", AccountScopeID: account, LifecycleState: WorkerLifecycleStateActive}); err != nil {
		t.Fatal(err)
	}
	for from, to := range map[string]string{"auto_real": "w_real", "w_self": "w_self", "w_loop_a": "w_loop_b", "w_loop_b": "w_loop_a"} {
		if err := s.PutJSON(KeyWorkerByAutomation(account, from), to); err != nil {
			t.Fatal(err)
		}
	}
	ws := NewWorkerStore(s)
	if w, found, err := ws.GetWorker(account, "auto_real"); err != nil || !found || w.ID != "w_real" {
		t.Fatalf("automation id: %+v %v %v", w, found, err)
	}
	for _, id := range []string{"w_self", "w_loop_a", "w_loop_b"} {
		if w, found, err := ws.GetWorker(account, id); err != nil || found {
			t.Fatalf("%s: %+v found=%v err=%v, want not found", id, w, found, err)
		}
	}
	if _, found, err := ws.GetWorker("other-account", "auto_real"); err != nil || found {
		t.Fatalf("another account resolved the worker: %v %v", found, err)
	}
}
