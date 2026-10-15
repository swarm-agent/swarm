package executioncapacity

import (
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
		m.drain = DrainStatus{Draining: true, Since: now, Deadline: now.Add(timeout)}
		m.signalChangedLocked()
	}
	return m.drainStatusLocked(), nil
}

func (m *Manager) DrainStatus() DrainStatus {
	m.mu.Lock()
	defer m.mu.Unlock()
	return m.drainStatusLocked()
}

func (m *Manager) drainStatusLocked() DrainStatus {
	out := m.drain
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
	out.TimedOut = out.Draining && !time.Now().Before(out.Deadline)
	out.Ready = out.Draining && !m.closed && len(out.Blockers) == 0
	return out
}
