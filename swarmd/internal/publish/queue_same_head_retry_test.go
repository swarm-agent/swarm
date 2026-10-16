package publish

import (
	"context"
	"testing"
)

// Purpose: Queue.EnqueueDelivery must durably consume follow-up identities at
// admission, so replay after a second failure or restart cannot renew a retry.
// Queue-level transitions are the narrowest boundary for budget and dedup checks.
func TestQueueCompletedFollowupRetryOnce(t *testing.T) {
	for _, changed := range []bool{false, true} {
		name := "unchanged"
		if changed {
			name = "changed"
		}
		t.Run(name, func(t *testing.T) {
			mem := queueMemory{}
			q := NewQueue(mem)
			req := Request{JobID: "job", Folder: "folder", Head: "original"}
			if _, err := q.Enqueue(Entry{Account: "account", Request: req}); err != nil {
				t.Fatal(err)
			}
			hooks := QueueHooks{Prepare: func(context.Context, *Entry) error { return &SendBackError{Reason: "same failure"} }, SendBack: func(context.Context, Entry) error { return nil }}
			fail := func() {
				t.Helper()
				if err := q.Process(context.Background(), "account", "folder", hooks); err != nil {
					t.Fatal(err)
				}
			}
			enqueue := func(run, want string, backs int) {
				t.Helper()
				got, _, err := q.EnqueueDelivery(Entry{Account: "account", Request: req, RetryFollowupRunID: run}, false)
				if err != nil || got.State != want || got.SendBacks != backs || got.Attempt != 0 {
					t.Fatalf("entry=%+v err=%v", got, err)
				}
				if run != "" && got.RetryFollowupRunID != run {
					t.Fatalf("missing durable run identity: %+v", got)
				}
			}
			fail()
			if changed {
				req.Head = "first-fix"
			}
			enqueue("followup-1", "queued", 1)
			fail()
			q = NewQueue(mem)
			enqueue("followup-1", "landing_stopped", 2)
			if changed {
				req.Head = "second-fix"
			}
			enqueue("followup-2", "queued", 2)
			fail()
			q = NewQueue(mem)
			enqueue("followup-2", "landing_stopped", 3)
		})
	}
}

// Purpose: legacy changed-head fix admission through Queue.EnqueueDelivery must
// still preserve its budget when no completed-run identity is supplied.
func TestQueueChangedHeadRetryPreserved(t *testing.T) {
	q := NewQueue(queueMemory{})
	req := Request{JobID: "job", Folder: "folder", Head: "original"}
	if _, err := q.Enqueue(Entry{Account: "account", Request: req}); err != nil {
		t.Fatal(err)
	}
	if err := q.Process(context.Background(), "account", "folder", QueueHooks{Prepare: func(context.Context, *Entry) error { return &SendBackError{Reason: "checks"} }, SendBack: func(context.Context, Entry) error { return nil }}); err != nil {
		t.Fatal(err)
	}
	req.Head = "fix"
	got, _, err := q.EnqueueDelivery(Entry{Account: "account", Request: req}, false)
	if err != nil || got.State != "queued" || got.SendBacks != 1 || got.Attempt != 0 {
		t.Fatalf("entry=%+v err=%v", got, err)
	}
}
