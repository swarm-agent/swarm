package pebblestore

import (
	"encoding/json"
	"fmt"
	"strings"

	"github.com/cockroachdb/pebble"
)

const (
	v3SessionLibraryIndexVersion = 1
	keyV3SessionLibraryMeta      = "v3/session_library/meta"
	keyV3SessionLibraryMetric    = "v3/session_library/metric/"
	keyV3SessionLibrarySummary   = "v3/session_library/summary/"
)

type V3SessionLibraryMetric struct {
	SessionID             string `json:"session_id"`
	ParentSessionID       string `json:"parent_session_id,omitempty"`
	RootSessionID         string `json:"root_session_id"`
	LineageKind           string `json:"lineage_kind,omitempty"`
	UnlinkedChild         bool   `json:"unlinked_child,omitempty"`
	UpdatedAt             int64  `json:"updated_at"`
	LogicalBytes          int64  `json:"logical_bytes"`
	ConversationUpdatedAt int64  `json:"conversation_updated_at"`
	ConversationBytes     int64  `json:"conversation_logical_bytes"`
	ConversationSessions  int    `json:"conversation_session_count"`
}

type V3SessionLibrarySummary struct {
	ActiveConversationCount   int   `json:"active_conversation_count"`
	ArchivedConversationCount int   `json:"archived_conversation_count"`
	RawSessionCount           int   `json:"raw_session_count"`
	AgentChildCount           int   `json:"agent_child_count"`
	LogicalContentBytes       int64 `json:"logical_content_bytes"`
}

type v3SessionLibraryIndexMeta struct {
	Version int `json:"version"`
}

type v3LibrarySession struct {
	session  SessionSnapshot
	archived bool
	bytes    int64
}

func keyV3SessionLibraryMetricFor(id string) string {
	return keyV3SessionLibraryMetric + strings.TrimSpace(id)
}
func keyV3SessionLibrarySummaryFor(account, user, workspace string) string {
	return keyV3SessionLibrarySummary + keyPart(account) + "/" + keyPart(user) + "/" + keyPart(workspace)
}

func v3LibraryMetadataString(metadata map[string]any, key string) string {
	if metadata == nil {
		return ""
	}
	value, _ := metadata[key].(string)
	return strings.TrimSpace(value)
}

// ResolveV3SessionLineage deterministically resolves roots. Missing parents and cycles
// stay attached to themselves and are explicitly marked unlinked.
func ResolveV3SessionLineage(sessions map[string]SessionSnapshot) map[string]V3SessionLibraryMetric {
	result := make(map[string]V3SessionLibraryMetric, len(sessions))
	for id, session := range sessions {
		parent := v3LibraryMetadataString(session.Metadata, "parent_session_id")
		metric := V3SessionLibraryMetric{SessionID: id, ParentSessionID: parent, RootSessionID: id, LineageKind: v3LibraryMetadataString(session.Metadata, "lineage_kind"), UpdatedAt: session.UpdatedAt}
		if parent != "" {
			seen := map[string]bool{id: true}
			cursor := parent
			for cursor != "" {
				if seen[cursor] {
					metric.UnlinkedChild = true
					break
				}
				seen[cursor] = true
				ancestor, ok := sessions[cursor]
				if !ok {
					metric.UnlinkedChild = true
					break
				}
				metric.RootSessionID = cursor
				cursor = v3LibraryMetadataString(ancestor.Metadata, "parent_session_id")
			}
		}
		result[id] = metric
	}
	return result
}

func (s *SessionStore) ensureV3SessionLibraryIndex() error {
	var meta v3SessionLibraryIndexMeta
	if ok, err := s.store.GetJSON(keyV3SessionLibraryMeta, &meta); err != nil {
		return err
	} else if ok && meta.Version == v3SessionLibraryIndexVersion {
		return nil
	}
	batch := s.store.NewBatch()
	defer batch.Close()
	if err := s.rebuildV3SessionLibraryIndexInBatch(batch, nil, nil, nil); err != nil {
		return err
	}
	payload, _ := json.Marshal(v3SessionLibraryIndexMeta{Version: v3SessionLibraryIndexVersion})
	if err := batch.Set([]byte(keyV3SessionLibraryMeta), payload, nil); err != nil {
		return err
	}
	return batch.Commit(pebble.Sync)
}

func (s *SessionStore) rebuildV3SessionLibraryIndexInBatch(batch *pebble.Batch, override *SessionSnapshot, appended *MessageSnapshot, removed map[string]struct{}) error {
	entries := map[string]v3LibrarySession{}
	if err := iteratePrefixFromReader(s.store.db, SessionPrefix(), sessionRecentIndexScanLimit(), func(_ string, value []byte) error {
		var session SessionSnapshot
		if err := json.Unmarshal(value, &session); err != nil {
			return err
		}
		if _, excluded := removed[session.ID]; !excluded {
			entries[session.ID] = v3LibrarySession{session: normalizeSessionOwnership(session)}
		}
		return nil
	}); err != nil {
		return err
	}
	if err := iteratePrefixFromReader(s.store.db, V3SessionTombstonePrefix(), sessionRecentIndexScanLimit(), func(_ string, value []byte) error {
		var tombstone V3SessionTombstone
		if err := json.Unmarshal(value, &tombstone); err != nil {
			return err
		}
		if tombstone.Archived && !tombstone.Deleted {
			if _, excluded := removed[tombstone.Session.ID]; !excluded {
				entries[tombstone.Session.ID] = v3LibrarySession{session: normalizeSessionOwnership(tombstone.Session), archived: true}
			}
		}
		return nil
	}); err != nil {
		return err
	}
	if override != nil {
		entries[override.ID] = v3LibrarySession{session: normalizeSessionOwnership(*override)}
	}
	if err := batch.DeleteRange([]byte(keyV3SessionLibraryMetric), []byte(keyV3SessionLibraryMetric+"\xff"), nil); err != nil {
		return err
	}
	if err := batch.DeleteRange([]byte(keyV3SessionLibrarySummary), []byte(keyV3SessionLibrarySummary+"\xff"), nil); err != nil {
		return err
	}

	snapshots := make(map[string]SessionSnapshot, len(entries))
	for id, entry := range entries {
		snapshots[id] = entry.session
	}
	lineage := ResolveV3SessionLineage(snapshots)
	rootUpdated := map[string]int64{}
	rootBytes := map[string]int64{}
	rootSessions := map[string]int{}
	for id, entry := range entries {
		var previous V3SessionLibraryMetric
		_, _ = getJSONFromReader(s.store.db, keyV3SessionLibraryMetricFor(id), &previous)
		logicalBytes := previous.LogicalBytes
		if logicalBytes == 0 {
			_ = scanRangeFromReader(s.store.db, scanRangeOptions{Prefix: V3SessionMessagePrefix(id), Limit: sessionRecentIndexScanLimit()}, func(_ string, value []byte) (bool, error) { logicalBytes += int64(len(value)); return true, nil })
		}
		if appended != nil && appended.SessionID == id {
			if payload, err := json.Marshal(appended); err == nil {
				logicalBytes += int64(len(payload))
			}
		}
		entry.bytes = logicalBytes
		entries[id] = entry
		root := lineage[id].RootSessionID
		rootBytes[root] += logicalBytes
		rootSessions[root]++
		if entry.session.UpdatedAt > rootUpdated[root] {
			rootUpdated[root] = entry.session.UpdatedAt
		}
	}
	for id := range entries {
		metric := lineage[id]
		metric.LogicalBytes = entries[id].bytes
		metric.ConversationUpdatedAt = rootUpdated[metric.RootSessionID]
		metric.ConversationBytes = rootBytes[metric.RootSessionID]
		metric.ConversationSessions = rootSessions[metric.RootSessionID]
		lineage[id] = metric
		payload, _ := json.Marshal(metric)
		if err := batch.Set([]byte(keyV3SessionLibraryMetricFor(id)), payload, nil); err != nil {
			return err
		}
	}

	type scopeKey struct{ account, user, workspace string }
	summaries := map[scopeKey]*V3SessionLibrarySummary{}
	roots := map[scopeKey]map[string]bool{}
	archivedRoots := map[scopeKey]map[string]bool{}
	for id, entry := range entries {
		metric := lineage[id]
		for _, workspace := range []string{"", entry.session.WorkspacePath} {
			key := scopeKey{entry.session.AccountScopeID, entry.session.UserID, workspace}
			if summaries[key] == nil {
				summaries[key] = &V3SessionLibrarySummary{}
				roots[key] = map[string]bool{}
				archivedRoots[key] = map[string]bool{}
			}
			summary := summaries[key]
			summary.RawSessionCount++
			summary.LogicalContentBytes += entry.bytes
			if metric.ParentSessionID != "" {
				summary.AgentChildCount++
			}
			if entry.archived {
				archivedRoots[key][metric.RootSessionID] = true
			} else {
				roots[key][metric.RootSessionID] = true
			}
		}
	}
	for key, summary := range summaries {
		summary.ActiveConversationCount = len(roots[key])
		summary.ArchivedConversationCount = len(archivedRoots[key])
		payload, _ := json.Marshal(summary)
		if err := batch.Set([]byte(keyV3SessionLibrarySummaryFor(key.account, key.user, key.workspace)), payload, nil); err != nil {
			return err
		}
	}
	return nil
}

func (s *SessionStore) GetV3SessionLibraryMetric(sessionID string) (V3SessionLibraryMetric, bool, error) {
	if err := s.ensureV3SessionLibraryIndex(); err != nil {
		return V3SessionLibraryMetric{}, false, err
	}
	var metric V3SessionLibraryMetric
	ok, err := getJSONFromReader(s.store.db, keyV3SessionLibraryMetricFor(sessionID), &metric)
	return metric, ok, err
}

func (s *SessionStore) v3SessionLibrarySummary(reader pebble.Reader, options V3SessionSearchOptions) (V3SessionLibrarySummary, error) {
	if len(options.WorkspacePaths) > 1 {
		return V3SessionLibrarySummary{}, fmt.Errorf("library summary requires global or one workspace")
	}
	workspace := ""
	if !options.Global && len(options.WorkspacePaths) == 1 {
		workspace = options.WorkspacePaths[0]
	}
	var summary V3SessionLibrarySummary
	_, err := getJSONFromReader(reader, keyV3SessionLibrarySummaryFor(options.AccountScopeID, options.UserID, workspace), &summary)
	return summary, err
}
