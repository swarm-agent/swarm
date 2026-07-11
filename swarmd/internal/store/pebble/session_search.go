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
	v3SessionSearchIndexVersion     = 2
	keyV3SessionSearchIndexMeta     = "v3/session_search_index/meta"
	keyV3SessionSearchMetaPrefix    = "v3/session_search_meta/"
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
	SessionID string   `json:"session_id"`
	Keys      []string `json:"keys"`
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
	filtered := result.Items[:0]
	for i := range result.Items {
		_, err = getJSONFromReader(snapshot, keyV3SessionLibraryMetricFor(result.Items[i].ID), &result.Items[i].LibraryMetric)
		if err != nil {
			return V3SessionSearchResult{}, err
		}
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
	if options.ArchivedMode != v3SessionSearchArchivedExclude && len(items) <= options.Limit {
		archived, err := s.searchV3ArchivedRecentFromReader(reader, options, options.Limit+1-len(items))
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
	prefixes := []string{}
	for _, tokens := range queryTokens {
		prefixes = append(prefixes, v3SessionSearchPrefixesForOptions(options, tokens[0])...)
	}
	startOrder := ""
	if options.BeforeUpdatedAt != nil {
		startOrder = sessionRecentIndexOrderPart(*options.BeforeUpdatedAt, options.BeforeSessionID) + "\x00"
	}
	itemsByID := map[string]V3SessionSearchItem{}
	seenKeys := map[string]struct{}{}
	for _, prefix := range prefixes {
		startKey := ""
		if startOrder != "" {
			startKey = prefix + startOrder
		}
		err := scanRangeFromReader(reader, scanRangeOptions{Prefix: prefix, StartKey: startKey, Limit: sessionRecentIndexScanLimit()}, func(_ string, value []byte) (bool, error) {
			var record v3SessionSearchIndexRecord
			if err := json.Unmarshal(value, &record); err != nil {
				return false, fmt.Errorf("decode session search record: %w", err)
			}
			record.SessionID = strings.TrimSpace(record.SessionID)
			if record.SessionID == "" {
				return true, nil
			}
			if _, ok := seenKeys[record.SessionID]; !ok {
				seenKeys[record.SessionID] = struct{}{}
			}
			item, ok, err := s.searchV3ItemForRecord(reader, record, options)
			if err != nil {
				return false, err
			}
			if !ok {
				return true, nil
			}
			matched := false
			for _, tokens := range queryTokens {
				ok, verifyErr := v3SessionSearchRecordHasTokens(reader, record.SessionID, record.Archived, tokens)
				if verifyErr != nil {
					return false, verifyErr
				}
				if ok {
					matched = true
					break
				}
			}
			if !matched {
				return true, nil
			}
			if existing, ok := itemsByID[item.ID]; ok {
				existing.Snippets = mergeV3SessionSearchSnippets(existing.Snippets, item.Snippets)
				itemsByID[item.ID] = existing
			} else {
				itemsByID[item.ID] = item
			}
			return len(itemsByID) <= options.Limit, nil
		})
		if err != nil {
			return V3SessionSearchResult{}, err
		}
	}
	items := make([]V3SessionSearchItem, 0, len(itemsByID))
	for _, item := range itemsByID {
		items = append(items, item)
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
		return v3SessionSearchItemFromSession(session, true, false, snippetList(record.Snippet)), true, nil
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
		items = append(items, v3SessionSearchItemFromSession(session, true, false, nil))
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
	batch := s.store.NewBatch()
	defer batch.Close()
	if err := deleteV3SessionSearchIndexKeysFromReader(batch, s.store.db); err != nil {
		return err
	}
	if err := iteratePrefixFromReader(s.store.db, SessionPrefix(), sessionRecentIndexScanLimit(), func(_ string, value []byte) error {
		var session SessionSnapshot
		if err := json.Unmarshal(value, &session); err != nil {
			return fmt.Errorf("decode session for search index backfill: %w", err)
		}
		return s.replaceV3SessionSearchIndexInBatch(batch, s.store.db, normalizeSessionOwnership(session), false, nil)
	}); err != nil {
		return err
	}
	if err := iteratePrefixFromReader(s.store.db, V3SessionTombstonePrefix(), sessionRecentIndexScanLimit(), func(_ string, value []byte) error {
		var tombstone V3SessionTombstone
		if err := json.Unmarshal(value, &tombstone); err != nil {
			return fmt.Errorf("decode tombstone for search index backfill: %w", err)
		}
		if !tombstone.Archived || tombstone.Session.ID == "" {
			return nil
		}
		return s.replaceV3SessionSearchIndexInBatch(batch, s.store.db, normalizeSessionOwnership(tombstone.Session), true, nil)
	}); err != nil {
		return err
	}
	payload, err := json.Marshal(v3SessionSearchIndexMeta{Version: v3SessionSearchIndexVersion, IndexedAt: time.Now().UnixMilli()})
	if err != nil {
		return err
	}
	if err := batch.Set([]byte(keyV3SessionSearchIndexMeta), payload, nil); err != nil {
		return err
	}
	return batch.Commit(pebble.Sync)
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
	if ok, err := getJSONFromReader(reader, keyV3SessionSearchMeta(session.ID), &previous); err != nil {
		return err
	} else if ok {
		for _, key := range previous.Keys {
			if err := batch.Delete([]byte(key), nil); err != nil {
				return err
			}
		}
	}
	keys, err := v3SessionSearchIndexEntriesForSession(reader, session, archived, extraMessages)
	if err != nil {
		return err
	}
	for key, record := range keys {
		payload, err := json.Marshal(record)
		if err != nil {
			return err
		}
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
	for _, key := range previous.Keys {
		var record v3SessionSearchIndexRecord
		if ok, err := getJSONFromReader(reader, key, &record); err != nil {
			return err
		} else if !ok {
			continue
		}
		if err := batch.Delete([]byte(key), nil); err != nil {
			return err
		}
		movedKey, ok := v3SessionSearchKeyWithUpdatedOrder(key, session.UpdatedAt, session.ID)
		if !ok {
			return s.replaceV3SessionSearchIndexInBatch(batch, reader, session, archived, []MessageSnapshot{message})
		}
		keys[movedKey] = record
	}
	for _, token := range v3SessionSearchTokens(message.Content) {
		messageSnippet := &V3SessionSearchSnippet{Source: "message", Role: message.Role, MessageID: message.ID, GlobalSeq: message.GlobalSeq, Text: matchCenteredV3SessionSearchSnippet(message.Content, token), CreatedAt: message.CreatedAt}
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
	var previous v3SessionSearchSessionMeta
	if ok, err := getJSONFromReader(reader, keyV3SessionSearchMeta(sessionID), &previous); err != nil {
		return err
	} else if ok {
		for _, key := range previous.Keys {
			if err := batch.Delete([]byte(key), nil); err != nil {
				return err
			}
		}
	}
	if err := batch.Delete([]byte(keyV3SessionSearchMeta(sessionID)), nil); err != nil {
		return err
	}
	return nil
}

func v3SessionSearchIndexEntriesForSession(reader pebble.Reader, session SessionSnapshot, archived bool, extraMessages []MessageSnapshot) (map[string]v3SessionSearchIndexRecord, error) {
	entries := map[string]v3SessionSearchIndexRecord{}
	add := func(text string, snippet *V3SessionSearchSnippet) {
		for _, token := range v3SessionSearchTokens(text) {
			key := keyV3SessionSearchAccount(session.AccountScopeID, archived, token, session.UpdatedAt, session.ID)
			if _, ok := entries[key]; !ok {
				centered := snippet
				if snippet != nil {
					copy := *snippet
					copy.Text = matchCenteredV3SessionSearchSnippet(text, token)
					centered = &copy
				}
				entries[key] = v3SessionSearchIndexRecord{SessionID: session.ID, Archived: archived, Snippet: centered}
			}
		}
	}
	add(session.Title, &V3SessionSearchSnippet{Source: "title", Text: session.Title, CreatedAt: session.CreatedAt})
	add(session.WorkspaceName, nil)
	add(session.WorkspacePath, nil)
	messages := append([]MessageSnapshot(nil), extraMessages...)
	if reader != nil {
		err := scanRangeFromReader(reader, scanRangeOptions{Prefix: V3SessionMessagePrefix(session.ID), Limit: sessionRecentIndexScanLimit()}, func(_ string, value []byte) (bool, error) {
			var message MessageSnapshot
			if err := json.Unmarshal(value, &message); err != nil {
				return false, err
			}
			messages = append(messages, message)
			return true, nil
		})
		if err != nil {
			return nil, err
		}
	}
	for _, message := range messages {
		snippet := &V3SessionSearchSnippet{Source: "message", Role: message.Role, MessageID: message.ID, GlobalSeq: message.GlobalSeq, CreatedAt: message.CreatedAt}
		add(message.Content, snippet)
	}
	return entries, nil
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

func matchCenteredV3SessionSearchSnippet(text, token string) string {
	normalized := strings.TrimSpace(strings.Join(strings.Fields(text), " "))
	runes := []rune(normalized)
	if len(runes) <= v3SessionSearchSnippetMaxRunes {
		return normalized
	}
	lower := []rune(strings.ToLower(normalized))
	needle := []rune(strings.ToLower(token))
	at := 0
	for i := 0; i+len(needle) <= len(lower); i++ {
		if string(lower[i:i+len(needle)]) == string(needle) {
			at = i
			break
		}
	}
	start := at - v3SessionSearchSnippetMaxRunes/3
	if start < 0 {
		start = 0
	}
	if start+v3SessionSearchSnippetMaxRunes > len(runes) {
		start = len(runes) - v3SessionSearchSnippetMaxRunes
	}
	return string(runes[start : start+v3SessionSearchSnippetMaxRunes])
}

func v3SessionSearchRecordHasTokens(reader pebble.Reader, sessionID string, archived bool, tokens []string) (bool, error) {
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
