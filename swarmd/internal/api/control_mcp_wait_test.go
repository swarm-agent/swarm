package api

import (
	"sync/atomic"
	"testing"
	"time"
)

// A busy job floods the wait with events and each state read is slow; the wait
// must still end at its deadline and read the state at most about once per gap.
func TestControlMCPWaitCoalescedEndsOnTimeUnderAnEventFlood(t *testing.T) {
	events := make(chan int)
	stop := make(chan struct{})
	defer close(stop)
	go func() {
		for i := 0; ; i++ {
			select {
			case events <- i:
			case <-stop:
				return
			}
		}
	}()
	var checks atomic.Int32
	check := func() bool { checks.Add(1); time.Sleep(50 * time.Millisecond); return false }
	started := time.Now()
	got := controlMCPWaitCoalesced(events, make(chan struct{}), make(chan struct{}), 600*time.Millisecond, 100*time.Millisecond, func(int) bool { return true }, check)
	if got != controlMCPWaitTimedOut {
		t.Fatalf("outcome = %v, want timed out", got)
	}
	if took := time.Since(started); took > 900*time.Millisecond {
		t.Fatalf("wait took %v, want about 600ms", took)
	}
	if n := checks.Load(); n < 2 || n > 8 {
		t.Fatalf("state read %d times, want a few (coalesced), not one per event", n)
	}
}

func TestControlMCPWaitCoalescedReturnsWhenSettled(t *testing.T) {
	events := make(chan int, 1)
	events <- 1
	got := controlMCPWaitCoalesced(events, make(chan struct{}), make(chan struct{}), 5*time.Second, 10*time.Millisecond, func(int) bool { return true }, func() bool { return true })
	if got != controlMCPWaitDone {
		t.Fatalf("outcome = %v, want done", got)
	}
}

func TestControlMCPWaitCoalescedIgnoresOtherJobsAndHonoursCancel(t *testing.T) {
	events := make(chan int, 3)
	events <- 1
	events <- 2
	events <- 3
	cancelled := make(chan struct{})
	time.AfterFunc(150*time.Millisecond, func() { close(cancelled) })
	var checks atomic.Int32
	got := controlMCPWaitCoalesced(events, make(chan struct{}), cancelled, 5*time.Second, 20*time.Millisecond, func(int) bool { return false }, func() bool { checks.Add(1); return true })
	if got != controlMCPWaitCancelled || checks.Load() != 0 {
		t.Fatalf("outcome = %v, checks = %d; want cancelled with no state reads for unrelated events", got, checks.Load())
	}
}

func TestControlMCPWaitCoalescedReadsTheLastChangeAfterAQuietGap(t *testing.T) {
	events := make(chan int, 2)
	events <- 1
	events <- 2 // arrives inside the gap: must still be looked at once the gap passes
	var checks atomic.Int32
	got := controlMCPWaitCoalesced(events, make(chan struct{}), make(chan struct{}), 2*time.Second, 50*time.Millisecond, func(int) bool { return true }, func() bool { return checks.Add(1) == 2 })
	if got != controlMCPWaitDone || checks.Load() != 2 {
		t.Fatalf("outcome = %v, checks = %d; want done after the second read", got, checks.Load())
	}
}
