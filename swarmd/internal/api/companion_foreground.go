package api

import (
	"encoding/json"
	"log"
	"math"
	"sort"
	"strings"

	runruntime "swarm/packages/swarmd/internal/run"
	sessionruntime "swarm/packages/swarmd/internal/session"
	pebblestore "swarm/packages/swarmd/internal/store/pebble"
)

// Foreground and background coordination of the Companion. The person's
// messages, background results and the Companion's own turns all meet in
// one serialized run per session (the executor runs one turn at a time); this
// file keeps the two places where that could lose or misreport input honest:
//
//   - In-flight tool calls are tracked per run, so the Companion's activity can
//     say truthfully that a message sent now waits for the running call (a
//     device command is not interrupted by new input) rather than claiming it
//     reaches the model at once.
//   - The person's queued messages survive a run the daemon ended without the
//     person stopping it (a restart interrupted it, or shutdown cancelled it):
//     they start the Companion's next turn instead of waiting until the person
//     writes again. A run the person stopped starts nothing, as before; the
//     interrupted run itself is not replayed (its tools may already have acted).

const (
	// companionActivityToolCallsMax bounds the in-flight tool calls listed.
	companionActivityToolCallsMax = 8
	// companionActivityDeliveriesMax bounds the background deliveries listed
	// (each costs one receipt point read).
	companionActivityDeliveriesMax = 20

	// CompanionInputStartsTurn: nothing runs; a message sent now starts a turn.
	CompanionInputStartsTurn = "starts_turn"
	// CompanionInputNextStep: a model step runs; a message sent now reaches
	// the Companion at its next step.
	CompanionInputNextStep = "next_step"
	// CompanionInputAfterToolCalls: tool calls run; a message sent now waits
	// for them to finish. They are not interrupted by it.
	CompanionInputAfterToolCalls = "after_tool_calls"

	// CompanionDeliveryWaitingForRun: a background result was handed to the
	// active run as a note; its next step reads it.
	CompanionDeliveryWaitingForRun = "waiting_for_run"
	// CompanionDeliveryRedelivering: the run it was handed to ended without
	// reading it; it is delivered again as its own turn.
	CompanionDeliveryRedelivering = "redelivering"
)

// sessionV3InFlightTool is a tool call of a run that started and has not
// recorded its terminal event yet.
type sessionV3InFlightTool struct {
	RunID     string
	CallID    string
	ToolName  string
	StartedAt int64
}

// noteToolStarted records a started tool call of job's run.
func (e *sessionV3Executor) noteToolStarted(job sessionV3ExecutorJob, event runruntime.StreamEvent, at int64) {
	callID := strings.TrimSpace(event.CallID)
	if e == nil || callID == "" {
		return
	}
	e.mu.Lock()
	defer e.mu.Unlock()
	if e.inFlightTools == nil {
		e.inFlightTools = make(map[string]map[string]sessionV3InFlightTool)
	}
	calls := e.inFlightTools[job.SessionID]
	if calls == nil {
		calls = make(map[string]sessionV3InFlightTool)
		e.inFlightTools[job.SessionID] = calls
	}
	if _, seen := calls[callID]; seen {
		return
	}
	calls[callID] = sessionV3InFlightTool{RunID: job.RunID, CallID: callID, ToolName: strings.TrimSpace(event.ToolName), StartedAt: at}
}

// noteToolEnded drops a tool call whose terminal event was recorded.
func (e *sessionV3Executor) noteToolEnded(job sessionV3ExecutorJob, input sessionruntime.SessionMutationInput) {
	if e == nil || !sessionV3ProviderToolTerminalEvent(input.EventType) {
		return
	}
	callID := ""
	if input.Message != nil {
		callID, _ = input.Message.Metadata["call_id"].(string)
	}
	if strings.TrimSpace(callID) == "" && len(input.EventPayload) > 0 {
		var payload struct {
			CallID string `json:"call_id"`
		}
		_ = json.Unmarshal(input.EventPayload, &payload)
		callID = payload.CallID
	}
	callID = strings.TrimSpace(callID)
	if callID == "" {
		return
	}
	e.mu.Lock()
	defer e.mu.Unlock()
	if calls := e.inFlightTools[job.SessionID]; calls != nil && calls[callID].RunID == job.RunID {
		delete(calls, callID)
		if len(calls) == 0 {
			delete(e.inFlightTools, job.SessionID)
		}
	}
}

// clearToolCalls drops every call of job's run (it ended).
func (e *sessionV3Executor) clearToolCalls(job sessionV3ExecutorJob) {
	if e == nil {
		return
	}
	e.mu.Lock()
	defer e.mu.Unlock()
	calls := e.inFlightTools[job.SessionID]
	for id, call := range calls {
		if call.RunID == job.RunID {
			delete(calls, id)
		}
	}
	if len(calls) == 0 {
		delete(e.inFlightTools, job.SessionID)
	}
}

// inFlightToolCalls lists runID's tool calls in flight, oldest first.
func (e *sessionV3Executor) inFlightToolCalls(sessionID, runID string) []sessionV3InFlightTool {
	if e == nil || strings.TrimSpace(runID) == "" {
		return nil
	}
	e.mu.Lock()
	var out []sessionV3InFlightTool
	for _, call := range e.inFlightTools[strings.TrimSpace(sessionID)] {
		if call.RunID == runID {
			out = append(out, call)
		}
	}
	e.mu.Unlock()
	sort.Slice(out, func(i, j int) bool {
		if out[i].StartedAt != out[j].StartedAt {
			return out[i].StartedAt < out[j].StartedAt
		}
		return out[i].CallID < out[j].CallID
	})
	return out
}

// companionRunEndLeavesQueuedInput reports whether a Companion run that ended
// as intent hands the person's undelivered queued messages to a follow-up
// turn beyond the ordinary rule (completed or failed): when the daemon ended
// it without the person stopping it, by a restart (interrupted) or by its
// shutdown (cancelled while shutting down). A person's stop starts nothing.
func companionRunEndLeavesQueuedInput(intent sessionruntime.SessionRunIntent, shuttingDown bool) bool {
	switch intent.Status {
	case sessionruntime.RunIntentInterrupted:
		return true
	case sessionruntime.RunIntentCancelled:
		return shuttingDown && !pebblestore.V3RunIntentStoppedByPerson(intent)
	}
	return false
}

// resumeCompanionQueuedInput starts the Companion's next turn for the person's
// messages queued for job's run that no provider step received (no delivery
// receipt), after the daemon ended that run (companionRunEndLeavesQueuedInput).
// The turn's ID derives from the first message, so a repeat (a second
// recovery pass, a restart during this one) records nothing new. Any other
// session, or a run the person stopped, starts nothing.
func (e *sessionV3Executor) resumeCompanionQueuedInput(job sessionV3ExecutorJob) error {
	if e == nil || e.server == nil || e.server.sessions == nil {
		return nil
	}
	session, found, err := e.server.sessions.GetSession(job.SessionID)
	if err != nil || !found || !runruntime.IsCompanionSession(session) {
		return err
	}
	unlock := runruntime.LockClientToolConfirmations(job.SessionID)
	defer unlock()
	intent, found, err := e.server.sessions.GetSessionRunIntent(job.SessionID, job.RunID)
	if err != nil || !found || !companionRunEndLeavesQueuedInput(intent, e.server.shuttingDown.Load()) {
		return err
	}
	notes, err := e.sessionV3UndeliveredHistoryQueuedNotes(job, math.MaxUint64)
	if err != nil {
		return err
	}
	for _, note := range notes {
		if runID, _ := note.Metadata[runruntime.QueuedNoteRunMetadataKey].(string); strings.TrimSpace(runID) == job.RunID && runruntime.CompanionPersonMessage(note) {
			return e.startQueuedNoteFollowup(job, note)
		}
	}
	return nil
}

// isCompanionSession reports whether sessionID is a Companion session.
func (e *sessionV3Executor) isCompanionSession(sessionID string) bool {
	session, found, err := e.server.sessions.GetSession(sessionID)
	return err == nil && found && runruntime.IsCompanionSession(session)
}

// resumeCompanionQueuedInputLogged is resumeCompanionQueuedInput for paths
// that cannot fail on it (run end, recovery).
func (e *sessionV3Executor) resumeCompanionQueuedInputLogged(job sessionV3ExecutorJob) {
	if err := e.resumeCompanionQueuedInput(job); err != nil {
		log.Printf("warning: companion queued input for session %q after run %q could not start the next turn: %v", job.SessionID, job.RunID, err)
	}
}

// CompanionToolCall is a tool call of the Companion's active run in flight.
// New input waits for it; it is not interrupted by new input.
type CompanionToolCall struct {
	CallID    string `json:"call_id"`
	ToolName  string `json:"tool_name"`
	StartedAt int64  `json:"started_at"`
	// Device is true for a call on the person's device or a linked machine
	// (a command, file or browser action), which runs to completion there.
	Device bool `json:"device,omitempty"`
}

// CompanionBackgroundDelivery is a background result (a task outcome or
// milestone, a background command, an environment or machine watch) handed to
// a run as a note that no provider step has read yet.
type CompanionBackgroundDelivery struct {
	WakeID    string `json:"wake_id"`
	Source    string `json:"source"`
	RunID     string `json:"run_id"`
	Status    string `json:"status"`
	CreatedAt int64  `json:"created_at,omitempty"`
}

// CompanionContextFreshness is how far the maintained context (state,
// continuity) has followed the Companion's event log, read from the state's
// cursor and the session's projection head (two point reads).
type CompanionContextFreshness struct {
	Cursor    uint64 `json:"cursor"`
	LatestSeq uint64 `json:"latest_seq"`
	Pending   uint64 `json:"pending"`
	CompanionMaintenanceView
}

// companionToolCallsActivity lists the active run's in-flight tool calls and
// says when input sent now reaches the Companion.
func (s *Server) companionToolCallsActivity(sessionID, activeRunID string) ([]CompanionToolCall, bool, string) {
	if activeRunID == "" {
		return []CompanionToolCall{}, false, CompanionInputStartsTurn
	}
	calls := s.v3SessionExecutor.inFlightToolCalls(sessionID, activeRunID)
	// A call still waiting at the permission gate (an approval, or a client
	// tool such as a device command waiting for the person's app) has no
	// started event yet: its pending permission record is the run's own
	// durable record of it. One bounded read of the session's pending
	// records.
	seen := make(map[string]bool, len(calls))
	for _, call := range calls {
		seen[call.CallID] = true
	}
	if s.perm != nil {
		pending, err := s.perm.ListPending(sessionID, companionActivityToolCallsMax+1)
		if err != nil {
			log.Printf("warning: companion activity could not list pending calls for session %q: %v", sessionID, err)
		}
		for _, record := range pending {
			callID := strings.TrimSpace(record.CallID)
			if record.RunID != activeRunID || callID == "" || seen[callID] {
				continue
			}
			seen[callID] = true
			calls = append(calls, sessionV3InFlightTool{RunID: activeRunID, CallID: callID, ToolName: record.ToolName, StartedAt: firstNonZeroInt64(record.PermissionRequested, record.CreatedAt)})
		}
	}
	out := make([]CompanionToolCall, 0, min(len(calls), companionActivityToolCallsMax))
	for _, call := range calls {
		if len(out) == companionActivityToolCallsMax {
			break
		}
		out = append(out, CompanionToolCall{CallID: call.CallID, ToolName: call.ToolName, StartedAt: call.StartedAt, Device: pebblestore.IsMachineToolName(call.ToolName) || pebblestore.IsBrowserToolName(call.ToolName)})
	}
	if len(calls) > 0 {
		return out, len(calls) > len(out), CompanionInputAfterToolCalls
	}
	return out, false, CompanionInputNextStep
}

func firstNonZeroInt64(values ...int64) int64 {
	for _, value := range values {
		if value != 0 {
			return value
		}
	}
	return 0
}

// companionBackgroundDeliveries lists the session's wake notes no run has read
// yet, oldest first: one bounded note scan and one receipt point read per
// listed note. A note whose receipt exists was read and only awaits settling.
func (s *Server) companionBackgroundDeliveries(sessionID, activeRunID string) ([]CompanionBackgroundDelivery, bool, error) {
	store := s.sessions.Store()
	notes, err := store.ListV3SessionWakeNotes(sessionID)
	if err != nil {
		return nil, false, err
	}
	sort.SliceStable(notes, func(i, j int) bool { return notes[i].CreatedAt < notes[j].CreatedAt })
	out := []CompanionBackgroundDelivery{}
	for _, note := range notes {
		if _, read, err := store.GetV3MessageByID(note.SessionID, pebblestore.SessionWakeReceiptID(note.NoteID, note.RunID)); err != nil {
			return nil, false, err
		} else if read {
			continue
		}
		if len(out) == companionActivityDeliveriesMax {
			return out, true, nil
		}
		status := CompanionDeliveryRedelivering
		if note.RunID == activeRunID && activeRunID != "" {
			status = CompanionDeliveryWaitingForRun
		}
		out = append(out, CompanionBackgroundDelivery{WakeID: note.WakeID, Source: note.Source, RunID: note.RunID, Status: status, CreatedAt: note.CreatedAt})
	}
	return out, false, nil
}

// companionContextFreshness reads the state cursor against the projection
// head. A state of another session or generation counts as none.
func (s *Server) companionContextFreshness(session pebblestore.SessionSnapshot) (*CompanionContextFreshness, error) {
	store := s.sessions.Store()
	out := &CompanionContextFreshness{}
	state, stored, err := store.GetCompanionState(session.AccountScopeID, session.UserID)
	if err != nil {
		return nil, err
	}
	if stored && state.SessionID == session.ID {
		out.Cursor = state.Cursor.EventSeq
	}
	projection, found, err := store.GetV3SessionProjection(session.ID)
	if err != nil {
		return nil, err
	}
	out.LatestSeq = out.Cursor
	if found && projection.LastEventSeq > out.Cursor {
		out.LatestSeq = projection.LastEventSeq
	}
	out.Pending = out.LatestSeq - out.Cursor
	out.CompanionMaintenanceView = s.companionMaintenanceStatus(session.ID, out.Pending)
	return out, nil
}
