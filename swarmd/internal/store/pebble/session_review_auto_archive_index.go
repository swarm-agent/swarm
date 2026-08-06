package pebblestore

import (
	"encoding/json"
	"errors"
	"fmt"
	"sort"
	"strings"

	"github.com/cockroachdb/pebble"
)

const keySessionReviewAutoArchiveDuePrefix = "session_review_auto_archive_due/"

const reviewAutoArchiveAfterMetadataKey = "review_auto_archive_after"
const reviewDoneAtMetadataKey = "review_done_at"

// normalizeSessionForReactivation clears review completion scheduling state. An
// archived snapshot can retain an already-expired deadline; restoring it would
// recreate the due index and allow the review scheduler to archive it again
// before the user can continue the session.
func normalizeSessionForReactivation(session SessionSnapshot) SessionSnapshot {
	if len(session.Metadata) == 0 {
		return session
	}
	metadata := cloneSessionMetadataMap(session.Metadata)
	delete(metadata, reviewAutoArchiveAfterMetadataKey)
	delete(metadata, reviewDoneAtMetadataKey)
	if len(metadata) == 0 {
		metadata = nil
	}
	session.Metadata = metadata
	return session
}

// SessionReviewAutoArchiveDue is one bounded unit of due review-archive work.
// The index key is ordered by DueAt so callers never need to scan sessions.
type SessionReviewAutoArchiveDue struct {
	SessionID string `json:"session_id"`
	DueAt     int64  `json:"due_at"`
}

// SessionReviewAutoArchiveCandidate is a session whose active plan needs review
// or whose durable archive deadline must be reconciled (for example, after the
// setting is disabled). Candidate discovery walks only the active-plan and due
// indexes rather than a capped page of unrelated sessions.
type SessionReviewAutoArchiveCandidate struct {
	Session     SessionSnapshot
	NeedsReview bool
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

// ListSessionReviewAutoArchiveCandidates returns the complete indexed candidate
// set for the requested account scope. An empty account scans all active-plan
// pointers; account-scoped reconciliation uses the account index directly.
func (s *SessionStore) ListSessionReviewAutoArchiveCandidates(accountScopeID string) ([]SessionReviewAutoArchiveCandidate, error) {
	if s == nil || s.store == nil {
		return nil, errors.New("session store is not configured")
	}
	accountScopeID = strings.TrimSpace(accountScopeID)
	snapshot := s.store.db.NewSnapshot()
	defer snapshot.Close()

	candidates := map[string]SessionReviewAutoArchiveCandidate{}
	activePrefix := SessionPlanActivePrefix()
	if accountScopeID != "" {
		activePrefix = SessionPlanActiveByAccountPrefix(accountScopeID)
	}
	if err := iteratePrefixFromReader(snapshot, activePrefix, sessionRecentIndexScanLimit(), func(_ string, value []byte) error {
		var active SessionPlanActive
		if err := json.Unmarshal(value, &active); err != nil {
			return fmt.Errorf("decode active plan for review auto-archive: %w", err)
		}
		sessionID := strings.TrimSpace(active.SessionID)
		planID := strings.TrimSpace(active.PlanID)
		if sessionID == "" || planID == "" {
			return nil
		}
		attention, err := v3SessionAttentionFromReader(snapshot, sessionID)
		if err != nil {
			return err
		}
		if attention.State != "needs_review" {
			return nil
		}
		session, ok, err := s.getSessionFromReader(snapshot, sessionID)
		if err != nil || !ok {
			return err
		}
		if accountScopeID != "" && strings.TrimSpace(session.AccountScopeID) != accountScopeID {
			return nil
		}
		candidates[sessionID] = SessionReviewAutoArchiveCandidate{Session: session, NeedsReview: true}
		return nil
	}); err != nil {
		return nil, err
	}

	if err := iteratePrefixFromReader(snapshot, keySessionReviewAutoArchiveDuePrefix, sessionRecentIndexScanLimit(), func(_ string, value []byte) error {
		var due SessionReviewAutoArchiveDue
		if err := json.Unmarshal(value, &due); err != nil {
			return fmt.Errorf("decode review auto-archive due row: %w", err)
		}
		sessionID := strings.TrimSpace(due.SessionID)
		if sessionID == "" {
			return nil
		}
		session, ok, err := s.getSessionFromReader(snapshot, sessionID)
		if err != nil || !ok {
			return err
		}
		if accountScopeID != "" && strings.TrimSpace(session.AccountScopeID) != accountScopeID {
			return nil
		}
		candidate := candidates[sessionID]
		candidate.Session = session
		candidates[sessionID] = candidate
		return nil
	}); err != nil {
		return nil, err
	}

	result := make([]SessionReviewAutoArchiveCandidate, 0, len(candidates))
	for _, candidate := range candidates {
		result = append(result, candidate)
	}
	sort.Slice(result, func(i, j int) bool { return result[i].Session.ID < result[j].Session.ID })
	return result, nil
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
