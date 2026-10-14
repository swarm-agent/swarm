package publish

import (
	"context"
	"encoding/json"
	"errors"
	"testing"
)

type queueMemory map[string][]byte

func (m queueMemory) GetJSON(k string, v any) (bool, error) {
	b, ok := m[k]
	if !ok {
		return false, nil
	}
	return true, json.Unmarshal(b, v)
}
func (m queueMemory) PutJSON(k string, v any) error { b, e := json.Marshal(v); m[k] = b; return e }

// Purpose: Queue owns durable FIFO delivery and bounded send-backs. This unit
// boundary proves restart recovery and MOVED limits without privileged commands;
// integration tests must separately prove sandbox Git and actual forge receipts.
func TestQueueLifecycle(t *testing.T) {
	for _, tc := range []struct {
		name  string
		moved int
		fail  bool
		want  string
	}{
		{"clean", 0, false, "landed"}, {"moved", 1, false, "landed"}, {"moving", 3, false, "land_failed"}, {"checks", 0, true, "sent_back"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			store := queueMemory{}
			q := NewQueue(store)
			ctx := context.Background()
			entry := Entry{Account: "account", Request: Request{Folder: "folder", JobID: "one"}}
			if p, e := q.Enqueue(entry); e != nil || p != 1 {
				t.Fatalf("enqueue %d %v", p, e)
			}
			n := 0
			backs := 0
			hooks := QueueHooks{Prepare: func(context.Context, *Entry) error {
				if tc.fail {
					return &SendBackError{Reason: "checks failed"}
				}
				return nil
			}, Land: func(context.Context, Entry) (LandResult, error) {
				n++
				return LandResult{Moved: n <= tc.moved, Commit: "commit"}, nil
			}, SendBack: func(context.Context, Entry) error { backs++; return nil }}
			if e := q.Process(ctx, "account", "folder", hooks); e != nil {
				t.Fatal(e)
			}
			got, _, e := q.Get("account", "one")
			if e != nil || got.State != tc.want {
				t.Fatalf("%+v %v", got, e)
			}
			if tc.fail {
				if backs != 1 {
					t.Fatal(backs)
				}
				if _, e := q.Enqueue(entry); e != nil {
					t.Fatal(e)
				}
				if e := q.Process(ctx, "account", "folder", hooks); e != nil {
					t.Fatal(e)
				}
				got, _, _ = q.Get("account", "one")
				if got.State != "landing_stopped" || backs != 2 {
					t.Fatalf("%+v backs=%d", got, backs)
				}
			}
		})
	}
}

// Purpose: Queue persistence must not lose an in-flight job on restart or let a
// failed job block subsequent jobs. A JSON store round trip is the narrowest
// layer exercising the production queue's recovery and transition contract.
func TestQueueRestartAndOrder(t *testing.T) {
	store := queueMemory{}
	q := NewQueue(store)
	for _, id := range []string{"one", "two"} {
		if _, e := q.Enqueue(Entry{Account: "a", Request: Request{Folder: "f", JobID: id}}); e != nil {
			t.Fatal(e)
		}
	}
	if e := q.Process(context.Background(), "a", "f", QueueHooks{Prepare: func(context.Context, *Entry) error { return context.Canceled }}); !errors.Is(e, context.Canceled) {
		t.Fatal(e)
	}
	q = NewQueue(store)
	var order []string
	if e := q.Process(context.Background(), "a", "f", QueueHooks{Prepare: func(_ context.Context, e *Entry) error { order = append(order, e.Request.JobID); return nil }, Land: func(context.Context, Entry) (LandResult, error) { return LandResult{Commit: "head"}, nil }}); e != nil {
		t.Fatal(e)
	}
	if len(order) != 2 || order[0] != "one" || order[1] != "two" {
		t.Fatal(order)
	}
}
