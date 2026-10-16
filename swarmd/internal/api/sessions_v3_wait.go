package api

import (
	"encoding/json"
	"errors"
	"net/http"
	"strconv"
	"strings"
	"sync"
	"time"

	"swarm/packages/swarmd/internal/identity"
	runruntime "swarm/packages/swarmd/internal/run"
	sessionruntime "swarm/packages/swarmd/internal/session"
	pebblestore "swarm/packages/swarmd/internal/store/pebble"
)

// Waiting instead of polling: a gateway serving a sealed agent waits on one
// conversation until something it must act on happens.
//
//	GET /v3/sessions/{id}/wait?after=<cursor>&wait_seconds=<0..30>
//
// The cursor is the session's durable event position. Without after, the
// answer is immediate (the current state and cursor). With after, the request
// returns as soon as an event after the cursor is a new assistant message, a
// pending tool call (including the call a confirmation's approval creates), a
// confirmation waiting for the person, the end of a run or the end of the
// session, or when the wait is over. Other events (streaming deltas, usage,
// phases) move the cursor without ending the wait. Every answer carries the
// session's current run, pending calls, pending confirmations, end user and
// labels, and ended state, read from durable records; the in-memory wake only
// shortens the wait.

const (
	sessionWaitMaxSeconds = 30
	sessionWaitScanPage   = 500
	// sessionWaitMaxScan bounds the events one request examines; past it the
	// request answers with the cursor it reached (more true) instead of blocking.
	sessionWaitMaxScan = 5_000
	// scopedTokenMaxLongPolls bounds the long-polls (session waits and agent
	// event feeds) one scoped token may hold open at once.
	scopedTokenMaxLongPolls = 32
	sessionWaitPendingLimit = 100
)

// Reasons a wait returned for.
const (
	sessionWaitAssistantMessage = "assistant_message"
	sessionWaitToolCall         = "tool_call"
	sessionWaitRunEnded         = "run_ended"
	sessionWaitSessionEnded     = "session_ended"
	sessionWaitConfirmation     = "confirmation"
)

// longPollLimiter counts the long-polls each scoped token holds open.
type longPollLimiter struct {
	mu     sync.Mutex
	active map[string]int
}

var scopedTokenLongPolls = &longPollLimiter{active: map[string]int{}}

func (l *longPollLimiter) acquire(key string, limit int) (func(), bool) {
	l.mu.Lock()
	defer l.mu.Unlock()
	if l.active[key] >= limit {
		return nil, false
	}
	l.active[key]++
	released := false
	return func() {
		l.mu.Lock()
		defer l.mu.Unlock()
		if released {
			return
		}
		released = true
		if l.active[key]--; l.active[key] <= 0 {
			delete(l.active, key)
		}
	}, true
}

// acquireLongPoll takes a long-poll slot for the request's scoped token, or
// writes 429 and returns false. The owner (no scoped token) is not limited.
func acquireLongPoll(w http.ResponseWriter, r *http.Request) (func(), bool) {
	rec, ok := ScopedTokenFromRequest(r)
	if !ok || rec == nil || strings.TrimSpace(rec.ID) == "" {
		return func() {}, true
	}
	release, ok := scopedTokenLongPolls.acquire(rec.ID, scopedTokenMaxLongPolls)
	if !ok {
		w.Header().Set("Retry-After", "1")
		writeError(w, http.StatusTooManyRequests, errors.New("too many open waits for this token"))
		return nil, false
	}
	return release, true
}

// parseLongPollSeconds reads a wait in seconds (0..max); empty is 0.
func parseLongPollSeconds(raw, name string, max int) (int, error) {
	raw = strings.TrimSpace(raw)
	if raw == "" {
		return 0, nil
	}
	parsed, err := strconv.Atoi(raw)
	if err != nil || parsed < 0 || parsed > max {
		return 0, errors.New(name + " must be a number of seconds from 0 to " + strconv.Itoa(max))
	}
	return parsed, nil
}

// sessionWaitEventPayload is the part of a V3 event payload a wait classifies.
type sessionWaitEventPayload struct {
	Role    string `json:"role"`
	Change  string `json:"change"`
	Message *struct {
		Role string `json:"role"`
	} `json:"message"`
	RunIntent *struct {
		Status string `json:"status"`
	} `json:"run_intent"`
	// Confirmation is set on the writes of runruntime.PutClientToolConfirmation.
	Confirmation *struct {
		Status                string `json:"status"`
		ExecutionPermissionID string `json:"execution_permission_id"`
		Delivered             bool   `json:"delivered"`
	} `json:"client_tool_confirmation"`
}

func sessionRunIntentTerminal(status string) bool {
	switch strings.TrimSpace(status) {
	case sessionruntime.RunIntentCompleted, sessionruntime.RunIntentFailed, sessionruntime.RunIntentCancelled,
		sessionruntime.RunIntentExpired, sessionruntime.RunIntentInterrupted, sessionruntime.RunIntentDispatchBlocked:
		return true
	}
	return false
}

// sessionWaitReasons reports what in event a gateway must act on.
func sessionWaitReasons(event pebblestore.V3SessionEvent) []string {
	eventType := strings.TrimSpace(event.EventType)
	if eventType == "session.assistant.delta" {
		return nil
	}
	var reasons []string
	if eventType == "permission.requested" {
		reasons = append(reasons, sessionWaitToolCall)
	}
	var payload sessionWaitEventPayload
	if len(event.Payload) > 0 && json.Unmarshal(event.Payload, &payload) == nil {
		role := payload.Role
		if payload.Message != nil && payload.Message.Role != "" {
			role = payload.Message.Role
		}
		if strings.EqualFold(strings.TrimSpace(role), "assistant") {
			reasons = append(reasons, sessionWaitAssistantMessage)
		}
		if payload.RunIntent != nil && sessionRunIntentTerminal(payload.RunIntent.Status) {
			reasons = append(reasons, sessionWaitRunEnded)
		}
		if payload.Change == sessionChangeEnded {
			reasons = append(reasons, sessionWaitSessionEnded)
		}
		if c := payload.Confirmation; c != nil && !c.Delivered {
			switch {
			case c.Status == runruntime.ClientToolConfirmationPending:
				reasons = append(reasons, sessionWaitConfirmation)
			case c.Status == runruntime.ClientToolConfirmationApproved && c.ExecutionPermissionID != "":
				// The approval created the call the gateway now runs; it is
				// recorded outside any run, so no permission.requested event.
				reasons = append(reasons, sessionWaitToolCall)
			}
		}
	}
	return reasons
}

func (s *Server) handleSessionV3Wait(w http.ResponseWriter, r *http.Request, principal identity.Principal, sessionID string) {
	if r.Method != http.MethodGet {
		methodNotAllowed(w)
		return
	}
	query := r.URL.Query()
	rawAfter := strings.TrimSpace(query.Get("after"))
	var after uint64
	if rawAfter != "" {
		parsed, err := strconv.ParseUint(rawAfter, 10, 64)
		if err != nil {
			writeError(w, http.StatusBadRequest, errors.New("after must be a cursor from a previous wait"))
			return
		}
		after = parsed
	}
	waitSeconds, err := parseLongPollSeconds(query.Get("wait_seconds"), "wait_seconds", sessionWaitMaxSeconds)
	if err != nil {
		writeError(w, http.StatusBadRequest, err)
		return
	}
	if found, err := s.authorizeSessionsV3PrimarySession(principal, sessionID); err != nil {
		writeError(w, http.StatusBadRequest, err)
		return
	} else if !found {
		writeSessionNotFound(w)
		return
	}
	if waitSeconds > 0 && rawAfter != "" {
		release, ok := acquireLongPoll(w, r)
		if !ok {
			return
		}
		defer release()
	}
	var deadline <-chan time.Time
	if waitSeconds > 0 && rawAfter != "" {
		timer := time.NewTimer(time.Duration(waitSeconds) * time.Second)
		defer timer.Stop()
		deadline = timer.C
	}
	reasons := []string{}
	staleCursor := false
	more := false
	scanned := 0
	for {
		changed, release := s.sessionChanges.watch(sessionChangeKey(sessionID))
		projection, ok, err := s.sessions.GetSessionProjection(sessionID)
		if err != nil {
			release()
			writeError(w, http.StatusInternalServerError, err)
			return
		}
		latest := uint64(0)
		if ok {
			latest = projection.LastEventSeq
		}
		if rawAfter == "" {
			after = latest
		} else if after > latest {
			// A cursor from another session or a recreated store never
			// silently resumes: answer now with where the session is.
			after, staleCursor = latest, true
		}
		for after < latest && len(reasons) == 0 && scanned < sessionWaitMaxScan {
			events, err := s.sessions.ListSessionEvents(sessionID, after, sessionWaitScanPage)
			if err != nil {
				release()
				writeError(w, http.StatusInternalServerError, err)
				return
			}
			if len(events) == 0 {
				break
			}
			for _, event := range events {
				scanned++
				after = event.Seq
				if found := sessionWaitReasons(event); len(found) > 0 {
					reasons = found
					break
				}
			}
		}
		more = after < latest
		if deadline == nil || len(reasons) > 0 || staleCursor || more {
			release()
			break
		}
		select {
		case <-changed:
			release()
			continue
		case <-deadline:
			release()
		case <-r.Context().Done():
			release()
			return
		}
		break
	}
	// Close a confirmation that expired, or whose approved call timed out,
	// while no timer ran (after a restart), as the confirmations read does.
	if _, _, err := s.advanceClientToolConfirmation(sessionID, true); err != nil {
		writeError(w, http.StatusInternalServerError, err)
		return
	}
	session, found, err := s.sessions.GetSession(sessionID)
	if err != nil {
		writeError(w, http.StatusInternalServerError, err)
		return
	}
	if !found {
		writeSessionNotFound(w)
		return
	}
	response := map[string]any{
		"ok": true, "session_id": sessionID, "cursor": after, "reasons": reasons,
		"timed_out": len(reasons) == 0 && !staleCursor && !more && rawAfter != "" && waitSeconds > 0,
		"ended":     sessionEnded(session),
	}
	if staleCursor {
		response["stale_cursor"] = true
	}
	if more {
		response["more"] = true
	}
	if ended := sessionEndedState(session); ended != nil {
		response["ended_state"] = ended
	}
	if state, ok, err := s.sessions.GetSessionRunState(sessionID); err != nil {
		writeError(w, http.StatusInternalServerError, err)
		return
	} else if ok {
		response["run"] = map[string]any{"run_id": state.RunID, "status": state.Status, "active": state.Active}
	}
	pending := []pebblestore.PermissionRecord{}
	if s.perm != nil {
		records, err := s.perm.ListPending(sessionID, sessionWaitPendingLimit)
		if err != nil {
			writeError(w, http.StatusInternalServerError, err)
			return
		}
		pending = append(pending, records...)
	}
	response["pending_calls"] = pending
	confirmations := []runruntime.ClientToolConfirmation{}
	if c := pendingClientToolConfirmationView(session, time.Now().UnixMilli()); c != nil {
		confirmations = append(confirmations, *c)
	}
	response["pending_confirmations"] = confirmations
	// Whose conversation this is, from the stored binding, as on the
	// permissions delivery.
	if err := addSessionEndUserFields(response, session.Metadata); err != nil {
		writeError(w, http.StatusInternalServerError, err)
		return
	}
	writeJSON(w, http.StatusOK, response)
}
