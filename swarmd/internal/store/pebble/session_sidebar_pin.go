package pebblestore

import (
	"encoding/json"
	"sort"
	"strings"

	"github.com/cockroachdb/pebble"
)

const V3SessionDesktopSidebarPinnedMetadataKey = "swarm_v3_desktop_sidebar_pinned"

func v3SessionDesktopSidebarPinned(session SessionSnapshot) bool {
	return session.Metadata != nil && session.Metadata[V3SessionDesktopSidebarPinnedMetadataKey] == true
}

func (s *SessionStore) selectV3PinnedSidebarWorksetSessions(reader pebble.Reader, options V3SessionWorksetOptions) ([]SessionSnapshot, error) {
	return s.selectV3PinnedSidebarSessions(reader, options.AccountScopeID, options.UserID, options.WorkspacePath, options.WorkspacePaths)
}

func (s *SessionStore) selectV3PinnedSidebarSyncSnapshotSessions(reader pebble.Reader, options V3SyncSnapshotOptions) ([]SessionSnapshot, error) {
	return s.selectV3PinnedSidebarSessions(reader, options.AccountScopeID, options.UserID, options.WorkspacePath, options.WorkspacePaths)
}

func (s *SessionStore) selectV3PinnedSidebarSessions(reader pebble.Reader, accountScopeID, userID, workspacePath string, workspacePaths []string) ([]SessionSnapshot, error) {
	prefix := SessionByAccountPrefix(accountScopeID)
	if strings.TrimSpace(accountScopeID) == "" {
		prefix = SessionPrefix()
	}

	sessions := make([]SessionSnapshot, 0)
	err := iteratePrefixFromReader(reader, prefix, int(^uint(0)>>1), func(_ string, value []byte) error {
		sessionID := strings.TrimSpace(string(value))
		var session SessionSnapshot
		if strings.TrimSpace(accountScopeID) == "" {
			if err := json.Unmarshal(value, &session); err != nil {
				return err
			}
		} else {
			if sessionID == "" {
				return nil
			}
			loaded, ok, err := s.getSessionFromReader(reader, sessionID)
			if err != nil {
				return err
			}
			if !ok {
				return nil
			}
			session = loaded
		}
		if !v3SessionDesktopSidebarPinned(session) {
			return nil
		}
		if !v3SessionWorksetSessionVisibleForWorkspaces(session, accountScopeID, userID, workspacePath, workspacePaths) {
			return nil
		}
		sessions = append(sessions, session)
		return nil
	})
	if err != nil {
		return nil, err
	}

	sort.Slice(sessions, func(i, j int) bool {
		if sessions[i].UpdatedAt == sessions[j].UpdatedAt {
			return sessions[i].ID < sessions[j].ID
		}
		return sessions[i].UpdatedAt > sessions[j].UpdatedAt
	})
	return sessions, nil
}
