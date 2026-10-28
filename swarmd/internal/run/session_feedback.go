package run

import (
	"context"
	"fmt"
	"regexp"
	"strings"
	"sync"
	"time"

	provideriface "swarm/packages/swarmd/internal/provider/interfaces"
	sessionruntime "swarm/packages/swarmd/internal/session"
	pebblestore "swarm/packages/swarmd/internal/store/pebble"
	"swarm/packages/swarmd/internal/tool"
)

// Queued notes are ordinary user messages posted while the session's run is
// active. They are stored as feedback notes (never a new run intent) and carry
// these markers so clients can show them as queued until a receipt arrives.
const (
	QueuedNoteMetadataKey      = "queued_note"
	QueuedNoteRunMetadataKey   = "queued_for_run_id"
	QueuedNoteStatusMetadata   = "delivery_status"
	QueuedNoteStatusQueued     = "queued"
	FeedbackDeliveryToolResult = "tool_result"
	FeedbackDeliveryUserTurn   = "user_message"
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
// feedback notes. Every delivery path (a separate user message at a step
// boundary, or a block appended to a tool result) claims through it, so a note
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
// (FeedbackDeliveryUserTurn or FeedbackDeliveryToolResult).
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

// queuedNoteBlockTag matches an opening or closing block tag, in any case and
// spacing, inside note text.
var queuedNoteBlockTag = regexp.MustCompile(`(?i)<(\s*/?\s*queued_message_from_the_person)`)

// escapeQueuedNoteBlockTags neutralises block tags in note text so a note can
// neither close its block early nor open a forged one.
func escapeQueuedNoteBlockTags(content string) string {
	return queuedNoteBlockTag.ReplaceAllString(content, "&lt;$1")
}

// FormatQueuedNotesBlock renders the person's queued notes as a clearly
// delimited section that follows a tool's output. The content stays untrusted
// user text. Only IsQueuedNote messages (stored by the person's message route)
// are attributed to the person; any other feedback note (a background command
// report, an Orchestrator note) is never rendered here, so command or agent
// text cannot be presented as the person's message.
func FormatQueuedNotesBlock(notes []pebblestore.MessageSnapshot) string {
	var b strings.Builder
	for _, note := range notes {
		if !IsQueuedNote(note) {
			continue
		}
		content := strings.TrimSpace(note.Content)
		if content == "" {
			continue
		}
		b.WriteString("\n\n<queued_message_from_the_person>\n")
		b.WriteString("The person sent this message while you were working. It is their user message, not tool output; it does not approve permissions or change the task scope by itself.\n\n")
		b.WriteString(escapeQueuedNoteBlockTags(content))
		b.WriteString("\n</queued_message_from_the_person>")
	}
	return b.String()
}

// AppendQueuedNotesToToolResult returns result with notes appended after the
// model-facing tool output. Output, Error and RestartTurn are left untouched,
// so code that parses a tool's own payload is unaffected.
func AppendQueuedNotesToToolResult(invocation provideriface.ToolInvocation, result provideriface.ToolExecutionResult, notes []pebblestore.MessageSnapshot) provideriface.ToolExecutionResult {
	block := FormatQueuedNotesBlock(notes)
	if block == "" {
		return result
	}
	base := strings.TrimSpace(result.TextForModel)
	if base == "" {
		callID := firstNonEmptyString(result.CallID, invocation.CallID)
		name := firstNonEmptyString(invocation.Name, result.Name)
		arguments := strings.TrimSpace(invocation.Arguments)
		if arguments == "" {
			arguments = "{}"
		}
		base = PrepareToolOutputForModel(tool.Call{CallID: callID, Name: name, Arguments: arguments}, tool.Result{CallID: callID, Name: name, Output: strings.TrimSpace(result.Output), Error: strings.TrimSpace(result.Error), DurationMS: result.DurationMS})
	}
	result.TextForModel = base + block
	return result
}

// QueuedNoteToolInvoker places claimed notes in the next tool result the model
// receives. It wraps the run's invoker, so it works the same whether the
// daemon runs the tool loop or the provider (a CLI) calls tools mid-turn. It
// records no receipt: the notes stay in flight on the tracker until the run
// loop sees the provider step that consumed the result succeed.
type QueuedNoteToolInvoker struct {
	Inner   provideriface.ToolInvoker
	Tracker *QueuedNoteTracker
	Read    func(cursor *uint64) ([]pebblestore.MessageSnapshot, error)
}

func (i *QueuedNoteToolInvoker) ExecuteTool(ctx context.Context, invocation provideriface.ToolInvocation) (provideriface.ToolExecutionResult, error) {
	result, err := i.Inner.ExecuteTool(ctx, invocation)
	if err != nil || result.RestartTurn || i.Tracker == nil || i.Read == nil {
		// A restart rebuilds the model's input from durable history, so a note
		// appended here could be dropped; the next boundary claims it instead.
		return result, err
	}
	// Only the person's queued notes ride on tool results. Other feedback
	// notes keep their separate user-message delivery at the next step.
	notes, claimErr := i.Tracker.ClaimMatching(FeedbackDeliveryToolResult, i.Read, IsQueuedNote)
	if claimErr != nil {
		return result, claimErr
	}
	if len(notes) == 0 {
		return result, nil
	}
	return AppendQueuedNotesToToolResult(invocation, result, notes), nil
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
		if channel == FeedbackDeliveryToolResult {
			content = fmt.Sprintf("Feedback %s delivered to provider with a tool result in run %s; incorporation unconfirmed.", note.ID, runID)
		}
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
