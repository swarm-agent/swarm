package executioncapacity

import (
	"context"
	"errors"
	"testing"
	"time"
)

// Purpose: BeginDrain and Acquire share one admission fence. This manager-level
// test proves new leases cannot escape it, existing parked owners can finish,
// timeout identifies blockers without releasing them, and close wakes waiters.
// Durable V3 intent replay is owned by the executor and is tested separately.
// Purpose: BeginDrainIf decides under the admission lock, so a refused check
// leaves admission open, the check sees every held and queued session, and a
// drain in progress is returned without asking again. Threat: a run admitted
// between the busy check and the fence, or a refused request that still
// fences (a partial drain). The manager is the narrowest layer owning the lock.
func TestBeginDrainIfChecksUnderAdmissionLock(t *testing.T) {
	m := NewManager(ManagerConfig{})
	m.SetLimit("a", 1)
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	running, err := m.Acquire(ctx, AcquireRequest{AccountScopeID: "a", SessionID: "running", RunID: "run"})
	if err != nil {
		t.Fatal(err)
	}
	waitCtx, stop := context.WithCancel(ctx)
	waited := make(chan error, 1)
	go func() {
		_, err := m.Acquire(waitCtx, AcquireRequest{AccountScopeID: "a", SessionID: "waiting", RunID: "run2"})
		waited <- err
	}()
	for deadline := time.Now().Add(5 * time.Second); len(m.QueuedSessions()) != 1; time.Sleep(5 * time.Millisecond) {
		if time.Now().After(deadline) {
			t.Fatal("waiter never queued")
		}
	}
	busy := errors.New("busy")
	var gotHeld, gotQueued []string
	if _, err := m.BeginDrainIf(time.Hour, func(held, queued []string) error {
		gotHeld, gotQueued = held, queued
		return busy
	}); !errors.Is(err, busy) {
		t.Fatalf("refused check returned %v", err)
	}
	if len(gotHeld) != 1 || gotHeld[0] != "running" || len(gotQueued) != 1 || gotQueued[0] != "waiting" {
		t.Fatalf("check saw held %v queued %v", gotHeld, gotQueued)
	}
	if m.DrainStatus().Draining {
		t.Fatal("a refused check fenced admission")
	}
	stop()
	<-waited
	if err := running.Release(); err != nil {
		t.Fatal(err)
	}
	if status, err := m.BeginDrainIf(time.Hour, func(held, queued []string) error { return nil }); err != nil || !status.Draining {
		t.Fatalf("allowed drain: %+v %v", status, err)
	}
	if status, err := m.BeginDrainIf(time.Hour, func(held, queued []string) error { return busy }); err != nil || !status.Draining {
		t.Fatalf("a drain in progress was not returned as is: %+v %v", status, err)
	}
}

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
// Purpose: HeldRuns names each held lease's account, session and run (the
// fleet's agents read filters it by account), the same set HeldSessions
// counts, in a stable order; a released lease leaves it at once.
// Threat: another account's runs listed as this account's agents, or a
// finished run still shown. Boundary: Manager.HeldRuns over real leases.
func TestHeldRunsNamesAccountSessionAndRun(t *testing.T) {
	m := NewManager(ManagerConfig{})
	ctx, cancel := context.WithTimeout(context.Background(), time.Second)
	defer cancel()
	first, err := m.Acquire(ctx, AcquireRequest{AccountScopeID: "b", SessionID: "s2", RunID: "r2"})
	if err != nil {
		t.Fatal(err)
	}
	second, err := m.Acquire(ctx, AcquireRequest{AccountScopeID: "a", SessionID: "s1", RunID: "r1"})
	if err != nil {
		t.Fatal(err)
	}
	got := m.HeldRuns()
	if len(got) != 2 || got[0] != (DrainBlocker{Account: "a", Session: "s1", Run: "r1"}) || got[1] != (DrainBlocker{Account: "b", Session: "s2", Run: "r2"}) || len(m.HeldSessions()) != 2 {
		t.Fatalf("held runs = %+v", got)
	}
	if err := second.Release(); err != nil {
		t.Fatal(err)
	}
	if got := m.HeldRuns(); len(got) != 1 || got[0].Account != "b" {
		t.Fatalf("after release: %+v", got)
	}
	_ = first.Release()
	if got := m.HeldRuns(); len(got) != 0 {
		t.Fatalf("after both released: %+v", got)
	}
}

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

// Purpose: restart reservations keep the admission fence through expiry and
// ordinary cancellation; only the owning installer's failure cleanup releases it.
func TestDrainRestartReservation(t *testing.T) {
	m := NewManager(ManagerConfig{})
	defer m.Close()
	s, err := m.BeginDrain(time.Hour)
	if err != nil {
		t.Fatal(err)
	}
	claim := "01234567890123456789012345678901"
	if _, err = m.ClaimRestart("stale", claim); err == nil {
		t.Fatal("stale generation accepted")
	}
	if _, err = m.ClaimRestart(s.Generation, claim); err != nil {
		t.Fatal(err)
	}
	m.mu.Lock()
	m.drain.Deadline = time.Now().Add(-time.Hour)
	m.mu.Unlock()
	if !m.CancelDrain(s.Generation).Draining {
		t.Fatal("cancel broke reservation")
	}
	if !m.ReleaseRestart(s.Generation, "wrong").Draining {
		t.Fatal("wrong owner released reservation")
	}
	ctx, cancel := context.WithTimeout(context.Background(), 20*time.Millisecond)
	defer cancel()
	if l, err := m.Acquire(ctx, AcquireRequest{SessionID: "held", RunID: "held"}); err == nil {
		l.Release()
		t.Fatal("admitted during restart")
	}
	if s := m.DrainStatus(); !s.Ready || !s.Reserved {
		t.Fatalf("reservation expired: %+v", s)
	}
	if m.ReleaseRestart(s.Generation, claim).Draining {
		t.Fatal("cleanup failed")
	}
}
