package pebblestore

import (
	"encoding/json"
	"strings"
	"unicode/utf8"
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
	const marker = " [truncated; retrieve completed run]"
	if len(summary) > 4000 {
		summary = summary[:4000-len(marker)]
		for !utf8.ValidString(summary) {
			summary = summary[:len(summary)-1]
		}
		summary += marker
	}
	return summary, true
}
