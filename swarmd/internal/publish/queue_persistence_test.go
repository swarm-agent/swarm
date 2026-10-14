package publish

import (
	"context"
	"errors"
	"path/filepath"
	pebblestore "swarm/packages/swarmd/internal/store/pebble"
	"testing"
)

// Purpose: Queue.Process must resume persisted landing at Prepare after a real
// store restart. This Pebble-backed boundary proves disk recovery, not live
// daemon restarts during external Git or forge pushes.
func TestQueuePebbleRestart(t *testing.T) {
	path := filepath.Join(t.TempDir(), "db")
	store, err := pebblestore.Open(path)
	if err != nil {
		t.Fatal(err)
	}
	q := NewQueue(store)
	if _, err = q.Enqueue(Entry{Account: "a", Request: Request{Folder: "f", JobID: "one"}}); err != nil {
		t.Fatal(err)
	}
	if err = q.Process(context.Background(), "a", "f", QueueHooks{Prepare: func(context.Context, *Entry) error { return context.Canceled }}); !errors.Is(err, context.Canceled) {
		t.Fatal(err)
	}
	if err = store.Close(); err != nil {
		t.Fatal(err)
	}
	store, err = pebblestore.Open(path)
	if err != nil {
		t.Fatal(err)
	}
	defer store.Close()
	q = NewQueue(store)
	entry, pos, err := q.Get("a", "one")
	if err != nil || entry.State != "landing" || pos != 1 {
		t.Fatalf("%+v %d %v", entry, pos, err)
	}
	prepares := 0
	if err = q.Process(context.Background(), "a", "f", QueueHooks{Prepare: func(context.Context, *Entry) error { prepares++; return nil }, Land: func(context.Context, Entry) (LandResult, error) { return LandResult{Commit: "head"}, nil }}); err != nil {
		t.Fatal(err)
	}
	entry, _, err = q.Get("a", "one")
	if err != nil || entry.State != "landed" || prepares != 1 {
		t.Fatalf("%+v %d %v", entry, prepares, err)
	}
}
