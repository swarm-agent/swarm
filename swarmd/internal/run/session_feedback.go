package run

import (
	"fmt"
	"time"

	sessionruntime "swarm/packages/swarmd/internal/session"
	pebblestore "swarm/packages/swarmd/internal/store/pebble"
)

// feedbackMessages selects only explicit non-triggering user notes. The cursor
// advances over every message so each boundary performs bounded incremental reads.
func feedbackMessages(messages []pebblestore.MessageSnapshot, cursor *uint64) []pebblestore.MessageSnapshot {
	var notes []pebblestore.MessageSnapshot
	for _, message := range messages {
		if message.GlobalSeq > *cursor {
			*cursor = message.GlobalSeq
		}
		if message.Role == "user" && message.Metadata["feedback_note"] == true {
			notes = append(notes, message)
		}
	}
	return notes
}

// RecordFeedbackDelivery persists receipts after a successful provider step.
// A receipt proves a successful provider response to input containing the note,
// not semantic acceptance, scope incorporation, or approval of revised work.
func RecordFeedbackDelivery(apply func(sessionruntime.SessionMutationInput) (sessionruntime.SessionMutationResult, error), sessionID, runID string, notes []pebblestore.MessageSnapshot) error {
	for _, note := range notes {
		key := "feedback-delivery:" + note.ID + ":" + runID
		now := time.Now().UnixMilli()
		result, err := apply(pebblestore.V3SessionMutationInput{
			SessionID: sessionID, UserID: note.UserID, AccountScopeID: note.AccountScopeID,
			ClientRequestID: key, IdempotencyKey: key, PayloadHash: key, RequestHash: key,
			Kind: pebblestore.V3SessionMutationAppendMessage, NowUnixMs: now,
			Message: &pebblestore.MessageSnapshot{
				ID: key, SessionID: sessionID, UserID: note.UserID, AccountScopeID: note.AccountScopeID,
				Role: "system", Content: fmt.Sprintf("Feedback %s delivered to provider in run %s; incorporation unconfirmed.", note.ID, runID), CreatedAt: now,
				Metadata: map[string]any{"source": "feedback_delivery", "message_id": note.ID, "run_id": runID, "delivery_status": "delivered", "incorporation_status": "unconfirmed"},
			},
		})
		if err != nil {
			return fmt.Errorf("persist feedback delivery receipt: %w", err)
		}
		if result.Error != nil || result.Conflict != nil {
			return fmt.Errorf("feedback delivery receipt rejected: error=%v conflict=%v", result.Error, result.Conflict)
		}
	}
	return nil
}
