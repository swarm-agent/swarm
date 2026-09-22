package executioncapacity

import (
	"context"
	"errors"
	"fmt"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"
)

// TestContention verifies that when multiple concurrent sessions compete for a
// bounded number of slots, active leases never exceed the limit, all acquire calls
// eventually succeed and release, and available capacity matches expected counts.
func TestContention(t *testing.T) {
	const limit = 5
	const numCallers = 30

	mgr := NewManager(ManagerConfig{
		DefaultLimit: limit,
	})
	defer mgr.Close()

	var maxConcurrent atomic.Int64
	var activeCount atomic.Int64
	var wg sync.WaitGroup

	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()

	for i := 0; i < numCallers; i++ {
		wg.Add(1)
		go func(id int) {
			defer wg.Done()

			lease, err := mgr.Acquire(ctx, AcquireRequest{
				AccountScopeID: "acc-contention",
				SessionID:      fmt.Sprintf("session-%d", id),
				RunID:          fmt.Sprintf("run-%d", id),
				Kind:           ExecutionKindOrdinary,
			})
			if err != nil {
				t.Errorf("caller %d failed to acquire: %v", id, err)
				return
			}
			defer lease.Release()

			cur := activeCount.Add(1)
			for {
				oldMax := maxConcurrent.Load()
				if cur <= oldMax || maxConcurrent.CompareAndSwap(oldMax, cur) {
					break
				}
			}

			if cur > limit {
				t.Errorf("concurrency exceeded limit: got %d, max %d", cur, limit)
			}

			// Simulate work
			time.Sleep(10 * time.Millisecond)

			activeCount.Add(-1)
		}(i)
	}

	wg.Wait()

	if maxConcurrent.Load() > limit {
		t.Fatalf("observed peak concurrency %d exceeded limit %d", maxConcurrent.Load(), limit)
	}
	if maxConcurrent.Load() == 0 {
		t.Fatalf("observed peak concurrency was 0; no goroutines ran")
	}

	snap := mgr.Snapshot("acc-contention")
	if snap.TotalActive != 0 {
		t.Fatalf("expected 0 active leases after completion, got %d", snap.TotalActive)
	}
	if snap.Pending != 0 {
		t.Fatalf("expected 0 pending waiters, got %d", snap.Pending)
	}
	if snap.Available != limit {
		t.Fatalf("expected %d available slots, got %d", limit, snap.Available)
	}
}

// TestAccountIsolation verifies that accounts are strictly isolated: saturating
// capacity in Account A does not affect available capacity, admission, or queuing in Account B.
func TestAccountIsolation(t *testing.T) {
	mgr := NewManager(ManagerConfig{
		DefaultLimit: 2,
	})
	defer mgr.Close()

	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()

	// Saturate Account A (limit 2)
	lA1, err := mgr.Acquire(ctx, AcquireRequest{
		AccountScopeID: "account-a",
		SessionID:      "sess-a-1",
		RunID:          "run-1",
	})
	if err != nil {
		t.Fatalf("account-a l1: %v", err)
	}
	defer lA1.Release()

	lA2, err := mgr.Acquire(ctx, AcquireRequest{
		AccountScopeID: "account-a",
		SessionID:      "sess-a-2",
		RunID:          "run-2",
	})
	if err != nil {
		t.Fatalf("account-a l2: %v", err)
	}
	defer lA2.Release()

	snapA := mgr.Snapshot("account-a")
	if snapA.TotalActive != 2 || snapA.Available != 0 {
		t.Fatalf("account-a should be saturated: %+v", snapA)
	}

	// Account B must still have full available capacity (2 slots)
	snapB := mgr.Snapshot("account-b")
	if snapB.TotalActive != 0 || snapB.Available != 2 {
		t.Fatalf("account-b should have full capacity: %+v", snapB)
	}

	lB1, err := mgr.Acquire(ctx, AcquireRequest{
		AccountScopeID: "account-b",
		SessionID:      "sess-b-1",
		RunID:          "run-1",
	})
	if err != nil {
		t.Fatalf("account-b acquire failed: %v", err)
	}
	defer lB1.Release()

	snapB = mgr.Snapshot("account-b")
	if snapB.TotalActive != 1 || snapB.Available != 1 {
		t.Fatalf("account-b snapshot unexpected: %+v", snapB)
	}

	// Account A's state remains unchanged
	snapA = mgr.Snapshot("account-a")
	if snapA.TotalActive != 2 || snapA.Available != 0 {
		t.Fatalf("account-a was affected by account-b: %+v", snapA)
	}
}

// TestQueueFull verifies that queued waiters are strictly bounded by MaxQueueWaiters
// and that exceeding this bound immediately returns ErrQueueFull.
func TestQueueFull(t *testing.T) {
	mgr := NewManager(ManagerConfig{
		DefaultLimit:    1,
		MaxQueueWaiters: 2,
	})
	defer mgr.Close()

	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()

	// Fill the 1 available slot
	l1, err := mgr.Acquire(ctx, AcquireRequest{
		AccountScopeID: "acc-q",
		SessionID:      "sess-1",
		RunID:          "run-1",
	})
	if err != nil {
		t.Fatalf("l1: %v", err)
	}
	defer l1.Release()

	// Start 2 background waiters (filling the queue to capacity 2)
	waiterErrCh := make(chan error, 2)
	for i := 2; i <= 3; i++ {
		go func(id int) {
			l, err := mgr.Acquire(ctx, AcquireRequest{
				AccountScopeID: "acc-q",
				SessionID:      fmt.Sprintf("sess-%d", id),
				RunID:          "run",
			})
			if err == nil {
				_ = l.Release()
			}
			waiterErrCh <- err
		}(i)
	}

	// Wait briefly for the two goroutines to enter the queue
	time.Sleep(50 * time.Millisecond)

	snap := mgr.Snapshot("acc-q")
	if snap.Pending != 2 {
		t.Fatalf("expected 2 pending waiters, got %d", snap.Pending)
	}

	// 4th request must be rejected with ErrQueueFull immediately
	_, err = mgr.Acquire(ctx, AcquireRequest{
		AccountScopeID: "acc-q",
		SessionID:      "sess-overflow",
		RunID:          "run",
	})
	if !errors.Is(err, ErrQueueFull) {
		t.Fatalf("expected ErrQueueFull, got %v", err)
	}

	// Release l1 to allow the queued waiters to finish
	l1.Release()

	for i := 0; i < 2; i++ {
		if err := <-waiterErrCh; err != nil {
			t.Errorf("queued waiter failed: %v", err)
		}
	}
}

// TestCancellationAndGrantRace verifies context cancellation while queued, pre-cancelled
// contexts, and races between grant and cancellation so that active slots are never leaked.
func TestCancellationAndGrantRace(t *testing.T) {
	mgr := NewManager(ManagerConfig{
		DefaultLimit: 1,
	})
	defer mgr.Close()

	// 1. Pre-cancelled context returns error immediately without queuing or occupying slots
	preCtx, preCancel := context.WithCancel(context.Background())
	preCancel()
	_, err := mgr.Acquire(preCtx, AcquireRequest{
		AccountScopeID: "acc-race",
		SessionID:      "sess-pre",
		RunID:          "run",
	})
	if !errors.Is(err, context.Canceled) {
		t.Fatalf("expected context.Canceled, got %v", err)
	}
	if snap := mgr.Snapshot("acc-race"); snap.TotalActive != 0 || snap.Pending != 0 {
		t.Fatalf("pre-cancelled request leaked state: %+v", snap)
	}

	// 2. Cancellation while queued removes waiter without leaking slots
	ctx := context.Background()
	l1, err := mgr.Acquire(ctx, AcquireRequest{
		AccountScopeID: "acc-race",
		SessionID:      "sess-1",
		RunID:          "run",
	})
	if err != nil {
		t.Fatalf("l1: %v", err)
	}

	cancelCtx, cancelFn := context.WithCancel(ctx)
	waiterErr := make(chan error, 1)
	go func() {
		_, err := mgr.Acquire(cancelCtx, AcquireRequest{
			AccountScopeID: "acc-race",
			SessionID:      "sess-waiter",
			RunID:          "run",
		})
		waiterErr <- err
	}()

	// Wait for waiter to enter queue
	time.Sleep(30 * time.Millisecond)
	if snap := mgr.Snapshot("acc-race"); snap.Pending != 1 {
		t.Fatalf("expected 1 pending waiter, got %d", snap.Pending)
	}

	// Cancel the waiter's context
	cancelFn()
	if err := <-waiterErr; !errors.Is(err, context.Canceled) {
		t.Fatalf("expected context.Canceled, got %v", err)
	}

	if snap := mgr.Snapshot("acc-race"); snap.Pending != 0 {
		t.Fatalf("pending count not 0 after cancellation: %d", snap.Pending)
	}

	// 3. Grant / cancellation race:
	// Goroutine acquires when slot freed, but its context cancels concurrently.
	// We verify that whether it gets context.Canceled or lease, no slot is leaked.
	raceCtx, raceCancel := context.WithCancel(ctx)
	raceDone := make(chan error, 1)

	go func() {
		l, err := mgr.Acquire(raceCtx, AcquireRequest{
			AccountScopeID: "acc-race",
			SessionID:      "sess-race",
			RunID:          "run",
		})
		if err == nil {
			_ = l.Release()
		}
		raceDone <- err
	}()

	time.Sleep(30 * time.Millisecond)

	// Concurrently release l1 and cancel raceCtx
	go raceCancel()
	l1.Release()

	<-raceDone

	// TotalActive must return to 0 with 0 pending
	time.Sleep(30 * time.Millisecond)
	snap := mgr.Snapshot("acc-race")
	if snap.TotalActive != 0 {
		t.Fatalf("race leaked active slot: %+v", snap)
	}
	if snap.Pending != 0 {
		t.Fatalf("race leaked pending waiter: %+v", snap)
	}
	if snap.Available != 1 {
		t.Fatalf("expected 1 available slot, got %d", snap.Available)
	}
}

// TestParkReacquireCap1 tests the critical single-slot capacity (limit=1) workflow:
// Parent acquires, parks (freeing slot to 0 while keeping session ownership),
// a second session acquires the freed slot (1/1), parent attempts Reacquire and blocks,
// second session releases, parent successfully reacquires the slot.
func TestParkReacquireCap1(t *testing.T) {
	mgr := NewManager(ManagerConfig{
		DefaultLimit: 1,
	})
	defer mgr.Close()

	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()

	// 1. Session 1 acquires the single available slot
	l1, err := mgr.Acquire(ctx, AcquireRequest{
		AccountScopeID: "acc-park",
		SessionID:      "sess-1",
		RunID:          "run-1",
	})
	if err != nil {
		t.Fatalf("l1 acquire: %v", err)
	}

	if !l1.IsActive() || l1.IsParked() || l1.IsReleased() {
		t.Fatalf("l1 unexpected state: active=%v, parked=%v, released=%v", l1.IsActive(), l1.IsParked(), l1.IsReleased())
	}
	snap := mgr.Snapshot("acc-park")
	if snap.TotalActive != 1 || snap.Available != 0 {
		t.Fatalf("snap after l1: %+v", snap)
	}

	// 2. Session 1 parks: frees active slot (totalActive drops to 0, available increases to 1)
	if err := l1.Park(); err != nil {
		t.Fatalf("l1 park: %v", err)
	}
	if l1.IsActive() || !l1.IsParked() || l1.IsReleased() {
		t.Fatalf("l1 after park state: active=%v, parked=%v, released=%v", l1.IsActive(), l1.IsParked(), l1.IsReleased())
	}

	snap = mgr.Snapshot("acc-park")
	if snap.TotalActive != 0 || snap.Available != 1 {
		t.Fatalf("snap after park: %+v", snap)
	}

	// 3. Session 2 (distinct session) can now acquire the freed slot
	l2, err := mgr.Acquire(ctx, AcquireRequest{
		AccountScopeID: "acc-park",
		SessionID:      "sess-2",
		RunID:          "run-2",
	})
	if err != nil {
		t.Fatalf("l2 acquire: %v", err)
	}
	defer l2.Release()

	snap = mgr.Snapshot("acc-park")
	if snap.TotalActive != 1 || snap.Available != 0 {
		t.Fatalf("snap after l2: %+v", snap)
	}

	// 4. Session 1 attempts Reacquire. Because limit=1 and Session 2 holds it, Reacquire blocks.
	reacquiredCh := make(chan error, 1)
	go func() {
		reacquiredCh <- l1.Reacquire(ctx)
	}()

	time.Sleep(40 * time.Millisecond)
	snap = mgr.Snapshot("acc-park")
	if snap.Pending != 1 {
		t.Fatalf("expected l1 to be pending in reacquire queue, got %d", snap.Pending)
	}

	// 5. Session 2 releases its lease.
	if err := l2.Release(); err != nil {
		t.Fatalf("l2 release: %v", err)
	}

	// 6. Session 1 reacquires successfully
	if err := <-reacquiredCh; err != nil {
		t.Fatalf("l1 reacquire failed: %v", err)
	}

	if !l1.IsActive() || l1.IsParked() {
		t.Fatalf("l1 after reacquire should be active: active=%v, parked=%v", l1.IsActive(), l1.IsParked())
	}
	snap = mgr.Snapshot("acc-park")
	if snap.TotalActive != 1 || snap.Available != 0 || snap.Pending != 0 {
		t.Fatalf("snap after reacquire: %+v", snap)
	}

	// 7. Session 1 finishes and releases
	if err := l1.Release(); err != nil {
		t.Fatalf("l1 release: %v", err)
	}
	snap = mgr.Snapshot("acc-park")
	if snap.TotalActive != 0 || snap.Available != 1 {
		t.Fatalf("snap after l1 release: %+v", snap)
	}
}

// TestSessionSerialization verifies that runs for the same account+session are serialized,
// even when the global capacity has ample slots. Furthermore, a parked owner retains session
// ownership, preventing another run from executing in that session while parked.
func TestSessionSerialization(t *testing.T) {
	mgr := NewManager(ManagerConfig{
		DefaultLimit: 100, // plenty of capacity
	})
	defer mgr.Close()

	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()

	// Run 1 acquires for session-serial
	l1, err := mgr.Acquire(ctx, AcquireRequest{
		AccountScopeID: "acc-serial",
		SessionID:      "session-serial",
		RunID:          "run-1",
	})
	if err != nil {
		t.Fatalf("l1: %v", err)
	}

	// Run 2 attempts to acquire for the SAME session
	run2Ch := make(chan error, 1)
	var l2 Lease
	go func() {
		var err error
		l2, err = mgr.Acquire(ctx, AcquireRequest{
			AccountScopeID: "acc-serial",
			SessionID:      "session-serial",
			RunID:          "run-2",
		})
		run2Ch <- err
	}()

	time.Sleep(40 * time.Millisecond)
	snap := mgr.Snapshot("acc-serial")
	if snap.Pending != 1 {
		t.Fatalf("run 2 should be pending due to session serialization, got %d", snap.Pending)
	}

	// Release Run 1 -> Run 2 immediately unblocks
	if err := l1.Release(); err != nil {
		t.Fatalf("l1 release: %v", err)
	}

	if err := <-run2Ch; err != nil {
		t.Fatalf("run 2 acquire: %v", err)
	}
	if l2 == nil || !l2.IsActive() {
		t.Fatalf("run 2 lease not active")
	}

	// Now park Run 2: session ownership must still be retained
	if err := l2.Park(); err != nil {
		t.Fatalf("l2 park: %v", err)
	}

	// Run 3 attempts to acquire while Run 2 is PARKED on that session
	run3Ch := make(chan error, 1)
	var l3 Lease
	go func() {
		var err error
		l3, err = mgr.Acquire(ctx, AcquireRequest{
			AccountScopeID: "acc-serial",
			SessionID:      "session-serial",
			RunID:          "run-3",
		})
		run3Ch <- err
	}()

	time.Sleep(40 * time.Millisecond)
	snap = mgr.Snapshot("acc-serial")
	if snap.Pending != 1 {
		t.Fatalf("run 3 should be blocked because parked owner retains session ownership, pending=%d", snap.Pending)
	}

	// Releasing the parked lease releases session ownership, allowing Run 3 to unblock
	if err := l2.Release(); err != nil {
		t.Fatalf("l2 release: %v", err)
	}

	if err := <-run3Ch; err != nil {
		t.Fatalf("run 3 acquire failed: %v", err)
	}
	defer l3.Release()

	if !l3.IsActive() {
		t.Fatalf("l3 lease should be active")
	}
}

// TestPolicyUpdatesWakeWaiters verifies that increasing the active execution limit
// dynamically via SetLimit immediately wakes eligible queued waiters without polling.
func TestPolicyUpdatesWakeWaiters(t *testing.T) {
	mgr := NewManager(ManagerConfig{
		DefaultLimit: 1,
	})
	defer mgr.Close()

	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()

	l1, err := mgr.Acquire(ctx, AcquireRequest{
		AccountScopeID: "acc-update",
		SessionID:      "sess-1",
		RunID:          "run-1",
	})
	if err != nil {
		t.Fatalf("l1: %v", err)
	}
	defer l1.Release()

	// Second session enqueues because limit is 1
	w2Ch := make(chan error, 1)
	var l2 Lease
	go func() {
		var err error
		l2, err = mgr.Acquire(ctx, AcquireRequest{
			AccountScopeID: "acc-update",
			SessionID:      "sess-2",
			RunID:          "run-2",
		})
		w2Ch <- err
	}()

	time.Sleep(40 * time.Millisecond)
	if snap := mgr.Snapshot("acc-update"); snap.Pending != 1 {
		t.Fatalf("expected 1 pending waiter, got %d", snap.Pending)
	}

	// Update policy limit from 1 to 2
	mgr.SetLimit("acc-update", 2)

	select {
	case err := <-w2Ch:
		if err != nil {
			t.Fatalf("waiter failed to wake on policy update: %v", err)
		}
	case <-time.After(500 * time.Millisecond):
		t.Fatalf("waiter was not woken up after limit increased")
	}
	defer l2.Release()

	snap := mgr.Snapshot("acc-update")
	if snap.EffectiveLimit != 2 {
		t.Fatalf("expected effective limit 2, got %d", snap.EffectiveLimit)
	}
	if snap.TotalActive != 2 {
		t.Fatalf("expected total active 2, got %d", snap.TotalActive)
	}
	if snap.Pending != 0 {
		t.Fatalf("expected 0 pending, got %d", snap.Pending)
	}
}

// TestLeaseTransitionsAndValidation verifies error handling for invalid operations,
// parameter validation, and idempotent release behavior.
func TestLeaseTransitionsAndValidation(t *testing.T) {
	mgr := NewManager(ManagerConfig{
		DefaultLimit: 5,
	})
	defer mgr.Close()

	ctx := context.Background()

	// 1. Missing session ID
	_, err := mgr.Acquire(ctx, AcquireRequest{
		AccountScopeID: "acc-val",
		SessionID:      "",
		RunID:          "run",
	})
	if !errors.Is(err, ErrSessionRequired) {
		t.Fatalf("expected ErrSessionRequired, got %v", err)
	}

	// 2. Missing run ID
	_, err = mgr.Acquire(ctx, AcquireRequest{
		AccountScopeID: "acc-val",
		SessionID:      "sess",
		RunID:          "",
	})
	if !errors.Is(err, ErrRunRequired) {
		t.Fatalf("expected ErrRunRequired, got %v", err)
	}

	// 3. Acquire valid lease
	lease, err := mgr.Acquire(ctx, AcquireRequest{
		AccountScopeID: "acc-val",
		SessionID:      "sess",
		RunID:          "run",
		Kind:           ExecutionKindDeployed,
	})
	if err != nil {
		t.Fatalf("acquire: %v", err)
	}

	snap := mgr.Snapshot("acc-val")
	if snap.DeployedActive != 1 || snap.TotalActive != 1 {
		t.Fatalf("deployed accounting mismatch: %+v", snap)
	}

	// 4. Reacquire on active lease fails
	if err := lease.Reacquire(ctx); !errors.Is(err, ErrLeaseNotParked) {
		t.Fatalf("expected ErrLeaseNotParked, got %v", err)
	}

	// 5. Park succeeds
	if err := lease.Park(); err != nil {
		t.Fatalf("park: %v", err)
	}

	// 6. Park on already parked lease fails
	if err := lease.Park(); !errors.Is(err, ErrLeaseAlreadyParked) {
		t.Fatalf("expected ErrLeaseAlreadyParked, got %v", err)
	}

	// 7. Release succeeds and is idempotent
	if err := lease.Release(); err != nil {
		t.Fatalf("release: %v", err)
	}
	if err := lease.Release(); err != nil {
		t.Fatalf("second release should be idempotent nil, got %v", err)
	}

	// 8. Park or Reacquire on released lease fails
	if err := lease.Park(); !errors.Is(err, ErrLeaseReleased) {
		t.Fatalf("expected ErrLeaseReleased on park, got %v", err)
	}
	if err := lease.Reacquire(ctx); !errors.Is(err, ErrLeaseReleased) {
		t.Fatalf("expected ErrLeaseReleased on reacquire, got %v", err)
	}

	// 9. Limit bounds validation
	if err := ValidateLimit(0); err == nil {
		t.Fatalf("expected error for limit 0")
	}
	if err := ValidateLimit(-1); err == nil {
		t.Fatalf("expected error for limit -1")
	}
	if err := ValidateLimit(10001); err == nil {
		t.Fatalf("expected error for limit 10001")
	}
	if err := ValidateLimit(1); err != nil {
		t.Fatalf("unexpected error for limit 1: %v", err)
	}
	if err := ValidateLimit(100); err != nil {
		t.Fatalf("unexpected error for limit 100: %v", err)
	}
	if err := ValidateLimit(10000); err != nil {
		t.Fatalf("unexpected error for limit 10000: %v", err)
	}
}

// TestContextHelpers verifies WithLease, LeaseFromContext (which enforces exact session
// and run ID matching to prevent child context leakage), and WithoutLease.
func TestContextHelpers(t *testing.T) {
	mgr := NewManager(ManagerConfig{DefaultLimit: 5})
	defer mgr.Close()

	ctx := context.Background()
	lease, err := mgr.Acquire(ctx, AcquireRequest{
		AccountScopeID: "acc-ctx",
		SessionID:      "sess-parent",
		RunID:          "run-1",
	})
	if err != nil {
		t.Fatalf("acquire: %v", err)
	}
	defer lease.Release()

	boundCtx := WithLease(ctx, lease)

	// Exact match retrieves the lease
	got, ok := LeaseFromContext(boundCtx, "sess-parent", "run-1")
	if !ok || got != lease {
		t.Fatalf("failed to retrieve lease from context with exact IDs: ok=%v, got=%v", ok, got)
	}

	// Child session or child task with different sessionID must NOT get the parent lease
	if _, ok := LeaseFromContext(boundCtx, "child-sess", "run-1"); ok {
		t.Fatalf("child session context accidentally inherited parent lease")
	}

	// Child run with different runID must NOT get the parent lease
	if _, ok := LeaseFromContext(boundCtx, "sess-parent", "child-run"); ok {
		t.Fatalf("child run context accidentally inherited parent lease")
	}

	// WithoutLease strips lease
	strippedCtx := WithoutLease(boundCtx)
	if _, ok := LeaseFromContext(strippedCtx, "sess-parent", "run-1"); ok {
		t.Fatalf("stripped context still returned lease")
	}
}

// TestCap1ParentParkWithSecondRunQueued tests the critical case where capacity is 1,
// parent Run 1 is active, parent Run 2 is queued (blocked by session ownership),
// parent Run 1 parks, an unrelated session acquires the freed slot, parent Run 1
// attempts Reacquire and queues behind Run 2, the unrelated session releases,
// and parent Run 1 reacquires without deadlocking with Run 2. Once parent Run 1
// finally releases, Run 2 unblocks and executes.
func TestCap1ParentParkWithSecondRunQueued(t *testing.T) {
	mgr := NewManager(ManagerConfig{DefaultLimit: 1})
	defer mgr.Close()

	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()

	// 1. Parent Run 1 acquires slot 1
	p1, err := mgr.Acquire(ctx, AcquireRequest{
		AccountScopeID: "acc-cap1",
		SessionID:      "sess-parent",
		RunID:          "run-1",
	})
	if err != nil {
		t.Fatalf("p1 acquire: %v", err)
	}

	// 2. Parent Run 2 arrives for same session: blocked by session serialization
	p2ErrCh := make(chan error, 1)
	var p2 Lease
	go func() {
		var err error
		p2, err = mgr.Acquire(ctx, AcquireRequest{
			AccountScopeID: "acc-cap1",
			SessionID:      "sess-parent",
			RunID:          "run-2",
		})
		p2ErrCh <- err
	}()

	time.Sleep(30 * time.Millisecond)
	snap := mgr.Snapshot("acc-cap1")
	if snap.TotalActive != 1 || snap.Pending != 1 {
		t.Fatalf("expected 1 active, 1 pending; got %+v", snap)
	}

	// 3. Parent Run 1 parks: slot freed, but sess-parent ownership is still held by p1
	if err := p1.Park(); err != nil {
		t.Fatalf("p1 park: %v", err)
	}
	snap = mgr.Snapshot("acc-cap1")
	if snap.TotalActive != 0 || snap.Available != 1 {
		t.Fatalf("after p1 park expected 0 active, 1 available; got %+v", snap)
	}

	// 4. Unrelated session can acquire the freed slot because p2 cannot run while p1 holds sess-parent
	u1, err := mgr.Acquire(ctx, AcquireRequest{
		AccountScopeID: "acc-cap1",
		SessionID:      "sess-unrelated",
		RunID:          "run-u1",
	})
	if err != nil {
		t.Fatalf("u1 acquire: %v", err)
	}

	snap = mgr.Snapshot("acc-cap1")
	if snap.TotalActive != 1 || snap.Available != 0 {
		t.Fatalf("after u1 acquire expected 1 active, 0 available; got %+v", snap)
	}

	// 5. Parent Run 1 attempts Reacquire while u1 holds the slot.
	// Reacquire queues behind p2 in waiters.
	p1ReacquiredCh := make(chan error, 1)
	go func() {
		p1ReacquiredCh <- p1.Reacquire(ctx)
	}()

	time.Sleep(30 * time.Millisecond)
	snap = mgr.Snapshot("acc-cap1")
	if snap.Pending != 2 {
		t.Fatalf("expected 2 pending waiters (p2 and p1-reacquire), got %d", snap.Pending)
	}

	// 6. Unrelated session u1 releases.
	// p1 MUST be dispatched and reacquire successfully, NOT deadlocking with p2!
	if err := u1.Release(); err != nil {
		t.Fatalf("u1 release: %v", err)
	}

	select {
	case err := <-p1ReacquiredCh:
		if err != nil {
			t.Fatalf("p1 reacquire failed: %v", err)
		}
	case <-time.After(500 * time.Millisecond):
		t.Fatalf("p1 reacquire DEADLOCKED behind p2 in queue")
	}

	if !p1.IsActive() || p1.IsParked() {
		t.Fatalf("p1 should be active after reacquire")
	}

	// p2 should STILL be pending because p1 is active
	select {
	case err := <-p2ErrCh:
		t.Fatalf("p2 should not have completed while p1 is active, err: %v", err)
	default:
	}

	// 7. p1 finishes and releases.
	// Now sess-parent is freed and p2 unblocks!
	if err := p1.Release(); err != nil {
		t.Fatalf("p1 release: %v", err)
	}

	select {
	case err := <-p2ErrCh:
		if err != nil {
			t.Fatalf("p2 acquire failed: %v", err)
		}
	case <-time.After(500 * time.Millisecond):
		t.Fatalf("p2 did not unblock after p1 released")
	}

	if p2 == nil || !p2.IsActive() {
		t.Fatalf("p2 lease not active")
	}
	_ = p2.Release()
}

// TestCloseSignalsErrClosedNoFakeSuccess verifies that closing the manager signals
// ErrClosed to all queued waiters in both Acquire and Reacquire, without fake nil-lease successes or panics.
func TestCloseSignalsErrClosedNoFakeSuccess(t *testing.T) {
	mgr := NewManager(ManagerConfig{DefaultLimit: 1})

	ctx := context.Background()

	// Fill slot
	l1, err := mgr.Acquire(ctx, AcquireRequest{
		AccountScopeID: "acc-close",
		SessionID:      "sess-1",
		RunID:          "run-1",
	})
	if err != nil {
		t.Fatalf("l1: %v", err)
	}
	defer l1.Release()

	// Park lParked in another account
	lParked, err := mgr.Acquire(ctx, AcquireRequest{
		AccountScopeID: "acc-close-2",
		SessionID:      "sess-parked",
		RunID:          "run-p",
	})
	if err != nil {
		t.Fatalf("lParked: %v", err)
	}
	if err := lParked.Park(); err != nil {
		t.Fatalf("park: %v", err)
	}

	// Saturate acc-close-2
	lOther, err := mgr.Acquire(ctx, AcquireRequest{
		AccountScopeID: "acc-close-2",
		SessionID:      "sess-other",
		RunID:          "run-o",
	})
	if err != nil {
		t.Fatalf("lOther: %v", err)
	}
	defer lOther.Release()

	// Enqueue Acquire waiter in acc-close
	acquireErrCh := make(chan error, 1)
	go func() {
		lease, err := mgr.Acquire(ctx, AcquireRequest{
			AccountScopeID: "acc-close",
			SessionID:      "sess-2",
			RunID:          "run-2",
		})
		if lease != nil {
			t.Errorf("expected nil lease on closed manager, got %v", lease)
		}
		acquireErrCh <- err
	}()

	// Enqueue Reacquire waiter in acc-close-2
	reacquireErrCh := make(chan error, 1)
	go func() {
		reacquireErrCh <- lParked.Reacquire(ctx)
	}()

	time.Sleep(40 * time.Millisecond)

	// Close manager
	mgr.Close()

	select {
	case err := <-acquireErrCh:
		if !errors.Is(err, ErrClosed) {
			t.Fatalf("expected ErrClosed for acquire, got: %v", err)
		}
	case <-time.After(500 * time.Millisecond):
		t.Fatalf("acquire waiter did not receive signal on close")
	}

	select {
	case err := <-reacquireErrCh:
		if !errors.Is(err, ErrClosed) {
			t.Fatalf("expected ErrClosed for reacquire, got: %v", err)
		}
	case <-time.After(500 * time.Millisecond):
		t.Fatalf("reacquire waiter did not receive signal on close")
	}

	// Further acquire calls fail immediately with ErrClosed
	_, err = mgr.Acquire(ctx, AcquireRequest{
		AccountScopeID: "acc-close",
		SessionID:      "sess-3",
		RunID:          "run-3",
	})
	if !errors.Is(err, ErrClosed) {
		t.Fatalf("expected ErrClosed for post-close acquire, got: %v", err)
	}
}

// TestReleaseParkedWithQueuedReacquireDoesNotResurrectOrGoNegative verifies that
// releasing a parked lease while its Reacquire is queued cleans up the waiter,
// returns ErrLeaseReleased to the reacquire call, does not resurrect the lease,
// and prevents totalActive from going negative.
func TestReleaseParkedWithQueuedReacquireDoesNotResurrectOrGoNegative(t *testing.T) {
	mgr := NewManager(ManagerConfig{DefaultLimit: 1})
	defer mgr.Close()

	ctx := context.Background()

	// 1. Acquire and park l1
	l1, err := mgr.Acquire(ctx, AcquireRequest{
		AccountScopeID: "acc-resurrect",
		SessionID:      "sess-1",
		RunID:          "run-1",
	})
	if err != nil {
		t.Fatalf("l1: %v", err)
	}
	if err := l1.Park(); err != nil {
		t.Fatalf("park l1: %v", err)
	}

	// 2. Saturate slot with l2
	l2, err := mgr.Acquire(ctx, AcquireRequest{
		AccountScopeID: "acc-resurrect",
		SessionID:      "sess-2",
		RunID:          "run-2",
	})
	if err != nil {
		t.Fatalf("l2: %v", err)
	}
	defer l2.Release()

	// 3. l1 attempts Reacquire and queues
	reacquireErrCh := make(chan error, 1)
	go func() {
		reacquireErrCh <- l1.Reacquire(ctx)
	}()

	time.Sleep(30 * time.Millisecond)
	snap := mgr.Snapshot("acc-resurrect")
	if snap.Pending != 1 {
		t.Fatalf("expected 1 pending reacquire, got %d", snap.Pending)
	}

	// 4. Release l1 while Reacquire is queued
	if err := l1.Release(); err != nil {
		t.Fatalf("l1 release: %v", err)
	}

	// Reacquire should wake and return ErrLeaseReleased
	select {
	case err := <-reacquireErrCh:
		if !errors.Is(err, ErrLeaseReleased) {
			t.Fatalf("expected ErrLeaseReleased, got: %v", err)
		}
	case <-time.After(500 * time.Millisecond):
		t.Fatalf("reacquire did not wake on release")
	}

	// 5. Release l2: capacity must return to 0 active, 1 available (never negative!)
	if err := l2.Release(); err != nil {
		t.Fatalf("l2 release: %v", err)
	}

	snap = mgr.Snapshot("acc-resurrect")
	if snap.TotalActive != 0 || snap.Available != 1 || snap.Pending != 0 {
		t.Fatalf("unexpected snapshot after release: %+v", snap)
	}

	// l1 must remain released and not active
	if l1.IsActive() || !l1.IsReleased() {
		t.Fatalf("l1 should remain released and inactive")
	}
}

// TestConcurrentDuplicateReacquire verifies that calling Reacquire concurrently
// on the same parked lease rejects duplicate calls and does not double-increment totalActive.
func TestConcurrentDuplicateReacquire(t *testing.T) {
	mgr := NewManager(ManagerConfig{DefaultLimit: 1})
	defer mgr.Close()

	ctx := context.Background()

	l1, err := mgr.Acquire(ctx, AcquireRequest{
		AccountScopeID: "acc-dup-reacquire",
		SessionID:      "sess-1",
		RunID:          "run-1",
	})
	if err != nil {
		t.Fatalf("l1: %v", err)
	}
	if err := l1.Park(); err != nil {
		t.Fatalf("l1 park: %v", err)
	}

	// Occupy slot with l2 so Reacquire must wait
	l2, err := mgr.Acquire(ctx, AcquireRequest{
		AccountScopeID: "acc-dup-reacquire",
		SessionID:      "sess-2",
		RunID:          "run-2",
	})
	if err != nil {
		t.Fatalf("l2: %v", err)
	}
	defer l2.Release()

	// Launch two concurrent Reacquire calls on l1
	errCh1 := make(chan error, 1)
	errCh2 := make(chan error, 1)

	go func() { errCh1 <- l1.Reacquire(ctx) }()
	time.Sleep(10 * time.Millisecond)
	go func() { errCh2 <- l1.Reacquire(ctx) }()

	// One should fail immediately with ErrLeaseAlreadyReacquiring (or ErrLeaseNotParked)
	var err2 error
	select {
	case err2 = <-errCh2:
		if !errors.Is(err2, ErrLeaseAlreadyReacquiring) && !errors.Is(err2, ErrLeaseNotParked) {
			t.Fatalf("expected ErrLeaseAlreadyReacquiring or ErrLeaseNotParked, got: %v", err2)
		}
	case <-time.After(500 * time.Millisecond):
		t.Fatalf("second Reacquire did not reject promptly")
	}

	// Release l2 to let the first Reacquire complete
	_ = l2.Release()

	select {
	case err := <-errCh1:
		if err != nil {
			t.Fatalf("first Reacquire failed: %v", err)
		}
	case <-time.After(500 * time.Millisecond):
		t.Fatalf("first Reacquire did not complete")
	}

	snap := mgr.Snapshot("acc-dup-reacquire")
	if snap.TotalActive != 1 {
		t.Fatalf("totalActive should be exactly 1, got %d", snap.TotalActive)
	}
	_ = l1.Release()
}

// TestUnrelatedSessionBehindBlockedSameSessionWaiter verifies that when an unrelated session
// enqueues behind a waiter blocked by same-session serialization, it dispatches immediately
// without deadlocking when capacity is available.
func TestUnrelatedSessionBehindBlockedSameSessionWaiter(t *testing.T) {
	mgr := NewManager(ManagerConfig{DefaultLimit: 2})
	defer mgr.Close()

	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()

	// Session 1 acquires slot 1
	l1, err := mgr.Acquire(ctx, AcquireRequest{
		AccountScopeID: "acc-noblock",
		SessionID:      "sess-1",
		RunID:          "run-1",
	})
	if err != nil {
		t.Fatalf("l1: %v", err)
	}
	defer l1.Release()

	// Session 1 Run 2 enqueues (blocked by session serialization, even though limit=2)
	run2ErrCh := make(chan error, 1)
	go func() {
		l, err := mgr.Acquire(ctx, AcquireRequest{
			AccountScopeID: "acc-noblock",
			SessionID:      "sess-1",
			RunID:          "run-2",
		})
		if err == nil {
			_ = l.Release()
		}
		run2ErrCh <- err
	}()

	time.Sleep(30 * time.Millisecond)

	// Session 2 Run 1 arrives. Since capacity=2 (totalActive=1) and sess-2 is free,
	// it must NOT deadlock behind sess-1 run-2!
	l2, err := mgr.Acquire(ctx, AcquireRequest{
		AccountScopeID: "acc-noblock",
		SessionID:      "sess-2",
		RunID:          "run-1",
	})
	if err != nil {
		t.Fatalf("sess-2 acquire failed: %v", err)
	}
	defer l2.Release()

	if !l2.IsActive() {
		t.Fatalf("sess-2 lease should be active")
	}
}

// TestCancellationDispatchesOthers verifies that removing a cancelled waiter from the queue
// immediately dispatches other eligible waiters.
func TestCancellationDispatchesOthers(t *testing.T) {
	mgr := NewManager(ManagerConfig{DefaultLimit: 1})
	defer mgr.Close()

	ctx := context.Background()

	// Saturated slot
	l1, err := mgr.Acquire(ctx, AcquireRequest{
		AccountScopeID: "acc-cancel-dispatch",
		SessionID:      "sess-1",
		RunID:          "run-1",
	})
	if err != nil {
		t.Fatalf("l1: %v", err)
	}

	// Waiter 1 arrives with cancellable context
	ctx1, cancel1 := context.WithCancel(ctx)
	w1ErrCh := make(chan error, 1)
	go func() {
		l, err := mgr.Acquire(ctx1, AcquireRequest{
			AccountScopeID: "acc-cancel-dispatch",
			SessionID:      "sess-2",
			RunID:          "run-2",
		})
		if err == nil {
			_ = l.Release()
		}
		w1ErrCh <- err
	}()

	// Waiter 2 arrives
	w2ErrCh := make(chan error, 1)
	var l2 Lease
	go func() {
		var err error
		l2, err = mgr.Acquire(ctx, AcquireRequest{
			AccountScopeID: "acc-cancel-dispatch",
			SessionID:      "sess-3",
			RunID:          "run-3",
		})
		w2ErrCh <- err
	}()

	time.Sleep(30 * time.Millisecond)
	snap := mgr.Snapshot("acc-cancel-dispatch")
	if snap.Pending != 2 {
		t.Fatalf("expected 2 pending waiters, got %d", snap.Pending)
	}

	// Cancel Waiter 1
	cancel1()
	if err := <-w1ErrCh; !errors.Is(err, context.Canceled) {
		t.Fatalf("expected context.Canceled, got: %v", err)
	}

	// Release l1: Waiter 2 must be dispatched immediately
	_ = l1.Release()

	select {
	case err := <-w2ErrCh:
		if err != nil {
			t.Fatalf("waiter 2 failed: %v", err)
		}
	case <-time.After(500 * time.Millisecond):
		t.Fatalf("waiter 2 was not dispatched")
	}

	if l2 == nil || !l2.IsActive() {
		t.Fatalf("l2 should be active")
	}
	_ = l2.Release()
}

// TestLimitResolverFailClosed verifies that when LimitResolver returns a persistence/lookup
// error, the manager fails closed (limit 0, no admissions, snapshot shows Unavailable=true and Error).
func TestLimitResolverFailClosed(t *testing.T) {
	expectedErr := errors.New("pebble disk error")
	mgr := NewManager(ManagerConfig{
		DefaultLimit: 100,
		LimitResolver: func(accountScopeID string) (int, error) {
			return 0, expectedErr
		},
	})
	defer mgr.Close()

	ctx := context.Background()
	_, err := mgr.Acquire(ctx, AcquireRequest{
		AccountScopeID: "acc-err",
		SessionID:      "sess-1",
		RunID:          "run-1",
	})
	if err == nil || !strings.Contains(err.Error(), "pebble disk error") {
		t.Fatalf("expected limit resolution error, got: %v", err)
	}

	snap := mgr.Snapshot("acc-err")
	if !snap.Unavailable {
		t.Fatalf("expected snap.Unavailable = true on resolver error")
	}
	if snap.Error != expectedErr.Error() {
		t.Fatalf("expected snap.Error = %q, got %q", expectedErr.Error(), snap.Error)
	}
	if snap.EffectiveLimit != 0 || snap.Available != 0 {
		t.Fatalf("expected limit=0 and available=0 when failing closed, got: %+v", snap)
	}
}

// TestActiveLeaseForSession verifies active lease lookup by session ID.
func TestActiveLeaseForSession(t *testing.T) {
	mgr := NewManager(ManagerConfig{DefaultLimit: 5})
	defer mgr.Close()

	ctx := context.Background()
	acc := "acc-lookup"
	sess := "sess-compact"

	if l := mgr.ActiveLeaseForSession(acc, sess); l != nil {
		t.Fatalf("expected nil before acquire")
	}

	lease, err := mgr.Acquire(ctx, AcquireRequest{
		AccountScopeID: acc,
		SessionID:      sess,
		RunID:          "run-1",
	})
	if err != nil {
		t.Fatalf("acquire: %v", err)
	}
	defer lease.Release()

	if l := mgr.ActiveLeaseForSession(acc, sess); l == nil || l.ID() != lease.ID() {
		t.Fatalf("expected active lease %s, got %v", lease.ID(), l)
	}

	if err := lease.Park(); err != nil {
		t.Fatalf("park: %v", err)
	}
	if l := mgr.ActiveLeaseForSession(acc, sess); l != nil {
		t.Fatalf("expected nil when parked")
	}

	if err := lease.Reacquire(ctx); err != nil {
		t.Fatalf("reacquire: %v", err)
	}
	if l := mgr.ActiveLeaseForSession(acc, sess); l == nil {
		t.Fatalf("expected active lease after reacquire")
	}

	if err := lease.Release(); err != nil {
		t.Fatalf("release: %v", err)
	}
	if l := mgr.ActiveLeaseForSession(acc, sess); l != nil {
		t.Fatalf("expected nil after release")
	}
}

// TestLeaseFromContextReleasedAndAccount verifies that released leases are never returned
// by LeaseFromContext, and LeaseFromContextForAccount validates accountScopeID.
func TestLeaseFromContextReleasedAndAccount(t *testing.T) {
	mgr := NewManager(ManagerConfig{DefaultLimit: 5})
	defer mgr.Close()

	ctx := context.Background()
	lease, err := mgr.Acquire(ctx, AcquireRequest{
		AccountScopeID: "acc-ctx-rel",
		SessionID:      "sess-parent",
		RunID:          "run-1",
	})
	if err != nil {
		t.Fatalf("acquire: %v", err)
	}

	boundCtx := WithLease(ctx, lease)

	// Valid with account check
	if got, ok := LeaseFromContextForAccount(boundCtx, "acc-ctx-rel", "sess-parent", "run-1"); !ok || got != lease {
		t.Fatalf("expected lease with matching account")
	}
	// Wrong account returns false
	if _, ok := LeaseFromContextForAccount(boundCtx, "wrong-acc", "sess-parent", "run-1"); ok {
		t.Fatalf("expected false for wrong account")
	}

	// Release lease
	if err := lease.Release(); err != nil {
		t.Fatalf("release: %v", err)
	}

	// Released lease must NOT be returned from context
	if _, ok := LeaseFromContext(boundCtx, "sess-parent", "run-1"); ok {
		t.Fatalf("expected false for released lease from context")
	}
	if _, ok := LeaseFromContextForAccount(boundCtx, "acc-ctx-rel", "sess-parent", "run-1"); ok {
		t.Fatalf("expected false for released lease from context with account")
	}
}

// Purpose: Changed must remain closed after shutdown and owner release; callers
// must not miss the terminal edge. Manager.Close/Changed own this contract.
func TestChangedRemainsClosedAfterShutdown(t *testing.T) {
	m := NewManager(ManagerConfig{DefaultLimit: 1})
	l, err := m.Acquire(context.Background(), AcquireRequest{AccountScopeID: "a", SessionID: "s", RunID: "r"})
	if err != nil {
		t.Fatal(err)
	}
	edge := m.Changed()
	m.Close()
	for _, ch := range []<-chan struct{}{edge, m.Changed()} {
		select {
		case <-ch:
		default:
			t.Fatal("shutdown edge is open")
		}
	}
	if err := l.Release(); err != nil {
		t.Fatal(err)
	}
	select {
	case <-m.Changed():
	default:
		t.Fatal("release reopened closed manager")
	}
	if s := m.Snapshot("a"); !s.Unavailable || s.TotalActive != 0 {
		t.Fatalf("closed snapshot %+v", s)
	}
}

// Purpose: persisted invalid limits must never widen to defaults. Exercise the
// manager resolver boundary with both non-positive and excessive stored values.
func TestInvalidResolvedLimitFailsClosed(t *testing.T) {
	for _, limit := range []int{-1, 0, MaxActiveExecutionLimit + 1} {
		m := NewManager(ManagerConfig{LimitResolver: func(string) (int, error) { return limit, nil }})
		if _, err := m.Acquire(context.Background(), AcquireRequest{AccountScopeID: "a", SessionID: "s", RunID: "r"}); err == nil {
			t.Fatalf("accepted invalid limit %d", limit)
		}
		if s := m.Snapshot("a"); !s.Unavailable || s.TotalActive != 0 || s.Available != 0 {
			t.Fatalf("invalid limit snapshot %+v", s)
		}
		m.Close()
	}
}
