package executioncapacity

import (
	"crypto/rand"
	"encoding/hex"
	"errors"
	"sort"
	"time"
)

const DefaultDrainTimeout = 2 * time.Hour

type DrainBlocker struct {
	Account string `json:"account"`
	Session string `json:"session"`
	Run     string `json:"run"`
}

type DrainStatus struct {
	Reserved          bool `json:"reserved"`
	claim             string
	Generation        string         `json:"generation"`
	EndReason         string         `json:"end_reason,omitempty"`
	EnvironmentBuilds int            `json:"environment_builds"`
	Draining          bool           `json:"draining"`
	Since             time.Time      `json:"since"`
	Deadline          time.Time      `json:"deadline"`
	TimedOut          bool           `json:"timed_out"`
	Ready             bool           `json:"ready"`
	Blockers          []DrainBlocker `json:"blockers"`
}

// BeginDrain fences new admissions under the very lock that grants leases.
// Existing owners (including parked parents) remain blockers and may reacquire.
// Pending V3 intents remain in their durable store; this manager is not a queue
// persistence authority. A new daemon manager reopens admission after restart.
func (m *Manager) BeginDrain(timeout time.Duration) (DrainStatus, error) {
	return m.BeginDrainIf(timeout, nil)
}

// BeginDrainIf is BeginDrain that starts a new drain only when allow, given
// the held and queued sessions (HeldSessions, QueuedSessions), returns nil.
// allow runs under the admission lock, so no run is admitted between its
// check and the fence; it must not call the manager. A drain already in
// progress is returned as is, without calling allow.
func (m *Manager) BeginDrainIf(timeout time.Duration, allow func(held, queued []string) error) (DrainStatus, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	if m.closed {
		return DrainStatus{}, ErrClosed
	}
	if !m.drain.Draining && allow != nil {
		if err := allow(m.heldSessionsLocked(), m.queuedSessionsLocked()); err != nil {
			return DrainStatus{}, err
		}
	}
	if !m.drain.Draining {
		if timeout <= 0 {
			timeout = DefaultDrainTimeout
		}
		now := time.Now().UTC()
		if m.generation == "" {
			var id [16]byte
			if _, err := rand.Read(id[:]); err != nil {
				return DrainStatus{}, err
			}
			m.generation = hex.EncodeToString(id[:])
		}
		m.drain = DrainStatus{Generation: m.generation, Draining: true, Since: now, Deadline: now.Add(timeout)}
		m.drainTimer = time.AfterFunc(timeout, func() {
			m.mu.Lock()
			defer m.mu.Unlock()
			if m.drain.Since == now && m.drain.Draining && !m.drain.Reserved {
				m.endDrainLocked("expired")
			}
		})
		m.signalChangedLocked()
	}
	return m.drainStatusLocked(), nil
}

func (m *Manager) DrainStatus() DrainStatus {
	m.mu.Lock()
	defer m.mu.Unlock()
	return m.drainStatusLocked()
}

// HeldSessions lists the sessions whose runs hold an execution lease now: the
// live set a drain waits on (drainStatusLocked's blockers), whatever the drain
// state. Session IDs may repeat across accounts.
func (m *Manager) HeldSessions() []string {
	m.mu.Lock()
	defer m.mu.Unlock()
	return m.heldSessionsLocked()
}

// QueuedSessions lists the sessions with a new run waiting for admission (for
// capacity, or held by a drain): runs a drain holds. A parked lease waiting to
// reacquire is already in HeldSessions. Session IDs may repeat.
func (m *Manager) QueuedSessions() []string {
	m.mu.Lock()
	defer m.mu.Unlock()
	return m.queuedSessionsLocked()
}

func (m *Manager) queuedSessionsLocked() []string {
	out := []string{}
	for _, acc := range m.accounts {
		for _, w := range acc.waiters {
			if w.reacquire == nil && (w.ctx == nil || w.ctx.Err() == nil) {
				out = append(out, w.req.SessionID)
			}
		}
	}
	return out
}

func (m *Manager) heldSessionsLocked() []string {
	out := []string{}
	for _, acc := range m.accounts {
		for _, l := range acc.sessions {
			out = append(out, l.sessionID)
		}
	}
	return out
}

// HeldRuns is HeldSessions with each lease's account and run: the runs
// holding an execution lease now, ordered by account, session and run.
func (m *Manager) HeldRuns() []DrainBlocker {
	m.mu.Lock()
	defer m.mu.Unlock()
	out := []DrainBlocker{}
	for account, acc := range m.accounts {
		for _, l := range acc.sessions {
			out = append(out, DrainBlocker{Account: account, Session: l.sessionID, Run: l.runID})
		}
	}
	sort.Slice(out, func(i, j int) bool {
		if out[i].Account != out[j].Account {
			return out[i].Account < out[j].Account
		}
		if out[i].Session != out[j].Session {
			return out[i].Session < out[j].Session
		}
		return out[i].Run < out[j].Run
	})
	return out
}

func (m *Manager) drainStatusLocked() DrainStatus {
	out := m.drain
	if out.EndReason == "expired" {
		out.Blockers = append([]DrainBlocker{}, out.Blockers...)
		out.Ready = false
		out.TimedOut = true
		return out
	}
	out.Blockers = []DrainBlocker{}
	for account, acc := range m.accounts {
		for _, l := range acc.sessions {
			out.Blockers = append(out.Blockers, DrainBlocker{Account: account, Session: l.sessionID, Run: l.runID})
		}
	}
	sort.Slice(out.Blockers, func(i, j int) bool {
		a, b := out.Blockers[i], out.Blockers[j]
		if a.Account != b.Account {
			return a.Account < b.Account
		}
		if a.Session != b.Session {
			return a.Session < b.Session
		}
		return a.Run < b.Run
	})
	out.TimedOut = out.EndReason == "expired" || (out.Draining && !out.Reserved && !time.Now().Before(out.Deadline))
	out.Ready = out.Draining && !out.TimedOut && !m.closed && len(out.Blockers) == 0
	return out
}

// SetDrainEndObserver installs a lifecycle sink. It must not call the manager.
func (m *Manager) SetDrainEndObserver(fn func(DrainStatus)) {
	m.mu.Lock()
	defer m.mu.Unlock()
	m.drainEnd = fn
}
func (m *Manager) CancelDrain(generation string) DrainStatus {
	m.mu.Lock()
	defer m.mu.Unlock()
	if m.drain.Draining && !m.drain.Reserved && (generation == "" || generation == m.generation) {
		m.endDrainLocked("cancelled")
	}
	return m.drainStatusLocked()
}
func (m *Manager) endDrainLocked(reason string) {
	if m.drainTimer != nil {
		m.drainTimer.Stop()
	}
	m.drain.Blockers = m.drainStatusLocked().Blockers
	m.drain.Draining = false
	m.drain.EndReason = reason
	if m.drainEnd != nil {
		m.drainEnd(m.drainStatusLocked())
	}
	for _, acc := range m.accounts {
		m.wakeEligibleWaitersLocked(acc)
	}
	m.signalChangedLocked()
}

// ClaimRestart transfers timeout ownership to the installer. Only its secret
// claim can release the fence; ordinary cancellation cannot interrupt restart.
func (m *Manager) ClaimRestart(generation, claim string) (DrainStatus, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	if generation != m.generation || len(claim) < 32 || !m.drainStatusLocked().Ready || m.drain.Reserved {
		return DrainStatus{}, errors.New("drain not ready for restart reservation")
	}
	m.drain.Reserved = true
	m.drain.claim = claim
	m.drainTimer.Stop()
	return m.drainStatusLocked(), nil
}
func (m *Manager) ReleaseRestart(generation, claim string) DrainStatus {
	m.mu.Lock()
	defer m.mu.Unlock()
	if m.drain.Reserved && generation == m.generation && claim == m.drain.claim {
		m.drain.Reserved = false
		m.endDrainLocked("cancelled")
	}
	return m.drainStatusLocked()
}
