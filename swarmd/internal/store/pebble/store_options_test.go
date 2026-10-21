package pebblestore

import (
	"fmt"
	"path/filepath"
	"testing"

	"github.com/cockroachdb/pebble"
)

// Requirement: the daemon store opens Pebble with a real block cache and a
// bloom filter on every level, for both read-write and read-only opens, and
// releases the cache on Close. Threat: all-default options (8 MiB cache, no
// filters) made every point Get, including Gets for absent keys, pread and
// checksum sstable blocks. Owner: storeOptions/openWithOptions/Store.Close.
// The store package is the narrowest layer that owns the Pebble options.
func TestStoreOpensWithBlockCacheAndBloomFilters(t *testing.T) {
	path := filepath.Join(t.TempDir(), "options.pebble")
	s, err := Open(path)
	if err != nil {
		t.Fatal(err)
	}
	assertStoreReadOptions(t, s)
	for i := 0; i < 64; i++ {
		if err := s.PutJSON(fmt.Sprintf("present/%03d", i), i); err != nil {
			t.Fatal(err)
		}
	}
	if err := s.db.Flush(); err != nil {
		t.Fatal(err)
	}
	before := s.db.Metrics().Filter
	for i := 0; i < 32; i++ {
		var v int
		if found, err := s.GetJSON(fmt.Sprintf("present/%03d-absent", i), &v); err != nil || found {
			t.Fatalf("absent key: found=%v err=%v", found, err)
		}
	}
	after := s.db.Metrics().Filter
	if after.Hits <= before.Hits {
		t.Fatalf("bloom filter hits %d -> %d: absent-key Gets were not filtered", before.Hits, after.Hits)
	}
	if err := s.Close(); err != nil {
		t.Fatal(err)
	}
	if s.cache != nil {
		t.Fatal("Close did not release the block cache reference")
	}

	ro, err := OpenReadOnly(path)
	if err != nil {
		t.Fatal(err)
	}
	assertStoreReadOptions(t, ro)
	var v int
	if found, err := ro.GetJSON("present/007", &v); err != nil || !found || v != 7 {
		t.Fatalf("read-only Get: found=%v v=%d err=%v", found, v, err)
	}
	if err := ro.Close(); err != nil {
		t.Fatal(err)
	}
	if ro.cache != nil {
		t.Fatal("read-only Close did not release the block cache reference")
	}
}

func assertStoreReadOptions(t *testing.T, s *Store) {
	t.Helper()
	if s.cache == nil || s.cache.MaxSize() != storeBlockCacheBytes {
		t.Fatalf("block cache = %v, want %d bytes", s.cache, storeBlockCacheBytes)
	}
	if got := s.db.Metrics().BlockCache.Size; got < 0 {
		t.Fatalf("block cache metrics unavailable: %d", got)
	}
	opts := storeOptions(false)
	defer opts.Cache.Unref()
	if len(opts.Levels) != 7 {
		t.Fatalf("levels = %d, want 7", len(opts.Levels))
	}
	for i, l := range opts.Levels {
		if l.FilterPolicy == nil || l.FilterType != pebble.TableFilter {
			t.Fatalf("level %d filter = %v/%v, want bloom table filter", i, l.FilterPolicy, l.FilterType)
		}
	}
	if _, ok := opts.Filters[opts.Levels[0].FilterPolicy.Name()]; !ok {
		t.Fatal("bloom filter policy is not registered for reading existing tables")
	}
}

// Requirement: the V2 schedule hint reports no work for an empty catalog so
// an idle daemon never sweeps. Owner: SessionStore.AutomationV2ScheduleHint.
func TestAutomationV2ScheduleHintEmptyCatalog(t *testing.T) {
	s, err := Open(filepath.Join(t.TempDir(), "hint.pebble"))
	if err != nil {
		t.Fatal(err)
	}
	defer s.Close()
	hint, err := NewSessionStore(s).AutomationV2ScheduleHint(1)
	if err != nil {
		t.Fatal(err)
	}
	if hint != (AutomationV2ScheduleHint{}) {
		t.Fatalf("empty catalog hint = %+v, want zero", hint)
	}
}
