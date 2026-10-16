package publish

import (
	"context"
	"strings"
	"testing"
)

// Purpose: Queue.EnqueueDelivery and Process must allow the final issued fix
// round to land without renewing its budget. This queue-level regression proves
// durable admission, stale-head rejection and exhaustion without external Git.
func TestPublishQueueStoppedFixRound(t *testing.T) {
	q := NewQueue(queueMemory{})
	req := Request{JobID: "job", Folder: "folder", Head: "original"}
	if _, err := q.Enqueue(Entry{Account: "account", Request: req}); err != nil {
		t.Fatal(err)
	}
	backs := 0
	hooks := QueueHooks{
		Prepare:  func(context.Context, *Entry) error { return &SendBackError{Reason: "conflict"} },
		SendBack: func(context.Context, Entry) error { backs++; return nil },
	}
	for _, head := range []string{"first-fix", "second-fix"} {
		if err := q.Process(context.Background(), "account", "folder", hooks); err != nil {
			t.Fatal(err)
		}
		old, _, _ := q.Get("account", "job")
		if old.State == "landing_stopped" {
			same, pos, err := q.EnqueueDelivery(Entry{Account: "account", Request: req}, false)
			if err != nil || pos != 0 || same.State != old.State {
				t.Fatalf("stale head admitted: %+v pos=%d err=%v", same, pos, err)
			}
		}
		req.Head = head
		got, pos, err := q.EnqueueDelivery(Entry{Account: "account", Request: req}, false)
		if err != nil || got.State != "queued" || pos != 1 || got.SendBacks != backs || got.Attempt != 0 {
			t.Fatalf("fix not queued with preserved budget: %+v pos=%d err=%v", got, pos, err)
		}
	}
	var terminal Entry
	hooks.Finished = func(_ context.Context, e Entry) error { terminal = e; return nil }
	if err := q.Process(context.Background(), "account", "folder", hooks); err != nil {
		t.Fatal(err)
	}
	if backs != 2 || terminal.State != "landing_stopped" || !strings.Contains(terminal.Reason, "exhausted") {
		t.Fatalf("unbounded rounds or missing terminal reason: backs=%d entry=%+v", backs, terminal)
	}
	req.Head = "extra-fix"
	got, pos, err := q.EnqueueDelivery(Entry{Account: "account", Request: req}, false)
	if err != nil || pos != 0 || got.State != "landing_stopped" || got.Request.Head != "second-fix" {
		t.Fatalf("exhausted job requeued: %+v pos=%d err=%v", got, pos, err)
	}
}
