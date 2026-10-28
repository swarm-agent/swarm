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
	m.mu.Lock()
	defer m.mu.Unlock()
	if m.closed {
		return DrainStatus{}, ErrClosed
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
	out := []string{}
	for _, acc := range m.accounts {
		for _, l := range acc.sessions {
			out = append(out, l.sessionID)
		}
	}
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
