package pebblestore

import (
	"encoding/json"
	"errors"
	"fmt"
	"strings"

	"github.com/cockroachdb/pebble"
)

const keySessionReviewAutoArchiveDuePrefix = "session_review_auto_archive_due/"

const reviewAutoArchiveAfterMetadataKey = "review_auto_archive_after"

// SessionReviewAutoArchiveDue is one bounded unit of due review-archive work.
// The index key is ordered by DueAt so callers never need to scan sessions.
type SessionReviewAutoArchiveDue struct {
	SessionID string `json:"session_id"`
	DueAt     int64  `json:"due_at"`
}

func KeySessionReviewAutoArchiveDue(dueAt int64, sessionID string) string {
	return fmt.Sprintf("%s%020d/%s", keySessionReviewAutoArchiveDuePrefix, dueAt, keyPart(sessionID))
}

func SessionReviewAutoArchiveDuePrefix() string {
	return keySessionReviewAutoArchiveDuePrefix
}

func sessionReviewAutoArchiveAfter(session SessionSnapshot) int64 {
	if session.Metadata == nil {
		return 0
	}
	switch value := session.Metadata[reviewAutoArchiveAfterMetadataKey].(type) {
	case int64:
		return value
	case int:
		return int64(value)
	case float64:
		return int64(value)
	case json.Number:
		parsed, _ := value.Int64()
		return parsed
	default:
		return 0
	}
}

func replaceSessionReviewAutoArchiveDueInBatch(batch *pebble.Batch, previous, next *SessionSnapshot) error {
	if batch == nil {
		return errors.New("review auto-archive index batch is required")
	}
	if previous != nil {
		if dueAt := sessionReviewAutoArchiveAfter(*previous); dueAt > 0 && strings.TrimSpace(previous.ID) != "" {
			if err := batch.Delete([]byte(KeySessionReviewAutoArchiveDue(dueAt, previous.ID)), nil); err != nil && !errors.Is(err, pebble.ErrNotFound) {
				return err
			}
		}
	}
	if next == nil {
		return nil
	}
	dueAt := sessionReviewAutoArchiveAfter(*next)
	if dueAt <= 0 || strings.TrimSpace(next.ID) == "" {
		return nil
	}
	payload, err := json.Marshal(SessionReviewAutoArchiveDue{SessionID: strings.TrimSpace(next.ID), DueAt: dueAt})
	if err != nil {
		return err
	}
	return batch.Set([]byte(KeySessionReviewAutoArchiveDue(dueAt, next.ID)), payload, nil)
}

// ListDueSessionReviewAutoArchives reads at most limit time-ordered due rows.
// Iterator bounds let Pebble skip unrelated key ranges and future work.
func (s *SessionStore) DeleteSessionReviewAutoArchiveDue(item SessionReviewAutoArchiveDue) error {
	if s == nil || s.store == nil {
		return errors.New("session store is not configured")
	}
	if item.DueAt <= 0 || strings.TrimSpace(item.SessionID) == "" {
		return nil
	}
	return s.store.Delete(KeySessionReviewAutoArchiveDue(item.DueAt, item.SessionID))
}

func (s *SessionStore) ListDueSessionReviewAutoArchives(nowUnixMs int64, limit int) ([]SessionReviewAutoArchiveDue, error) {
	if s == nil || s.store == nil {
		return nil, errors.New("session store is not configured")
	}
	if limit <= 0 {
		return nil, errors.New("review auto-archive due limit must be positive")
	}
	lower := []byte(keySessionReviewAutoArchiveDuePrefix)
	upper := []byte(fmt.Sprintf("%s%020d0", keySessionReviewAutoArchiveDuePrefix, nowUnixMs))
	iter, err := s.store.db.NewIter(&pebble.IterOptions{LowerBound: lower, UpperBound: upper})
	if err != nil {
		return nil, err
	}
	defer iter.Close()
	items := make([]SessionReviewAutoArchiveDue, 0, limit)
	for iter.First(); iter.Valid() && len(items) < limit; iter.Next() {
		var item SessionReviewAutoArchiveDue
		if err := json.Unmarshal(iter.Value(), &item); err != nil {
			return nil, fmt.Errorf("decode review auto-archive due row %q: %w", string(iter.Key()), err)
		}
		if item.SessionID != "" && item.DueAt > 0 {
			items = append(items, item)
		}
	}
	if err := iter.Error(); err != nil {
		return nil, err
	}
	return items, nil
}
