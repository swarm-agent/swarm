package executioncapacity

import (
	"context"
	"testing"
	"time"
)

// Purpose: BeginDrain and Acquire share one admission fence. This manager-level
// test proves new leases cannot escape it, existing parked owners can finish,
// timeout identifies blockers without releasing them, and close wakes waiters.
// Durable V3 intent replay is owned by the executor and is tested separately.
func TestDrainAdmissionFence(t *testing.T) {
	m := NewManager(ManagerConfig{})
	ctx, cancel := context.WithTimeout(context.Background(), time.Second)
	defer cancel()
	active, err := m.Acquire(ctx, AcquireRequest{AccountScopeID: "a", SessionID: "running", RunID: "run"})
	if err != nil {
		t.Fatal(err)
	}
	if err := active.Park(); err != nil {
		t.Fatal(err)
	}
	status, err := m.BeginDrain(time.Hour)
	if err != nil || status.Ready || len(status.Blockers) != 1 {
		t.Fatalf("drain: %+v %v", status, err)
	}
	if err := active.Reacquire(ctx); err != nil {
		t.Fatal(err)
	}
	if active.IsReleased() {
		t.Fatal("drain interrupted existing lease")
	}
	result := make(chan error, 1)
	go func() {
		l, err := m.Acquire(ctx, AcquireRequest{AccountScopeID: "b", SessionID: "queued", RunID: "next"})
		if l != nil {
			_ = l.Release()
		}
		result <- err
	}()
	for m.Pending("b") == 0 {
		select {
		case <-ctx.Done():
			t.Fatal(ctx.Err())
		default:
		}
	}
	status = m.DrainStatus()
	if status.TimedOut || len(status.Blockers) != 1 || status.Blockers[0].Run != "run" {
		t.Fatalf("timeout: %+v", status)
	}
	again, _ := m.BeginDrain(time.Hour)
	if again.Deadline != status.Deadline {
		t.Fatal("retry extended deadline")
	}
	if err := active.Release(); err != nil {
		t.Fatal(err)
	}
	if !m.DrainStatus().Ready {
		t.Fatal("released owner still blocks")
	}
	select {
	case err := <-result:
		t.Fatalf("new admission escaped drain: %v", err)
	default:
	}
	m.Close()
	if err := <-result; err != ErrClosed {
		t.Fatalf("close: %v", err)
	}
	fresh := NewManager(ManagerConfig{})
	resumed, err := fresh.Acquire(ctx, AcquireRequest{AccountScopeID: "b", SessionID: "queued", RunID: "next"})
	if err != nil {
		t.Fatal(err)
	}
	_ = resumed.Release()
}

// Purpose: concurrent drain and Acquire must linearize: a grant is either a
// visible blocker or stays queued. Testing the shared lock directly is narrower
// and deterministic compared with timing an installer against a live daemon.
func TestDrainConcurrentAdmission(t *testing.T) {
	for i := 0; i < 50; i++ {
		m := NewManager(ManagerConfig{})
		ctx, cancel := context.WithTimeout(context.Background(), time.Second)
		started := make(chan struct{})
		result := make(chan Lease, 1)
		go func() {
			close(started)
			l, _ := m.Acquire(ctx, AcquireRequest{SessionID: "s", RunID: "r"})
			result <- l
		}()
		<-started
		status, err := m.BeginDrain(time.Hour)
		if err != nil {
			t.Fatal(err)
		}
		m.Close()
		l := <-result
		if l != nil {
			if status.Ready || len(status.Blockers) != 1 {
				t.Fatal("grant missing from blockers")
			}
			_ = l.Release()
		} else if !status.Ready {
			t.Fatal("phantom blocker")
		}
		cancel()
	}
}

// Purpose: expiry/cancellation must wake queued admissions without releasing
// active owners; the manager is the narrowest authority for both transitions.
func TestDrainEndReopensAdmissions(t *testing.T) {
	for _, reason := range []string{"expired", "cancelled"} {
		t.Run(reason, func(t *testing.T) {
			m := NewManager(ManagerConfig{})
			defer m.Close()
			ctx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
			defer cancel()
			active, err := m.Acquire(ctx, AcquireRequest{SessionID: "active", RunID: "blocker"})
			if err != nil {
				t.Fatal(err)
			}
			defer active.Release()
			ended := make(chan DrainStatus, 1)
			m.SetDrainEndObserver(func(s DrainStatus) { ended <- s })
			duration := time.Hour
			if reason == "expired" {
				duration = 20 * time.Millisecond
			}
			first, err := m.BeginDrain(duration)
			if err != nil {
				t.Fatal(err)
			}
			if reason == "cancelled" {
				if !m.CancelDrain("wrong-generation").Draining {
					t.Fatal("stale cancel changed drain")
				}
				m.CancelDrain(first.Generation)
			}
			held, err := m.Acquire(ctx, AcquireRequest{SessionID: "held", RunID: "next"})
			if err != nil {
				t.Fatal(err)
			}
			defer held.Release()
			select {
			case s := <-ended:
				if s.EndReason != reason || len(s.Blockers) != 1 || s.Blockers[0].Run != "blocker" {
					t.Fatalf("end: %+v", s)
				}
			case <-ctx.Done():
				t.Fatal(ctx.Err())
			}
			if active.IsReleased() || m.DrainStatus().Draining {
				t.Fatal("invalid end state")
			}
			second, err := m.BeginDrain(time.Hour)
			if err != nil || !second.Deadline.After(first.Deadline) {
				t.Fatalf("fresh drain: %+v %v", second, err)
			}
			m.CancelDrain(second.Generation)
		})
	}
}

// Purpose: readiness from a prior daemon must not authorize a replacement;
// manager identities are the narrowest boundary for installer generation checks.
func TestDrainGenerationChangesAfterRestart(t *testing.T) {
	old := NewManager(ManagerConfig{})
	first, err := old.BeginDrain(time.Hour)
	if err != nil {
		t.Fatal(err)
	}
	old.Close()
	fresh := NewManager(ManagerConfig{})
	defer fresh.Close()
	if fresh.DrainStatus().Ready {
		t.Fatal("new daemon inherited readiness")
	}
	second, err := fresh.BeginDrain(time.Hour)
	if err != nil {
		t.Fatal(err)
	}
	if first.Generation == "" || first.Generation == second.Generation {
		t.Fatal("generation reused")
	}
	if !fresh.CancelDrain(first.Generation).Draining {
		t.Fatal("old installer cancelled new generation")
	}
	fresh.CancelDrain(second.Generation)
}
