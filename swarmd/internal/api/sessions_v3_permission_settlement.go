package api

import (
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"log"
	"strconv"
	"strings"
	"time"

	"swarm/packages/swarmd/internal/identity"
	sessionruntime "swarm/packages/swarmd/internal/session"
	pebblestore "swarm/packages/swarmd/internal/store/pebble"
)

// A permission request belongs to the run that asked for it, and its
// permission.updated event is normally that run's own write
// (publishSessionV3PermissionUpdatedFromRecord), which the store accepts only
// while the run is running. A request can still be decided after its run
// ended: its pending requests are cancelled when the run is stopped, when the
// run itself ends by failure, cancellation or timeout (run.Service.runTurn),
// and for every waiting run at daemon start (ReconcilePendingRuns); or the
// person answers a request the ended run left pending. The permission store
// has the decision, but the session's event log, which the Companion state,
// the agent feed and the clients follow, would keep the request waiting
// forever.
//
// Every run cancellation goes through permission.Service.CancelRunPending,
// which tells its observer (publishSessionV3RunPendingCancelled, installed by
// NewServer) of exactly the requests that call cancelled, whichever caller
// won; a cancellation made before the server exists (the restart
// reconciliation) is delivered when the observer is installed.
//
// settleSessionV3PermissionAfterRun closes that gap with one session-level
// permission.updated event per request: no run intent, so it neither revives
// the ended run nor passes its epoch fence (it commits in the session's
// active epoch, like the daemon's other session-level writes); idempotent by
// the request's id, so a repeated or retried publication is a replay; and
// carrying the exact original request (run, step, call) with the stored
// decision. It never executes anything: a late approval of an ended run's
// request is recorded as approved, the call it gated stays unrun.

// publishSessionV3PermissionDecision publishes a decision the caller just
// made on a permission request. decided is true only when this call moved
// the request off pending (a repeated answer returns the already decided
// record and must not publish it again).
func (s *Server) publishSessionV3PermissionDecision(principal identity.Principal, sessionID string, record pebblestore.PermissionRecord, decided bool) (sessionruntime.SessionMutationResult, bool, error) {
	result, published, err := s.publishSessionV3PermissionUpdatedFromRecord(principal, sessionID, record)
	if published || !decided {
		return result, published, err
	}
	if err != nil {
		// The run may have stopped running between the run-owned writer's
		// check and its write (a stop racing the run's own end). Settle
		// only when it no longer runs; otherwise the error stands.
		intent, found, readErr := s.sessions.GetSessionRunIntent(strings.TrimSpace(sessionID), strings.TrimSpace(record.RunID))
		if readErr != nil || !found || strings.TrimSpace(intent.Status) == sessionruntime.RunIntentRunning {
			return result, false, err
		}
		settled, ok, settleErr := s.settleSessionV3PermissionAfterRun(principal, sessionID, record)
		if settleErr != nil || ok {
			return settled, ok, settleErr
		}
		return result, false, err
	}
	return s.settleSessionV3PermissionAfterRun(principal, sessionID, record)
}

// publishSessionV3RunPendingCancelled is permission.Service's
// RunPendingCancelledObserver: it publishes each request one CancelRunPending
// call cancelled, as the run's own write while the run still runs (the run's
// deferred cancellation), else as the settlement. The session's owner is the
// writer; a failure is logged, the cancellation itself already happened.
func (s *Server) publishSessionV3RunPendingCancelled(sessionID, runID string, cancelled []pebblestore.PermissionRecord) {
	if s == nil || s.sessions == nil {
		return
	}
	for _, record := range cancelled {
		if _, _, err := s.publishSessionV3PermissionDecision(identity.Principal{}, sessionID, record, true); err != nil {
			log.Printf("warning: publish cancelled permission %s of run %s in session %s: %v", strings.TrimSpace(record.ID), runID, sessionID, err)
		}
	}
}

// cancelSessionV3RunPendingPermissions cancels the pending requests of a run
// that was just stopped. The cancellations are published by the permission
// service's observer (publishSessionV3RunPendingCancelled), for whichever of
// this call and the run's own deferred cancellation moved them off pending.
func (s *Server) cancelSessionV3RunPendingPermissions(sessionID, runID, reason string) error {
	if s == nil || s.perm == nil {
		return nil
	}
	_, err := s.perm.CancelRunPending(sessionID, runID, reason)
	return err
}

func sessionV3PermissionSettledClientRequestID(permissionID string) string {
	return "v3-permission-settled-" + strings.NewReplacer(".", "_", "/", "_", " ", "_", ":", "_").Replace(strings.TrimSpace(permissionID))
}

// settleSessionV3PermissionAfterRun writes the decided request's
// permission.updated event when its run is no longer running. It writes
// nothing for a request still pending, a request without a run and call, or
// a run that is still running (that run's own writer publishes it).
func (s *Server) settleSessionV3PermissionAfterRun(principal identity.Principal, sessionID string, record pebblestore.PermissionRecord) (sessionruntime.SessionMutationResult, bool, error) {
	if s == nil || s.sessions == nil {
		return sessionruntime.SessionMutationResult{}, false, errors.New("sessions v3 service is not configured")
	}
	sessionID = strings.TrimSpace(sessionID)
	permissionID := strings.TrimSpace(record.ID)
	runID := strings.TrimSpace(record.RunID)
	callID := strings.TrimSpace(record.CallID)
	status := strings.TrimSpace(record.Status)
	if sessionID == "" || permissionID == "" || runID == "" || callID == "" || strings.TrimSpace(record.SessionID) != sessionID {
		return sessionruntime.SessionMutationResult{}, false, nil
	}
	switch status {
	case pebblestore.PermissionStatusApproved, pebblestore.PermissionStatusDenied, pebblestore.PermissionStatusCancelled:
	default:
		return sessionruntime.SessionMutationResult{}, false, nil
	}
	intent, found, err := s.sessions.GetSessionRunIntent(sessionID, runID)
	if err != nil {
		return sessionruntime.SessionMutationResult{}, false, err
	}
	if found && strings.TrimSpace(intent.Status) == sessionruntime.RunIntentRunning {
		return sessionruntime.SessionMutationResult{}, false, nil
	}
	// The session's owner writes it; a caller acting for someone else is
	// refused rather than writing into their session.
	session, ok, err := s.sessions.GetSession(sessionID)
	if err != nil {
		return sessionruntime.SessionMutationResult{}, false, err
	}
	if !ok {
		return sessionruntime.SessionMutationResult{}, false, errors.New("session not found")
	}
	userID, accountScopeID := strings.TrimSpace(session.UserID), strings.TrimSpace(session.AccountScopeID)
	if principal.Valid() && (strings.TrimSpace(principal.UserID) != userID || strings.TrimSpace(principal.AccountScopeID) != accountScopeID) {
		return sessionruntime.SessionMutationResult{}, false, errors.New("permission belongs to another account's session")
	}
	toolName := strings.TrimSpace(record.ToolName)
	if toolName == "" {
		toolName = "tool"
	}
	payload := sessionV3PermissionUpdatedPayload(sessionID, runID, record.Step, toolName, callID, strings.TrimSpace(firstNonEmpty(record.ToolCallArguments, record.ToolArguments)), record)
	payload["run_ended"] = true
	raw, err := json.Marshal(payload)
	if err != nil {
		return sessionruntime.SessionMutationResult{}, false, err
	}
	// The hash names the decision, not the record's serialization: a retried
	// publication of the same decision (re-read from the store) replays, while
	// a different decision under the same request id is a conflict.
	sum := sha256.Sum256([]byte(strings.Join([]string{sessionID, runID, callID, permissionID, status, strings.TrimSpace(record.Decision), strconv.FormatInt(record.ResolvedAt, 10)}, "\x00")))
	payloadHash := hex.EncodeToString(sum[:])
	key := sessionV3PermissionSettledClientRequestID(permissionID)
	result, err := s.applySessionV3PrimaryMutation(sessionruntime.SessionMutationInput{
		SessionID:       sessionID,
		UserID:          userID,
		AccountScopeID:  accountScopeID,
		ClientRequestID: key,
		IdempotencyKey:  key,
		PayloadHash:     payloadHash,
		RequestHash:     payloadHash,
		Kind:            sessionruntime.SessionMutationRecordDiagnostic,
		EventType:       "permission.updated",
		EventPayload:    raw,
		NowUnixMs:       time.Now().UnixMilli(),
	})
	if err != nil {
		return result, false, err
	}
	return result, !result.Replayed, nil
}
