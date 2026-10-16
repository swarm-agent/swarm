package publish

import (
	"context"
	"fmt"
	"sync"
	"testing"
	"time"
)

// Purpose: Queue.Process must overlap candidate checks while preserving FIFO
// pushes. The queue hook boundary is the narrowest deterministic layer for
// detecting serial execution without a provider or daemon.
func TestLandingTrainConcurrent(t *testing.T) {
	q := NewQueue(queueMemory{})
	if configurable, ok := any(q).(interface{ SetParallel(int) }); ok {
		configurable.SetParallel(3)
	}
	for i := 1; i <= 3; i++ {
		_, err := q.Enqueue(Entry{Account: "account", Request: Request{JobID: fmt.Sprint(i), Folder: "folder"}})
		if err != nil {
			t.Fatal(err)
		}
	}
	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
	defer cancel()
	var mu sync.Mutex
	started := 0
	ready := make(chan struct{})
	var pushed []string
	err := q.Process(ctx, "account", "folder", QueueHooks{
		Prepare: func(ctx context.Context, e *Entry) error {
			mu.Lock()
			started++
			if started == 3 {
				close(ready)
			}
			mu.Unlock()
			select {
			case <-ready:
				return nil
			case <-ctx.Done():
				return ctx.Err()
			}
		},
		Land: func(_ context.Context, e Entry) (LandResult, error) {
			pushed = append(pushed, e.Request.JobID)
			return LandResult{Commit: e.Request.JobID}, nil
		},
	})
	if err != nil {
		t.Fatalf("three checks did not overlap: %v", err)
	}
	if fmt.Sprint(pushed) != "[1 2 3]" {
		t.Fatalf("push order %v", pushed)
	}
}

// Purpose: exercise failed-check/conflict suffix invalidation, the serial
// compatibility path, external movement and crash reconciliation at Queue's
// durable boundary. The fake target rejects any untested base/head pair.
func TestLandingTrainRecovery(t *testing.T) {
	for _, scenario := range []string{"pass", "failure", "conflict", "serial", "moved", "restart"} {
		t.Run(scenario, func(t *testing.T) {
			store := queueMemory{}
			q := NewQueue(store)
			q.SetParallel(3)
			if scenario == "serial" {
				q.SetParallel(1)
			}
			for i := 1; i <= 3; i++ {
				if _, err := q.Enqueue(Entry{Account: "a", Request: Request{JobID: fmt.Sprint(i), Folder: "f"}}); err != nil {
					t.Fatal(err)
				}
			}
			ctx, cancel := context.WithTimeout(context.Background(), 3*time.Second)
			defer cancel()
			tip := "base"
			var pushed, backs []string
			counts := map[string]int{}
			tested := map[string]bool{}
			var mu sync.Mutex
			stopped := false
			moved := false
			stack := func(_ context.Context, e *Entry, prev *Entry) error {
				if scenario == "conflict" && e.Request.JobID == "2" {
					return &SendBackError{Reason: "conflict file.go"}
				}
				e.Request.BaseCommit = tip
				if prev != nil {
					e.Request.BaseCommit = prev.Request.Head
				}
				e.Request.Head = e.Request.BaseCommit + "+" + e.Request.JobID
				return nil
			}
			hooks := QueueHooks{Stack: stack, Prepare: func(ctx context.Context, e *Entry) error {
				if scenario == "serial" {
					if err := stack(ctx, e, nil); err != nil {
						return err
					}
				}
				mu.Lock()
				defer mu.Unlock()
				counts[e.Request.JobID]++
				tested[e.Request.Head] = true
				if scenario == "failure" && e.Request.JobID == "2" {
					return &SendBackError{Reason: "failing lines"}
				}
				return nil
			}, Land: func(_ context.Context, e Entry) (LandResult, error) {
				if !tested[e.Request.Head] {
					t.Fatalf("untested push %s", e.Request.Head)
				}
				if scenario == "moved" && !moved {
					moved = true
					tip = "external"
					return LandResult{Moved: true}, nil
				}
				if e.Request.BaseCommit != tip {
					t.Fatalf("wrong base: %s != %s", e.Request.BaseCommit, tip)
				}
				tip = e.Request.Head
				pushed = append(pushed, e.Request.JobID)
				if scenario == "restart" && !stopped {
					stopped = true
					return LandResult{}, context.Canceled
				}
				return LandResult{Commit: tip}, nil
			}, Recover: func(_ context.Context, e Entry) (bool, error) { return e.Request.Head == tip, nil }, SendBack: func(_ context.Context, e Entry) error { backs = append(backs, e.Reason); return nil }}
			err := q.Process(ctx, "a", "f", hooks)
			if scenario == "restart" {
				if err == nil {
					t.Fatal("missing interruption")
				}
				q = NewQueue(store)
				q.SetParallel(3)
				err = q.Process(ctx, "a", "f", hooks)
			}
			if err != nil {
				t.Fatal(err)
			}
			want := "[1 2 3]"
			if scenario == "failure" || scenario == "conflict" {
				want = "[1 3]"
				if len(backs) != 1 {
					t.Fatalf("sendbacks %v", backs)
				}
			}
			if fmt.Sprint(pushed) != want {
				t.Fatalf("pushes %v", pushed)
			}
			if scenario == "failure" && (counts["3"] != 2 || tip != "base+1+3") {
				t.Fatalf("suffix not retested: %v %s", counts, tip)
			}
			if scenario == "conflict" && (counts["2"] != 0 || tip != "base+1+3") {
				t.Fatalf("conflicted job included: %v %s", counts, tip)
			}
			if scenario == "serial" {
				for _, n := range counts {
					if n != 1 {
						t.Fatal(counts)
					}
				}
			}
			if scenario == "moved" && counts["3"] != 2 {
				t.Fatal(counts)
			}
		})
	}
}
