package publish

import (
	"context"
	"errors"
	"strings"
	"testing"
)

// Purpose: Queue.Process must durably journal terminal notifications and resume
// them without relanding. JSON round trips and a hook crash exercise the exact
// persistence/effect boundary, including a crash before acknowledgement.
func TestPublishLandNotificationRestart(t *testing.T) {
	for _, crash := range []bool{false, true} {
		t.Run(map[bool]string{false: "error", true: "crash"}[crash], func(t *testing.T) {
			store := queueMemory{}
			q := NewQueue(store)
			_, err := q.Enqueue(Entry{Account: "a", Request: Request{Folder: "f", JobID: "j"}})
			if err != nil {
				t.Fatal(err)
			}
			hooks := QueueHooks{
				Prepare: func(context.Context, *Entry) error { return nil },
				Land:    func(context.Context, Entry) (LandResult, error) { return LandResult{Commit: "head"}, nil },
				Finished: func(_ context.Context, e Entry) error {
					saved, _, err := NewQueue(store).Get("a", "j")
					if err != nil || !saved.NotificationPending || saved.State != "landed" {
						t.Fatalf("not journaled: %+v %v", saved, err)
					}
					if crash {
						panic("crash")
					}
					return errors.New("unavailable")
				},
			}
			func() {
				defer func() {
					if r := recover(); r != nil && r != "crash" {
						panic(r)
					}
				}()
				if err := q.Process(context.Background(), "a", "f", hooks); err == nil {
					t.Fatal("expected hook failure")
				}
			}()
			q = NewQueue(store)
			pending, err := q.Pending("a")
			if err != nil || len(pending) != 1 {
				t.Fatalf("pending=%v err=%v", pending, err)
			}
			calls := 0
			hooks.Prepare = func(context.Context, *Entry) error { t.Fatal("reprepared terminal entry"); return nil }
			hooks.Finished = func(context.Context, Entry) error { calls++; return nil }
			if err := q.Process(context.Background(), "a", "f", hooks); err != nil {
				t.Fatal(err)
			}
			if err := q.Process(context.Background(), "a", "f", hooks); err != nil {
				t.Fatal(err)
			}
			pending, err = q.Pending("a")
			if err != nil || len(pending) != 0 || calls != 1 {
				t.Fatalf("pending=%v calls=%d err=%v", pending, calls, err)
			}
		})
	}
}

// Purpose: Queue.Process journals follow-up identity/count before dispatch.
// Crashes before and after external acceptance must replay the same ID without
// Prepare, while the next rejection gets a new ID. This unit boundary models
// an idempotent dispatcher and checks durable postconditions after restart.
func TestPublishLandSendBackRestart(t *testing.T) {
	for _, accepted := range []bool{false, true} {
		t.Run(map[bool]string{false: "before-dispatch", true: "after-acceptance"}[accepted], func(t *testing.T) {
			store := queueMemory{}
			q := NewQueue(store)
			entry := Entry{Account: "a", Request: Request{Folder: "f", JobID: "j"}}
			if _, err := q.Enqueue(entry); err != nil {
				t.Fatal(err)
			}
			ids := map[string]bool{}
			first := ""
			prepares := 0
			hooks := QueueHooks{Prepare: func(context.Context, *Entry) error { prepares++; return &SendBackError{Reason: "fix"} }, SendBack: func(_ context.Context, e Entry) error {
				saved, _, err := NewQueue(store).Get("a", "j")
				if err != nil || !saved.SendBackPending || saved.SendBacks != 1 || saved.FollowUpID == "" || saved.FollowUpID != e.FollowUpID {
					t.Fatalf("not journaled: %+v %v", saved, err)
				}
				first = e.FollowUpID
				if accepted {
					ids[first] = true
				}
				panic("crash")
			}}
			func() {
				defer func() {
					if r := recover(); r != "crash" {
						t.Fatalf("expected crash: %v", r)
					}
				}()
				_ = q.Process(context.Background(), "a", "f", hooks)
			}()
			q = NewQueue(store)
			if _, _, err := q.EnqueueDelivery(entry, true); err != nil {
				t.Fatal(err)
			}
			hooks.SendBack = func(_ context.Context, e Entry) error {
				if e.SendBacks == 1 && e.FollowUpID != first {
					t.Fatal("changed ID")
				}
				ids[e.FollowUpID] = true
				return nil
			}
			if err := q.Process(context.Background(), "a", "f", hooks); err != nil {
				t.Fatal(err)
			}
			if prepares != 1 || len(ids) != 1 {
				t.Fatalf("prepares=%d ids=%v", prepares, ids)
			}
			if _, err := q.Enqueue(entry); err != nil {
				t.Fatal(err)
			}
			if err := q.Process(context.Background(), "a", "f", hooks); err != nil {
				t.Fatal(err)
			}
			saved, _, err := q.Get("a", "j")
			if err != nil || saved.State != "landing_stopped" || saved.SendBacks != 2 || saved.SendBackPending || len(ids) != 2 || prepares != 2 {
				t.Fatalf("entry=%+v ids=%v prepares=%d err=%v", saved, ids, prepares, err)
			}
		})
	}
}

// Purpose: formatter and Queue.Process must preserve full reproduction headers
// through actual send-back delivery, including oversized command definitions;
// only output and conflict detail may be bounded. No shell execution is needed.
func TestPublishLandSendBackInstructions(t *testing.T) {
	base := Base{Branch: "dev", Commit: strings.Repeat("a", 40)}
	for _, command := range []string{"go test ./internal/publish", strings.Repeat("x", 8000)} {
		reason := ChecksSendBack(base, []string{command}, false, strings.Repeat("output", 2000)).Reason
		q := NewQueue(queueMemory{})
		if _, err := q.Enqueue(Entry{Account: "a", Request: Request{Folder: "f", JobID: "j"}}); err != nil {
			t.Fatal(err)
		}
		err := q.Process(context.Background(), "a", "f", QueueHooks{Prepare: func(context.Context, *Entry) error { return &SendBackError{Reason: reason} }, SendBack: func(_ context.Context, e Entry) error {
			if e.Reason != reason || !strings.Contains(e.Reason, base.Commit) || !strings.Contains(e.Reason, command) {
				t.Fatal("instructions truncated")
			}
			return nil
		}})
		if err != nil {
			t.Fatal(err)
		}
		if len(command) < 4096 && len(reason) > 4096 {
			t.Fatal("output exceeded budget")
		}
	}
	reason := RebaseConflictSendBack(base, strings.Repeat("file\n", 2000)).Reason
	if !strings.Contains(reason, base.Commit) || !strings.Contains(reason, "and 1980 more") || len(reason) > 4096 {
		t.Fatalf("bad conflict summary: %s", reason)
	}
}
