package pebblestore

import (
	"encoding/json"
	"fmt"
	"strings"
	"time"

	"github.com/cockroachdb/pebble"
)

const sessionRecentIndexVersion = 1

type sessionRecentIndexMeta struct {
	Version   int   `json:"version"`
	IndexedAt int64 `json:"indexed_at"`
}

type sessionRecentIndexEntry struct {
	key       string
	sessionID string
}

func (s *SessionStore) ensureSessionRecentIndex() error {
	if s == nil || s.store == nil {
		return fmt.Errorf("session store is not configured")
	}
	var meta sessionRecentIndexMeta
	if ok, err := s.store.GetJSON(KeySessionRecentIndexMeta(), &meta); err != nil {
		return fmt.Errorf("read session recent index metadata: %w", err)
	} else if ok && meta.Version == sessionRecentIndexVersion {
		return nil
	}

	batch := s.store.NewBatch()
	defer batch.Close()

	if err := deleteSessionRecentIndexKeysFromReader(batch, s.store.db); err != nil {
		return err
	}
	if err := iteratePrefixFromReader(s.store.db, SessionPrefix(), sessionRecentIndexScanLimit(), func(_ string, value []byte) error {
		var session SessionSnapshot
		if err := json.Unmarshal(value, &session); err != nil {
			return fmt.Errorf("decode session for recent index backfill: %w", err)
		}
		session = normalizeSessionOwnership(session)
		if strings.TrimSpace(session.ID) == "" {
			return nil
		}
		return writeSessionRecentIndexEntriesInBatch(batch, session)
	}); err != nil {
		return err
	}
	payload, err := json.Marshal(sessionRecentIndexMeta{Version: sessionRecentIndexVersion, IndexedAt: time.Now().UnixMilli()})
	if err != nil {
		return fmt.Errorf("marshal session recent index metadata: %w", err)
	}
	if err := batch.Set([]byte(KeySessionRecentIndexMeta()), payload, nil); err != nil {
		return fmt.Errorf("write session recent index metadata: %w", err)
	}
	return batch.Commit(pebble.Sync)
}

func deleteSessionRecentIndexKeysFromReader(batch *pebble.Batch, reader pebble.Reader) error {
	if batch == nil || reader == nil {
		return fmt.Errorf("session recent index delete requires batch and reader")
	}
	return iteratePrefixFromReader(reader, "session_recent/", sessionRecentIndexScanLimit(), func(key string, _ []byte) error {
		if err := batch.Delete([]byte(key), nil); err != nil {
			return fmt.Errorf("delete session recent index key %q: %w", key, err)
		}
		return nil
	})
}

func replaceSessionRecentIndexInBatch(batch *pebble.Batch, previous *SessionSnapshot, next *SessionSnapshot) error {
	if batch == nil {
		return fmt.Errorf("session recent index update requires batch")
	}
	if previous != nil {
		for _, entry := range sessionRecentIndexEntries(*previous) {
			if err := batch.Delete([]byte(entry.key), nil); err != nil {
				return fmt.Errorf("delete session recent index key %q: %w", entry.key, err)
			}
		}
	}
	if next != nil {
		return writeSessionRecentIndexEntriesInBatch(batch, *next)
	}
	return nil
}

func writeSessionRecentIndexEntriesInBatch(batch *pebble.Batch, session SessionSnapshot) error {
	for _, entry := range sessionRecentIndexEntries(session) {
		if err := batch.Set([]byte(entry.key), []byte(entry.sessionID), nil); err != nil {
			return fmt.Errorf("write session recent index key %q: %w", entry.key, err)
		}
	}
	return nil
}

func sessionRecentIndexEntries(session SessionSnapshot) []sessionRecentIndexEntry {
	session = normalizeSessionOwnership(session)
	session.ID = strings.TrimSpace(session.ID)
	if session.ID == "" {
		return nil
	}
	workspacePath := strings.TrimSpace(session.WorkspacePath)
	if normalized, err := normalizeSessionPath(workspacePath); err == nil {
		workspacePath = normalized
	}
	accountScopeID := strings.TrimSpace(session.AccountScopeID)
	entries := []sessionRecentIndexEntry{{key: KeySessionRecentGlobal(session.UpdatedAt, session.ID), sessionID: session.ID}}
	if accountScopeID != "" {
		entries = append(entries, sessionRecentIndexEntry{key: KeySessionRecentForAccount(accountScopeID, session.UpdatedAt, session.ID), sessionID: session.ID})
	}
	if workspacePath != "" {
		entries = append(entries, sessionRecentIndexEntry{key: KeySessionRecentForWorkspace(workspacePath, session.UpdatedAt, session.ID), sessionID: session.ID})
		if accountScopeID != "" {
			entries = append(entries, sessionRecentIndexEntry{key: KeySessionRecentForAccountWorkspace(accountScopeID, workspacePath, session.UpdatedAt, session.ID), sessionID: session.ID})
		}
	}
	return entries
}

func (s *SessionStore) selectV3RecentWorksetSessionsFromIndex(reader pebble.Reader, options V3SessionWorksetOptions) ([]SessionSnapshot, V3SessionWorksetPagination, error) {
	limit := options.RecentLimit
	if limit <= 0 {
		return nil, V3SessionWorksetPagination{}, nil
	}
	prefix := sessionRecentIndexPrefixForWorkset(options)
	startKey := sessionRecentIndexStartAfter(prefix, options.RecentBeforeUpdatedAt, options.RecentBeforeSessionID)
	sessions := make([]SessionSnapshot, 0, limit+1)
	seen := map[string]struct{}{}
	err := scanRangeFromReader(reader, scanRangeOptions{Prefix: prefix, StartKey: startKey, Limit: sessionRecentIndexScanLimit()}, func(_ string, value []byte) (bool, error) {
		sessionID := strings.TrimSpace(string(value))
		if sessionID == "" {
			return true, nil
		}
		if _, ok := seen[sessionID]; ok {
			return true, nil
		}
		seen[sessionID] = struct{}{}
		session, ok, err := s.getSessionFromReader(reader, sessionID)
		if err != nil {
			return false, err
		}
		if !ok {
			return true, nil
		}
		if !v3SessionWorksetSessionVisibleForWorkspaces(session, options.AccountScopeID, options.WorkspacePath, options.WorkspacePaths) {
			return true, nil
		}
		sessions = append(sessions, session)
		return len(sessions) <= limit, nil
	})
	if err != nil {
		return nil, V3SessionWorksetPagination{}, err
	}
	pagination := V3SessionWorksetPagination{}
	if len(sessions) > limit {
		last := sessions[limit-1]
		updatedAt := last.UpdatedAt
		pagination.HasMore = true
		pagination.NextBeforeUpdatedAt = &updatedAt
		pagination.NextBeforeSessionID = last.ID
		sessions = sessions[:limit]
	}
	return sessions, pagination, nil
}

func sessionRecentIndexScanLimit() int {
	return int(^uint(0) >> 1)
}

func sessionRecentIndexPrefixForWorkset(options V3SessionWorksetOptions) string {
	accountScopeID := strings.TrimSpace(options.AccountScopeID)
	paths := options.WorkspacePaths
	if len(paths) == 0 {
		paths = normalizeV3SessionWorksetWorkspacePaths(options.WorkspacePath, nil)
	}
	if len(paths) == 1 {
		if accountScopeID != "" {
			return SessionRecentPrefixForAccountWorkspace(accountScopeID, paths[0])
		}
		return SessionRecentPrefixForWorkspace(paths[0])
	}
	if accountScopeID != "" {
		return SessionRecentPrefixForAccount(accountScopeID)
	}
	return SessionRecentGlobalPrefix()
}
