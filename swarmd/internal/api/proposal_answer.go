package api

import (
	"errors"
	"net/http"
	"strings"

	"swarm/packages/swarmd/internal/permission"
	pebblestore "swarm/packages/swarmd/internal/store/pebble"
)

// answerProposalRequest handles an answer to a session's pending request
// when it is a proposal request; handled is false for any other request. The
// answer must be the person's (proposalPersonRequest): allow accepts, deny
// discards. It decides the proposal once, under the proposal's lock
// (decideProposalLocked), and answers the request with the outcome.
//
// An answer never errors on a proposal decided some other way: a request
// already answered returns its recorded outcome; a proposal decided
// elsewhere (on a linked machine, in the app, by another answer) is read
// again and the request settled with what became of it
// (settleProposalRequestsFromState), answered 200 with already_decided. Only
// a genuinely newer pending revision is a 409: this revision is cancelled
// with the reason and the conversation's request now shows the new one.
func (s *Server) answerProposalRequest(w http.ResponseWriter, r *http.Request, sessionID, permissionID, action, reason string) bool {
	if s.perm == nil {
		return false
	}
	record, ok, err := s.perm.GetPermission(sessionID, permissionID)
	if err != nil || !ok || !permission.IsProposalDecision(record) {
		return false
	}
	person, isPerson := proposalPersonRequest(r)
	if !isPerson {
		writeError(w, http.StatusForbidden, permission.ErrProposalDecisionPersonOnly)
		return true
	}
	var decision string
	switch strings.ToLower(strings.TrimSpace(action)) {
	case permission.ActionAllowOnce, "allow", "approve", "accept", "yes":
		decision = proposalAccept
	case permission.ActionDenyOnce, "deny", "reject", "discard", "no":
		decision = proposalDiscard
	default:
		writeError(w, http.StatusBadRequest, errors.New("a proposal is answered with allow_once (Accept) or deny_once (Discard); a lasting rule cannot decide proposals"))
		return true
	}
	lock := s.proposalLock(person.AccountScopeID, record.CallID)
	lock.Lock()
	defer lock.Unlock()
	// Read again under the lock: an answer that raced this one, or a
	// decision made elsewhere, may have settled it.
	if record, ok, err = s.perm.GetPermission(sessionID, permissionID); err != nil || !ok {
		writeError(w, http.StatusNotFound, errors.New("this pending request is gone"))
		return true
	}
	if record.Status != pebblestore.PermissionStatusPending {
		writeJSON(w, http.StatusOK, proposalAnswerBody(sessionID, record, map[string]any{"already_answered": true}))
		return true
	}
	payload, ok := proposalRequestPayloadOf(record)
	if !ok || payload.Decide.ExpectedRevision == 0 {
		writeError(w, http.StatusConflict, errors.New("this pending request's proposal reference is unreadable; decide the proposal in the app's Automations page"))
		return true
	}
	in := payload.Decide
	in.Decision = decision
	out, err := s.decideProposalLocked(r.Context(), person, in)
	if err == nil {
		updated, _, _ := s.perm.GetPermission(sessionID, permissionID)
		writeJSON(w, http.StatusOK, proposalAnswerBody(sessionID, updated, map[string]any{"proposal": out}))
		return true
	}
	var stale proposalStaleError
	if !errors.As(err, &stale) {
		writeError(w, linkedTaskStatus(err), err)
		return true
	}
	// Not pending at this revision: settle from what became of it.
	settled, settleErr := s.settleProposalRequestsFromState(r.Context(), person, payload)
	updated, _, _ := s.perm.GetPermission(sessionID, permissionID)
	switch settled.Outcome.State {
	case proposalOutcomeAccepted, proposalOutcomeDiscarded, proposalOutcomeDecided:
		extra := map[string]any{"already_decided": true, "requests_answered": settled.Answered}
		if settleErr != nil {
			extra["requests_error"] = settleErr.Error()
		}
		writeJSON(w, http.StatusOK, proposalAnswerBody(sessionID, updated, extra))
	case proposalOutcomeStale:
		body := proposalAnswerBody(sessionID, updated, map[string]any{"requests_answered": settled.Answered})
		body["ok"], body["error"] = false, settled.Outcome.Text
		body["outcome"], body["outcome_text"] = proposalOutcomeStale, settled.Outcome.Text
		writeJSON(w, http.StatusConflict, body)
	default:
		// Its state could not be read (a machine unreachable), or it is
		// pending again: the request stays as it is.
		message := stale.Error()
		if settleErr != nil {
			message += "; what became of it could not be read: " + settleErr.Error()
		}
		writeJSON(w, http.StatusConflict, map[string]any{"ok": false, "error": message, "session_id": sessionID, "permission": updated})
	}
	return true
}

// proposalAnswerBody is an answer to a proposal request: the request and its
// outcome (accepted, discarded, cancelled) with the reason it records.
func proposalAnswerBody(sessionID string, record pebblestore.PermissionRecord, extra map[string]any) map[string]any {
	body := map[string]any{"ok": true, "session_id": sessionID, "permission": record, "outcome": proposalRequestOutcome(record), "outcome_text": record.Reason}
	for key, value := range extra {
		body[key] = value
	}
	return body
}
