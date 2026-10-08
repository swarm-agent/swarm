package api

import pebblestore "swarm/packages/swarmd/internal/store/pebble"

func (e *sessionV3Executor) sessionV3FeedbackCursor(sessionID string) (uint64, error) {
	messages, err := e.server.sessions.ListSessionMessageTail(sessionID, 1)
	if err != nil {
		return 0, err
	}
	var cursor uint64
	for _, message := range messages {
		if message.GlobalSeq > cursor {
			cursor = message.GlobalSeq
		}
	}
	return cursor, nil
}

// Read a finite snapshot of the append-only queue, in bounded pages. Notes
// appended after the snapshot wait for the next step, never a new execution.
// The caller commits the cursor only after the consuming step and receipt succeed.
func (e *sessionV3Executor) sessionV3FeedbackSince(sessionID string, cursor *uint64) ([]pebblestore.MessageSnapshot, error) {
	end, err := e.sessionV3FeedbackCursor(sessionID)
	if err != nil {
		return nil, err
	}
	var notes []pebblestore.MessageSnapshot
	for *cursor < end {
		messages, err := e.server.sessions.ListSessionMessages(sessionID, *cursor, 100)
		if err != nil {
			return nil, err
		}
		if len(messages) == 0 {
			break
		}
		for _, message := range messages {
			if message.GlobalSeq > end {
				break
			}
			*cursor = message.GlobalSeq
			if message.Role == "user" && message.Metadata["feedback_note"] == true {
				notes = append(notes, message)
			}
		}
	}
	return notes, nil
}
