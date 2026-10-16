package publish

import (
	"context"
	"fmt"
	"sync"
	"testing"
	"time"
)

// Purpose: Queue.Process must admit durable arrivals into a running bounded
// train, not a batch barrier. Fake Git rejects untested combinations and wrong
// bases; gated checks prove overlap, FIFO pushes, failure restacking and serial
// compatibility at the narrow queue/environment hook boundary.
func TestLandingTrainLiveArrivals(t *testing.T) {
	for _, scenario := range []string{"arrival", "failure", "full", "serial"} {
		t.Run(scenario, func(t *testing.T) {
			q := NewQueue(queueMemory{})
			q.SetParallel(2)
			if scenario == "serial" {
				q.SetParallel(1)
			}
			ctx, cancel := context.WithTimeout(context.Background(), 3*time.Second)
			defer cancel()
			started := make(chan string, 16)
			gates := map[string]chan struct{}{"A": make(chan struct{}), "B": make(chan struct{}), "C": make(chan struct{})}
			var mu sync.Mutex
			tip := "base"
			tested := map[string]bool{}
			var pushes, backs []string
			enqueue := func(id string) {
				t.Helper()
				if _, err := q.Enqueue(Entry{Account: "a", Request: Request{JobID: id, Folder: "f"}}); err != nil {
					t.Fatal(err)
				}
			}
			stack := func(_ context.Context, e *Entry, prev *Entry) error {
				mu.Lock()
				defer mu.Unlock()
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
				started <- e.Request.Head
				select {
				case <-gates[e.Request.JobID]:
				case <-ctx.Done():
					return ctx.Err()
				}
				if scenario == "failure" && e.Request.JobID == "A" {
					return &SendBackError{Reason: "failed checks"}
				}
				mu.Lock()
				tested[e.Request.Head] = true
				mu.Unlock()
				return nil
			}, Land: func(_ context.Context, e Entry) (LandResult, error) {
				mu.Lock()
				defer mu.Unlock()
				if !tested[e.Request.Head] || e.Request.BaseCommit != tip {
					return LandResult{}, fmt.Errorf("untested or wrong base: %+v tip=%s", e.Request, tip)
				}
				tip = e.Request.Head
				pushes = append(pushes, e.Request.JobID)
				return LandResult{Commit: tip}, nil
			}, SendBack: func(_ context.Context, e Entry) error {
				mu.Lock()
				defer mu.Unlock()
				backs = append(backs, e.Request.JobID)
				return nil
			}}
			enqueue("A")
			done := make(chan error, 1)
			go func() { done <- q.Process(ctx, "a", "f", hooks) }()
			defer func() {
				cancel()
				select {
				case <-done:
				case <-time.After(time.Second):
					t.Error("processor did not stop")
				}
			}()
			await := func(want string) {
				t.Helper()
				select {
				case got := <-started:
					if got != want {
						t.Fatalf("check %s, want %s", got, want)
					}
				case <-ctx.Done():
					t.Fatalf("checks never started for %s", want)
				}
			}
			await("base+A")
			enqueue("B")
			if scenario == "serial" {
				select {
				case got := <-started:
					t.Fatalf("serial overlap %s", got)
				case <-time.After(30 * time.Millisecond):
				}
				close(gates["A"])
				await("base+A+B")
				close(gates["B"])
			} else {
				await("base+A+B")
				if scenario == "full" {
					enqueue("C")
					select {
					case got := <-started:
						t.Fatalf("window exceeded: %s", got)
					case <-time.After(30 * time.Millisecond):
					}
					close(gates["A"])
					await("base+A+B+C") // B is still checking; A alone freed the slot.
					close(gates["B"])
					close(gates["C"])
				} else {
					close(gates["B"])
					close(gates["A"])
					if scenario == "failure" {
						await("base+B")
					}
				}
			}
			select {
			case err := <-done:
				done <- err
				if err != nil {
					t.Fatal(err)
				}
			case <-ctx.Done():
				t.Fatal(ctx.Err())
			}
			mu.Lock()
			defer mu.Unlock()
			want := "[A B]"
			if scenario == "full" {
				want = "[A B C]"
			}
			if scenario == "failure" {
				want = "[B]"
				if fmt.Sprint(backs) != "[A]" || tip != "base+B" {
					t.Fatalf("failure recovery backs=%v tip=%s", backs, tip)
				}
			}
			if fmt.Sprint(pushes) != want {
				t.Fatalf("pushes=%v want=%s", pushes, want)
			}
		})
	}
}
