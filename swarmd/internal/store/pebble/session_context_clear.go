package pebblestore

import (
	"errors"
	"strings"
)

const V3SessionMutationClearContext = "session.context.clear"
const ExecutionEpochReasonContextCleared = "context_cleared"

var ErrSessionContextClearConflict = errors.New("context can only be cleared in an idle project conversation without an active plan")

// Clear uses the existing atomic epoch boundary: history stays in the sealed
// predecessor and provider state starts fresh. No session or project is replaced.
func (s *SessionStore) applyV3ClearContext(input V3SessionMutationInput) (V3SessionMutationResult, error) {
	if strings.TrimSpace(input.ClientRequestID) == "" || input.ExpectedLastEventSeq == nil {
		return V3SessionMutationResult{}, errors.New("context clear requires request identity and expected last event sequence")
	}
	result, err := s.BeginExecutionEpoch(BeginExecutionEpochInput{
		SessionID: input.SessionID, UserID: input.UserID, AccountScopeID: input.AccountScopeID,
		ClientRequestID: "clear-context:" + input.ClientRequestID, PayloadHash: input.PayloadHash,
		Reason: ExecutionEpochReasonContextCleared, SkipRunIntent: true,
		ExpectedLastEventSeq: input.ExpectedLastEventSeq,
	})
	if err != nil {
		return V3SessionMutationResult{}, err
	}
	return V3SessionMutationResult{
		SessionID: input.SessionID, PrimarySeq: result.Event.Seq, FirstSeq: result.Event.Seq, LastSeq: result.Event.Seq,
		EventIDs: []string{result.Event.ID}, Event: result.Event, Projection: result.Projection,
		RealtimeOutbox: &result.Outbox, Replayed: result.Replayed,
	}, nil
}

// Called with the same session mutation lock used by message/run/plan writes.
func (s *SessionStore) validateContextClear(input BeginExecutionEpochInput, replay bool) error {
	session, ok, err := s.GetSession(input.SessionID)
	if err != nil {
		return err
	}
	if !ok || session.UserID != input.UserID || session.AccountScopeID != input.AccountScopeID || ProjectConversationID(session) == "" {
		return errors.New("project conversation not found")
	}
	if replay {
		return nil
	}
	if tombstone, found, err := s.GetV3SessionTombstone(input.SessionID); err != nil {
		return err
	} else if found && (tombstone.Deleted || tombstone.Archived) {
		return ErrSessionContextClearConflict
	}
	if _, active, err := s.GetV3SessionActiveRunIntent(input.SessionID); err != nil {
		return err
	} else if active {
		return ErrSessionContextClearConflict
	}
	if lifecycle, found, err := s.GetSessionLifecycle(input.SessionID); err != nil {
		return err
	} else if found && lifecycle.Active {
		return ErrSessionContextClearConflict
	}
	if active, found, err := s.GetActivePlan(input.SessionID); err != nil {
		return err
	} else if found {
		plan, exists, err := s.GetPlan(input.SessionID, active.PlanID)
		if err != nil {
			return err
		}
		if !exists || plan.Document == nil {
			return ErrSessionContextClearConflict
		}
		status := strings.ToLower(strings.TrimSpace(plan.Document.Status))
		if status != "completed" && status != "archived" && status != "cancelled" {
			return ErrSessionContextClearConflict
		}
		if v3SyncSnapshotPlanUnresolved(plan) {
			return ErrSessionContextClearConflict
		}
	}
	seq, err := s.readV3SessionSequence(input.SessionID)
	if err != nil {
		return err
	}
	if input.ExpectedLastEventSeq == nil || seq != *input.ExpectedLastEventSeq {
		expected := uint64(0)
		if input.ExpectedLastEventSeq != nil {
			expected = *input.ExpectedLastEventSeq
		}
		return &V3ProjectionConflictError{SessionID: input.SessionID, Expected: expected, Actual: seq}
	}
	return nil
}
