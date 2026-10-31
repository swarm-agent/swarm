package pebblestore

import (
	"path/filepath"
	"reflect"
	"strings"
	"sync"
	"testing"
	"time"
)

// Purpose: AuthStore's machine-slot mutations preserve exact order and the
// longest observed reset under concurrency and restart. Invalid permutations
// and traversal IDs must fail without changing routing state. Real Pebble is
// the narrowest boundary that proves atomic persistence rather than a mock.
func TestClaudeCodeSlotsAtomicOrderLimitsAndRestart(t *testing.T) {
	path := filepath.Join(t.TempDir(), "db")
	db, err := Open(path)
	if err != nil {
		t.Fatal(err)
	}
	auth := NewAuthStore(db)
	a, b := strings.Repeat("a", 32), strings.Repeat("b", 32)
	for _, id := range []string{a, b} {
		if err := auth.AddClaudeCodeSlot(id); err != nil {
			t.Fatal(err)
		}
	}
	original, err := auth.ClaudeCodeSlots()
	if err != nil {
		t.Fatal(err)
	}
	for _, bad := range [][]string{{a, b}, {a, a, "default"}, {a, b, "../bad"}} {
		if err := auth.OrderClaudeCodeSlots(bad); err == nil {
			t.Fatalf("accepted order %v", bad)
		}
		after, _ := auth.ClaudeCodeSlots()
		if !reflect.DeepEqual(original, after) {
			t.Fatal("rejected order changed state")
		}
	}
	if err := auth.AddClaudeCodeSlot("../../escape"); err == nil {
		t.Fatal("accepted traversal")
	}
	if err := auth.OrderClaudeCodeSlots([]string{b, "default", a}); err != nil {
		t.Fatal(err)
	}
	longer := time.Date(2026, 11, 2, 13, 0, 0, 0, time.UTC)
	var wg sync.WaitGroup
	for _, at := range []time.Time{longer, longer.Add(-time.Hour)} {
		wg.Add(1)
		go func(at time.Time) {
			defer wg.Done()
			if err := auth.RecordClaudeCodeLimit(a, at); err != nil {
				t.Error(err)
			}
		}(at)
	}
	wg.Wait()
	if err := db.Close(); err != nil {
		t.Fatal(err)
	}
	db, err = Open(path)
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	actual, err := NewAuthStore(db).ClaudeCodeSlots()
	if err != nil {
		t.Fatal(err)
	}
	if len(actual) != 3 || actual[0].ID != b || !actual[1].Legacy || actual[2].ID != a || !actual[2].LimitUntil.Equal(longer) {
		t.Fatalf("restart slots=%+v", actual)
	}
}
