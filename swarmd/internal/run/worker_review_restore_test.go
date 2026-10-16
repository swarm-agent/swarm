package run

import (
	"encoding/json"
	"errors"
	"testing"
	"time"

	store "swarm/packages/swarmd/internal/store/pebble"
)

// Purpose: the worker sweep's restoreReviewWithBackoff must retain a durable
// deadline on checkout failure, preventing hot retries across restarts. The
// callback boundary isolates scheduling from sandbox/Git; serialization proves
// the persisted record carries the deadline, not a process-local timer.
func TestReviewRestoreBackoff(t *testing.T) {
	now := time.Unix(1000, 0)
	r := store.WorkerRunRecord{ID: "review", Role: store.WorkerRoleReviewer}
	var durable []byte
	save := func(next store.WorkerRunRecord) error { var err error; durable, err = json.Marshal(next); return err }
	calls := 0
	missing := errors.New("review checkout missing")
	restore := func() error {
		calls++
		var persisted store.WorkerRunRecord
		if err := json.Unmarshal(durable, &persisted); err != nil {
			t.Fatal(err)
		}
		if persisted.ReviewRestore.NextAttemptAt <= now.UnixMilli() {
			t.Fatal("attempt not fenced before filesystem access")
		}
		return missing
	}
	for attempt := 0; attempt < 7; attempt++ {
		ok, err := restoreReviewWithBackoff(&r, now, save, restore)
		if ok || !errors.Is(err, missing) {
			t.Fatalf("failure concealed: %v %v", ok, err)
		}
		delay := time.Duration(r.ReviewRestore.NextAttemptAt-now.UnixMilli()) * time.Millisecond
		want := 30 * time.Second << min(attempt, 3)
		if delay != want || r.ReviewRestore.LastError != missing.Error() {
			t.Fatalf("retry state: %+v", r.ReviewRestore)
		}
		for sweep := 1; sweep <= 10; sweep++ {
			if err := json.Unmarshal(durable, &r); err != nil {
				t.Fatal(err)
			}
			ok, err := restoreReviewWithBackoff(&r, now.Add(time.Duration(sweep)*time.Second), save, restore)
			if ok || err != nil || calls != attempt+1 {
				t.Fatalf("hot retry: calls=%d err=%v", calls, err)
			}
		}
		now = now.Add(delay)
	}
	ok, err := restoreReviewWithBackoff(&r, now, save, func() error { return nil })
	if !ok || err != nil || r.ReviewRestore != nil {
		t.Fatalf("recovery: %v %v %+v", ok, err, r)
	}
	// A persistence failure must not touch Git or claim successful recovery.
	calls = 0
	ok, err = restoreReviewWithBackoff(&r, now, func(store.WorkerRunRecord) error { return errors.New("store unavailable") }, restore)
	if ok || err == nil || calls != 0 {
		t.Fatal("unfenced attempt")
	}
}
