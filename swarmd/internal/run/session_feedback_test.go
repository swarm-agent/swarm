package run

import (
	"testing"

	sessionruntime "swarm/packages/swarmd/internal/session"
	pebblestore "swarm/packages/swarmd/internal/store/pebble"
)

// Purpose: feedbackMessages must admit only explicit user feedback at a provider
// boundary, not system receipts or arbitrary transcript entries. This unit layer
// proves bounded cursor advancement without executing another run.
func TestFeedbackMessages(t *testing.T) {
	cursor := uint64(3)
	messages := []pebblestore.MessageSnapshot{
		{ID: "assistant", GlobalSeq: 4, Role: "assistant", Metadata: map[string]any{"feedback_note": true}},
		{ID: "note", GlobalSeq: 5, Role: "user", Content: "feedback", Metadata: map[string]any{"feedback_note": true}},
		{ID: "receipt", GlobalSeq: 6, Role: "system"},
	}
	notes := feedbackMessages(messages, &cursor)
	if len(notes) != 1 || notes[0].ID != "note" || cursor != 6 {
		t.Fatalf("notes=%+v cursor=%d", notes, cursor)
	}
}

// Purpose: recordFeedbackDelivery must persist a verifiable, idempotent V3
// receipt without claiming incorporation or creating a run. The real temporary
// store proves the receipt survives reads and duplicate publication is harmless.
func TestFeedbackDeliveryReceipt(t *testing.T) {
	db, err := pebblestore.Open(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = db.Close() })
	events, err := pebblestore.NewEventLog(db)
	if err != nil {
		t.Fatal(err)
	}
	sessions := sessionruntime.NewService(pebblestore.NewSessionStore(db), events)
	snapshot := pebblestore.SessionSnapshot{ID: "feedback-session", UserID: "owner", AccountScopeID: "account"}
	created, err := sessions.ApplySessionMutation(pebblestore.V3SessionMutationInput{
		SessionID: snapshot.ID, UserID: snapshot.UserID, AccountScopeID: snapshot.AccountScopeID,
		Kind: pebblestore.V3SessionMutationCreateSession, Session: &snapshot,
		ClientRequestID: "create", IdempotencyKey: "create", PayloadHash: "create", RequestHash: "create",
	})
	if err != nil || created.Error != nil || created.Conflict != nil {
		t.Fatalf("create: %+v %v", created, err)
	}
	notes := []pebblestore.MessageSnapshot{{ID: "note", UserID: "owner", AccountScopeID: "account"}}
	for i := 0; i < 2; i++ {
		if err := RecordFeedbackDelivery(sessions.ApplySessionMutation, snapshot.ID, "existing-run", notes); err != nil {
			t.Fatal(err)
		}
	}
	messages, err := sessions.ListSessionMessages(snapshot.ID, 0, 10)
	if err != nil || len(messages) != 1 {
		t.Fatalf("receipt count: %+v %v", messages, err)
	}
	metadata := messages[0].Metadata
	if metadata["message_id"] != "note" || metadata["run_id"] != "existing-run" || metadata["delivery_status"] != "delivered" || metadata["incorporation_status"] != "unconfirmed" {
		t.Fatalf("receipt overclaims: %+v", metadata)
	}
	state, found, err := sessions.GetSessionRunState(snapshot.ID)
	if err != nil || (found && state.Active) {
		t.Fatalf("receipt started execution: %+v %v", state, err)
	}
}
