package run

import (
	"fmt"
	"strings"
	"sync"
	"time"

	provideriface "swarm/packages/swarmd/internal/provider/interfaces"
	sessionruntime "swarm/packages/swarmd/internal/session"
	pebblestore "swarm/packages/swarmd/internal/store/pebble"
)

// Queued notes are ordinary user messages posted while the session's run is
// active. They are stored as feedback notes (never a new run intent) and carry
// these markers so clients can show them as queued until a receipt arrives.
const (
	QueuedNoteMetadataKey    = "queued_note"
	QueuedNoteRunMetadataKey = "queued_for_run_id"
	QueuedNoteStatusMetadata = "delivery_status"
	QueuedNoteStatusQueued   = "queued"
	// FeedbackDeliveryUserTurn is the only delivery channel: a note always
	// reaches the model as a user-role turn (its own user message, or the
	// person's input to a running CLI), never inside a tool result.
	FeedbackDeliveryUserTurn = "user_turn"
)

// IsFeedbackNote reports whether message is a non-triggering user note.
func IsFeedbackNote(message pebblestore.MessageSnapshot) bool {
	return message.Role == "user" && message.Metadata["feedback_note"] == true
}

// IsQueuedNote reports whether message is a person's message queued during an
// active run (a feedback note that must not be dropped when the run ends).
func IsQueuedNote(message pebblestore.MessageSnapshot) bool {
	return IsFeedbackNote(message) && message.Metadata[QueuedNoteMetadataKey] == true
}

// feedbackMessages selects only explicit non-triggering user notes. The cursor
// advances over every message so each boundary performs bounded incremental reads.
func feedbackMessages(messages []pebblestore.MessageSnapshot, cursor *uint64) []pebblestore.MessageSnapshot {
	var notes []pebblestore.MessageSnapshot
	for _, message := range messages {
		if message.GlobalSeq > *cursor {
			*cursor = message.GlobalSeq
		}
		if IsFeedbackNote(message) {
			notes = append(notes, message)
		}
	}
	return notes
}

// QueuedNoteTracker is one run's single claim cursor over its session's
// feedback notes. Every delivery path (a user message at a step boundary, or
// a running provider's own user-input channel) claims through it, so a note
// is in at most one provider input at a time. A claimed note stays in flight
// until a provider step that carried it succeeds (TakeInFlight, then the
// receipt). When the input carrying it is discarded first (the run ends, or
// its input is rebuilt), ReturnInFlight hands it back: the next claim or the
// run's follow-up turn delivers it. Safe for concurrent tool calls.
type QueuedNoteTracker struct {
	mu       sync.Mutex
	cursor   uint64
	inFlight []ClaimedQueuedNote
	returned []pebblestore.MessageSnapshot
}

// ClaimedQueuedNote is a note placed in provider input, with how it was placed
// (FeedbackDeliveryUserTurn).
type ClaimedQueuedNote struct {
	Note    pebblestore.MessageSnapshot
	Channel string
}

// NewQueuedNoteTracker starts claiming after cursor (a session message seq).
func NewQueuedNoteTracker(cursor uint64) *QueuedNoteTracker {
	return &QueuedNoteTracker{cursor: cursor}
}

// Advance moves the cursor forward; it never moves backward.
func (t *QueuedNoteTracker) Advance(cursor uint64) {
	if t == nil {
		return
	}
	t.mu.Lock()
	if cursor > t.cursor {
		t.cursor = cursor
	}
	t.mu.Unlock()
}

// Cursor returns the highest message seq already claimed or skipped.
func (t *QueuedNoteTracker) Cursor() uint64 {
	if t == nil {
		return 0
	}
	t.mu.Lock()
	defer t.mu.Unlock()
	return t.cursor
}

// Claim returns notes handed back earlier, then reads notes after the cursor
// with read (which advances the cursor it is given) and moves the tracker past
// them. Every returned note is in flight on channel until TakeInFlight or
// ReturnInFlight. The read and the advance are one critical section: two
// concurrent claims never return the same note.
func (t *QueuedNoteTracker) Claim(channel string, read func(cursor *uint64) ([]pebblestore.MessageSnapshot, error)) ([]pebblestore.MessageSnapshot, error) {
	return t.ClaimMatching(channel, read, nil)
}

// ClaimMatching is Claim limited to notes for which keep reports true. Every
// other note read past the cursor stays pending, unreceipted and withheld from
// rebuilt history, as if handed back: the next unfiltered claim delivers it
// through its own channel. A nil keep claims every note.
func (t *QueuedNoteTracker) ClaimMatching(channel string, read func(cursor *uint64) ([]pebblestore.MessageSnapshot, error), keep func(pebblestore.MessageSnapshot) bool) ([]pebblestore.MessageSnapshot, error) {
	if t == nil || read == nil {
		return nil, nil
	}
	t.mu.Lock()
	defer t.mu.Unlock()
	next := t.cursor
	fresh, err := read(&next)
	if err != nil {
		return nil, err
	}
	if next > t.cursor {
		t.cursor = next
	}
	candidates := append(t.returned, fresh...)
	t.returned = nil
	var notes []pebblestore.MessageSnapshot
	for _, note := range candidates {
		if keep != nil && !keep(note) {
			t.returned = append(t.returned, note)
			continue
		}
		notes = append(notes, note)
		t.inFlight = append(t.inFlight, ClaimedQueuedNote{Note: note, Channel: channel})
	}
	return notes, nil
}

// TakeInFlight returns and clears the notes in flight. Call it only after a
// provider step whose input carried them succeeded, then receipt them.
func (t *QueuedNoteTracker) TakeInFlight() []ClaimedQueuedNote {
	if t == nil {
		return nil
	}
	t.mu.Lock()
	defer t.mu.Unlock()
	claimed := t.inFlight
	t.inFlight = nil
	return claimed
}

// ReturnInFlight hands notes in flight back, unreceipted, because the input
// carrying them was discarded before a successful provider step. They are
// claimed again first, ahead of newer notes.
func (t *QueuedNoteTracker) ReturnInFlight() {
	if t == nil {
		return
	}
	t.mu.Lock()
	defer t.mu.Unlock()
	if len(t.inFlight) == 0 {
		return
	}
	returned := make([]pebblestore.MessageSnapshot, 0, len(t.inFlight)+len(t.returned))
	for _, claimed := range t.inFlight {
		returned = append(returned, claimed.Note)
	}
	t.returned = append(returned, t.returned...)
	t.inFlight = nil
}

// Returned lists notes handed back and not claimed again, in claim order.
func (t *QueuedNoteTracker) Returned() []pebblestore.MessageSnapshot {
	if t == nil {
		return nil
	}
	t.mu.Lock()
	defer t.mu.Unlock()
	return append([]pebblestore.MessageSnapshot(nil), t.returned...)
}

// Withholds reports whether message is a note the run has not yet delivered
// (not claimed, in flight or handed back). Input rebuilt from durable history
// must leave such notes out: the claim path delivers them, exactly once.
func (t *QueuedNoteTracker) Withholds(message pebblestore.MessageSnapshot) bool {
	if t == nil || !IsFeedbackNote(message) {
		return false
	}
	t.mu.Lock()
	defer t.mu.Unlock()
	if message.GlobalSeq > t.cursor {
		return true
	}
	for _, claimed := range t.inFlight {
		if claimed.Note.ID == message.ID {
			return true
		}
	}
	for _, note := range t.returned {
		if note.ID == message.ID {
			return true
		}
	}
	return false
}

// QueuedNoteUserInput hands a running provider request the person's queued
// notes (only IsQueuedNote messages; other feedback notes keep their
// step-boundary user message) for delivery through the provider's own
// user-input channel, as the person's turn. It claims through the run's
// tracker on FeedbackDeliveryUserTurn and records no receipt: the notes stay
// in flight until the run loop sees the provider step that carried them
// succeed.
type QueuedNoteUserInput struct {
	Tracker *QueuedNoteTracker
	Read    func(cursor *uint64) ([]pebblestore.MessageSnapshot, error)
}

var _ provideriface.QueuedUserInput = (*QueuedNoteUserInput)(nil)

func (i *QueuedNoteUserInput) ClaimQueuedUserMessages() ([]string, error) {
	if i == nil || i.Tracker == nil || i.Read == nil {
		return nil, nil
	}
	notes, err := i.Tracker.ClaimMatching(FeedbackDeliveryUserTurn, i.Read, IsQueuedNote)
	if err != nil {
		return nil, err
	}
	texts := make([]string, 0, len(notes))
	for _, note := range notes {
		if content := strings.TrimSpace(note.Content); content != "" {
			texts = append(texts, content)
		}
	}
	return texts, nil
}

// RecordClaimedFeedbackDelivery receipts notes taken from a tracker after the
// successful provider step that carried them, each on its own channel.
func RecordClaimedFeedbackDelivery(apply func(sessionruntime.SessionMutationInput) (sessionruntime.SessionMutationResult, error), sessionID, runID string, claimed []ClaimedQueuedNote) error {
	for _, entry := range claimed {
		if err := RecordFeedbackDeliveryVia(apply, sessionID, runID, entry.Channel, []pebblestore.MessageSnapshot{entry.Note}); err != nil {
			return err
		}
	}
	return nil
}

// RecordFeedbackDelivery persists receipts after a successful provider step.
// A receipt proves a successful provider response to input containing the note,
// not semantic acceptance, scope incorporation, or approval of revised work.
func RecordFeedbackDelivery(apply func(sessionruntime.SessionMutationInput) (sessionruntime.SessionMutationResult, error), sessionID, runID string, notes []pebblestore.MessageSnapshot) error {
	return RecordFeedbackDeliveryVia(apply, sessionID, runID, FeedbackDeliveryUserTurn, notes)
}

// RecordFeedbackDeliveryVia persists one receipt per note, naming how the note
// reached the model. The receipt key is per note and run, so a note is
// receipted once per run however many paths observe it.
func RecordFeedbackDeliveryVia(apply func(sessionruntime.SessionMutationInput) (sessionruntime.SessionMutationResult, error), sessionID, runID, channel string, notes []pebblestore.MessageSnapshot) error {
	for _, note := range notes {
		key := "feedback-delivery:" + note.ID + ":" + runID
		now := time.Now().UnixMilli()
		content := fmt.Sprintf("Feedback %s delivered to provider in run %s; incorporation unconfirmed.", note.ID, runID)
		result, err := apply(pebblestore.V3SessionMutationInput{
			SessionID: sessionID, UserID: note.UserID, AccountScopeID: note.AccountScopeID,
			ClientRequestID: key, IdempotencyKey: key, PayloadHash: key, RequestHash: key,
			Kind: pebblestore.V3SessionMutationAppendMessage, NowUnixMs: now,
			Message: &pebblestore.MessageSnapshot{
				ID: key, SessionID: sessionID, UserID: note.UserID, AccountScopeID: note.AccountScopeID,
				Role: "system", Content: content, CreatedAt: now,
				Metadata: map[string]any{"source": "feedback_delivery", "message_id": note.ID, "run_id": runID, "delivery_status": "delivered", "delivery_channel": channel, "incorporation_status": "unconfirmed"},
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
