package executioncapacity

import (
	"context"
	"strings"
	"testing"
	"time"
)

// Purpose:
//   - Requirement: request_update's bounded drain (BeginDrainWith with
//     Requested) holds new runs at once and becomes ready for the restart as
//     soon as the agent runs holding a lease end (the safe point the daemon
//     can guarantee), without waiting for runs that only wait for admission:
//     those are durable pending intents that run after the restart. A later
//     request can widen the drain (Requested, Force), never narrow it, and
//     never moves its deadline.
//   - Threat: an update that waits for an idle machine forever (the observed
//     factory that sat on old code for hours), or one whose queued runs slip
//     through the fence between ready and the restart.
//   - Boundary: Manager.BeginDrainWith, drainStatusLocked (Ready, Reason),
//     ClaimRestart. The manager owns admission and the fence, so it is the
//     narrowest layer that proves both.
func TestRequestedDrainRestartsWhenRunsFinish(t *testing.T) {
	m := NewManager(ManagerConfig{})
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	running, err := m.Acquire(ctx, AcquireRequest{AccountScopeID: "a", SessionID: "s-run", RunID: "r-run"})
	if err != nil {
		t.Fatal(err)
	}
	status, err := m.BeginDrainWith(DrainOptions{Timeout: time.Hour, Requested: true}, nil)
	if err != nil || !status.Draining || !status.Requested || status.Force || status.Ready {
		t.Fatalf("requested drain: %+v %v", status, err)
	}
	if !strings.Contains(status.Reason, "waiting for 1 agent run(s) (s-run/r-run)") {
		t.Fatalf("reason does not name the blocking run: %q", status.Reason)
	}
	// A new run arrives: held, and not a blocker.
	admitted := make(chan error, 1)
	go func() {
		l, err := m.Acquire(ctx, AcquireRequest{AccountScopeID: "b", SessionID: "s-new", RunID: "r-new"})
		if l != nil {
			_ = l.Release()
		}
		admitted <- err
	}()
	for deadline := time.Now().Add(5 * time.Second); len(m.QueuedSessions()) != 1; time.Sleep(5 * time.Millisecond) {
		if time.Now().After(deadline) {
			t.Fatal("the new run never queued")
		}
	}
	if s := m.DrainStatus(); len(s.Blockers) != 1 || s.Ready {
		t.Fatalf("a queued run counted as a blocker or the drain was ready early: %+v", s)
	}
	// A second request widens the drain to force; the deadline stays.
	again, err := m.BeginDrainWith(DrainOptions{Timeout: time.Minute, Force: true}, nil)
	if err != nil || !again.Requested || !again.Force || again.Forced || !again.Deadline.Equal(status.Deadline) {
		t.Fatalf("second request: %+v %v", again, err)
	}
	// The running run reaches its end: ready, and the restart is claimed.
	if err := running.Release(); err != nil {
		t.Fatal(err)
	}
	ready := m.DrainStatus()
	if !ready.Ready || ready.TimedOut || len(ready.Blockers) != 0 || !strings.HasPrefix(ready.Reason, "ready:") {
		t.Fatalf("drain after the run ended: %+v", ready)
	}
	if _, err := m.ClaimRestart(ready.Generation, strings.Repeat("c", 32)); err != nil {
		t.Fatalf("claim: %v", err)
	}
	select {
	case err := <-admitted:
		t.Fatalf("a held run was admitted before the restart: %v", err)
	default:
	}
	m.Close()
	if err := <-admitted; err != ErrClosed {
		t.Fatalf("close: %v", err)
	}
}

// Purpose:
//   - Requirement: an agent run in progress does not resume after a daemon
//     restart (startup records it interrupted), so a requested drain whose
//     deadline passes with runs still running refuses the update: it ends
//     (expired), reopens admission, cannot be claimed for a restart, names
//     the runs and never releases them. Only with force does the deadline
//     make it ready over those runs (Forced), for the installer to restart.
//   - Threat: an update that silently kills running agent work, or a forced
//     update that never restarts.
//   - Boundary: Manager.BeginDrainWith's deadline timer, drainStatusLocked
//     (TimedOut, Forced, Ready, Reason), ClaimRestart.
func TestRequestedDrainRefusesAtDeadlineUnlessForced(t *testing.T) {
	const timeout = 50 * time.Millisecond
	for _, force := range []bool{false, true} {
		m := NewManager(ManagerConfig{})
		ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
		running, err := m.Acquire(ctx, AcquireRequest{AccountScopeID: "a", SessionID: "s-long", RunID: "r-long"})
		if err != nil {
			t.Fatal(err)
		}
		ended := make(chan DrainStatus, 1)
		m.SetDrainEndObserver(func(s DrainStatus) { ended <- s })
		status, err := m.BeginDrainWith(DrainOptions{Timeout: timeout, Requested: true, Force: force}, nil)
		if err != nil || status.Ready {
			t.Fatalf("force=%v drain: %+v %v", force, status, err)
		}
		if !force {
			select {
			case s := <-ended:
				if s.EndReason != "expired" {
					t.Fatalf("drain ended %q", s.EndReason)
				}
			case <-ctx.Done():
				t.Fatal("the drain did not end at its deadline")
			}
			s := m.DrainStatus()
			if s.Draining || s.Ready || !s.TimedOut || s.Forced || len(s.Blockers) != 1 || s.Blockers[0].Run != "r-long" {
				t.Fatalf("refused drain: %+v", s)
			}
			if !strings.Contains(s.Reason, "refused") || !strings.Contains(s.Reason, "s-long/r-long") || !strings.Contains(s.Reason, "force") {
				t.Fatalf("refusal reason: %q", s.Reason)
			}
			if _, err := m.ClaimRestart(s.Generation, strings.Repeat("c", 32)); err == nil {
				t.Fatal("a refused drain was claimed for a restart")
			}
			// Admission is open again: a new run starts.
			next, err := m.Acquire(ctx, AcquireRequest{AccountScopeID: "b", SessionID: "s-next", RunID: "r-next"})
			if err != nil {
				t.Fatalf("admission stayed closed after the refusal: %v", err)
			}
			_ = next.Release()
		} else {
			var s DrainStatus
			for deadline := time.Now().Add(5 * time.Second); ; time.Sleep(5 * time.Millisecond) {
				if s = m.DrainStatus(); s.Forced || time.Now().After(deadline) {
					break
				}
			}
			if !s.Draining || !s.Forced || !s.Ready || s.TimedOut || len(s.Blockers) != 1 || !strings.Contains(s.Reason, "interrupts 1 agent run(s) (s-long/r-long)") {
				t.Fatalf("forced drain: %+v", s)
			}
			claimed, err := m.ClaimRestart(s.Generation, strings.Repeat("c", 32))
			if err != nil || !claimed.Ready || !claimed.Forced {
				t.Fatalf("forced claim: %+v %v", claimed, err)
			}
			select {
			case s := <-ended:
				t.Fatalf("a forced drain ended: %+v", s)
			default:
			}
		}
		// The manager never ends the run itself, forced or not: the restart
		// does, and startup records it interrupted.
		if running.IsReleased() {
			t.Fatalf("force=%v: the drain released a running run", force)
		}
		_ = running.Release()
		m.Close()
		cancel()
	}
}
