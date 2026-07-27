package pebblestore

import (
	"encoding/base64"
	"encoding/json"
	"errors"
	"fmt"
	"sort"
	"strings"
	"time"
	"unicode"
	"unicode/utf8"

	"github.com/cockroachdb/pebble"
)

const (
	v3SessionSearchIndexVersion     = 3
	keyV3SessionSearchIndexMeta     = "v3/session_search_index/meta"
	keyV3SessionSearchMetaPrefix    = "v3/session_search_meta/"
	keyV3SessionSearchPostingPrefix = "v3/session_search_posting/"
	// keyV3SessionSearchAccountPrefix is the read-only v2 compatibility index.
	// New postings are session-prefixed and never contain volatile UpdatedAt.
	keyV3SessionSearchAccountPrefix = "v3/session_search/account/"
	v3SessionSearchArchivedExclude  = "exclude"
	v3SessionSearchArchivedInclude  = "include"
	v3SessionSearchArchivedOnly     = "only"
	v3SessionSearchDefaultLimit     = 50
	v3SessionSearchMaxLimit         = 50
	v3SessionSearchSnippetMaxRunes  = 240
	v3SessionSearchMaxQueries       = 8
	v3SessionSearchMaxQueryRunes    = 200
)

type V3SessionSearchOptions struct {
	AccountScopeID  string
	UserID          string
	Query           string
	Queries         []string
	State           string
	ArchivedMode    string
	Global          bool
	WorkspacePath   string
	WorkspacePaths  []string
	FromUpdatedAt   *int64
	ToUpdatedAt     *int64
	BeforeUpdatedAt *int64
	BeforeSessionID string
	Limit           int
}

type V3SessionSearchResult struct {
	Items      []V3SessionSearchItem     `json:"items"`
	Pagination V3SessionSearchPagination `json:"pagination"`
	Summary    V3SessionLibrarySummary   `json:"summary"`
}

type V3SessionSearchPagination struct {
	NextCursor          string `json:"next_cursor,omitempty"`
	NextBeforeUpdatedAt *int64 `json:"next_before_updated_at,omitempty"`
	NextBeforeSessionID string `json:"next_before_session_id,omitempty"`
	HasMore             bool   `json:"has_more"`
}

type V3SessionSearchItem struct {
	ID                      string                    `json:"id"`
	UserID                  string                    `json:"user_id,omitempty"`
	AccountScopeID          string                    `json:"account_scope_id,omitempty"`
	WorkspacePath           string                    `json:"workspace_path"`
	WorkspaceName           string                    `json:"workspace_name"`
	TemporaryWorkspaceRoots []string                  `json:"temporary_workspace_roots,omitempty"`
	Title                   string                    `json:"title"`
	Mode                    string                    `json:"mode"`
	WorktreeEnabled         bool                      `json:"worktree_enabled,omitempty"`
	WorktreeRootPath        string                    `json:"worktree_root_path,omitempty"`
	WorktreeBaseBranch      string                    `json:"worktree_base_branch,omitempty"`
	WorktreeBranch          string                    `json:"worktree_branch,omitempty"`
	Metadata                map[string]any            `json:"metadata,omitempty"`
	CreatedAt               int64                     `json:"created_at"`
	UpdatedAt               int64                     `json:"updated_at"`
	MessageCount            int                       `json:"message_count"`
	LastMessageAt           int64                     `json:"last_message_at"`
	Lifecycle               *SessionLifecycleSnapshot `json:"lifecycle,omitempty"`
	Archived                bool                      `json:"archived"`
	Deleted                 bool                      `json:"deleted,omitempty"`
	Snippets                []V3SessionSearchSnippet  `json:"snippets,omitempty"`
	LibraryMetric           V3SessionLibraryMetric    `json:"library_metric"`
	Attention               V3SessionAttentionSummary `json:"attention"`
}

type V3SessionAttentionSummary struct {
	State            string `json:"state"`
	PlanID           string `json:"plan_id,omitempty"`
	PlanStatus       string `json:"plan_status,omitempty"`
	CheckpointID     string `json:"checkpoint_id,omitempty"`
	CheckpointStatus string `json:"checkpoint_status,omitempty"`
	ExecutionStatus  string `json:"execution_status,omitempty"`
	LastOutcome      string `json:"last_outcome,omitempty"`
}

type V3SessionSearchSnippet struct {
	Source    string `json:"source"`
	Role      string `json:"role,omitempty"`
	MessageID string `json:"message_id,omitempty"`
	GlobalSeq uint64 `json:"global_seq,omitempty"`
	Text      string `json:"text"`
	CreatedAt int64  `json:"created_at,omitempty"`
}

type v3SessionSearchIndexMeta struct {
	Version   int   `json:"version"`
	IndexedAt int64 `json:"indexed_at"`
}

type v3SessionSearchSessionMeta struct {
	SessionID      string   `json:"session_id"`
	Version        int      `json:"version,omitempty"`
	MetadataTokens []string `json:"metadata_tokens,omitempty"`
	// Keys contains v2 posting keys during bounded, per-session compatibility.
	// It is never extended by v3 writes and can be dropped without rebuilding.
	Keys []string `json:"keys,omitempty"`
}

type v3SessionSearchIndexRecord struct {
	SessionID string                  `json:"session_id"`
	Archived  bool                    `json:"archived,omitempty"`
	Snippet   *V3SessionSearchSnippet `json:"snippet,omitempty"`
}

type v3SessionSearchCursor struct {
	BeforeUpdatedAt int64  `json:"before_updated_at"`
	BeforeSessionID string `json:"before_session_id"`
}

func (s *SessionStore) SearchV3Sessions(options V3SessionSearchOptions) (result V3SessionSearchResult, err error) {
	if s == nil || s.store == nil {
		return V3SessionSearchResult{}, errors.New("session store is not configured")
	}
	options = normalizeV3SessionSearchOptions(options)
	if options.AccountScopeID == "" {
		return V3SessionSearchResult{}, errors.New("account scope id is required")
	}
	if options.UserID == "" {
		return V3SessionSearchResult{}, errors.New("user id is required")
	}
	if !options.Global && len(options.WorkspacePaths) == 0 {
		return V3SessionSearchResult{}, errors.New("session search requires explicit workspace_path, workspace_paths, or global=true")
	}
	if err := s.ensureV3SessionSearchIndex(); err != nil {
		return V3SessionSearchResult{}, err
	}
	if err := s.ensureV3SessionLibraryIndex(); err != nil {
		return V3SessionSearchResult{}, err
	}
	snapshot := s.store.db.NewSnapshot()
	defer func() {
		if closeErr := snapshot.Close(); err == nil && closeErr != nil {
			err = closeErr
		}
	}()
	if len(options.Queries) == 0 {
		result, err = s.searchV3SessionsRecentFromReader(snapshot, options)
	} else {
		result, err = s.searchV3SessionsQueryFromReader(snapshot, options)
	}
	if err != nil {
		return V3SessionSearchResult{}, err
	}
	result.Summary, err = s.v3SessionLibrarySummary(snapshot, options)
	if err != nil {
		return V3SessionSearchResult{}, err
	}
	metrics, err := v3LibraryMetricsFromReader(snapshot)
	if err != nil {
		return V3SessionSearchResult{}, err
	}
	filtered := result.Items[:0]
	for i := range result.Items {
		result.Items[i].LibraryMetric = metrics[result.Items[i].ID]
		result.Items[i].Attention, err = v3SessionAttentionFromReader(snapshot, result.Items[i].ID)
		if err != nil {
			return V3SessionSearchResult{}, err
		}
		if options.State == "" || result.Items[i].Attention.State == options.State {
			filtered = append(filtered, result.Items[i])
		}
	}
	result.Items = filtered
	return result, nil
}

func normalizeV3SessionSearchOptions(options V3SessionSearchOptions) V3SessionSearchOptions {
	options.AccountScopeID = strings.TrimSpace(options.AccountScopeID)
	options.UserID = strings.TrimSpace(options.UserID)
	options.Query = truncateRunes(strings.TrimSpace(options.Query), v3SessionSearchMaxQueryRunes)
	queries := append([]string(nil), options.Queries...)
	if options.Query != "" {
		queries = append([]string{options.Query}, queries...)
	}
	options.Queries = nil
	seenQueries := map[string]struct{}{}
	for _, query := range queries {
		query = truncateRunes(strings.TrimSpace(query), v3SessionSearchMaxQueryRunes)
		key := strings.ToLower(query)
		if query == "" {
			continue
		}
		if _, ok := seenQueries[key]; ok {
			continue
		}
		seenQueries[key] = struct{}{}
		options.Queries = append(options.Queries, query)
		if len(options.Queries) == v3SessionSearchMaxQueries {
			break
		}
	}
	if len(options.Queries) > 0 {
		options.Query = options.Queries[0]
	}
	options.State = strings.ToLower(strings.TrimSpace(options.State))
	options.WorkspacePath = strings.TrimSpace(options.WorkspacePath)
	options.WorkspacePaths = normalizeV3SessionWorksetWorkspacePaths(options.WorkspacePath, options.WorkspacePaths)
	options.BeforeSessionID = strings.TrimSpace(options.BeforeSessionID)
	options.ArchivedMode = strings.TrimSpace(strings.ToLower(options.ArchivedMode))
	if options.ArchivedMode == "" {
		options.ArchivedMode = v3SessionSearchArchivedExclude
	}
	if options.ArchivedMode != v3SessionSearchArchivedExclude && options.ArchivedMode != v3SessionSearchArchivedInclude && options.ArchivedMode != v3SessionSearchArchivedOnly {
		options.ArchivedMode = v3SessionSearchArchivedExclude
	}
	if options.Limit <= 0 || options.Limit > v3SessionSearchMaxLimit {
		options.Limit = v3SessionSearchDefaultLimit
	}
	return options
}

func (s *SessionStore) searchV3SessionsRecentFromReader(reader pebble.Reader, options V3SessionSearchOptions) (V3SessionSearchResult, error) {
	items := make([]V3SessionSearchItem, 0, options.Limit+1)
	if options.ArchivedMode != v3SessionSearchArchivedOnly {
		worksetOptions := V3SessionWorksetOptions{
			AccountScopeID:        options.AccountScopeID,
			UserID:                options.UserID,
			Global:                options.Global,
			WorkspacePaths:        options.WorkspacePaths,
			RecentLimit:           options.Limit + 1,
			RecentBeforeUpdatedAt: options.BeforeUpdatedAt,
			RecentBeforeSessionID: options.BeforeSessionID,
			History:               V3SessionWorksetHistoryOptions{Mode: V3SessionWorksetHistoryModeNone},
		}
		sessions, _, err := s.selectV3RecentWorksetSessionsFromIndex(reader, worksetOptions)
		if err != nil {
			return V3SessionSearchResult{}, err
		}
		for _, session := range sessions {
			if !v3SessionSearchDateVisible(session.UpdatedAt, options) {
				continue
			}
			items = append(items, v3SessionSearchItemFromSession(session, false, false, nil))
			if len(items) > options.Limit {
				break
			}
		}
	}
	if options.ArchivedMode != v3SessionSearchArchivedExclude {
		archived, err := s.searchV3ArchivedRecentFromReader(reader, options, options.Limit+1)
		if err != nil {
			return V3SessionSearchResult{}, err
		}
		items = append(items, archived...)
		sort.SliceStable(items, func(i, j int) bool { return v3SessionSearchLess(items[i], items[j]) })
	}
	return paginateV3SessionSearchItems(items, options.Limit), nil
}

func (s *SessionStore) searchV3SessionsQueryFromReader(reader pebble.Reader, options V3SessionSearchOptions) (V3SessionSearchResult, error) {
	queryTokens := make([][]string, 0, len(options.Queries))
	for _, query := range options.Queries {
		if tokens := v3SessionSearchTokens(query); len(tokens) > 0 {
			queryTokens = append(queryTokens, tokens)
		}
	}
	if len(queryTokens) == 0 {
		return s.searchV3SessionsRecentFromReader(reader, options)
	}

	// Search membership is stable and session-prefixed. Recency and archive
	// ordering belong to the existing recent/tombstone indexes, so query search
	// walks those indexes and performs bounded point lookups for each token.
	items := make([]V3SessionSearchItem, 0, options.Limit+1)
	visitSession := func(session SessionSnapshot, archived bool) error {
		var snippets []V3SessionSearchSnippet
		matched := false
		for _, tokens := range queryTokens {
			querySnippets, ok, err := v3SessionSearchSessionHasTokens(reader, session.ID, tokens)
			if err != nil {
				return err
			}
			if ok {
				matched = true
				snippets = mergeV3SessionSearchSnippets(snippets, querySnippets)
			}
		}
		if matched {
			items = append(items, v3SessionSearchItemFromSession(session, archived, false, snippets))
		}
		return nil
	}
	if options.ArchivedMode != v3SessionSearchArchivedOnly {
		prefix := sessionRecentIndexPrefixForSearch(options)
		startKey := sessionRecentIndexStartAfter(prefix, options.BeforeUpdatedAt, options.BeforeSessionID)
		err := scanRangeFromReader(reader, scanRangeOptions{Prefix: prefix, StartKey: startKey, Limit: sessionRecentIndexScanLimit()}, func(_ string, value []byte) (bool, error) {
			session, ok, err := s.getSessionFromReader(reader, strings.TrimSpace(string(value)))
			if err != nil || !ok {
				return err == nil, err
			}
			if v3SessionWorksetSessionVisibleForWorkspaces(session, options.AccountScopeID, options.UserID, options.WorkspacePath, options.WorkspacePaths) && v3SessionSearchDateVisible(session.UpdatedAt, options) {
				if err := visitSession(session, false); err != nil {
					return false, err
				}
			}
			return len(items) <= options.Limit, nil
		})
		if err != nil {
			return V3SessionSearchResult{}, err
		}
	}
	if options.ArchivedMode != v3SessionSearchArchivedExclude {
		prefix := V3SessionTombstoneByAccountUserPrefix(options.AccountScopeID, options.UserID)
		if len(options.WorkspacePaths) == 1 {
			prefix = V3SessionTombstoneByAccountUserWorkspacePrefix(options.AccountScopeID, options.UserID, options.WorkspacePaths[0])
		}
		startKey := sessionRecentIndexStartAfter(prefix, options.BeforeUpdatedAt, options.BeforeSessionID)
		archivedMatches := 0
		err := scanRangeFromReader(reader, scanRangeOptions{Prefix: prefix, StartKey: startKey, Limit: sessionRecentIndexScanLimit()}, func(_ string, value []byte) (bool, error) {
			var tombstone V3SessionTombstone
			if err := json.Unmarshal(value, &tombstone); err != nil {
				return false, fmt.Errorf("decode session tombstone: %w", err)
			}
			session := normalizeSessionOwnership(tombstone.Session)
			if tombstone.Archived && v3SessionWorksetSessionVisibleForWorkspaces(session, options.AccountScopeID, options.UserID, options.WorkspacePath, options.WorkspacePaths) && v3SessionSearchDateVisible(session.UpdatedAt, options) {
				before := len(items)
				if err := visitSession(session, true); err != nil {
					return false, err
				}
				if len(items) > before {
					archivedMatches++
				}
			}
			return archivedMatches <= options.Limit, nil
		})
		if err != nil {
			return V3SessionSearchResult{}, err
		}
	}
	sort.SliceStable(items, func(i, j int) bool { return v3SessionSearchLess(items[i], items[j]) })
	return paginateV3SessionSearchItems(items, options.Limit), nil
}

func (s *SessionStore) searchV3ItemForRecord(reader pebble.Reader, record v3SessionSearchIndexRecord, options V3SessionSearchOptions) (V3SessionSearchItem, bool, error) {
	if record.Archived {
		if options.ArchivedMode == v3SessionSearchArchivedExclude {
			return V3SessionSearchItem{}, false, nil
		}
		var tombstone V3SessionTombstone
		ok, err := getJSONFromReader(reader, KeyV3SessionTombstone(record.SessionID), &tombstone)
		if err != nil || !ok || !tombstone.Archived {
			return V3SessionSearchItem{}, false, err
		}
		session := normalizeSessionOwnership(tombstone.Session)
		if !v3SessionWorksetSessionVisibleForWorkspaces(session, options.AccountScopeID, options.UserID, options.WorkspacePath, options.WorkspacePaths) || !v3SessionSearchDateVisible(session.UpdatedAt, options) {
			return V3SessionSearchItem{}, false, nil
		}
		item := v3SessionSearchItemFromSession(session, true, false, snippetList(record.Snippet))
		item.UpdatedAt = tombstone.UpdatedAt
		return item, true, nil
	}
	if options.ArchivedMode == v3SessionSearchArchivedOnly {
		return V3SessionSearchItem{}, false, nil
	}
	session, ok, err := s.getSessionFromReader(reader, record.SessionID)
	if err != nil || !ok {
		return V3SessionSearchItem{}, false, err
	}
	if !v3SessionWorksetSessionVisibleForWorkspaces(session, options.AccountScopeID, options.UserID, options.WorkspacePath, options.WorkspacePaths) || !v3SessionSearchDateVisible(session.UpdatedAt, options) {
		return V3SessionSearchItem{}, false, nil
	}
	return v3SessionSearchItemFromSession(session, false, false, snippetList(record.Snippet)), true, nil
}

func (s *SessionStore) searchV3ArchivedRecentFromReader(reader pebble.Reader, options V3SessionSearchOptions, limit int) ([]V3SessionSearchItem, error) {
	if limit <= 0 {
		return nil, nil
	}
	prefix := V3SessionTombstoneByAccountUserPrefix(options.AccountScopeID, options.UserID)
	if len(options.WorkspacePaths) == 1 {
		prefix = V3SessionTombstoneByAccountUserWorkspacePrefix(options.AccountScopeID, options.UserID, options.WorkspacePaths[0])
	}
	startKey := sessionRecentIndexStartAfter(prefix, options.BeforeUpdatedAt, options.BeforeSessionID)
	items := make([]V3SessionSearchItem, 0, limit)
	err := scanRangeFromReader(reader, scanRangeOptions{Prefix: prefix, StartKey: startKey, Limit: sessionRecentIndexScanLimit()}, func(_ string, value []byte) (bool, error) {
		var tombstone V3SessionTombstone
		if err := json.Unmarshal(value, &tombstone); err != nil {
			return false, fmt.Errorf("decode session tombstone: %w", err)
		}
		if !tombstone.Archived {
			return true, nil
		}
		session := normalizeSessionOwnership(tombstone.Session)
		if !v3SessionWorksetSessionVisibleForWorkspaces(session, options.AccountScopeID, options.UserID, options.WorkspacePath, options.WorkspacePaths) || !v3SessionSearchDateVisible(session.UpdatedAt, options) {
			return true, nil
		}
		item := v3SessionSearchItemFromSession(session, true, false, nil)
		item.UpdatedAt = tombstone.UpdatedAt
		items = append(items, item)
		return len(items) < limit, nil
	})
	return items, err
}

func paginateV3SessionSearchItems(items []V3SessionSearchItem, limit int) V3SessionSearchResult {
	result := V3SessionSearchResult{Items: items}
	if len(items) > limit {
		last := items[limit-1]
		updatedAt := last.UpdatedAt
		result.Items = items[:limit]
		result.Pagination = V3SessionSearchPagination{HasMore: true, NextBeforeUpdatedAt: &updatedAt, NextBeforeSessionID: last.ID, NextCursor: encodeV3SessionSearchCursor(updatedAt, last.ID)}
	}
	return result
}

func encodeV3SessionSearchCursor(updatedAt int64, sessionID string) string {
	payload, _ := json.Marshal(v3SessionSearchCursor{BeforeUpdatedAt: updatedAt, BeforeSessionID: strings.TrimSpace(sessionID)})
	return base64.RawURLEncoding.EncodeToString(payload)
}

func DecodeV3SessionSearchCursor(cursor string) (*int64, string, error) {
	cursor = strings.TrimSpace(cursor)
	if cursor == "" {
		return nil, "", nil
	}
	payload, err := base64.RawURLEncoding.DecodeString(cursor)
	if err != nil {
		return nil, "", fmt.Errorf("invalid session search cursor")
	}
	var decoded v3SessionSearchCursor
	if err := json.Unmarshal(payload, &decoded); err != nil {
		return nil, "", fmt.Errorf("invalid session search cursor")
	}
	if decoded.BeforeUpdatedAt <= 0 || strings.TrimSpace(decoded.BeforeSessionID) == "" {
		return nil, "", fmt.Errorf("invalid session search cursor")
	}
	return &decoded.BeforeUpdatedAt, strings.TrimSpace(decoded.BeforeSessionID), nil
}

func (s *SessionStore) ensureV3SessionSearchIndex() error {
	var meta v3SessionSearchIndexMeta
	if ok, err := s.store.GetJSON(keyV3SessionSearchIndexMeta, &meta); err != nil {
		return err
	} else if ok && meta.Version == v3SessionSearchIndexVersion {
		return nil
	}
	// v3 migration is intentionally O(1): old postings remain readable only as
	// compatibility data until an individual session is next mutated. No search
	// request may trigger a global session/message backfill.
	payload, err := json.Marshal(v3SessionSearchIndexMeta{Version: v3SessionSearchIndexVersion, IndexedAt: time.Now().UnixMilli()})
	if err != nil {
		return err
	}
	return s.store.db.Set([]byte(keyV3SessionSearchIndexMeta), payload, pebble.Sync)
}

func deleteV3SessionSearchIndexKeysFromReader(batch *pebble.Batch, reader pebble.Reader) error {
	for _, prefix := range []string{keyV3SessionSearchAccountPrefix, keyV3SessionSearchMetaPrefix} {
		if err := iteratePrefixFromReader(reader, prefix, sessionRecentIndexScanLimit(), func(key string, _ []byte) error {
			return batch.Delete([]byte(key), nil)
		}); err != nil {
			return err
		}
	}
	return nil
}

func (s *SessionStore) replaceV3SessionSearchIndexInBatch(batch *pebble.Batch, reader pebble.Reader, session SessionSnapshot, archived bool, extraMessages []MessageSnapshot) error {
	if batch == nil || reader == nil {
		return errors.New("session search index update requires batch and reader")
	}
	session = normalizeSessionOwnership(session)
	if strings.TrimSpace(session.ID) == "" || strings.TrimSpace(session.AccountScopeID) == "" {
		return nil
	}
	var previous v3SessionSearchSessionMeta
	if _, err := getJSONFromReader(reader, keyV3SessionSearchMeta(session.ID), &previous); err != nil {
		return err
	}
	// Retain v2 compatibility postings during the bounded migration window.
	// They are lifecycle-neutral at read time and are purged by session prefix
	// deletion; ordinary mutations never rewrite the complete legacy set.
	oldTokens := make(map[string]struct{}, len(previous.MetadataTokens))
	for _, token := range previous.MetadataTokens {
		oldTokens[token] = struct{}{}
	}
	newTokens := v3SessionSearchMetadataTokens(session)
	for token := range oldTokens {
		if _, keep := newTokens[token]; !keep {
			if err := batch.Delete([]byte(keyV3SessionSearchPosting(session.ID, "metadata", token)), nil); err != nil {
				return err
			}
		}
	}
	for token, record := range newTokens {
		payload, err := json.Marshal(record)
		if err != nil {
			return err
		}
		observeV3SearchPostingSet(keyV3SessionSearchPosting(session.ID, "metadata", token), payload)
		if err := batch.Set([]byte(keyV3SessionSearchPosting(session.ID, "metadata", token)), payload, nil); err != nil {
			return err
		}
	}
	for _, message := range extraMessages {
		if err := appendV3SessionSearchMessagePostingsInBatch(batch, session.ID, message); err != nil {
			return err
		}
	}
	metadataTokens := make([]string, 0, len(newTokens))
	for token := range newTokens {
		metadataTokens = append(metadataTokens, token)
	}
	sort.Strings(metadataTokens)
	payload, err := json.Marshal(v3SessionSearchSessionMeta{SessionID: session.ID, Version: v3SessionSearchIndexVersion, MetadataTokens: metadataTokens, Keys: previous.Keys})
	if err != nil {
		return err
	}
	return batch.Set([]byte(keyV3SessionSearchMeta(session.ID)), payload, nil)
}

func (s *SessionStore) transitionV3SessionSearchLifecycleInBatchV2(batch *pebble.Batch, reader pebble.Reader, session SessionSnapshot, archived bool) error {
	if batch == nil || reader == nil {
		return errors.New("session search index transition requires batch and reader")
	}
	session = normalizeSessionOwnership(session)
	if strings.TrimSpace(session.ID) == "" || strings.TrimSpace(session.AccountScopeID) == "" {
		return nil
	}

	var previous v3SessionSearchSessionMeta
	ok, err := getJSONFromReader(reader, keyV3SessionSearchMeta(session.ID), &previous)
	if err != nil {
		return err
	}
	if !ok || previous.SessionID != session.ID || len(previous.Keys) == 0 {
		return s.replaceV3SessionSearchIndexInBatch(batch, reader, session, archived, nil)
	}

	type movedRecord struct {
		oldKey string
		newKey string
		record v3SessionSearchIndexRecord
	}
	moved := make([]movedRecord, 0, len(previous.Keys))
	seen := make(map[string]struct{}, len(previous.Keys))
	observeV3SearchAllTokenRekey()
	for _, oldKey := range previous.Keys {
		var record v3SessionSearchIndexRecord
		observeV3SearchPostingRead()
		found, readErr := getJSONFromReader(reader, oldKey, &record)
		if readErr != nil {
			return readErr
		}
		newKey, valid := v3SessionSearchKeyWithLifecycle(oldKey, session.AccountScopeID, archived, session.UpdatedAt, session.ID)
		if !found || !valid || strings.TrimSpace(record.SessionID) != session.ID {
			return s.replaceV3SessionSearchIndexInBatch(batch, reader, session, archived, nil)
		}
		if _, duplicate := seen[newKey]; duplicate {
			return s.replaceV3SessionSearchIndexInBatch(batch, reader, session, archived, nil)
		}
		seen[newKey] = struct{}{}
		record.Archived = archived
		moved = append(moved, movedRecord{oldKey: oldKey, newKey: newKey, record: record})
	}

	metaKeys := make([]string, 0, len(moved))
	for _, item := range moved {
		observeV3SearchPostingDeleted(item.oldKey)
		if err := batch.Delete([]byte(item.oldKey), nil); err != nil {
			return err
		}
		payload, err := json.Marshal(item.record)
		if err != nil {
			return err
		}
		observeV3SearchPostingSet(item.newKey, payload)
		if err := batch.Set([]byte(item.newKey), payload, nil); err != nil {
			return err
		}
		metaKeys = append(metaKeys, item.newKey)
	}
	sort.Strings(metaKeys)
	payload, err := json.Marshal(v3SessionSearchSessionMeta{SessionID: session.ID, Keys: metaKeys})
	if err != nil {
		return err
	}
	return batch.Set([]byte(keyV3SessionSearchMeta(session.ID)), payload, nil)
}

func v3SessionSearchKeyWithLifecycle(key, accountScopeID string, archived bool, updatedAt int64, sessionID string) (string, bool) {
	rest, ok := strings.CutPrefix(key, keyV3SessionSearchAccountPrefix)
	if !ok {
		return "", false
	}
	parts := strings.SplitN(rest, "/", 4)
	if len(parts) != 4 || parts[0] != keyPart(accountScopeID) || (parts[1] != "active" && parts[1] != "archived") || parts[2] == "" || parts[3] == "" {
		return "", false
	}
	state := "active"
	if archived {
		state = "archived"
	}
	return fmt.Sprintf("%s%s/%s/%s/%s", keyV3SessionSearchAccountPrefix, parts[0], state, parts[2], sessionRecentIndexOrderPart(updatedAt, sessionID)), true
}

func (s *SessionStore) appendV3SessionSearchMessageInBatchV2(batch *pebble.Batch, reader pebble.Reader, session SessionSnapshot, archived bool, message MessageSnapshot) error {
	if batch == nil || reader == nil {
		return errors.New("session search index update requires batch and reader")
	}
	session = normalizeSessionOwnership(session)
	if strings.TrimSpace(session.ID) == "" || strings.TrimSpace(session.AccountScopeID) == "" {
		return nil
	}
	var previous v3SessionSearchSessionMeta
	if ok, err := getJSONFromReader(reader, keyV3SessionSearchMeta(session.ID), &previous); err != nil {
		return err
	} else if !ok {
		return s.replaceV3SessionSearchIndexInBatch(batch, reader, session, archived, []MessageSnapshot{message})
	}
	keys := make(map[string]v3SessionSearchIndexRecord, len(previous.Keys))
	observeV3SearchAllTokenRekey()
	for _, key := range previous.Keys {
		var record v3SessionSearchIndexRecord
		observeV3SearchPostingRead()
		if ok, err := getJSONFromReader(reader, key, &record); err != nil {
			return err
		} else if !ok {
			continue
		}
		observeV3SearchPostingDeleted(key)
		if err := batch.Delete([]byte(key), nil); err != nil {
			return err
		}
		movedKey, ok := v3SessionSearchKeyWithUpdatedOrder(key, session.UpdatedAt, session.ID)
		if !ok {
			return s.replaceV3SessionSearchIndexInBatch(batch, reader, session, archived, []MessageSnapshot{message})
		}
		keys[movedKey] = record
	}
	tokens := v3SessionSearchTokens(message.Content)
	snippetSource := newV3SessionSearchSnippetSource(message.Content)
	for _, token := range tokens {
		messageSnippet := &V3SessionSearchSnippet{Source: "message", Role: message.Role, MessageID: message.ID, GlobalSeq: message.GlobalSeq, Text: snippetSource.matchCentered(token), CreatedAt: message.CreatedAt}
		key := keyV3SessionSearchAccount(session.AccountScopeID, archived, token, session.UpdatedAt, session.ID)
		if _, ok := keys[key]; ok {
			continue
		}
		keys[key] = v3SessionSearchIndexRecord{SessionID: session.ID, Archived: archived, Snippet: messageSnippet}
	}
	for key, record := range keys {
		payload, err := json.Marshal(record)
		if err != nil {
			return err
		}
		observeV3SearchPostingSet(key, payload)
		if err := batch.Set([]byte(key), payload, nil); err != nil {
			return err
		}
	}
	metaKeys := make([]string, 0, len(keys))
	for key := range keys {
		metaKeys = append(metaKeys, key)
	}
	sort.Strings(metaKeys)
	payload, err := json.Marshal(v3SessionSearchSessionMeta{SessionID: session.ID, Keys: metaKeys})
	if err != nil {
		return err
	}
	return batch.Set([]byte(keyV3SessionSearchMeta(session.ID)), payload, nil)
}

func (s *SessionStore) appendV3SessionSearchMessageInBatch(batch *pebble.Batch, reader pebble.Reader, session SessionSnapshot, archived bool, message MessageSnapshot) error {
	if batch == nil || reader == nil {
		return errors.New("session search index update requires batch and reader")
	}
	session = normalizeSessionOwnership(session)
	if session.ID == "" || session.AccountScopeID == "" {
		return nil
	}
	var meta v3SessionSearchSessionMeta
	if ok, err := getJSONFromReader(reader, keyV3SessionSearchMeta(session.ID), &meta); err != nil {
		return err
	} else if !ok || meta.Version != v3SessionSearchIndexVersion {
		// Per-session migration is bounded to metadata. Historical message rows are
		// never scanned on an ordinary append; subsequent messages build v3
		// postings incrementally.
		if err := s.replaceV3SessionSearchIndexInBatch(batch, reader, session, archived, nil); err != nil {
			return err
		}
	}
	return appendV3SessionSearchMessagePostingsInBatch(batch, session.ID, message)
}

func appendV3SessionSearchMessagePostingsInBatch(batch *pebble.Batch, sessionID string, message MessageSnapshot) error {
	searchContent := v3SessionSearchMessageContent(message)
	tokens := v3SessionSearchTokens(searchContent)
	snippetSource := newV3SessionSearchSnippetSource(searchContent)
	for _, token := range tokens {
		key := keyV3SessionSearchPosting(sessionID, "message", token)
		record := v3SessionSearchIndexRecord{SessionID: sessionID, Snippet: &V3SessionSearchSnippet{Source: "message", Role: message.Role, MessageID: message.ID, GlobalSeq: message.GlobalSeq, Text: snippetSource.matchCentered(token), CreatedAt: message.CreatedAt}}
		payload, err := json.Marshal(record)
		if err != nil {
			return err
		}
		observeV3SearchPostingSet(key, payload)
		if err := batch.Set([]byte(key), payload, nil); err != nil {
			return err
		}
	}
	return nil
}

func v3SessionSearchMessageContent(message MessageSnapshot) string {
	content := message.Content
	if strings.EqualFold(strings.TrimSpace(message.Role), "tool") && message.Metadata != nil {
		if bounded, ok := message.Metadata["search_index_content"].(string); ok && strings.TrimSpace(bounded) != "" {
			return bounded
		}
	}
	return content
}

func v3SessionSearchMetadataTokens(session SessionSnapshot) map[string]v3SessionSearchIndexRecord {
	entries := map[string]v3SessionSearchIndexRecord{}
	add := func(text string, snippet *V3SessionSearchSnippet) {
		tokens := v3SessionSearchTokens(text)
		var snippetSource v3SessionSearchSnippetSource
		if snippet != nil {
			snippetSource = newV3SessionSearchSnippetSource(text)
		}
		for _, token := range tokens {
			centered := snippet
			if snippet != nil {
				copy := *snippet
				copy.Text = snippetSource.matchCentered(token)
				centered = &copy
			}
			entries[token] = v3SessionSearchIndexRecord{SessionID: session.ID, Snippet: centered}
		}
	}
	add(session.Title, &V3SessionSearchSnippet{Source: "title", Text: session.Title, CreatedAt: session.CreatedAt})
	add(session.WorkspaceName, nil)
	add(session.WorkspacePath, nil)
	return entries
}

func keyV3SessionSearchPosting(sessionID, source, token string) string {
	return fmt.Sprintf("%s%s/%s/%s", keyV3SessionSearchPostingPrefix, keyPart(sessionID), keyPart(source), keyPart(token))
}

func v3SessionSearchPostingPrefix(sessionID string) string {
	return fmt.Sprintf("%s%s/", keyV3SessionSearchPostingPrefix, keyPart(sessionID))
}

func v3SessionSearchSessionHasTokens(reader pebble.Reader, sessionID string, tokens []string) ([]V3SessionSearchSnippet, bool, error) {
	var meta v3SessionSearchSessionMeta
	metaOK, err := getJSONFromReader(reader, keyV3SessionSearchMeta(sessionID), &meta)
	if err != nil {
		return nil, false, err
	}
	var snippets []V3SessionSearchSnippet
	for _, token := range tokens {
		var record v3SessionSearchIndexRecord
		ok, err := getJSONFromReader(reader, keyV3SessionSearchPosting(sessionID, "message", token), &record)
		if err != nil {
			return nil, false, err
		}
		if !ok {
			ok, err = getJSONFromReader(reader, keyV3SessionSearchPosting(sessionID, "metadata", token), &record)
			if err != nil {
				return nil, false, err
			}
		}
		if !ok && metaOK {
			legacyKey, found := v3SessionSearchLegacyKeyForToken(meta.Keys, token)
			if found {
				ok, err = getJSONFromReader(reader, legacyKey, &record)
				if err != nil {
					return nil, false, err
				}
			}
		}
		if !ok {
			return nil, false, nil
		}
		snippets = mergeV3SessionSearchSnippets(snippets, snippetList(record.Snippet))
	}
	return snippets, true, nil
}

func v3SessionSearchLegacyKeyForToken(keys []string, token string) (string, bool) {
	tokenPart := keyPart(token)
	for _, key := range keys {
		rest, ok := strings.CutPrefix(key, keyV3SessionSearchAccountPrefix)
		if !ok {
			continue
		}
		parts := strings.SplitN(rest, "/", 4)
		if len(parts) == 4 && parts[2] == tokenPart {
			return key, true
		}
	}
	return "", false
}

func sessionRecentIndexPrefixForSearch(options V3SessionSearchOptions) string {
	return sessionRecentIndexPrefixForWorkset(V3SessionWorksetOptions{AccountScopeID: options.AccountScopeID, UserID: options.UserID, Global: options.Global, WorkspacePath: options.WorkspacePath, WorkspacePaths: options.WorkspacePaths})
}

func v3SessionSearchKeyWithUpdatedOrder(key string, updatedAt int64, sessionID string) (string, bool) {
	rest, ok := strings.CutPrefix(key, keyV3SessionSearchAccountPrefix)
	if !ok {
		return "", false
	}
	parts := strings.SplitN(rest, "/", 4)
	if len(parts) != 4 || parts[0] == "" || parts[1] == "" || parts[2] == "" {
		return "", false
	}
	return fmt.Sprintf("%s%s/%s/%s/%s", keyV3SessionSearchAccountPrefix, parts[0], parts[1], parts[2], sessionRecentIndexOrderPart(updatedAt, sessionID)), true
}

func removeV3SessionSearchIndexInBatch(batch *pebble.Batch, reader pebble.Reader, sessionID string) error {
	if batch == nil || reader == nil {
		return errors.New("session search index delete requires batch and reader")
	}
	// Stable v3 postings are session-prefixed, making purge one bounded range
	// tombstone rather than one delete per token. Legacy v2 keys become
	// unreachable when their per-session metadata is removed.
	if err := deletePrefixInBatch(batch, v3SessionSearchPostingPrefix(sessionID)); err != nil {
		return err
	}
	return batch.Delete([]byte(keyV3SessionSearchMeta(sessionID)), nil)
}

func (s *SessionStore) transitionV3SessionSearchLifecycleInBatch(batch *pebble.Batch, reader pebble.Reader, session SessionSnapshot, archived bool) error {
	// Posting membership is lifecycle-neutral. Archive ordering and visibility are
	// represented by the tombstone/recent indexes, so no token is moved here.
	var meta v3SessionSearchSessionMeta
	if ok, err := getJSONFromReader(reader, keyV3SessionSearchMeta(session.ID), &meta); err != nil {
		return err
	} else if !ok || meta.Version != v3SessionSearchIndexVersion {
		return s.replaceV3SessionSearchIndexInBatch(batch, reader, session, archived, nil)
	}
	return nil
}

func v3SessionSearchMetadataChanged(previous, next SessionSnapshot) bool {
	return previous.Title != next.Title || previous.WorkspaceName != next.WorkspaceName || previous.WorkspacePath != next.WorkspacePath
}

func v3SessionMutationChangesSearchMetadata(kind string) bool {
	switch kind {
	case V3SessionMutationCreateSession, V3SessionMutationUpdateTitle:
		return true
	default:
		return false
	}
}

func keyV3SessionSearchMeta(sessionID string) string {
	return keyV3SessionSearchMetaPrefix + keyPart(sessionID)
}

func keyV3SessionSearchAccount(accountScopeID string, archived bool, token string, updatedAt int64, sessionID string) string {
	state := "active"
	if archived {
		state = "archived"
	}
	return fmt.Sprintf("%s%s/%s/%s/%s", keyV3SessionSearchAccountPrefix, keyPart(accountScopeID), state, keyPart(token), sessionRecentIndexOrderPart(updatedAt, sessionID))
}

func v3SessionSearchPrefixesForOptions(options V3SessionSearchOptions, token string) []string {
	accountPart := keyPart(options.AccountScopeID)
	tokenPart := keyPart(token)
	prefixes := []string{}
	if options.ArchivedMode != v3SessionSearchArchivedOnly {
		prefixes = append(prefixes, fmt.Sprintf("%s%s/active/%s/", keyV3SessionSearchAccountPrefix, accountPart, tokenPart))
	}
	if options.ArchivedMode != v3SessionSearchArchivedExclude {
		prefixes = append(prefixes, fmt.Sprintf("%s%s/archived/%s/", keyV3SessionSearchAccountPrefix, accountPart, tokenPart))
	}
	return prefixes
}

func v3SessionSearchTokens(text string) []string {
	text = strings.ToLower(strings.TrimSpace(text))
	seen := map[string]struct{}{}
	var tokens []string
	var b strings.Builder
	flush := func() {
		token := b.String()
		b.Reset()
		if len(token) < 2 {
			return
		}
		if _, ok := seen[token]; ok {
			return
		}
		seen[token] = struct{}{}
		tokens = append(tokens, token)
	}
	for _, r := range text {
		if unicode.IsLetter(r) || unicode.IsDigit(r) {
			b.WriteRune(r)
		} else {
			flush()
		}
	}
	flush()
	return tokens
}

func v3SessionSearchItemMatchesQuery(item V3SessionSearchItem, tokens []string, rawQuery string) bool {
	haystack := strings.ToLower(strings.Join([]string{item.Title, item.WorkspaceName, item.WorkspacePath, snippetsText(item.Snippets)}, " "))
	for _, token := range tokens {
		if !strings.Contains(haystack, token) {
			return false
		}
	}
	return strings.Contains(haystack, strings.ToLower(strings.TrimSpace(rawQuery))) || len(tokens) > 0
}

func snippetsText(snippets []V3SessionSearchSnippet) string {
	parts := make([]string, 0, len(snippets))
	for _, snippet := range snippets {
		parts = append(parts, snippet.Text)
	}
	return strings.Join(parts, " ")
}

func v3SessionSearchDateVisible(updatedAt int64, options V3SessionSearchOptions) bool {
	if options.FromUpdatedAt != nil && updatedAt < *options.FromUpdatedAt {
		return false
	}
	if options.ToUpdatedAt != nil && updatedAt > *options.ToUpdatedAt {
		return false
	}
	return true
}

func v3SessionSearchItemFromSession(session SessionSnapshot, archived, deleted bool, snippets []V3SessionSearchSnippet) V3SessionSearchItem {
	return V3SessionSearchItem{ID: session.ID, UserID: session.UserID, AccountScopeID: session.AccountScopeID, WorkspacePath: session.WorkspacePath, WorkspaceName: session.WorkspaceName, TemporaryWorkspaceRoots: session.TemporaryWorkspaceRoots, Title: session.Title, Mode: session.Mode, WorktreeEnabled: session.WorktreeEnabled, WorktreeRootPath: session.WorktreeRootPath, WorktreeBaseBranch: session.WorktreeBaseBranch, WorktreeBranch: session.WorktreeBranch, Metadata: session.Metadata, CreatedAt: session.CreatedAt, UpdatedAt: session.UpdatedAt, MessageCount: session.MessageCount, LastMessageAt: session.LastMessageAt, Lifecycle: session.Lifecycle, Archived: archived, Deleted: deleted, Snippets: snippets}
}

func v3SessionSearchLess(a, b V3SessionSearchItem) bool {
	if a.UpdatedAt != b.UpdatedAt {
		return a.UpdatedAt > b.UpdatedAt
	}
	return a.ID > b.ID
}

func snippetList(snippet *V3SessionSearchSnippet) []V3SessionSearchSnippet {
	if snippet == nil || strings.TrimSpace(snippet.Text) == "" {
		return nil
	}
	return []V3SessionSearchSnippet{*snippet}
}

func mergeV3SessionSearchSnippets(a, b []V3SessionSearchSnippet) []V3SessionSearchSnippet {
	seen := map[string]struct{}{}
	out := make([]V3SessionSearchSnippet, 0, len(a)+len(b))
	for _, snippet := range append(a, b...) {
		key := snippet.Source + "\x00" + snippet.MessageID + "\x00" + snippet.Text
		if _, ok := seen[key]; ok {
			continue
		}
		seen[key] = struct{}{}
		out = append(out, snippet)
		if len(out) >= 3 {
			break
		}
	}
	return out
}

func truncateV3SessionSearchSnippet(text string) string {
	return truncateRunes(strings.TrimSpace(strings.Join(strings.Fields(text), " ")), v3SessionSearchSnippetMaxRunes)
}

func truncateRunes(text string, limit int) string {
	if utf8.RuneCountInString(text) <= limit {
		return text
	}
	return string([]rune(text)[:limit])
}

type v3SessionSearchSnippetSource struct {
	normalized string
	runes      []rune
	lower      []rune
}

func newV3SessionSearchSnippetSource(text string) v3SessionSearchSnippetSource {
	normalized := strings.TrimSpace(strings.Join(strings.Fields(text), " "))
	source := v3SessionSearchSnippetSource{normalized: normalized}
	if utf8.RuneCountInString(normalized) > v3SessionSearchSnippetMaxRunes {
		source.runes = []rune(normalized)
		source.lower = []rune(strings.ToLower(normalized))
	}
	return source
}

func (s v3SessionSearchSnippetSource) matchCentered(token string) string {
	if len(s.runes) == 0 {
		return s.normalized
	}
	needle := []rune(strings.ToLower(token))
	at := 0
search:
	for i := 0; i+len(needle) <= len(s.lower); i++ {
		for j := range needle {
			if s.lower[i+j] != needle[j] {
				continue search
			}
		}
		at = i
		break
	}
	start := at - v3SessionSearchSnippetMaxRunes/3
	if start < 0 {
		start = 0
	}
	if start+v3SessionSearchSnippetMaxRunes > len(s.runes) {
		start = len(s.runes) - v3SessionSearchSnippetMaxRunes
	}
	return string(s.runes[start : start+v3SessionSearchSnippetMaxRunes])
}

func matchCenteredV3SessionSearchSnippet(text, token string) string {
	return newV3SessionSearchSnippetSource(text).matchCentered(token)
}

func v3SessionSearchRecordHasTokensV2(reader pebble.Reader, sessionID string, archived bool, tokens []string) (bool, error) {
	var meta v3SessionSearchSessionMeta
	ok, err := getJSONFromReader(reader, keyV3SessionSearchMeta(sessionID), &meta)
	if err != nil || !ok {
		return false, err
	}
	wanted := make(map[string]bool, len(tokens))
	for _, token := range tokens {
		wanted[keyPart(token)] = false
	}
	state := "/active/"
	if archived {
		state = "/archived/"
	}
	for _, key := range meta.Keys {
		if !strings.Contains(key, state) {
			continue
		}
		rest, ok := strings.CutPrefix(key, keyV3SessionSearchAccountPrefix)
		if !ok {
			continue
		}
		parts := strings.SplitN(rest, "/", 4)
		if len(parts) == 4 {
			if _, exists := wanted[parts[2]]; exists {
				wanted[parts[2]] = true
			}
		}
	}
	for _, found := range wanted {
		if !found {
			return false, nil
		}
	}
	return true, nil
}

func v3SessionAttentionFromReader(reader pebble.Reader, sessionID string) (V3SessionAttentionSummary, error) {
	summary := V3SessionAttentionSummary{State: "inactive"}
	active, ok, err := getActivePlanFromReader(reader, sessionID)
	if err != nil || !ok {
		return summary, err
	}
	plan, ok, err := getPlanFromReader(reader, sessionID, active.PlanID)
	if err != nil || !ok {
		return summary, err
	}
	summary.PlanID, summary.PlanStatus = plan.ID, strings.ToLower(plan.Status)
	if plan.Document != nil {
		summary.CheckpointID = plan.Document.ActiveCheckpointID
		if plan.Document.ExecutionState != nil {
			summary.ExecutionStatus = strings.ToLower(plan.Document.ExecutionState.Status)
			summary.LastOutcome = strings.ToLower(plan.Document.ExecutionState.LastOutcome)
		}
		for _, cp := range plan.Document.Checkpoints {
			if cp.ID == summary.CheckpointID {
				summary.CheckpointStatus = strings.ToLower(cp.Status)
				break
			}
		}
	}
	switch {
	case summary.CheckpointStatus == "needs_review" || summary.ExecutionStatus == "waiting_review" || summary.LastOutcome == "needs_review":
		summary.State = "needs_review"
	case summary.CheckpointStatus == "blocked" || summary.ExecutionStatus == "blocked":
		summary.State = "blocked"
	case summary.CheckpointStatus == "failed" || summary.ExecutionStatus == "failed":
		summary.State = "failed"
	case summary.CheckpointStatus == "in_progress" || summary.ExecutionStatus == "in_progress" || summary.ExecutionStatus == "running":
		summary.State = "in_progress"
	case summary.CheckpointStatus == "pending" || summary.PlanStatus == "pending":
		summary.State = "pending"
	}
	return summary, nil
}
