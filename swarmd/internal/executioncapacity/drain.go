package executioncapacity

import (
	"crypto/rand"
	"encoding/hex"
	"errors"
	"fmt"
	"sort"
	"strings"
	"time"
)

const DefaultDrainTimeout = 2 * time.Hour

// DefaultForcedDrainClaimGrace is how long a forced drain stays ready after
// its deadline for the installer to claim the restart (ClaimRestart). An
// unclaimed forced drain then ends (expired) and admission reopens, so a
// forced drain never holds new runs without a time limit (automatic updates
// off, the installer timer not running, a build that keeps failing). The
// installer checks every few minutes and builds before it claims.
const DefaultForcedDrainClaimGrace = 30 * time.Minute

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
	// Requested: someone asked for this update (request_update), so the
	// installer's automatic update takes this drain on its next run instead
	// of waiting for an idle machine.
	Requested bool `json:"requested"`
	// Force: at the deadline, runs still running are interrupted by the
	// restart (Forced) instead of the update being refused.
	Force bool `json:"force"`
	// Forced: the deadline passed with Force set; the drain is ready over
	// the Blockers, which the restart records interrupted.
	Forced bool `json:"forced"`
	// Reason says, in one line, what the drain waits on or why it ended.
	Reason string `json:"reason,omitempty"`
	// WorkflowSteps is how many workflow run step scripts run now (set by
	// the API with EnvironmentBuilds, AddHolds); the restart waits for them
	// until a forced drain's deadline, then goes ahead over them (they run
	// again after the restart, their interrupted attempt in their history).
	WorkflowSteps int `json:"workflow_steps"`
}

// AddHolds adds what the API counts beside agent runs: environment builds
// (the restart always waits for them) and workflow run step scripts (the
// restart waits for them until a forced drain is Forced). Ready and Reason
// are recomputed, so the reason names what the drain waits on.
func (s *DrainStatus) AddHolds(builds, steps int) {
	s.EnvironmentBuilds, s.WorkflowSteps = builds, steps
	if builds > 0 {
		s.Ready = false
	}
	if steps > 0 && !s.Forced {
		s.Ready = false
	}
	s.Reason = drainReason(*s)
}

// DrainOptions are what a drain request asks for.
type DrainOptions struct {
	// Timeout is the most the drain waits for running runs (default
	// DefaultDrainTimeout).
	Timeout time.Duration
	// Requested marks an update someone asked for (DrainStatus.Requested).
	Requested bool
	// Force restarts over runs still running at the deadline
	// (DrainStatus.Force).
	Force bool
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
	return m.BeginDrainWith(DrainOptions{Timeout: timeout}, allow)
}

// BeginDrainWith is BeginDrainIf with the request's options. A drain already
// in progress is returned as is, except that a request marks it Requested
// and a forced request makes it Force (never the other way): a later
// request can widen what an earlier one asked, never undo it.
func (m *Manager) BeginDrainWith(opts DrainOptions, allow func(held, queued []string) error) (DrainStatus, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	if m.closed {
		return DrainStatus{}, ErrClosed
	}
	if m.drain.Draining {
		if opts.Requested && !m.drain.Requested {
			m.drain.Requested = true
			m.signalChangedLocked()
		}
		if opts.Force && !m.drain.Force {
			m.drain.Force = true
			if !m.drain.Reserved && !time.Now().Before(m.drain.Deadline) {
				// Past the deadline before the timer ended the drain:
				// forced now, and the timer ends it unclaimed after the
				// grace instead of refusing it.
				m.drain.Forced = true
				m.drainTimer.Reset(time.Until(m.drain.Deadline.Add(m.forcedDrainGrace)))
			}
			m.signalChangedLocked()
		}
		return m.drainStatusLocked(), nil
	}
	if allow != nil {
		if err := allow(m.heldSessionsLocked(), m.queuedSessionsLocked()); err != nil {
			return DrainStatus{}, err
		}
	}
	timeout := opts.Timeout
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
	m.drain = DrainStatus{Generation: m.generation, Draining: true, Since: now, Deadline: now.Add(timeout), Requested: opts.Requested, Force: opts.Force}
	m.drainTimer = time.AfterFunc(timeout, func() {
		m.mu.Lock()
		defer m.mu.Unlock()
		if m.drain.Since != now || !m.drain.Draining || m.drain.Reserved {
			return
		}
		if m.drain.Force && !m.drain.Forced {
			// Forced: ready over the runs still running, which the
			// restart records interrupted. New runs stay held until
			// the installer claims the restart, at most the grace;
			// unclaimed then, the drain ends (expired).
			m.drain.Forced = true
			m.drainTimer.Reset(time.Until(m.drain.Deadline.Add(m.forcedDrainGrace)))
			m.signalChangedLocked()
			return
		}
		m.endDrainLocked("expired")
	})
	m.signalChangedLocked()
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
		out.Reason = drainReason(out)
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
	if out.Draining && out.Force && !out.Reserved && !time.Now().Before(out.Deadline) {
		// The timer may not have fired yet; the deadline decides.
		out.Forced = true
	}
	// A forced drain not claimed within the grace after its deadline is
	// timed out (and no longer claimable) even before its timer ends it.
	unclaimed := out.Draining && out.Forced && !out.Reserved && !time.Now().Before(out.Deadline.Add(m.forcedDrainGrace))
	out.TimedOut = out.EndReason == "expired" || unclaimed || (out.Draining && !out.Reserved && !out.Forced && !time.Now().Before(out.Deadline))
	out.Ready = out.Draining && !out.TimedOut && !m.closed && (len(out.Blockers) == 0 || out.Forced)
	out.Reason = drainReason(out)
	return out
}

// drainReason is the drain's state in one line, naming the runs it waits on.
func drainReason(s DrainStatus) string {
	runs := func() string {
		if len(s.Blockers) == 0 {
			return "no agent run"
		}
		parts := make([]string, 0, len(s.Blockers))
		for i, b := range s.Blockers {
			if i == 5 {
				parts = append(parts, fmt.Sprintf("and %d more", len(s.Blockers)-5))
				break
			}
			parts = append(parts, b.Session+"/"+b.Run)
		}
		return fmt.Sprintf("%d agent run(s) (%s)", len(s.Blockers), strings.Join(parts, ", "))
	}
	// waits is everything a drain not ready waits on.
	waits := func() string {
		parts := []string{}
		if len(s.Blockers) > 0 {
			parts = append(parts, runs())
		}
		if s.WorkflowSteps > 0 {
			parts = append(parts, fmt.Sprintf("%d workflow step script(s)", s.WorkflowSteps))
		}
		if s.EnvironmentBuilds > 0 {
			parts = append(parts, fmt.Sprintf("%d environment build(s)", s.EnvironmentBuilds))
		}
		if len(parts) == 0 {
			return runs()
		}
		return strings.Join(parts, " and ")
	}
	switch {
	case s.EndReason == "expired" && !s.Forced && len(s.Blockers) == 0:
		return "the update was refused: no agent run held it, but the restart was not taken by the deadline (workflow step scripts or environment builds still ran, or no installer claimed it), so nothing was interrupted and new runs start again"
	case s.EndReason == "expired" && s.Forced:
		return "the forced update was not taken: no installer claimed the restart within the grace after the deadline (automatic updates off, the installer not running, or its build failed), so nothing was interrupted and new runs start again"
	case s.EndReason == "expired":
		return "the update was refused: " + runs() + " still running at the deadline would not resume after a restart, so none was interrupted and new runs start again; request_update with force restarts over them"
	case s.EndReason == "cancelled" && !s.Draining:
		return "the drain was cancelled; new runs start again"
	case !s.Draining:
		return ""
	case s.Forced:
		reason := "forced: the deadline passed; "
		if s.EnvironmentBuilds > 0 {
			reason += fmt.Sprintf("waiting for %d environment build(s) to finish, then ", s.EnvironmentBuilds)
		}
		reason += "the restart interrupts " + runs()
		if s.WorkflowSteps > 0 {
			reason += fmt.Sprintf(" and %d workflow step script(s) (their steps run again after the restart, or are reported interrupted)", s.WorkflowSteps)
		}
		return reason
	case s.Ready:
		return "ready: no agent run is running; new runs are held until the restart"
	}
	return "new runs are held; waiting for " + waits() + " to finish"
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
	current := m.drainStatusLocked()
	if generation != m.generation || len(claim) < 32 || !current.Ready || m.drain.Reserved {
		return DrainStatus{}, errors.New("drain not ready for restart reservation")
	}
	// A forced drain stays forced once reserved: the deadline no longer
	// decides after the installer owns the restart.
	m.drain.Forced = current.Forced
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
