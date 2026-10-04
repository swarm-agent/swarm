package pebblestore

import (
	"encoding/json"
	"errors"
	"strings"

	"github.com/cockroachdb/pebble"
)

// V3RecentMessageByteBudget bounds the JSON message array, not the surrounding
// session envelope. A single indivisible oversized record is returned alone so
// clients can always advance before_seq without losing content.
const V3RecentMessageByteBudget = 256 * 1024

func (s *SessionStore) ListV3SessionMessagesBeforeByteBudget(sessionID string, beforeSeq uint64, limit, maxBytes int) ([]MessageSnapshot, bool, error) {
	return listV3SessionMessagesBeforeByteBudget(s.store.db, sessionID, beforeSeq, limit, maxBytes)
}

func listV3SessionMessagesBeforeByteBudget(reader pebble.Reader, sessionID string, beforeSeq uint64, limit, maxBytes int) ([]MessageSnapshot, bool, error) {
	sessionID = strings.TrimSpace(sessionID)
	if sessionID == "" || limit <= 0 || maxBytes <= 0 {
		return nil, false, errors.New("session id and positive message count/byte budgets are required")
	}
	out := []MessageSnapshot{}
	used, more := 2, false // JSON array brackets
	start := ""
	if beforeSeq > 0 {
		start = KeyV3SessionMessage(sessionID, beforeSeq)
	}
	err := scanRangeFromReader(reader, scanRangeOptions{Prefix: V3SessionMessagePrefix(sessionID), StartKey: start, Reverse: true, Limit: limit + 1}, func(_ string, value []byte) (bool, error) {
		// Stop before decoding the next potentially multi-megabyte record. Raw
		// persisted JSON is a conservative bound; verify the wire size below too.
		if len(out) > 0 && (len(out) >= limit || used+1+len(value) > maxBytes) {
			more = true
			return false, nil
		}
		var message MessageSnapshot
		if err := json.Unmarshal(value, &message); err != nil {
			return false, err
		}
		message.Metadata = sanitizeMessageMetadata(message.Metadata)
		wire, err := json.Marshal(message)
		if err != nil {
			return false, err
		}
		size := len(wire)
		if len(out) > 0 {
			size++
			if used+size > maxBytes {
				more = true
				return false, nil
			}
		}
		out = append(out, message)
		used += size
		return true, nil
	})
	if err != nil {
		return nil, false, err
	}
	for i, j := 0, len(out)-1; i < j; i, j = i+1, j-1 {
		out[i], out[j] = out[j], out[i]
	}
	return out, more, nil
}
