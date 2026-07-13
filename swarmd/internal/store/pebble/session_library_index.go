package pebblestore

import (
	"encoding/json"
	"fmt"
	"strings"

	"github.com/cockroachdb/pebble"
)

const (
	v3SessionLibraryIndexVersion = 3
	keyV3SessionLibraryMeta      = "v3/session_library/meta"
	keyV3SessionLibraryMetric    = "v3/session_library/metric/"
	keyV3SessionLibrarySummary   = "v3/session_library/summary/" // v1 repair cleanup only
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

	// Scope and lifecycle fields make every metric a self-contained contribution
	// that can be folded into exact summaries from a consistent snapshot.
	AccountScopeID   string `json:"account_scope_id,omitempty"`
	UserID           string `json:"user_id,omitempty"`
	WorkspacePath    string `json:"workspace_path,omitempty"`
	Archived         bool   `json:"archived,omitempty"`
	NavigationHidden bool   `json:"navigation_hidden,omitempty"`
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

	// A versioned repair is the only full-library rewrite. Exclude live incremental
	// writers for the scan plus atomic replacement so it cannot erase their rows.
	s.store.sessionMutations.libraryRepairMu.Lock()
	defer s.store.sessionMutations.libraryRepairMu.Unlock()
	if ok, err := s.store.GetJSON(keyV3SessionLibraryMeta, &meta); err != nil {
		return err
	} else if ok && meta.Version == v3SessionLibraryIndexVersion {
		return nil
	}
	batch := s.store.NewBatch()
	defer batch.Close()
	if err := s.rebuildV3SessionLibraryIndexInBatch(batch); err != nil {
		return err
	}
	payload, _ := json.Marshal(v3SessionLibraryIndexMeta{Version: v3SessionLibraryIndexVersion})
	if err := batch.Set([]byte(keyV3SessionLibraryMeta), payload, nil); err != nil {
		return err
	}
	return batch.Commit(pebble.Sync)
}

// rebuildV3SessionLibraryIndexInBatch is reserved for versioned backfill/repair.
func (s *SessionStore) rebuildV3SessionLibraryIndexInBatch(batch *pebble.Batch) error {
	entries := map[string]v3LibrarySession{}
	if err := iteratePrefixFromReader(s.store.db, SessionPrefix(), sessionRecentIndexScanLimit(), func(_ string, value []byte) error {
		var session SessionSnapshot
		if err := json.Unmarshal(value, &session); err != nil {
			return err
		}
		entries[session.ID] = v3LibrarySession{session: normalizeSessionOwnership(session)}
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
			entries[tombstone.Session.ID] = v3LibrarySession{session: normalizeSessionOwnership(tombstone.Session), archived: true}
		}
		return nil
	}); err != nil {
		return err
	}
	if err := batch.DeleteRange([]byte(keyV3SessionLibraryMetric), []byte(keyV3SessionLibraryMetric+"\xff"), nil); err != nil {
		return err
	}
	if err := batch.DeleteRange([]byte(keyV3SessionLibrarySummary), []byte(keyV3SessionLibrarySummary+"\xff"), nil); err != nil {
		return err
	}
	for id, entry := range entries {
		var logicalBytes int64
		if err := scanRangeFromReader(s.store.db, scanRangeOptions{Prefix: V3SessionMessagePrefix(id), Limit: sessionRecentIndexScanLimit()}, func(_ string, value []byte) (bool, error) {
			logicalBytes += int64(len(value))
			return true, nil
		}); err != nil {
			return err
		}
		metric := v3LibraryBaseMetric(entry.session, logicalBytes, entry.archived)
		payload, _ := json.Marshal(metric)
		if err := batch.Set([]byte(keyV3SessionLibraryMetricFor(id)), payload, nil); err != nil {
			return err
		}
	}
	return nil
}

func v3LibraryBaseMetric(session SessionSnapshot, logicalBytes int64, archived bool) V3SessionLibraryMetric {
	session = normalizeSessionOwnership(session)
	return V3SessionLibraryMetric{
		SessionID: session.ID, ParentSessionID: v3LibraryMetadataString(session.Metadata, "parent_session_id"),
		RootSessionID: session.ID, LineageKind: v3LibraryMetadataString(session.Metadata, "lineage_kind"),
		UpdatedAt: session.UpdatedAt, LogicalBytes: logicalBytes, AccountScopeID: session.AccountScopeID,
		UserID: session.UserID, WorkspacePath: session.WorkspacePath, Archived: archived,
		NavigationHidden: V3SessionNavigationHidden(session),
	}
}

// updateV3SessionLibraryMetricInBatch changes exactly one session contribution.
// Per-session mutation locking prevents lost byte increments; disjoint sessions
// write disjoint keys and may enter Pebble's commit pipeline concurrently.
func (s *SessionStore) updateV3SessionLibraryMetricInBatch(batch *pebble.Batch, session SessionSnapshot, appended *MessageSnapshot, archived, deleted bool) error {
	key := keyV3SessionLibraryMetricFor(session.ID)
	if deleted {
		return batch.Delete([]byte(key), nil)
	}
	var previous V3SessionLibraryMetric
	_, err := getJSONFromReader(s.store.db, key, &previous)
	if err != nil {
		return err
	}
	logicalBytes := previous.LogicalBytes
	if appended != nil {
		payload, err := json.Marshal(appended)
		if err != nil {
			return err
		}
		logicalBytes += int64(len(payload))
	}
	metric := v3LibraryBaseMetric(session, logicalBytes, archived)
	payload, err := json.Marshal(metric)
	if err != nil {
		return err
	}
	return batch.Set([]byte(key), payload, nil)
}

func v3LibraryMetricsFromReader(reader pebble.Reader) (map[string]V3SessionLibraryMetric, error) {
	metrics := map[string]V3SessionLibraryMetric{}
	err := iteratePrefixFromReader(reader, keyV3SessionLibraryMetric, sessionRecentIndexScanLimit(), func(_ string, value []byte) error {
		var metric V3SessionLibraryMetric
		if err := json.Unmarshal(value, &metric); err != nil {
			return err
		}
		metrics[metric.SessionID] = metric
		return nil
	})
	if err != nil {
		return nil, err
	}
	// Resolve current lineage from contribution rows, so parent metadata changes
	// immediately affect descendants without rewriting unrelated session rows.
	for id, metric := range metrics {
		metric.RootSessionID, metric.UnlinkedChild = id, false
		if metric.ParentSessionID != "" {
			seen, cursor := map[string]bool{id: true}, metric.ParentSessionID
			for cursor != "" {
				if seen[cursor] {
					metric.UnlinkedChild = true
					break
				}
				seen[cursor] = true
				ancestor, ok := metrics[cursor]
				if !ok {
					metric.UnlinkedChild = true
					break
				}
				metric.RootSessionID = cursor
				cursor = ancestor.ParentSessionID
			}
		}
		metrics[id] = metric
	}
	type aggregate struct {
		bytes    int64
		sessions int
		updated  int64
	}
	aggregates := map[string]aggregate{}
	for _, metric := range metrics {
		a := aggregates[metric.RootSessionID]
		a.bytes += metric.LogicalBytes
		a.sessions++
		if metric.UpdatedAt > a.updated {
			a.updated = metric.UpdatedAt
		}
		aggregates[metric.RootSessionID] = a
	}
	for id, metric := range metrics {
		a := aggregates[metric.RootSessionID]
		metric.ConversationBytes, metric.ConversationSessions, metric.ConversationUpdatedAt = a.bytes, a.sessions, a.updated
		metrics[id] = metric
	}
	return metrics, nil
}

func (s *SessionStore) GetV3SessionLibraryMetric(sessionID string) (V3SessionLibraryMetric, bool, error) {
	if err := s.ensureV3SessionLibraryIndex(); err != nil {
		return V3SessionLibraryMetric{}, false, err
	}
	metrics, err := v3LibraryMetricsFromReader(s.store.db)
	if err != nil {
		return V3SessionLibraryMetric{}, false, err
	}
	metric, ok := metrics[sessionID]
	return metric, ok, nil
}

func (s *SessionStore) v3SessionLibrarySummary(reader pebble.Reader, options V3SessionSearchOptions) (V3SessionLibrarySummary, error) {
	if len(options.WorkspacePaths) > 1 {
		return V3SessionLibrarySummary{}, fmt.Errorf("library summary requires global or one workspace")
	}
	workspace := ""
	if !options.Global && len(options.WorkspacePaths) == 1 {
		workspace = options.WorkspacePaths[0]
	}
	metrics, err := v3LibraryMetricsFromReader(reader)
	if err != nil {
		return V3SessionLibrarySummary{}, err
	}
	var summary V3SessionLibrarySummary
	activeRoots, archivedRoots := map[string]bool{}, map[string]bool{}
	for _, metric := range metrics {
		if metric.NavigationHidden {
			continue
		}
		if metric.AccountScopeID != options.AccountScopeID || metric.UserID != options.UserID || (workspace != "" && metric.WorkspacePath != workspace) {
			continue
		}
		summary.RawSessionCount++
		summary.LogicalContentBytes += metric.LogicalBytes
		if metric.ParentSessionID != "" {
			summary.AgentChildCount++
		}
		if metric.Archived {
			archivedRoots[metric.RootSessionID] = true
		} else {
			activeRoots[metric.RootSessionID] = true
		}
	}
	summary.ActiveConversationCount = len(activeRoots)
	summary.ArchivedConversationCount = len(archivedRoots)
	return summary, nil
}
