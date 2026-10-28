package api

import (
	"encoding/json"
	"log"
	"sort"
	"strings"
	"time"

	"swarm/packages/swarmd/internal/identity"
	provideriface "swarm/packages/swarmd/internal/provider/interfaces"
	runruntime "swarm/packages/swarmd/internal/run"
	sessionruntime "swarm/packages/swarmd/internal/session"
	pebblestore "swarm/packages/swarmd/internal/store/pebble"
)

// A person's message posted while the session's run is active is queued: it is
// stored as a non-triggering note (no run intent), reaches the running model
// with its next tool result, and, if the run ends without one, starts the next
// turn. Notes are untrusted user content; they never approve permissions or
// change task scope on their own.

// sessionV3QueuedNoteHistoryWindow bounds the tail scanned for queued notes a
// run carries in its context without a delivery receipt.
const sessionV3QueuedNoteHistoryWindow = 200

// sessionsV3QueuedNoteReservedMetadata are delivery markers only the runtime
// writes; caller-supplied values are dropped.
var sessionsV3QueuedNoteReservedMetadata = []string{"feedback_note", runruntime.QueuedNoteMetadataKey, runruntime.QueuedNoteRunMetadataKey, runruntime.QueuedNoteStatusMetadata}

// sessionsV3QueuedNoteRunStatus stands in for the run status in a queued
// note's idempotency hash: a queued note records no run intent.
const sessionsV3QueuedNoteRunStatus = "queued_note"

// acceptSessionsV3QueuedNote stores message as a queued note when the session
// has an active run that accepts one; ok is false when the message must take
// the ordinary turn path instead. The caller holds the session's message lock.
// A retry replays the original decision, even after the run has ended.
func (s *Server) acceptSessionsV3QueuedNote(principal identity.Principal, session pebblestore.SessionSnapshot, req sessionsV3MessageRequest, message pebblestore.MessageSnapshot) (sessionruntime.SessionMutationResult, bool, error) {
	if s.v3SessionExecutor == nil || !sessionsV3MessageQueueable(session, req, message) {
		return sessionruntime.SessionMutationResult{}, false, nil
	}
	clientRequestID := strings.TrimSpace(firstNonEmpty(req.ClientRequestID, req.IdempotencyKey))
	if clientRequestID == "" {
		return sessionruntime.SessionMutationResult{}, false, nil
	}
	payloadHash, err := sessionsV3MessagePayloadHash(session.ID, req, message, sessionsV3QueuedNoteRunStatus, "")
	if err != nil {
		return sessionruntime.SessionMutationResult{}, false, err
	}
	runID := s.v3SessionExecutor.queuedNoteRunID(session.ID)
	if runID != "" {
		// The durable run record is the authority: a run that has already
		// recorded its end, but not yet released the session in memory, takes
		// no note; the message starts the next turn as before.
		intent, found, err := s.sessions.GetSessionRunIntent(session.ID, runID)
		if err != nil {
			return sessionruntime.SessionMutationResult{}, false, err
		}
		if !found || (intent.Status != sessionruntime.RunIntentPendingExecutor && intent.Status != sessionruntime.RunIntentRunning) {
			runID = ""
		}
	}
	if existing, replay, err := s.sessions.Store().GetV3SessionOperationIdempotencyRecord(principal.AccountScopeID, session.ID, sessionruntime.SessionMutationAppendMessage, clientRequestID); err != nil {
		return sessionruntime.SessionMutationResult{}, false, err
	} else if replay {
		if existing.PayloadHash != payloadHash {
			return sessionruntime.SessionMutationResult{}, false, nil
		}
	} else if runID == "" {
		return sessionruntime.SessionMutationResult{}, false, nil
	}
	if message.Metadata == nil {
		message.Metadata = map[string]any{}
	}
	message.Metadata["feedback_note"] = true
	message.Metadata[runruntime.QueuedNoteMetadataKey] = true
	message.Metadata[runruntime.QueuedNoteStatusMetadata] = runruntime.QueuedNoteStatusQueued
	if runID != "" {
		message.Metadata[runruntime.QueuedNoteRunMetadataKey] = runID
	}
	result, err := s.applySessionV3PrimaryMutation(sessionruntime.SessionMutationInput{
		SessionID: session.ID, UserID: principal.UserID, AccountScopeID: principal.AccountScopeID,
		ClientRequestID: clientRequestID, IdempotencyKey: clientRequestID, PayloadHash: payloadHash, RequestHash: payloadHash,
		Kind: sessionruntime.SessionMutationAppendMessage, Message: &message, NowUnixMs: time.Now().UnixMilli(),
	})
	return result, true, err
}

// sessionsV3MessageQueueable reports whether a message may be queued as a
// note: plain text from the person, with no attachments, explicit run or
// dispatch routing. Anything else keeps the ordinary turn path.
func sessionsV3MessageQueueable(session pebblestore.SessionSnapshot, req sessionsV3MessageRequest, message pebblestore.MessageSnapshot) bool {
	if !req.queueWhileRunning || strings.HasPrefix(session.ID, "worker-execution-") || sessionTakeoverActive(session) {
		return false
	}
	if message.Role != "user" || strings.TrimSpace(message.Content) == "" {
		return false
	}
	if len(message.Media) > 0 || len(message.VideoAttachments) > 0 || len(message.ArtifactSelections) > 0 {
		return false
	}
	if strings.TrimSpace(req.RunID) != "" || len(req.DispatchAuthority) > 0 {
		return false
	}
	_, selectedWorker := req.Metadata["selected_worker"]
	return !selectedWorker
}

// sessionsV3QueuedMessageResponseFields reports a queued note in a message
// response: queued, the run it was queued for and its delivery status.
func sessionsV3QueuedMessageResponseFields(message *pebblestore.MessageSnapshot) map[string]any {
	if message == nil || !runruntime.IsQueuedNote(*message) {
		return map[string]any{"queued": false}
	}
	fields := map[string]any{"queued": true, "delivery_status": runruntime.QueuedNoteStatusQueued}
	if runID, _ := message.Metadata[runruntime.QueuedNoteRunMetadataKey].(string); runID != "" {
		fields["queued_for_run_id"] = runID
	}
	return fields
}

type sessionV3QueuedNoteRun struct {
	runID   string
	tracker *runruntime.QueuedNoteTracker
}

// openQueuedNotesLocked records job as the session's run accepting queued
// notes. Caller holds e.mu.
func (e *sessionV3Executor) openQueuedNotesLocked(job sessionV3ExecutorJob) {
	if job.queuedNotes == nil {
		return
	}
	if e.queuedNoteRuns == nil {
		e.queuedNoteRuns = make(map[string]sessionV3QueuedNoteRun)
	}
	e.queuedNoteRuns[job.SessionID] = sessionV3QueuedNoteRun{runID: job.RunID, tracker: job.queuedNotes}
}

// queuedNoteRunID returns the run that accepts a queued note for sessionID, or
// "" when the session has no such active run. Callers hold the session's
// message lock (runruntime.LockClientToolConfirmations) through the append, so
// the run cannot close its queue between this check and the note landing.
func (e *sessionV3Executor) queuedNoteRunID(sessionID string) string {
	if e == nil {
		return ""
	}
	e.mu.Lock()
	defer e.mu.Unlock()
	return e.queuedNoteRuns[strings.TrimSpace(sessionID)].runID
}

// sessionV3QueuedNoteToolInvoker appends claimed notes to tool results. The
// run loop receipts them after the provider step that consumed the result
// succeeds, or hands them back when that step never happens.
func (e *sessionV3Executor) sessionV3QueuedNoteToolInvoker(job sessionV3ExecutorJob, inner provideriface.ToolInvoker) provideriface.ToolInvoker {
	if inner == nil || job.queuedNotes == nil {
		return inner
	}
	return &runruntime.QueuedNoteToolInvoker{
		Inner:   inner,
		Tracker: job.queuedNotes,
		Read: func(cursor *uint64) ([]pebblestore.MessageSnapshot, error) {
			return e.sessionV3FeedbackSince(job.SessionID, cursor)
		},
	}
}

// closeQueuedNotes ends job's queue. A completed or failed run whose queued
// notes never reached the model hands them to a follow-up turn, where they
// are ordinary user messages: nothing the person sent is dropped. A stopped
// run starts nothing; its notes stay in history for the next turn.
func (e *sessionV3Executor) closeQueuedNotes(job sessionV3ExecutorJob) {
	if e == nil || e.server == nil || e.server.sessions == nil {
		return
	}
	e.mu.Lock()
	entry, ok := e.queuedNoteRuns[job.SessionID]
	e.mu.Unlock()
	if !ok || entry.runID != job.RunID {
		return
	}
	unlock := runruntime.LockClientToolConfirmations(job.SessionID)
	defer unlock()
	e.mu.Lock()
	current, ok := e.queuedNoteRuns[job.SessionID]
	if !ok || current.runID != job.RunID {
		e.mu.Unlock()
		return
	}
	delete(e.queuedNoteRuns, job.SessionID)
	e.mu.Unlock()

	intent, found, err := e.server.sessions.GetSessionRunIntent(job.SessionID, job.RunID)
	if err != nil || !found {
		return
	}
	if intent.Status != sessionruntime.RunIntentCompleted && intent.Status != sessionruntime.RunIntentFailed {
		return
	}
	// The session's message lock is held, so no note can land after this read.
	cursor := entry.tracker.Cursor()
	notes, err := e.sessionV3FeedbackSince(job.SessionID, &cursor)
	if err != nil {
		log.Printf("warning: queued notes for session %q after run %q could not be read: %v", job.SessionID, job.RunID, err)
		return
	}
	// Notes handed back (placed in input that no successful provider step
	// consumed) come first: they were sent before anything still unclaimed.
	var pending []pebblestore.MessageSnapshot
	for _, note := range append(entry.tracker.Returned(), notes...) {
		if runruntime.IsQueuedNote(note) {
			pending = append(pending, note)
		}
	}
	if len(pending) == 0 {
		return
	}
	if err := e.startQueuedNoteFollowup(job, pending[0]); err != nil {
		log.Printf("warning: queued notes for session %q could not start the next turn after run %q: %v", job.SessionID, job.RunID, err)
	}
}

// startQueuedNoteFollowup records a pending run whose context carries the
// queued notes as user messages. Its ID derives from the first note, so a
// repeat records nothing new. The executor's backlog refill starts it once the
// finishing run releases the session.
func (e *sessionV3Executor) startQueuedNoteFollowup(job sessionV3ExecutorJob, first pebblestore.MessageSnapshot) error {
	session, ok, err := e.server.sessions.GetSession(job.SessionID)
	if err != nil || !ok {
		return err
	}
	key := "queued-notes:" + first.ID
	runID := stableSessionsV3PrimaryRunID(job.SessionID, key)
	now := time.Now().UnixMilli()
	_, err = e.server.applySessionV3PrimaryMutation(sessionruntime.SessionMutationInput{
		SessionID: job.SessionID, UserID: session.UserID, AccountScopeID: session.AccountScopeID,
		ClientRequestID: key, IdempotencyKey: key, PayloadHash: key, RequestHash: key,
		Kind: sessionruntime.SessionMutationRecordRunIntent, NowUnixMs: now,
		RunIntent: &pebblestore.V3SessionRunIntent{
			SessionID: job.SessionID, UserID: session.UserID, AccountScopeID: session.AccountScopeID,
			RunID: runID, SourceMessageID: first.ID, RunSessionID: job.SessionID,
			Status: sessionruntime.RunIntentPendingExecutor, CreatedAt: now, UpdatedAt: now,
		},
	})
	return err
}

// carrySessionV3HistoryQueuedNotes makes a run's initial provider input carry
// every undelivered queued note in its context. Transcript-built input already
// holds them; checkpoint startup input (canonical checkpoint payload only) and
// the bounded handoff (summary plus latest turn) may not, so a missing note is
// appended as an ordinary user message. It returns the input and the notes it
// now carries.
func carrySessionV3HistoryQueuedNotes(input []map[string]any, notes []pebblestore.MessageSnapshot) ([]map[string]any, []pebblestore.MessageSnapshot) {
	var carried []pebblestore.MessageSnapshot
	for _, note := range notes {
		text := strings.TrimSpace(note.Content)
		if text == "" {
			continue
		}
		if !sessionV3InputCarriesText(input, text) {
			input = append(input, map[string]any{"role": "user", "content": []map[string]any{{"type": "input_text", "text": note.Content}}})
		}
		carried = append(carried, note)
	}
	return input, carried
}

// sessionV3NotesCarriedBy returns the notes whose text is in input. Only these
// may be receipted for a successful step that sent input.
func sessionV3NotesCarriedBy(input []map[string]any, notes []pebblestore.MessageSnapshot) []pebblestore.MessageSnapshot {
	var carried []pebblestore.MessageSnapshot
	for _, note := range notes {
		if text := strings.TrimSpace(note.Content); text != "" && sessionV3InputCarriesText(input, text) {
			carried = append(carried, note)
		}
	}
	return carried
}

func sessionV3InputCarriesText(input []map[string]any, text string) bool {
	encoded, err := json.Marshal(text)
	if err != nil || len(encoded) < 2 {
		return false
	}
	needle := string(encoded[1 : len(encoded)-1])
	for _, item := range input {
		raw, err := json.Marshal(item)
		if err == nil && strings.Contains(string(raw), needle) {
			return true
		}
	}
	return false
}

// sessionV3UndeliveredHistoryQueuedNotes returns queued notes at or before
// cursor (so in this run's context) that have no delivery receipt yet, within
// a bounded tail.
func (e *sessionV3Executor) sessionV3UndeliveredHistoryQueuedNotes(job sessionV3ExecutorJob, cursor uint64) ([]pebblestore.MessageSnapshot, error) {
	messages, err := e.server.sessions.ListSessionMessageTail(job.SessionID, sessionV3QueuedNoteHistoryWindow)
	if err != nil {
		return nil, err
	}
	receipted := make(map[string]bool)
	for _, message := range messages {
		if message.Role == "system" && message.Metadata["source"] == "feedback_delivery" {
			if id, _ := message.Metadata["message_id"].(string); id != "" {
				receipted[id] = true
			}
		}
	}
	var notes []pebblestore.MessageSnapshot
	for _, message := range messages {
		if message.GlobalSeq <= cursor && runruntime.IsQueuedNote(message) && !receipted[message.ID] {
			notes = append(notes, message)
		}
	}
	sort.SliceStable(notes, func(i, j int) bool { return notes[i].GlobalSeq < notes[j].GlobalSeq })
	return notes, nil
}
