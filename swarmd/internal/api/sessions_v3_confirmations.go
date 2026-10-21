package api

import (
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"log"
	"net/http"
	"strings"
	"sync"
	"time"
	"unicode/utf8"

	"swarm/packages/swarmd/internal/identity"
	"swarm/packages/swarmd/internal/permission"
	runruntime "swarm/packages/swarmd/internal/run"
	sessionruntime "swarm/packages/swarmd/internal/session"
	pebblestore "swarm/packages/swarmd/internal/store/pebble"
)

// Confirmations of client tool calls (see run/client_tool_confirmation.go for
// how one is recorded). The routes, all scoped to one session:
//
//	GET  /v3/sessions/{id}/confirmations        the pending confirmation, if any
//	GET  /v3/sessions/{id}/confirmations/{cid}  one confirmation in any state
//	POST /v3/sessions/{id}/confirmations/{cid}  {"decision":"approve"|"reject","note"?,"arguments_hash"}
//
// Approving needs the arguments_hash the person was shown, so an approval can
// only ever run the exact arguments on the card. Approval creates an ordinary
// pending client tool call with those arguments; the gateway answers it like
// any other call. Rejection, expiry and the answered call each append the
// outcome as a tool result and start a run so the model can react.
//
// Expiry is enforced by an in-process timer and, because timers do not survive
// a restart, again whenever the session's confirmations are read or decided,
// a client tool call in it is answered, or a message is posted.

const clientToolConfirmationNoteMax = 500

type sessionsV3ConfirmationDecisionRequest struct {
	Decision      string `json:"decision"`
	Note          string `json:"note,omitempty"`
	ArgumentsHash string `json:"arguments_hash,omitempty"`
}

func (s *Server) handleSessionV3Confirmations(w http.ResponseWriter, r *http.Request, principal identity.Principal, sessionID, confirmationID string) {
	confirmationID = strings.Trim(confirmationID, "/")
	if strings.Contains(confirmationID, "/") {
		writeError(w, http.StatusBadRequest, errors.New("invalid confirmation path"))
		return
	}
	switch {
	case r.Method == http.MethodGet:
		if found, err := s.authorizeReadableSessionV3Access(principal, sessionID); err != nil {
			writeError(w, http.StatusBadRequest, err)
			return
		} else if !found {
			writeSessionNotFound(w)
			return
		}
		current, ok, err := s.advanceClientToolConfirmation(sessionID, true)
		if err != nil {
			writeError(w, http.StatusInternalServerError, err)
			return
		}
		if confirmationID == "" {
			pending := []runruntime.ClientToolConfirmation{}
			if ok && current.Status == runruntime.ClientToolConfirmationPending {
				pending = append(pending, current)
			}
			s.writeConfirmationResponse(w, sessionID, map[string]any{"ok": true, "session_id": sessionID, "confirmations": pending})
			return
		}
		if !ok || current.ID != confirmationID {
			writeError(w, http.StatusNotFound, errors.New("confirmation not found"))
			return
		}
		s.writeConfirmationResponse(w, sessionID, map[string]any{"ok": true, "session_id": sessionID, "confirmation": current})
	case r.Method == http.MethodPost && confirmationID != "":
		// A decision is the person's, relayed by the gateway: only the owner or
		// a gateway token decides (its gate has matched it to this session's
		// agent), never an SDK, client app or operator key.
		if rec, scoped := ScopedTokenFromRequest(r); scoped && (strings.TrimSpace(rec.AgentName) == "" || rec.AgentOperator) {
			writeError(w, http.StatusForbidden, errors.New("only the owner or the agent's gateway token can decide a confirmation"))
			return
		}
		if found, err := s.authorizeSessionsV3PrimarySession(principal, sessionID); err != nil {
			writeError(w, http.StatusBadRequest, err)
			return
		} else if !found {
			writeSessionNotFound(w)
			return
		}
		var req sessionsV3ConfirmationDecisionRequest
		if err := decodeJSON(r, &req); err != nil {
			writeError(w, http.StatusBadRequest, err)
			return
		}
		decided, status, err := s.decideClientToolConfirmation(sessionID, confirmationID, req)
		if err != nil {
			writeError(w, status, err)
			return
		}
		s.writeConfirmationResponse(w, sessionID, map[string]any{"ok": true, "session_id": sessionID, "confirmation": decided})
	default:
		methodNotAllowed(w)
	}
}

// writeConfirmationResponse adds the session's end user and labels, from the
// stored binding (as on the permissions delivery), so the gateway shows the
// card to, and runs the approved call for, the visitor who owns the session.
func (s *Server) writeConfirmationResponse(w http.ResponseWriter, sessionID string, response map[string]any) {
	session, found, err := s.sessions.GetSession(sessionID)
	if err != nil || !found {
		writeError(w, http.StatusInternalServerError, errors.New("session not readable"))
		return
	}
	if err := addSessionEndUserFields(response, session.Metadata); err != nil {
		writeError(w, http.StatusInternalServerError, err)
		return
	}
	writeJSON(w, http.StatusOK, response)
}

// decideClientToolConfirmation records the person's decision and moves the
// confirmation on. Repeating the decision already recorded returns the current
// state; a different decision, or one after expiry, is a conflict.
func (s *Server) decideClientToolConfirmation(sessionID, confirmationID string, req sessionsV3ConfirmationDecisionRequest) (runruntime.ClientToolConfirmation, int, error) {
	decision := strings.TrimSpace(req.Decision)
	want := map[string]string{"approve": runruntime.ClientToolConfirmationApproved, "reject": runruntime.ClientToolConfirmationRejected}[decision]
	if want == "" {
		return runruntime.ClientToolConfirmation{}, http.StatusBadRequest, errors.New(`decision must be "approve" or "reject"`)
	}
	note := strings.TrimSpace(req.Note)
	if utf8.RuneCountInString(note) > clientToolConfirmationNoteMax {
		return runruntime.ClientToolConfirmation{}, http.StatusBadRequest, errors.New("note is at most 500 characters")
	}
	unlock := runruntime.LockClientToolConfirmations(sessionID)
	defer unlock()
	current, ok, err := s.advanceClientToolConfirmationLocked(sessionID, true)
	if err != nil {
		return runruntime.ClientToolConfirmation{}, http.StatusInternalServerError, err
	}
	if !ok || current.ID != confirmationID {
		return runruntime.ClientToolConfirmation{}, http.StatusNotFound, errors.New("confirmation not found")
	}
	session, found, err := s.sessions.GetSession(sessionID)
	if err != nil || !found {
		return runruntime.ClientToolConfirmation{}, http.StatusNotFound, errors.New("session not found")
	}
	if sessionEnded(session) {
		// Ending closed the confirmation; nothing may run or start a run now.
		return runruntime.ClientToolConfirmation{}, http.StatusConflict, errSessionV3Ended
	}
	if current.Status != runruntime.ClientToolConfirmationPending {
		if current.Status == want {
			return current, http.StatusOK, nil
		}
		return runruntime.ClientToolConfirmation{}, http.StatusConflict, errors.New("confirmation is already " + current.Status)
	}
	if want == runruntime.ClientToolConfirmationApproved {
		if strings.TrimSpace(req.ArgumentsHash) == "" {
			return runruntime.ClientToolConfirmation{}, http.StatusBadRequest, errors.New("arguments_hash is required to approve: send the hash of the arguments the person was shown")
		}
		if strings.TrimSpace(req.ArgumentsHash) != current.ArgumentsHash {
			return runruntime.ClientToolConfirmation{}, http.StatusConflict, errors.New("arguments_hash does not match this confirmation's arguments")
		}
		if _, err := current.VerifyArguments(); err != nil {
			return runruntime.ClientToolConfirmation{}, http.StatusConflict, err
		}
	}
	now := time.Now().UnixMilli()
	current.Status = want
	current.DecidedAt = now
	current.Note = note
	if want == runruntime.ClientToolConfirmationApproved {
		definition, _ := s.confirmedClientToolDefinition(session, current.Tool)
		current.ExecutionDeadline = now + runruntime.ClientToolTimeout(definition).Milliseconds()
	}
	if err := runruntime.PutClientToolConfirmation(s.applySessionV3PrimaryMutation, session, current, now); err != nil {
		return runruntime.ClientToolConfirmation{}, http.StatusInternalServerError, err
	}
	next, _, err := s.advanceClientToolConfirmationLocked(sessionID, true)
	if err != nil {
		return runruntime.ClientToolConfirmation{}, http.StatusInternalServerError, err
	}
	return next, http.StatusOK, nil
}

// advanceClientToolConfirmation moves a session's open confirmation forward
// as far as time and the client allow (expire it, create the approved call,
// time that call out, deliver the outcome) and returns its current state.
// startRun false delivers an outcome without starting a run, for a caller
// that is about to start one anyway.
func (s *Server) advanceClientToolConfirmation(sessionID string, startRun bool) (runruntime.ClientToolConfirmation, bool, error) {
	unlock := runruntime.LockClientToolConfirmations(sessionID)
	defer unlock()
	return s.advanceClientToolConfirmationLocked(sessionID, startRun)
}

func (s *Server) advanceClientToolConfirmationLocked(sessionID string, startRun bool) (runruntime.ClientToolConfirmation, bool, error) {
	if s == nil || s.sessions == nil {
		return runruntime.ClientToolConfirmation{}, false, errors.New("sessions v3 service is not configured")
	}
	session, found, err := s.sessions.GetSession(sessionID)
	if err != nil || !found {
		return runruntime.ClientToolConfirmation{}, false, err
	}
	c, ok := runruntime.ClientToolConfirmationFromSession(session)
	if !ok || !c.Open() {
		return c, ok, nil
	}
	now := time.Now().UnixMilli()
	if c.Status == runruntime.ClientToolConfirmationPending {
		if now < c.ExpiresAt {
			s.armClientToolConfirmationTimer(sessionID, c.ExpiresAt)
			return c, true, nil
		}
		c.Status = runruntime.ClientToolConfirmationExpired
		c.DecidedAt = now
		if err := runruntime.PutClientToolConfirmation(s.applySessionV3PrimaryMutation, session, c, now); err != nil {
			return c, true, err
		}
	}
	definition, available := s.confirmedClientToolDefinition(session, c.Tool)
	var execution *pebblestore.PermissionRecord
	if c.Status == runruntime.ClientToolConfirmationApproved && available {
		record, created, err := s.clientToolConfirmationExecution(session, &c, now)
		if err != nil {
			return c, true, err
		}
		if created {
			s.armClientToolConfirmationTimer(sessionID, c.ExecutionDeadline)
			return c, true, nil
		}
		if strings.EqualFold(record.Status, pebblestore.PermissionStatusPending) {
			if now < c.ExecutionDeadline {
				s.armClientToolConfirmationTimer(sessionID, c.ExecutionDeadline)
				return c, true, nil
			}
			// The client did not answer in time: close the call so a late
			// answer cannot land, then report the timeout to the model.
			record, err = s.perm.Resolve(sessionID, record.ID, "deny", "client tool timed out")
			if err != nil {
				return c, true, err
			}
		}
		execution = &record
	}
	delivered, err := s.deliverClientToolConfirmationOutcome(session, c, definition, execution, startRun)
	if err != nil {
		return c, true, err
	}
	s.stopClientToolConfirmationTimer(sessionID)
	return delivered, true, nil
}

// closeClientToolConfirmationForEndLocked closes the session's open
// confirmation when the conversation ends: a pending one expires, and the
// call an approval created is failed so no gateway can still run it. The
// outcome is recorded without starting a run. The caller holds
// LockClientToolConfirmations.
func (s *Server) closeClientToolConfirmationForEndLocked(sessionID string) error {
	session, found, err := s.sessions.GetSession(sessionID)
	if err != nil || !found {
		return err
	}
	c, ok := runruntime.ClientToolConfirmationFromSession(session)
	if !ok || !c.Open() {
		return nil
	}
	now := time.Now().UnixMilli()
	switch {
	case c.Status == runruntime.ClientToolConfirmationPending:
		c.Status = runruntime.ClientToolConfirmationExpired
		c.DecidedAt = now
		if err := runruntime.PutClientToolConfirmation(s.applySessionV3PrimaryMutation, session, c, now); err != nil {
			return err
		}
	case c.Status == runruntime.ClientToolConfirmationApproved && c.ExecutionPermissionID != "" && s.perm != nil:
		record, found, err := s.perm.GetPermission(sessionID, c.ExecutionPermissionID)
		if err != nil {
			return err
		}
		if found && strings.EqualFold(record.Status, pebblestore.PermissionStatusPending) {
			if _, err := s.perm.Resolve(sessionID, record.ID, "deny", "the conversation was ended"); err != nil {
				return err
			}
		}
	}
	_, _, err = s.advanceClientToolConfirmationLocked(sessionID, false)
	return err
}

// confirmedClientToolDefinition returns the tool a confirmation names, if it
// is still a client tool of the session's account.
func (s *Server) confirmedClientToolDefinition(session pebblestore.SessionSnapshot, name string) (pebblestore.AgentCustomToolDefinition, bool) {
	if s.agents == nil {
		return pebblestore.AgentCustomToolDefinition{}, false
	}
	definition, ok, err := s.agents.GetCustomToolForAccount(session.AccountScopeID, name)
	if err != nil || !ok || definition.Kind != pebblestore.AgentCustomToolKindClient {
		return pebblestore.AgentCustomToolDefinition{}, false
	}
	return definition, true
}

// clientToolConfirmationExecution returns the approved call's pending client
// tool record, creating it (with exactly the confirmed arguments) the first
// time. created reports that it was created now.
func (s *Server) clientToolConfirmationExecution(session pebblestore.SessionSnapshot, c *runruntime.ClientToolConfirmation, now int64) (pebblestore.PermissionRecord, bool, error) {
	if s.perm == nil {
		return pebblestore.PermissionRecord{}, false, errors.New("permission service is not configured")
	}
	if c.ExecutionPermissionID != "" {
		record, ok, err := s.perm.GetPermission(session.ID, c.ExecutionPermissionID)
		if err != nil {
			return pebblestore.PermissionRecord{}, false, err
		}
		if !ok {
			return pebblestore.PermissionRecord{}, false, errors.New("the approved call's record is missing")
		}
		return record, false, nil
	}
	arguments, err := c.VerifyArguments()
	if err != nil {
		return pebblestore.PermissionRecord{}, false, err
	}
	// A record from an earlier attempt that stopped before it was linked is
	// reused, never recreated: recreating would reopen an answered call.
	records, err := s.perm.ListPermissions(session.ID, 1000)
	if err != nil {
		return pebblestore.PermissionRecord{}, false, err
	}
	var record pebblestore.PermissionRecord
	for _, existing := range records {
		if existing.CallID == c.ExecutionCallID && strings.TrimSpace(existing.RunID) == "" {
			record = existing
			break
		}
	}
	if record.ID == "" {
		// No run ID: the record waits on the client, not on a run, so a daemon
		// restart does not cancel it.
		record, err = s.perm.CreatePending(permission.CreateInput{
			SessionID: session.ID, CallID: c.ExecutionCallID, ToolName: c.Tool,
			ToolArguments: arguments, ToolCallArguments: arguments,
			Requirement: permission.RequirementClientTool, Mode: session.Mode,
		})
		if err != nil {
			return pebblestore.PermissionRecord{}, false, err
		}
	}
	c.ExecutionPermissionID = record.ID
	if err := runruntime.PutClientToolConfirmation(s.applySessionV3PrimaryMutation, session, *c, now); err != nil {
		return pebblestore.PermissionRecord{}, false, err
	}
	return record, strings.EqualFold(record.Status, pebblestore.PermissionStatusPending), nil
}

// deliverClientToolConfirmationOutcome appends the outcome as a tool result,
// starts a run (unless startRun is false or a person holds the conversation)
// and marks the confirmation delivered. Every step replays safely.
func (s *Server) deliverClientToolConfirmationOutcome(session pebblestore.SessionSnapshot, c runruntime.ClientToolConfirmation, definition pebblestore.AgentCustomToolDefinition, execution *pebblestore.PermissionRecord, startRun bool) (runruntime.ClientToolConfirmation, error) {
	if execution != nil {
		// Defense in depth: the call that ran must carry the confirmed arguments.
		if arguments, err := c.VerifyArguments(); err != nil || strings.TrimSpace(execution.ToolArguments) != arguments {
			return c, errors.New("the approved call's arguments differ from the confirmed ones")
		}
	}
	output, errText := runruntime.ClientToolConfirmationOutcome(definition, c, execution)
	requestID := "client-tool-confirmation-outcome:" + c.ID
	runID := stableSessionsV3PrimaryRunID(session.ID, requestID)
	content, metadata, err := runruntime.ClientToolConfirmationOutcomeRecord(c, runID, output, errText)
	if err != nil {
		return c, err
	}
	now := time.Now().UnixMilli()
	sum := sha256.Sum256([]byte(session.ID + "\x00" + requestID))
	message := pebblestore.MessageSnapshot{ID: "v3msg_confirmation_" + hex.EncodeToString(sum[:16]), Role: "tool", Content: content, CreatedAt: now, Metadata: metadata}
	var runIntent *pebblestore.V3SessionRunIntent
	if startRun && !sessionEnded(session) {
		status, reason := sessionruntime.RunIntentPendingExecutor, ""
		if sessionTakeoverActive(session) {
			status, reason = sessionruntime.RunIntentDispatchBlocked, sessionTakeoverBlockedReason
		}
		runIntent = &pebblestore.V3SessionRunIntent{RunID: runID, SourceMessageID: message.ID, Status: status, BlockedReason: reason}
	}
	hashSum := sha256.Sum256([]byte(content))
	payloadHash := hex.EncodeToString(hashSum[:])
	result, err := s.applySessionV3PrimaryMutation(sessionruntime.SessionMutationInput{
		SessionID: session.ID, UserID: session.UserID, AccountScopeID: session.AccountScopeID,
		ClientRequestID: requestID, IdempotencyKey: requestID, PayloadHash: payloadHash, RequestHash: payloadHash,
		Kind: sessionruntime.SessionMutationAppendMessage, EventType: "session.message.appended", Message: &message, RunIntent: runIntent, NowUnixMs: now,
	})
	if err != nil {
		return c, err
	}
	c.Delivered = true
	c.DeliveredAt = now
	if result.Message != nil {
		c.OutcomeMessageID = result.Message.ID
	}
	if result.RunIntent != nil {
		c.ContinuationRunID = result.RunIntent.RunID
	}
	latest, found, err := s.sessions.GetSession(session.ID)
	if err != nil || !found {
		return c, errors.New("session not found")
	}
	if err := runruntime.PutClientToolConfirmation(s.applySessionV3PrimaryMutation, latest, c, now); err != nil {
		return c, err
	}
	if result.RunIntent != nil && result.RunIntent.Status == sessionruntime.RunIntentPendingExecutor && s.v3SessionExecutor != nil {
		principal := identity.Principal{Type: identity.PrincipalTypeUser, UserID: session.UserID, AccountScopeID: session.AccountScopeID}
		s.v3SessionExecutor.EnqueueRun(sessionV3ExecutorJob{Principal: principal, SessionID: session.ID, RunID: result.RunIntent.RunID, EpochID: result.RunIntent.EpochID, SourceMessageID: message.ID})
	}
	return c, nil
}

// sessionV3RunAwaitsClientToolConfirmation reports whether job's run just
// asked the person to confirm a call; if so it arms the expiry timer.
func (e *sessionV3Executor) sessionV3RunAwaitsClientToolConfirmation(job sessionV3ExecutorJob) bool {
	if e == nil || e.server == nil || e.server.sessions == nil {
		return false
	}
	session, found, err := e.server.sessions.GetSession(job.SessionID)
	if err != nil || !found {
		return false
	}
	c, ok := runruntime.ClientToolConfirmationFromSession(session)
	if !ok || !c.Open() || c.Status != runruntime.ClientToolConfirmationPending || c.RunID != job.RunID {
		return false
	}
	e.server.armClientToolConfirmationTimer(job.SessionID, c.ExpiresAt)
	return true
}

// settleClientToolConfirmationAfterAnswer delivers a confirmed call's outcome
// as soon as the client answers it. Only an approved confirmation's call (a
// client tool record outside any run) can settle one.
func (s *Server) settleClientToolConfirmationAfterAnswer(sessionID string, record pebblestore.PermissionRecord) {
	if record.Requirement != permission.RequirementClientTool || strings.TrimSpace(record.RunID) != "" {
		return
	}
	if _, _, err := s.advanceClientToolConfirmation(sessionID, true); err != nil {
		log.Printf("warning: client tool confirmation for session %s: %v", sessionID, err)
	}
}

// pendingClientToolConfirmationView is the session read's pending_confirmation:
// the confirmation while it waits for the person, otherwise nil.
func pendingClientToolConfirmationView(session pebblestore.SessionSnapshot, now int64) *runruntime.ClientToolConfirmation {
	c, ok := runruntime.ClientToolConfirmationFromSession(session)
	if !ok || !c.Open() || c.Status != runruntime.ClientToolConfirmationPending || now >= c.ExpiresAt {
		return nil
	}
	return &c
}

// Timers are accelerators only: the durable expiry and deadline are rechecked
// on every read, decision, answer and message.
type clientToolConfirmationTimerSet struct {
	mu     sync.Mutex
	timers map[string]*time.Timer
}

var clientToolConfirmationTimers = &clientToolConfirmationTimerSet{timers: map[string]*time.Timer{}}

func (s *Server) armClientToolConfirmationTimer(sessionID string, atUnixMS int64) {
	delay := time.Until(time.UnixMilli(atUnixMS)) + 20*time.Millisecond
	if delay < 0 {
		delay = 0
	}
	set := clientToolConfirmationTimers
	set.mu.Lock()
	defer set.mu.Unlock()
	if existing := set.timers[sessionID]; existing != nil {
		existing.Stop()
	}
	set.timers[sessionID] = time.AfterFunc(delay, func() {
		if s.isShuttingDown() {
			return
		}
		if _, _, err := s.advanceClientToolConfirmation(sessionID, true); err != nil {
			log.Printf("warning: client tool confirmation for session %s: %v", sessionID, err)
		}
	})
}

func (s *Server) stopClientToolConfirmationTimer(sessionID string) {
	set := clientToolConfirmationTimers
	set.mu.Lock()
	defer set.mu.Unlock()
	if existing := set.timers[sessionID]; existing != nil {
		existing.Stop()
		delete(set.timers, sessionID)
	}
}
