package api

import (
	"errors"
	"log"
	"net/http"
	"strings"
	"sync"
	"time"

	"swarm/packages/swarmd/internal/identity"
	runruntime "swarm/packages/swarmd/internal/run"
	pebblestore "swarm/packages/swarmd/internal/store/pebble"
)

// CompanionStatePath is the account user's materialized Companion state: the
// focus, unresolved commitments, topic and decision references, followed work
// and recent results, each naming the exact event of the Companion's durable
// V3 log it came from, with the maintenance cursor and how many events are
// still pending after it. It is read only; maintenance follows the log on
// its own (a run start, or a commit nudge for the Companion session).
//
// The route, the Companion's native manage_companion state action and Swarm
// Control's manage_companion state all run companionStateRead.
const CompanionStatePath = "/v3/companion/state"

// CompanionStateView is the state read. State is nil before the Companion's
// first maintenance. Pending is the number of committed events of the
// Companion session after the state's cursor (LatestSeq minus the cursor):
// nonzero means the state does not yet reflect them.
type CompanionStateView struct {
	SessionID string                      `json:"session_id"`
	State     *pebblestore.CompanionState `json:"state"`
	Focus     *pebblestore.CompanionFocus `json:"focus,omitempty"`
	LatestSeq uint64                      `json:"latest_seq"`
	Pending   uint64                      `json:"pending"`
	// Segments are the newest sealed continuity segments (at most
	// pebblestore.CompanionSegmentMaxRead), their items re-checked now
	// against their records and source messages. The open draft is
	// State.Continuity.
	Segments    []pebblestore.CompanionSegmentView `json:"segments"`
	Maintenance CompanionMaintenanceView           `json:"maintenance"`
	ReadAt      int64                              `json:"read_at"`
}

// CompanionMaintenanceView is how fresh the maintained context is: current
// when no committed event is pending, else pending; whether a background
// pass is running for the Companion; and the last background failure since
// this daemon started (cleared by the next successful pass). Maintenance
// makes no model calls.
type CompanionMaintenanceView struct {
	Freshness   string                       `json:"freshness"`
	Running     bool                         `json:"running"`
	LastFailure *companionMaintenanceFailure `json:"last_failure,omitempty"`
}

type companionMaintenanceFailure struct {
	At    int64  `json:"at"`
	Error string `json:"error"`
}

func (s *Server) handleCompanionState(w http.ResponseWriter, r *http.Request) {
	principal, ok := PrincipalFromRequest(r)
	if !ok || !principal.Valid() {
		writeError(w, http.StatusUnauthorized, identity.ErrPrincipalRequired)
		return
	}
	if r.URL.Path != CompanionStatePath {
		http.NotFound(w, r)
		return
	}
	if r.Method != http.MethodGet {
		methodNotAllowed(w)
		return
	}
	if !s.requireScope(w, r, "sessions:read") {
		return
	}
	if s.sessions == nil || s.sessions.Store() == nil {
		writeError(w, http.StatusServiceUnavailable, errors.New("session store unavailable"))
		return
	}
	answer, err := s.companionStateRead(principal)
	if err != nil {
		writeCompanionContextError(w, err)
		return
	}
	writeJSON(w, http.StatusOK, answer)
}

// companionStateRead is the one read of the account user's Companion state.
// It reads the identity, the state record, the focus and the session's
// projection head (four point reads) and the newest sealed continuity
// segments with their items' records and sources (bounded by
// pebblestore.CompanionSegmentMaxRead), whatever the history size. A state left
// by a deleted generation is not this Companion's and reads as none.
func (s *Server) companionStateRead(principal identity.Principal) (map[string]any, error) {
	store := s.sessions.Store()
	record, found, err := store.GetCompanion(principal.AccountScopeID, principal.UserID)
	if err != nil {
		return nil, err
	}
	if !found {
		return nil, companionContextRefusal(http.StatusNotFound, "you have no Companion yet; open it with POST %s", CompanionPath)
	}
	view := CompanionStateView{SessionID: record.SessionID, ReadAt: time.Now().UnixMilli()}
	state, stored, err := store.GetCompanionState(principal.AccountScopeID, principal.UserID)
	if err != nil {
		return nil, err
	}
	cursor := uint64(0)
	if stored && state.SessionID == record.SessionID && state.Generation == record.Generation {
		view.State = &state
		cursor = state.Cursor.EventSeq
		if focus, ok, err := store.GetCompanionFocus(principal.AccountScopeID, principal.UserID); err != nil {
			return nil, err
		} else if ok {
			view.Focus = &focus
		}
	}
	projection, ok, err := store.GetV3SessionProjection(record.SessionID)
	if err != nil {
		return nil, err
	}
	view.LatestSeq = cursor
	if ok && projection.LastEventSeq > cursor {
		view.LatestSeq = projection.LastEventSeq
	}
	view.Pending = view.LatestSeq - cursor
	view.Segments = []pebblestore.CompanionSegmentView{}
	if view.State != nil {
		segments, _, err := store.ListCompanionSegments(principal.AccountScopeID, principal.UserID, record.SessionID, pebblestore.CompanionSegmentMaxRead)
		if err != nil {
			return nil, err
		}
		view.Segments = segments
	}
	view.Maintenance = s.companionMaintenanceStatus(record.SessionID, view.Pending)
	return map[string]any{"ok": true, "companion_state": view}, nil
}

func (s *Server) companionMaintenanceStatus(sessionID string, pending uint64) CompanionMaintenanceView {
	view := CompanionMaintenanceView{Freshness: "current"}
	if pending > 0 {
		view.Freshness = "pending"
	}
	m := &s.companionMaintenance
	m.mu.Lock()
	defer m.mu.Unlock()
	view.Running = m.running[sessionID]
	if failure, ok := m.failures[sessionID]; ok {
		copied := failure
		view.LastFailure = &copied
	}
	return view
}

// companionMaintainer folds committed Companion events into the Companion
// state in the background. A commit nudge for a Companion session starts one
// pass for it, or marks a running pass to go again when it finishes, so
// nudges coalesce on completion (no timer, no polling) and one session is
// never maintained by two passes at once. Each maintenance call is one page
// (CompanionMaintainBackgroundEvents), releasing the principal's lock between
// pages so a run start never waits behind a long catch-up; a longer backlog
// runs further calls until the log is consumed.
type companionMaintainer struct {
	mu      sync.Mutex
	idle    *sync.Cond // broadcast when a session's passes end
	running map[string]bool
	again   map[string]bool
	// failures is the last failed background pass per session since the
	// daemon started; a successful pass clears it.
	failures map[string]companionMaintenanceFailure
}

// ConfigureCompanionMaintenance makes every durable mutation of a Companion
// session nudge its state maintenance.
func (s *Server) ConfigureCompanionMaintenance(store *pebblestore.Store) {
	if s == nil || store == nil {
		return
	}
	pebblestore.NewSessionStore(store).SetSessionCommitObserver(func(sessionID string, _ uint64) {
		s.nudgeCompanionMaintenance(sessionID)
	})
}

// companionSessionIDPrefix is the prefix of every Companion session ID
// (pebblestore.CompanionSessionID). It only skips other sessions cheaply;
// the maintainer itself checks the session is the owner's current Companion.
const companionSessionIDPrefix = "companion_"

func (s *Server) nudgeCompanionMaintenance(sessionID string) {
	sessionID = strings.TrimSpace(sessionID)
	if s == nil || !strings.HasPrefix(sessionID, companionSessionIDPrefix) {
		return
	}
	m := &s.companionMaintenance
	m.mu.Lock()
	defer m.mu.Unlock()
	if m.running == nil {
		m.running, m.again, m.idle = map[string]bool{}, map[string]bool{}, sync.NewCond(&m.mu)
	}
	if m.running[sessionID] {
		m.again[sessionID] = true
		return
	}
	// A pass is in-flight work: the daemon's shutdown drain waits for it
	// before the store closes, and none starts once shutdown began (the
	// durable cursor resumes it after the restart).
	s.activeRunMu.Lock()
	if s.shuttingDown.Load() {
		s.activeRunMu.Unlock()
		return
	}
	s.runWG.Add(1)
	s.activeRunMu.Unlock()
	m.running[sessionID] = true
	go s.runCompanionMaintenance(sessionID)
}

func (s *Server) runCompanionMaintenance(sessionID string) {
	m := &s.companionMaintenance
	defer s.runWG.Done()
	for {
		pending := !s.shuttingDown.Load() && s.companionMaintenancePass(sessionID)
		m.mu.Lock()
		if (!m.again[sessionID] || s.shuttingDown.Load()) && !pending {
			delete(m.running, sessionID)
			m.idle.Broadcast()
			m.mu.Unlock()
			return
		}
		delete(m.again, sessionID)
		m.mu.Unlock()
	}
}

// companionMaintenancePass runs one bounded pass and reports whether events
// are still pending. A session that is not (or no longer) the owner's
// current Companion, or a state another writer moved, ends quietly: the next
// nudge or run start continues from the durable cursor.
func (s *Server) companionMaintenancePass(sessionID string) bool {
	if s.sessions == nil || s.sessions.Store() == nil {
		return false
	}
	session, found, err := s.sessions.GetSession(sessionID)
	if err != nil || !found || !runruntime.IsCompanionSession(session) {
		return false
	}
	result, err := runruntime.MaintainCompanionState(s.sessions.Store(), session, runruntime.CompanionMaintainBackgroundEvents, time.Now().UnixMilli())
	switch {
	case err == nil:
		s.recordCompanionMaintenanceFailure(sessionID, nil)
		return result.Pending > 0 && result.Consumed > 0
	case errors.Is(err, runruntime.ErrCompanionNotCurrent), errors.Is(err, pebblestore.ErrCompanionStateConflict):
		return false
	default:
		log.Printf("warning: companion state maintenance session=%q: %v", sessionID, err)
		s.recordCompanionMaintenanceFailure(sessionID, err)
		return false
	}
}

// recordCompanionMaintenanceFailure keeps (err non-nil) or clears the last
// background failure of a session, bounded to one entry per session.
func (s *Server) recordCompanionMaintenanceFailure(sessionID string, err error) {
	m := &s.companionMaintenance
	m.mu.Lock()
	defer m.mu.Unlock()
	if err == nil {
		delete(m.failures, sessionID)
		return
	}
	if m.failures == nil {
		m.failures = map[string]companionMaintenanceFailure{}
	}
	message := err.Error()
	if len(message) > 400 {
		message = message[:400]
	}
	m.failures[sessionID] = companionMaintenanceFailure{At: time.Now().UnixMilli(), Error: message}
}
