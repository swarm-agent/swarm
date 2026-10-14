package publish

import (
	"context"
	"errors"
	"testing"
)

// Purpose: Queue.Enqueue must make explicit stopped-job delivery a fresh FIFO
// attempt, not silently keep a terminal entry. The queue/store unit boundary is
// the narrowest layer proving reset metadata, ordering and durable postconditions.
func TestQueueStoppedRedelivery(t *testing.T) {
	q := NewQueue(queueMemory{})
	entry := Entry{Account: "a", Request: Request{Folder: "f", JobID: "stopped"}}
	if _, err := q.Enqueue(entry); err != nil {
		t.Fatal(err)
	}
	entry.State, entry.SendBacks, entry.Reason = "landing_stopped", 2, "old conflict"
	entry.Checks = []CheckResult{{ExitCode: 1, Output: "old failure"}}
	if err := q.save(entry); err != nil {
		t.Fatal(err)
	}
	if _, err := q.Enqueue(Entry{Account: "a", Request: Request{Folder: "f", JobID: "other"}}); err != nil {
		t.Fatal(err)
	}
	receipt, autoPos, err := q.EnqueueDelivery(entry, false)
	if err != nil || receipt.State != "landing_stopped" || receipt.SendBacks != 2 || autoPos != 0 {
		t.Fatalf("automatic replay restarted stopped job: %+v pos=%d err=%v", receipt, autoPos, err)
	}
	pos, err := q.Enqueue(entry)
	if err != nil {
		t.Fatal(err)
	}
	got, storedPos, err := q.Get("a", "stopped")
	if err != nil || pos != 2 || storedPos != 2 || got.State != "queued" || got.SendBacks != 0 || got.Attempt != 1 || got.Reason != "" || len(got.Checks) != 0 {
		t.Fatalf("redelivery pos=%d storedPos=%d entry=%+v err=%v", pos, storedPos, got, err)
	}
}

// Purpose: Queue.Enqueue/Get must preserve landed receipts and actual active
// positions, preventing duplicate Git landing or misleading delivery results.
// Store round trips and hook call counts prove the queue contract without Git.
func TestQueueRedeliveryReceipts(t *testing.T) {
	q := NewQueue(queueMemory{})
	for _, id := range []string{"landed", "first", "active"} {
		entry := Entry{Account: "a", Request: Request{Folder: "f", JobID: id}}
		if _, err := q.Enqueue(entry); err != nil {
			t.Fatal(err)
		}
		if id == "landed" {
			entry.State, entry.Commit = "landed", "verified-commit"
			if err := q.save(entry); err != nil {
				t.Fatal(err)
			}
		}
		if id == "active" {
			entry.State = "landing"
			if err := q.save(entry); err != nil {
				t.Fatal(err)
			}
		}
	}
	for _, tc := range []struct {
		id, state, commit string
		pos               int
	}{{"landed", "landed", "verified-commit", 0}, {"active", "landing", "", 2}} {
		receipt, pos, err := q.EnqueueDelivery(Entry{Account: "a", Request: Request{Folder: "f", JobID: tc.id}}, true)
		if receipt.State != tc.state || receipt.Commit != tc.commit {
			t.Fatalf("false receipt: %+v", receipt)
		}
		got, storedPos, getErr := q.Get("a", tc.id)
		if err != nil || getErr != nil || pos != tc.pos || storedPos != tc.pos || got.State != tc.state || got.Commit != tc.commit {
			t.Fatalf("%s: %+v pos=%d/%d err=%v/%v", tc.id, got, pos, storedPos, err, getErr)
		}
	}
	calls := 0
	if err := q.Process(context.Background(), "a", "f", QueueHooks{Prepare: func(_ context.Context, e *Entry) error {
		if e.Request.JobID == "landed" {
			t.Fatal("landed job rerun")
		}
		calls++
		return nil
	}, Land: func(context.Context, Entry) (LandResult, error) { return LandResult{Commit: "new"}, nil }}); err != nil {
		t.Fatal(err)
	}
	if calls != 2 {
		t.Fatalf("prepare calls=%d", calls)
	}
}

// Purpose: Queue.Process must defer unsettled jobs rather than recording a
// terminal landing failure. Prepare owns readiness; this unit test proves the
// durable queued state, absence of side effects and later settled processing.
func TestQueueUnsettledDeferral(t *testing.T) {
	q := NewQueue(queueMemory{})
	if _, err := q.Enqueue(Entry{Account: "a", Request: Request{Folder: "f", JobID: "running"}}); err != nil {
		t.Fatal(err)
	}
	settled, landed, finished := false, 0, 0
	h := QueueHooks{Prepare: func(context.Context, *Entry) error {
		if !settled {
			return ErrNotSettled
		}
		return nil
	}, Land: func(context.Context, Entry) (LandResult, error) {
		landed++
		return LandResult{Commit: "settled-head"}, nil
	}, Finished: func(context.Context, Entry) error { finished++; return nil }}
	if err := q.Process(context.Background(), "a", "f", h); err != nil && !errors.Is(err, ErrNotSettled) {
		t.Fatal(err)
	}
	got, pos, err := q.Get("a", "running")
	if err != nil || got.State != "queued" || got.Reason != "" || got.SendBacks != 0 || pos != 1 || landed != 0 || finished != 0 {
		t.Fatalf("deferred %+v pos=%d land=%d finish=%d err=%v", got, pos, landed, finished, err)
	}
	settled = true
	if err := q.Process(context.Background(), "a", "f", h); err != nil {
		t.Fatal(err)
	}
	got, _, err = q.Get("a", "running")
	if err != nil || got.State != "landed" || got.Commit != "settled-head" || landed != 1 || finished != 1 {
		t.Fatalf("settled %+v land=%d finish=%d err=%v", got, landed, finished, err)
	}
}
