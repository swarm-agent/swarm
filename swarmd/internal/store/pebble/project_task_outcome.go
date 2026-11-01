package pebblestore

import (
	"encoding/json"
	"strings"
)

// completedTaskRunSummary reads only the terminal event addressed by the durable
// run state. It never searches the transcript or infers success from text.
func (s *SessionStore) completedTaskRunSummary(state V3SessionRunState) (string, bool) {
	if state.Active || state.Status != V3RunIntentCompleted || state.RunID == "" || state.EventSeq == 0 {
		return "", false
	}
	event, found, err := s.GetV3SessionEvent(state.SessionID, state.EventSeq)
	if err != nil || !found {
		return "", false
	}
	return completedTaskEventSummary(state, event)
}

func completedTaskEventSummary(state V3SessionRunState, event V3SessionEvent) (string, bool) {
	if state.Active || state.Status != V3RunIntentCompleted || state.RunID == "" || state.EventSeq == 0 || event.Seq != state.EventSeq || event.SessionID != state.SessionID || event.EventType != "session.assistant.completed" {
		return "", false
	}
	var payload v3SessionEventReplayPayload
	if json.Unmarshal(event.Payload, &payload) != nil || payload.RunIntent == nil || payload.RunIntent.SessionID != state.SessionID || payload.RunIntent.RunID != state.RunID || payload.RunIntent.AccountScopeID != state.AccountScopeID || payload.RunIntent.Status != V3RunIntentCompleted || payload.Message == nil || payload.Message.Role != "assistant" || payload.Message.Metadata["run_id"] != state.RunID {
		return "", false
	}
	summary := strings.TrimSpace(payload.Message.Content)
	if summary == "" {
		return "", false
	}
	// The handoff is at the end of the final message (what was done, the
	// tests run, the commit): a long message keeps its end and is marked;
	// the whole message is paged with read_messages (completedTaskRunMessage).
	summary, _ = ClipProjectTaskResultTail(summary, ProjectTaskWakeResultBytes)
	return summary, true
}

// ProjectTaskRunMessage is the exact final assistant message of a completed
// task run: what read_messages pages (message_id, message_seq) and its size.
type ProjectTaskRunMessage struct {
	SessionID string
	RunID     string
	MessageID string
	Seq       uint64
	Bytes     int
}

// completedTaskRunMessage is the final assistant message of the session's
// completed run, read from the terminal event its durable run state
// addresses (as completedTaskRunSummary reads it).
func (s *SessionStore) completedTaskRunMessage(state V3SessionRunState) (ProjectTaskRunMessage, bool) {
	if state.Active || state.Status != V3RunIntentCompleted || state.RunID == "" || state.EventSeq == 0 {
		return ProjectTaskRunMessage{}, false
	}
	event, found, err := s.GetV3SessionEvent(state.SessionID, state.EventSeq)
	if err != nil || !found {
		return ProjectTaskRunMessage{}, false
	}
	if _, ok := completedTaskEventSummary(state, event); !ok {
		return ProjectTaskRunMessage{}, false
	}
	var payload v3SessionEventReplayPayload
	if json.Unmarshal(event.Payload, &payload) != nil || payload.Message == nil || payload.Message.ID == "" {
		return ProjectTaskRunMessage{}, false
	}
	return ProjectTaskRunMessage{SessionID: state.SessionID, RunID: state.RunID, MessageID: payload.Message.ID, Seq: payload.Message.GlobalSeq, Bytes: len(strings.TrimSpace(payload.Message.Content))}, true
}
