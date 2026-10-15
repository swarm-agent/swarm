package pebblestore

import (
	"encoding/json"
	"fmt"
	"io"
	"path/filepath"
	"testing"

	"github.com/cockroachdb/pebble"
)

type permissionCountingReader struct {
	pebble.Reader
	gets []string
	t    *testing.T
}

func (r *permissionCountingReader) Get(key []byte) ([]byte, io.Closer, error) {
	r.gets = append(r.gets, string(key))
	return r.Reader.Get(key)
}
func (r *permissionCountingReader) NewIter(o *pebble.IterOptions) (*pebble.Iterator, error) {
	r.t.Fatal("permission lookup must not iterate history")
	return nil, nil
}

// Purpose: permission classification must have history-independent work and
// verify its account/session binding. Real Pebble history plus a counting reader
// proves a negative lookup reads only the index, positive reads verify the
// receipt, and stale/cross-session locators cannot confer worker authority.
func TestPermissionWorkerSessionIndexBounded(t *testing.T) {
	s, err := Open(filepath.Join(t.TempDir(), "db"))
	if err != nil {
		t.Fatal(err)
	}
	defer s.Close()
	batch := s.NewBatch()
	defer batch.Close()
	for i := 0; i < 2000; i++ {
		run := WorkerRunRecord{AccountScopeID: "account", WorkerID: "worker", ID: fmt.Sprint(i), SessionID: fmt.Sprintf("session-%d", i)}
		raw, _ := json.Marshal(run)
		if err := batch.Set([]byte(KeyWorkerRun("account", "worker", run.ID)), raw, nil); err != nil {
			t.Fatal(err)
		}
		if err := indexWorkerRunInBatch(batch, "account", raw); err != nil {
			t.Fatal(err)
		}
	}
	if err := batch.Commit(pebble.Sync); err != nil {
		t.Fatal(err)
	}
	for _, tc := range []struct {
		session string
		count   int
		found   bool
	}{{"interactive", 1, false}, {"session-1999", 2, true}} {
		r := &permissionCountingReader{Reader: s.db, t: t}
		_, found, err := workerRunForSessionFromReader(r, "account", tc.session)
		if err != nil || found != tc.found || len(r.gets) != tc.count || r.gets[0] != keyWorkerRunSession("account", tc.session) {
			t.Fatalf("lookup: found=%v err=%v reads=%v", found, err, r.gets)
		}
	}
	if err := s.PutJSON(keyWorkerRunSession("account", "forged"), workerRunIndexEntry{WorkerID: "worker", RunID: "1999"}); err != nil {
		t.Fatal(err)
	}
	if _, found, err := NewPermissionStore(s).WorkerRunForSession("account", "forged"); err == nil || found {
		t.Fatalf("stale locator accepted: %v %v", found, err)
	}
}

// Purpose: version-1 stores must gain session indexes before serving, survive
// restart, and skip history on subsequent startup. The production migration and
// real database exercise the version upgrade and canonical receipt postcondition.
func TestPermissionWorkerSessionIndexMigration(t *testing.T) {
	path := filepath.Join(t.TempDir(), "db")
	s, err := Open(path)
	if err != nil {
		t.Fatal(err)
	}
	run := WorkerRunRecord{AccountScopeID: "account", WorkerID: "worker", ID: "run", SessionID: "session"}
	if err := s.PutJSON(KeyWorkerRun("account", "worker", "run"), run); err != nil {
		t.Fatal(err)
	}
	if err := s.PutJSON(keyWorkerRunIndexVersion, map[string]int{"version": 1}); err != nil {
		t.Fatal(err)
	}
	n, err := RunWorkerRunIndexMigration(s)
	if err != nil || n != 1 {
		t.Fatalf("migration: %d %v", n, err)
	}
	if err := s.Close(); err != nil {
		t.Fatal(err)
	}
	s, err = Open(path)
	if err != nil {
		t.Fatal(err)
	}
	defer s.Close()
	n, err = RunWorkerRunIndexMigration(s)
	if err != nil || n != 0 {
		t.Fatalf("repeat migration: %d %v", n, err)
	}
	got, found, err := NewPermissionStore(s).WorkerRunForSession("account", "session")
	if err != nil || !found || got.ID != "run" {
		t.Fatalf("backfill missing: %+v %v %v", got, found, err)
	}
}
