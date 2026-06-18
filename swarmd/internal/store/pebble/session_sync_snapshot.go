package pebblestore

import (
	"encoding/json"
	"errors"
	"fmt"
	"sort"
	"strings"
	"sync"
	"time"

	"github.com/cockroachdb/pebble"
)

const (
	v3SessionTombstoneScopeIndexVersion = 1

	V3SyncSnapshotHistoryModeNone = "none"
	V3SyncSnapshotHistoryModeTail = "tail"
	V3SyncSnapshotHistoryModeFull = "full"

	V3SyncSnapshotManifestPolicyError    = "error"
	V3SyncSnapshotManifestPolicyOmit     = "omit"
	V3SyncSnapshotManifestPolicyManifest = "manifest"

	V3SyncSnapshotOmissionRequiresManifest = "requires_manifest"
	V3SyncSnapshotOmissionPageBoundary     = "page_boundary"
)

var v3SyncSnapshotAfterSnapshotHookForTest struct {
	mu sync.Mutex
	fn func()
}

func setV3SyncSnapshotAfterSnapshotHookForTest(fn func()) func() {
	v3SyncSnapshotAfterSnapshotHookForTest.mu.Lock()
	previous := v3SyncSnapshotAfterSnapshotHookForTest.fn
	v3SyncSnapshotAfterSnapshotHookForTest.fn = fn
	v3SyncSnapshotAfterSnapshotHookForTest.mu.Unlock()
	return func() {
		v3SyncSnapshotAfterSnapshotHookForTest.mu.Lock()
		v3SyncSnapshotAfterSnapshotHookForTest.fn = previous
		v3SyncSnapshotAfterSnapshotHookForTest.mu.Unlock()
	}
}

func runV3SyncSnapshotAfterSnapshotHook() {
	v3SyncSnapshotAfterSnapshotHookForTest.mu.Lock()
	fn := v3SyncSnapshotAfterSnapshotHookForTest.fn
	v3SyncSnapshotAfterSnapshotHookForTest.mu.Unlock()
	if fn != nil {
		fn()
	}
}

type v3SessionTombstoneScopeIndexMeta struct {
	Version   int   `json:"version"`
	IndexedAt int64 `json:"indexed_at"`
}

type V3SyncSnapshotOptions struct {
	AccountScopeID                     string
	UserID                             string
	Global                             bool
	SessionIDs                         []string
	WorkspacePath                      string
	WorkspacePaths                     []string
	RestrictSessionIDsToWorkspacePaths bool
	RecentLimit                        int
	RecentBeforeUpdatedAt              *int64
	RecentBeforeSessionID              string
	History                            V3SyncSnapshotHistoryOptions
	IncludeRunIntents                  bool
	IncludeActiveSessions              bool
	IncludeActivePlan                  bool
	IncludePlanRevisions               bool
}

type V3SyncSnapshotHistoryOptions struct {
	Mode                  string
	MaxMessagesPerSession int
	MaxEventsPerSession   int
	ManifestPolicy        string
	IncludeMessages       bool
	IncludeEvents         bool
}

type V3SyncSnapshotResult struct {
	Rev                       uint64                                       `json:"rev"`
	SessionsByID              map[string]SessionSnapshot                   `json:"sessions_by_id"`
	ProjectionsBySession      map[string]V3SessionProjection               `json:"projections_by_session"`
	MessagesBySession         map[string][]MessageSnapshot                 `json:"messages_by_session"`
	EventsBySession           map[string][]V3SessionEvent                  `json:"events_by_session"`
	RunIntentsBySession       map[string][]V3SessionRunIntent              `json:"run_intents_by_session"`
	HistoryManifestsBySession map[string][]V3SessionHistoryChunkDescriptor `json:"history_manifests_by_session"`
	HistoryChunksByID         map[string]V3SessionHistoryChunk             `json:"history_chunks_by_id"`
	Omissions                 []V3SyncSnapshotOmission                     `json:"omissions"`
	Pagination                V3SyncSnapshotPagination                     `json:"pagination"`
	Watermarks                V3SyncSnapshotWatermarks                     `json:"watermarks"`
	SessionOrder              []string                                     `json:"session_order"`
	PermissionsBySession      map[string][]PermissionRecord                `json:"permissions_by_session"`
	UsageBySession            map[string]SessionUsageSummary               `json:"usage_by_session"`
	PlansBySession            map[string]SessionPlanSnapshot               `json:"plans_by_session"`
	PlanRevisionsBySession    map[string][]SessionPlanSnapshot             `json:"plan_revisions_by_session"`
	TombstonesBySession       map[string]V3SessionTombstone                `json:"tombstones_by_session"`
}

type V3SyncSnapshotPagination struct {
	NextBeforeUpdatedAt *int64 `json:"next_before_updated_at,omitempty"`
	NextBeforeSessionID string `json:"next_before_session_id,omitempty"`
	HasMore             bool   `json:"has_more"`
}

type V3SyncSnapshotWatermarks struct {
	LoadedAt     int64 `json:"loaded_at"`
	MaxUpdatedAt int64 `json:"max_updated_at,omitempty"`
}

type V3SyncSnapshotOmission struct {
	SessionID   string `json:"session_id,omitempty"`
	Resource    string `json:"resource"`
	Reason      string `json:"reason"`
	NextCursor  string `json:"next_cursor,omitempty"`
	ManifestRef string `json:"manifest_ref,omitempty"`
}

func (s *SessionStore) BuildV3SyncSnapshot(options V3SyncSnapshotOptions) (result V3SyncSnapshotResult, err error) {
	if s == nil || s.store == nil {
		return V3SyncSnapshotResult{}, errors.New("session store is not configured")
	}
	options = normalizeV3SyncSnapshotOptions(options)
	if options.RecentLimit > 0 {
		if err := s.ensureSessionRecentIndex(); err != nil {
			return V3SyncSnapshotResult{}, err
		}
	}
	if err := s.ensureV3SessionTombstoneScopeIndexes(options.AccountScopeID); err != nil {
		return V3SyncSnapshotResult{}, err
	}
	if !options.Global && len(options.SessionIDs) == 0 && options.RecentLimit <= 0 && strings.TrimSpace(options.WorkspacePath) == "" && len(options.WorkspacePaths) == 0 {
		return V3SyncSnapshotResult{}, errors.New("at least one sync snapshot selector is required")
	}
	snapshot := s.store.db.NewSnapshot()
	runV3SyncSnapshotAfterSnapshotHook()
	defer func() {
		if closeErr := snapshot.Close(); err == nil && closeErr != nil {
			err = closeErr
		}
	}()
	return s.buildV3SyncSnapshotFromReader(snapshot, options)
}

func normalizeV3SyncSnapshotOptions(options V3SyncSnapshotOptions) V3SyncSnapshotOptions {
	options.AccountScopeID = strings.TrimSpace(options.AccountScopeID)
	options.UserID = strings.TrimSpace(options.UserID)
	options.WorkspacePath = strings.TrimSpace(options.WorkspacePath)
	options.WorkspacePaths = normalizeV3SyncSnapshotWorkspacePaths(options.WorkspacePath, options.WorkspacePaths)
	options.RecentBeforeSessionID = strings.TrimSpace(options.RecentBeforeSessionID)
	seen := map[string]struct{}{}
	ids := make([]string, 0, len(options.SessionIDs))
	for _, id := range options.SessionIDs {
		id = strings.TrimSpace(id)
		if id == "" {
			continue
		}
		if _, ok := seen[id]; ok {
			continue
		}
		seen[id] = struct{}{}
		ids = append(ids, id)
	}
	options.SessionIDs = ids
	options.History.Mode = strings.TrimSpace(strings.ToLower(options.History.Mode))
	if options.History.Mode == "" {
		options.History.Mode = V3SyncSnapshotHistoryModeNone
	}
	if options.History.Mode == V3SyncSnapshotHistoryModeTail || options.History.Mode == V3SyncSnapshotHistoryModeFull {
		options.History.IncludeMessages = true
	}
	options.History.ManifestPolicy = strings.TrimSpace(strings.ToLower(options.History.ManifestPolicy))
	if options.History.ManifestPolicy == "" {
		options.History.ManifestPolicy = V3SyncSnapshotManifestPolicyError
	}
	return options
}

func (s *SessionStore) buildV3SyncSnapshotFromReader(reader pebble.Reader, options V3SyncSnapshotOptions) (V3SyncSnapshotResult, error) {
	selected, pagination, err := s.selectV3SyncSnapshotSessions(reader, options)
	if err != nil {
		return V3SyncSnapshotResult{}, err
	}
	rev, err := readV3RealtimeOutboxSequenceFromReader(reader)
	if err != nil {
		return V3SyncSnapshotResult{}, err
	}
	result := V3SyncSnapshotResult{
		Rev:                       rev,
		SessionsByID:              map[string]SessionSnapshot{},
		ProjectionsBySession:      map[string]V3SessionProjection{},
		MessagesBySession:         map[string][]MessageSnapshot{},
		EventsBySession:           map[string][]V3SessionEvent{},
		RunIntentsBySession:       map[string][]V3SessionRunIntent{},
		HistoryManifestsBySession: map[string][]V3SessionHistoryChunkDescriptor{},
		HistoryChunksByID:         map[string]V3SessionHistoryChunk{},
		Omissions:                 []V3SyncSnapshotOmission{},
		Pagination:                pagination,
		Watermarks:                V3SyncSnapshotWatermarks{LoadedAt: time.Now().UnixMilli()},
		SessionOrder:              make([]string, 0, len(selected)),
		PermissionsBySession:      map[string][]PermissionRecord{},
		UsageBySession:            map[string]SessionUsageSummary{},
		PlansBySession:            map[string]SessionPlanSnapshot{},
		PlanRevisionsBySession:    map[string][]SessionPlanSnapshot{},
		TombstonesBySession:       map[string]V3SessionTombstone{},
	}
	for _, session := range selected {
		if strings.TrimSpace(session.ID) == "" {
			continue
		}
		projection, ok, err := getV3SessionProjectionFromReader(reader, session.ID)
		if err != nil {
			return V3SyncSnapshotResult{}, err
		}
		if !ok {
			projection = V3SessionProjection{SessionID: session.ID}
		}
		if result.Watermarks.MaxUpdatedAt == 0 || session.UpdatedAt > result.Watermarks.MaxUpdatedAt {
			result.Watermarks.MaxUpdatedAt = session.UpdatedAt
		}
		result.SessionsByID[session.ID] = session
		result.ProjectionsBySession[session.ID] = projection
		result.MessagesBySession[session.ID] = []MessageSnapshot{}
		result.EventsBySession[session.ID] = []V3SessionEvent{}
		result.RunIntentsBySession[session.ID] = []V3SessionRunIntent{}
		result.HistoryManifestsBySession[session.ID] = []V3SessionHistoryChunkDescriptor{}
		result.PermissionsBySession[session.ID] = []PermissionRecord{}
		result.SessionOrder = append(result.SessionOrder, session.ID)
		if err := s.addV3SyncSnapshotHistory(reader, options, session, projection, &result); err != nil {
			return V3SyncSnapshotResult{}, err
		}
		if options.IncludeRunIntents {
			if err := s.addV3SyncSnapshotRunIntents(reader, options, session, projection, &result); err != nil {
				return V3SyncSnapshotResult{}, err
			}
		}
		permissions, err := listPendingPermissionsFromReader(reader, session.ID, 200)
		if err != nil {
			return V3SyncSnapshotResult{}, err
		}
		result.PermissionsBySession[session.ID] = permissions
		if usage, ok, err := getUsageSummaryFromReader(reader, session.ID); err != nil {
			return V3SyncSnapshotResult{}, err
		} else if ok {
			result.UsageBySession[session.ID] = usage
		}
		if err := s.addV3SyncSnapshotPlans(reader, options, session.ID, &result); err != nil {
			return V3SyncSnapshotResult{}, err
		}
	}
	if err := s.addV3SyncSnapshotTombstones(reader, options, &result); err != nil {
		return V3SyncSnapshotResult{}, err
	}
	return result, nil
}

func (s *SessionStore) selectV3SyncSnapshotSessions(reader pebble.Reader, options V3SyncSnapshotOptions) ([]SessionSnapshot, V3SyncSnapshotPagination, error) {
	byID := map[string]SessionSnapshot{}
	order := []string{}
	appendSession := func(session SessionSnapshot) {
		id := strings.TrimSpace(session.ID)
		if id == "" {
			return
		}
		if _, ok := byID[id]; ok {
			return
		}
		byID[id] = session
		order = append(order, id)
	}
	for _, id := range options.SessionIDs {
		session, ok, err := s.getSessionFromReader(reader, id)
		if err != nil {
			return nil, V3SyncSnapshotPagination{}, err
		}
		if !ok || !v3SyncSnapshotSessionVisible(session, options.AccountScopeID, options.UserID, "") {
			continue
		}
		if options.RestrictSessionIDsToWorkspacePaths && !v3SyncSnapshotSessionVisibleForWorkspaces(session, options.AccountScopeID, options.UserID, options.WorkspacePath, options.WorkspacePaths) {
			continue
		}
		appendSession(session)
	}
	if options.Global {
		global, err := s.selectV3GlobalSyncSnapshotSessions(reader, options)
		if err != nil {
			return nil, V3SyncSnapshotPagination{}, err
		}
		for _, session := range global {
			appendSession(session)
		}
	}
	pagination := V3SyncSnapshotPagination{}
	if options.RecentLimit > 0 {
		recent, page, err := s.selectV3RecentSyncSnapshotSessions(reader, options)
		if err != nil {
			return nil, V3SyncSnapshotPagination{}, err
		}
		pagination = page
		for _, session := range recent {
			appendSession(session)
		}
	}
	if options.IncludeActiveSessions {
		active, err := s.selectV3ActiveSyncSnapshotSessions(reader, options)
		if err != nil {
			return nil, V3SyncSnapshotPagination{}, err
		}
		for _, session := range active {
			appendSession(session)
		}
	}
	selected := make([]SessionSnapshot, 0, len(order))
	for _, id := range order {
		selected = append(selected, byID[id])
	}
	return selected, pagination, nil
}

func (s *SessionStore) selectV3GlobalSyncSnapshotSessions(reader pebble.Reader, options V3SyncSnapshotOptions) ([]SessionSnapshot, error) {
	prefix := SessionByAccountPrefix(options.AccountScopeID)
	if strings.TrimSpace(options.AccountScopeID) == "" {
		prefix = SessionPrefix()
	}
	sessions := make([]SessionSnapshot, 0)
	err := iteratePrefixFromReader(reader, prefix, int(^uint(0)>>1), func(_ string, value []byte) error {
		sessionID := strings.TrimSpace(string(value))
		if strings.TrimSpace(options.AccountScopeID) == "" {
			var session SessionSnapshot
			if err := json.Unmarshal(value, &session); err != nil {
				return err
			}
			if !v3SyncSnapshotSessionVisibleForWorkspaces(session, options.AccountScopeID, options.UserID, options.WorkspacePath, options.WorkspacePaths) {
				return nil
			}
			sessions = append(sessions, session)
			return nil
		}
		if sessionID == "" {
			return nil
		}
		session, ok, err := s.getSessionFromReader(reader, sessionID)
		if err != nil {
			return err
		}
		if !ok || !v3SyncSnapshotSessionVisibleForWorkspaces(session, options.AccountScopeID, options.UserID, options.WorkspacePath, options.WorkspacePaths) {
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

func (s *SessionStore) selectV3RecentSyncSnapshotSessions(reader pebble.Reader, options V3SyncSnapshotOptions) ([]SessionSnapshot, V3SyncSnapshotPagination, error) {
	limit := options.RecentLimit
	if limit <= 0 {
		return nil, V3SyncSnapshotPagination{}, nil
	}
	prefix := sessionRecentIndexPrefixForSyncSnapshot(options)
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
		if !v3SyncSnapshotSessionVisibleForWorkspaces(session, options.AccountScopeID, options.UserID, options.WorkspacePath, options.WorkspacePaths) {
			return true, nil
		}
		sessions = append(sessions, session)
		return len(sessions) <= limit, nil
	})
	if err != nil {
		return nil, V3SyncSnapshotPagination{}, err
	}
	pagination := V3SyncSnapshotPagination{}
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

func sessionRecentIndexPrefixForSyncSnapshot(options V3SyncSnapshotOptions) string {
	accountScopeID := strings.TrimSpace(options.AccountScopeID)
	paths := options.WorkspacePaths
	if len(paths) == 0 {
		paths = normalizeV3SyncSnapshotWorkspacePaths(options.WorkspacePath, nil)
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

func (s *SessionStore) selectV3ActiveSyncSnapshotSessions(reader pebble.Reader, options V3SyncSnapshotOptions) ([]SessionSnapshot, error) {
	states, err := listV3ActiveSessionRunStatesFromReader(reader, options.AccountScopeID, 0)
	if err != nil {
		return nil, err
	}
	out := make([]SessionSnapshot, 0, len(states))
	for _, state := range states {
		session, ok, err := s.getSessionFromReader(reader, state.SessionID)
		if err != nil {
			return nil, err
		}
		if !ok || !v3SyncSnapshotSessionVisibleForWorkspaces(session, options.AccountScopeID, options.UserID, options.WorkspacePath, options.WorkspacePaths) {
			continue
		}
		out = append(out, session)
	}
	return out, nil
}

func normalizeV3SyncSnapshotWorkspacePaths(workspacePath string, workspacePaths []string) []string {
	seen := map[string]struct{}{}
	out := make([]string, 0, len(workspacePaths)+1)
	appendPath := func(path string) {
		path = strings.TrimSpace(path)
		if path == "" {
			return
		}
		normalized, err := normalizeSessionPath(path)
		if err != nil {
			return
		}
		if _, ok := seen[normalized]; ok {
			return
		}
		seen[normalized] = struct{}{}
		out = append(out, normalized)
	}
	appendPath(workspacePath)
	for _, path := range workspacePaths {
		appendPath(path)
	}
	return out
}

func (s *SessionStore) ensureV3SessionTombstoneScopeIndexes(accountScopeID string) error {
	if s == nil || s.store == nil {
		return errors.New("session store is not configured")
	}
	accountScopeID = strings.TrimSpace(accountScopeID)
	metaKey := KeyV3SessionTombstoneScopeIndexMeta(accountScopeID)
	var meta v3SessionTombstoneScopeIndexMeta
	if ok, err := s.store.GetJSON(metaKey, &meta); err != nil {
		return fmt.Errorf("read v3 session tombstone scope index metadata: %w", err)
	} else if ok && meta.Version == v3SessionTombstoneScopeIndexVersion {
		return nil
	}

	prefix := V3SessionTombstonePrefix()
	if accountScopeID != "" {
		prefix = V3SessionTombstoneByAccountPrefix(accountScopeID)
	}
	batch := s.store.NewBatch()
	defer batch.Close()
	if err := iteratePrefixFromReader(s.store.db, prefix, sessionRecentIndexScanLimit(), func(_ string, value []byte) error {
		var tombstone V3SessionTombstone
		if err := json.Unmarshal(value, &tombstone); err != nil {
			return fmt.Errorf("decode v3 tombstone for scope index backfill: %w", err)
		}
		tombstone = normalizeV3SessionTombstone(tombstone)
		if tombstone.SessionID == "" {
			return nil
		}
		if accountScopeID != "" && tombstone.AccountScopeID != accountScopeID {
			return nil
		}
		return setV3SessionTombstoneInBatch(batch, tombstone)
	}); err != nil {
		return err
	}
	payload, err := json.Marshal(v3SessionTombstoneScopeIndexMeta{Version: v3SessionTombstoneScopeIndexVersion, IndexedAt: time.Now().UnixMilli()})
	if err != nil {
		return fmt.Errorf("marshal v3 session tombstone scope index metadata: %w", err)
	}
	if err := batch.Set([]byte(metaKey), payload, nil); err != nil {
		return fmt.Errorf("write v3 session tombstone scope index metadata: %w", err)
	}
	return batch.Commit(pebble.Sync)
}

func (s *SessionStore) addV3SyncSnapshotTombstones(reader pebble.Reader, options V3SyncSnapshotOptions, result *V3SyncSnapshotResult) error {
	selectedIDs := make(map[string]struct{}, len(result.SessionsByID)+len(options.SessionIDs))
	for sessionID := range result.SessionsByID {
		selectedIDs[sessionID] = struct{}{}
	}
	for _, sessionID := range options.SessionIDs {
		sessionID = strings.TrimSpace(sessionID)
		if sessionID == "" {
			continue
		}
		selectedIDs[sessionID] = struct{}{}
	}
	for sessionID := range selectedIDs {
		var tombstone V3SessionTombstone
		ok, err := getJSONFromReader(reader, KeyV3SessionTombstone(sessionID), &tombstone)
		if err != nil {
			return err
		}
		if !ok {
			continue
		}
		tombstone = normalizeV3SessionTombstone(tombstone)
		if !v3SyncSnapshotTombstoneVisibleForSelector(tombstone, options) {
			if len(options.SessionIDs) > 0 && v3SyncSnapshotTombstoneFailsClosedForRequestedAccount(tombstone, options) {
				result.Omissions = append(result.Omissions, V3SyncSnapshotOmission{SessionID: sessionID, Resource: "tombstones", Reason: "bootstrap_required"})
			}
			continue
		}
		result.TombstonesBySession[tombstone.SessionID] = tombstone
	}

	if len(options.SessionIDs) > 0 {
		return nil
	}
	if options.RecentLimit > 0 {
		return s.addV3SyncSnapshotRecentTombstones(reader, options, result)
	}
	return s.addV3SyncSnapshotBoundedSelectorTombstones(reader, options, result)
}

func v3SyncSnapshotTombstoneFailsClosedForRequestedAccount(tombstone V3SessionTombstone, options V3SyncSnapshotOptions) bool {
	if strings.TrimSpace(tombstone.SessionID) == "" {
		return false
	}
	if strings.TrimSpace(options.AccountScopeID) == "" || strings.TrimSpace(tombstone.AccountScopeID) != strings.TrimSpace(options.AccountScopeID) {
		return false
	}
	return strings.TrimSpace(options.UserID) != "" && strings.TrimSpace(tombstone.UserID) == ""
}

func v3SyncSnapshotTombstonePageCursor(page V3SyncSnapshotPagination) string {
	if page.NextBeforeUpdatedAt == nil || strings.TrimSpace(page.NextBeforeSessionID) == "" {
		return "recent.before_updated_at/before_session_id"
	}
	return fmt.Sprintf("recent.before_updated_at=%d&before_session_id=%s", *page.NextBeforeUpdatedAt, page.NextBeforeSessionID)
}

func (s *SessionStore) addV3SyncSnapshotRecentTombstones(reader pebble.Reader, options V3SyncSnapshotOptions, result *V3SyncSnapshotResult) error {
	limit := options.RecentLimit
	if limit <= 0 {
		return nil
	}
	tombstones, page, err := listV3SyncSnapshotRecentTombstonesFromReader(reader, options, limit)
	if err != nil {
		return err
	}
	for _, tombstone := range tombstones {
		if _, already := result.TombstonesBySession[tombstone.SessionID]; already {
			continue
		}
		if !v3SyncSnapshotTombstoneVisibleForSelector(tombstone, options) {
			continue
		}
		result.TombstonesBySession[tombstone.SessionID] = tombstone
	}
	if page.HasMore && !result.Pagination.HasMore {
		result.Pagination = page
	} else if page.HasMore {
		result.Omissions = append(result.Omissions, V3SyncSnapshotOmission{Resource: "tombstones", Reason: V3SyncSnapshotOmissionPageBoundary, NextCursor: v3SyncSnapshotTombstonePageCursor(page)})
	}
	return nil
}

func (s *SessionStore) addV3SyncSnapshotBoundedSelectorTombstones(reader pebble.Reader, options V3SyncSnapshotOptions, result *V3SyncSnapshotResult) error {
	const limit = 1000
	tombstones, capped, err := listV3SyncSnapshotBoundedTombstonesFromReader(reader, options, limit)
	if err != nil {
		return err
	}
	for _, tombstone := range tombstones {
		if _, already := result.TombstonesBySession[tombstone.SessionID]; already {
			continue
		}
		if !v3SyncSnapshotTombstoneVisibleForSelector(tombstone, options) {
			continue
		}
		result.TombstonesBySession[tombstone.SessionID] = tombstone
	}
	if capped {
		return errors.New("sync snapshot tombstones exceeded bounded selector limit; retry with recent.limit and pagination")
	}
	return nil
}

func v3SyncSnapshotTombstoneVisible(tombstone V3SessionTombstone, accountScopeID, userID string) bool {
	if strings.TrimSpace(tombstone.SessionID) == "" {
		return false
	}
	if strings.TrimSpace(accountScopeID) != "" {
		if strings.TrimSpace(tombstone.AccountScopeID) == "" || strings.TrimSpace(tombstone.AccountScopeID) != strings.TrimSpace(accountScopeID) {
			return false
		}
	}
	if strings.TrimSpace(userID) != "" {
		if strings.TrimSpace(tombstone.UserID) == "" || strings.TrimSpace(tombstone.UserID) != strings.TrimSpace(userID) {
			return false
		}
	}
	return true
}

func v3SyncSnapshotTombstoneVisibleForSelector(tombstone V3SessionTombstone, options V3SyncSnapshotOptions) bool {
	if !v3SyncSnapshotTombstoneVisible(tombstone, options.AccountScopeID, options.UserID) {
		return false
	}
	if len(options.SessionIDs) > 0 {
		for _, sessionID := range options.SessionIDs {
			if strings.TrimSpace(sessionID) == tombstone.SessionID {
				return true
			}
		}
		return false
	}
	return v3SyncSnapshotTombstoneVisibleForWorkspaces(tombstone, options.WorkspacePath, options.WorkspacePaths)
}

func v3SyncSnapshotTombstoneVisibleForWorkspaces(tombstone V3SessionTombstone, workspacePath string, workspacePaths []string) bool {
	paths := workspacePaths
	if len(paths) == 0 {
		paths = normalizeV3SyncSnapshotWorkspacePaths(workspacePath, nil)
	}
	if len(paths) == 0 {
		return true
	}
	normalizedTombstonePath, err := normalizeSessionPath(tombstone.WorkspacePath)
	if err != nil {
		return false
	}
	for _, path := range paths {
		if normalizedTombstonePath == path {
			return true
		}
	}
	return false
}

func v3SyncSnapshotSessionVisibleForWorkspaces(session SessionSnapshot, accountScopeID, userID, workspacePath string, workspacePaths []string) bool {
	if !v3SyncSnapshotSessionVisible(session, accountScopeID, userID, "") {
		return false
	}
	paths := workspacePaths
	if len(paths) == 0 {
		paths = normalizeV3SyncSnapshotWorkspacePaths(workspacePath, nil)
	}
	if len(paths) == 0 {
		return true
	}
	normalizedSessionPath, err := normalizeSessionPath(session.WorkspacePath)
	if err != nil {
		return false
	}
	for _, path := range paths {
		if normalizedSessionPath == path {
			return true
		}
	}
	return false
}

func v3SyncSnapshotSessionVisible(session SessionSnapshot, accountScopeID, userID, workspacePath string) bool {
	if strings.TrimSpace(accountScopeID) != "" {
		if strings.TrimSpace(session.AccountScopeID) == "" || strings.TrimSpace(session.AccountScopeID) != strings.TrimSpace(accountScopeID) {
			return false
		}
	}
	if strings.TrimSpace(userID) != "" {
		if strings.TrimSpace(session.UserID) == "" || strings.TrimSpace(session.UserID) != strings.TrimSpace(userID) {
			return false
		}
	}
	workspacePath = strings.TrimSpace(workspacePath)
	if workspacePath == "" {
		return true
	}
	normalizedSessionPath, err := normalizeSessionPath(session.WorkspacePath)
	if err != nil {
		return false
	}
	normalizedWorkspacePath, err := normalizeSessionPath(workspacePath)
	if err != nil {
		return false
	}
	return normalizedSessionPath == normalizedWorkspacePath
}

func (s *SessionStore) addV3SyncSnapshotHistory(reader pebble.Reader, options V3SyncSnapshotOptions, session SessionSnapshot, projection V3SessionProjection, result *V3SyncSnapshotResult) error {
	switch options.History.Mode {
	case V3SyncSnapshotHistoryModeNone, V3SyncSnapshotHistoryModeTail, V3SyncSnapshotHistoryModeFull:
	default:
		return fmt.Errorf("unsupported sync snapshot history mode %q", options.History.Mode)
	}
	if options.History.IncludeMessages {
		if err := s.addV3SyncSnapshotMessages(reader, options, session, result); err != nil {
			return err
		}
	}
	if options.History.IncludeEvents {
		if err := s.addV3SyncSnapshotEvents(reader, options, session, projection, result); err != nil {
			return err
		}
	}
	return nil
}

func (s *SessionStore) addV3SyncSnapshotMessages(reader pebble.Reader, options V3SyncSnapshotOptions, session SessionSnapshot, result *V3SyncSnapshotResult) error {
	limit, capped := v3SyncSnapshotResourceLimit(options.History.Mode, options.History.MaxMessagesPerSession, session.MessageCount)
	if limit == 0 && session.MessageCount > 0 {
		return s.handleV3SyncSnapshotResourceOmission(options, session.ID, "messages", V3SyncSnapshotOmissionRequiresManifest, fmt.Sprintf("%s:messages:1", session.ID), nil, result)
	}
	messages := []MessageSnapshot{}
	var err error
	if limit > 0 {
		if options.History.Mode == V3SyncSnapshotHistoryModeTail {
			messages, err = listV3SessionMessageTailFromReader(reader, session.ID, limit)
		} else {
			messages, err = listV3SessionMessagesFromReader(reader, session.ID, 0, limit)
		}
		if err != nil {
			return err
		}
	}
	if capped || len(messages) < session.MessageCount {
		result.MessagesBySession[session.ID] = messages
		if err := s.handleV3SyncSnapshotResourceOmission(options, session.ID, "messages", V3SyncSnapshotOmissionRequiresManifest, v3SyncSnapshotMessagesNextCursor(session.ID, messages), &messages, result); err != nil {
			return err
		}
		return nil
	}
	result.MessagesBySession[session.ID] = messages
	return nil
}

func (s *SessionStore) addV3SyncSnapshotEvents(reader pebble.Reader, options V3SyncSnapshotOptions, session SessionSnapshot, projection V3SessionProjection, result *V3SyncSnapshotResult) error {
	if projection.LastEventSeq == 0 {
		return nil
	}
	limit := options.History.MaxEventsPerSession
	if limit <= 0 && options.History.Mode == V3SyncSnapshotHistoryModeFull {
		limit = int(projection.LastEventSeq)
	}
	if limit <= 0 {
		return s.handleV3SyncSnapshotResourceOmission(options, session.ID, "events", V3SyncSnapshotOmissionRequiresManifest, fmt.Sprintf("%s:events:1", session.ID), nil, result)
	}
	events, capped, err := listV3SyncSnapshotEventsFromReader(reader, session.ID, 0, limit)
	if err != nil {
		return err
	}
	if capped {
		if err := s.handleV3SyncSnapshotResourceOmission(options, session.ID, "events", V3SyncSnapshotOmissionRequiresManifest, v3SyncSnapshotEventsNextCursor(session.ID, events), &events, result); err != nil {
			return err
		}
		return nil
	}
	result.EventsBySession[session.ID] = events
	return nil
}

func listV3SyncSnapshotEventsFromReader(reader pebble.Reader, sessionID string, afterSeq uint64, limit int) ([]V3SessionEvent, bool, error) {
	sessionID = strings.TrimSpace(sessionID)
	if sessionID == "" {
		return nil, false, errors.New("session id is required")
	}
	if limit <= 0 {
		return []V3SessionEvent{}, false, nil
	}
	out := make([]V3SessionEvent, 0, limit)
	capped := false
	prefix := V3SessionEventPrefix(sessionID)
	err := scanRangeFromReader(reader, scanRangeOptions{Prefix: prefix, StartKey: KeyV3SessionEvent(sessionID, afterSeq+1), Limit: int(^uint(0) >> 1)}, func(_ string, value []byte) (bool, error) {
		var event V3SessionEvent
		if err := json.Unmarshal(value, &event); err != nil {
			return false, err
		}
		if event.Seq <= afterSeq || v3SyncSnapshotEventOmitted(event) {
			return true, nil
		}
		if len(out) >= limit {
			capped = true
			return false, nil
		}
		out = append(out, event)
		return true, nil
	})
	if err != nil {
		return nil, false, err
	}
	return out, capped, nil
}

func v3SyncSnapshotEventOmitted(event V3SessionEvent) bool {
	return strings.HasPrefix(strings.TrimSpace(event.EventType), "session.diagnostic")
}

func (s *SessionStore) addV3SyncSnapshotRunIntents(reader pebble.Reader, options V3SyncSnapshotOptions, session SessionSnapshot, projection V3SessionProjection, result *V3SyncSnapshotResult) error {
	if projection.LastEventSeq == 0 {
		return nil
	}
	limit := int(projection.LastEventSeq)
	if options.History.MaxEventsPerSession > 0 && options.History.MaxEventsPerSession < limit {
		limit = options.History.MaxEventsPerSession
	}
	intents, err := listV3SessionRunIntentsFromReader(reader, session.ID, 0, limit)
	if err != nil {
		return err
	}
	result.RunIntentsBySession[session.ID] = intents
	return nil
}

func v3SyncSnapshotResourceLimit(mode string, requested, total int) (limit int, capped bool) {
	if total <= 0 {
		return 0, false
	}
	if requested > 0 {
		return requested, requested < total
	}
	if mode == V3SyncSnapshotHistoryModeFull {
		return total, false
	}
	return 0, true
}

func (s *SessionStore) handleV3SyncSnapshotResourceOmission(options V3SyncSnapshotOptions, sessionID, resource, reason, nextCursor string, inline any, result *V3SyncSnapshotResult) error {
	switch options.History.ManifestPolicy {
	case V3SyncSnapshotManifestPolicyOmit:
		omission := V3SyncSnapshotOmission{SessionID: sessionID, Resource: resource, Reason: reason, NextCursor: nextCursor}
		result.Omissions = append(result.Omissions, omission)
		return nil
	case V3SyncSnapshotManifestPolicyManifest:
		descriptor, _ := v3SyncSnapshotHistoryDescriptor(sessionID, resource, inline)
		result.HistoryManifestsBySession[sessionID] = append(result.HistoryManifestsBySession[sessionID], descriptor)
		manifestRef := fmt.Sprintf("%s:%s", sessionID, resource)
		omission := V3SyncSnapshotOmission{SessionID: sessionID, Resource: resource, Reason: reason, NextCursor: nextCursor, ManifestRef: manifestRef}
		result.Omissions = append(result.Omissions, omission)
		return nil
	case V3SyncSnapshotManifestPolicyError:
		fallthrough
	default:
		return fmt.Errorf("sync snapshot %s for session %q resource %q", reason, sessionID, resource)
	}
}

func v3SyncSnapshotHistoryDescriptor(sessionID, resource string, inline any) (V3SessionHistoryChunkDescriptor, V3SessionHistoryChunk) {
	descriptor := V3SessionHistoryChunkDescriptor{Resource: resource, Complete: true}
	chunk := V3SessionHistoryChunk{Resource: resource}
	switch values := inline.(type) {
	case *[]MessageSnapshot:
		messages := append([]MessageSnapshot(nil), (*values)...)
		chunk.Messages = messages
		if len(messages) > 0 {
			descriptor.FromSeq = messages[0].GlobalSeq
			descriptor.ToSeq = messages[len(messages)-1].GlobalSeq
		}
		descriptor.MessageCount = len(messages)
	case *[]V3SessionEvent:
		events := append([]V3SessionEvent(nil), (*values)...)
		chunk.Events = events
		if len(events) > 0 {
			descriptor.FromSeq = events[0].Seq
			descriptor.ToSeq = events[len(events)-1].Seq
		}
		descriptor.EventCount = len(events)
	default:
		descriptor.Complete = false
	}
	if descriptor.FromSeq == 0 {
		descriptor.FromSeq = 1
	}
	if descriptor.ToSeq == 0 {
		descriptor.ToSeq = descriptor.FromSeq
	}
	descriptor.ChunkID = fmt.Sprintf("%s:%s:%d-%d", sessionID, resource, descriptor.FromSeq, descriptor.ToSeq)
	chunk.ChunkID = descriptor.ChunkID
	return descriptor, chunk
}

func v3SyncSnapshotMessagesNextCursor(sessionID string, messages []MessageSnapshot) string {
	if len(messages) == 0 {
		return fmt.Sprintf("%s:messages:1", sessionID)
	}
	return fmt.Sprintf("%s:messages:%d", sessionID, messages[len(messages)-1].GlobalSeq+1)
}

func v3SyncSnapshotEventsNextCursor(sessionID string, events []V3SessionEvent) string {
	if len(events) == 0 {
		return fmt.Sprintf("%s:events:1", sessionID)
	}
	return fmt.Sprintf("%s:events:%d", sessionID, events[len(events)-1].Seq+1)
}

func listPendingPermissionsFromReader(reader pebble.Reader, sessionID string, limit int) ([]PermissionRecord, error) {
	if limit <= 0 {
		limit = 200
	}
	out := make([]PermissionRecord, 0, limit)
	err := iteratePrefixFromReader(reader, PermissionPendingPrefix(sessionID), limit, func(_ string, value []byte) error {
		recordKey := strings.TrimSpace(string(value))
		if recordKey == "" {
			return nil
		}
		var record PermissionRecord
		ok, err := getJSONFromReader(reader, recordKey, &record)
		if err != nil {
			return err
		}
		if !ok || !strings.EqualFold(strings.TrimSpace(record.Status), PermissionStatusPending) {
			return nil
		}
		out = append(out, record)
		return nil
	})
	if err != nil {
		return nil, err
	}
	sort.Slice(out, func(i, j int) bool {
		if out[i].CreatedAt == out[j].CreatedAt {
			return out[i].ID < out[j].ID
		}
		return out[i].CreatedAt < out[j].CreatedAt
	})
	if len(out) > limit {
		out = out[:limit]
	}
	return out, nil
}

func getUsageSummaryFromReader(reader pebble.Reader, sessionID string) (SessionUsageSummary, bool, error) {
	var summary SessionUsageSummary
	ok, err := getJSONFromReader(reader, KeySessionUsageSummary(sessionID), &summary)
	if err != nil || !ok {
		return SessionUsageSummary{}, ok, err
	}
	return summary, true, nil
}

func (s *SessionStore) addV3SyncSnapshotPlans(reader pebble.Reader, options V3SyncSnapshotOptions, sessionID string, result *V3SyncSnapshotResult) error {
	if !options.IncludeActivePlan && !options.IncludePlanRevisions {
		return nil
	}
	active, ok, err := getActivePlanFromReader(reader, sessionID)
	if err != nil || !ok {
		return err
	}
	plan, found, err := getPlanFromReader(reader, sessionID, active.PlanID)
	if err != nil || !found {
		return err
	}
	plan.Active = true
	if options.IncludeActivePlan {
		result.PlansBySession[sessionID] = plan
	}
	if options.IncludePlanRevisions {
		revisions, err := listPlanRevisionsFromReader(reader, sessionID, plan.ID, 100)
		if err != nil {
			return err
		}
		result.PlanRevisionsBySession[sessionID] = revisions
	}
	return nil
}

func getActivePlanFromReader(reader pebble.Reader, sessionID string) (SessionPlanActive, bool, error) {
	var active SessionPlanActive
	ok, err := getJSONFromReader(reader, KeySessionPlanActive(sessionID), &active)
	if err != nil || !ok {
		return SessionPlanActive{}, ok, err
	}
	active.UserID = strings.TrimSpace(active.UserID)
	active.AccountScopeID = strings.TrimSpace(active.AccountScopeID)
	return active, true, nil
}

func getPlanFromReader(reader pebble.Reader, sessionID, planID string) (SessionPlanSnapshot, bool, error) {
	var plan SessionPlanSnapshot
	ok, err := getJSONFromReader(reader, KeySessionPlan(sessionID, planID), &plan)
	if err != nil || !ok {
		return SessionPlanSnapshot{}, ok, err
	}
	return plan, true, nil
}

func listPlanRevisionsFromReader(reader pebble.Reader, sessionID, planID string, limit int) ([]SessionPlanSnapshot, error) {
	if limit <= 0 {
		limit = 200
	}
	out := make([]SessionPlanSnapshot, 0, limit)
	err := iteratePrefixFromReader(reader, SessionPlanRevisionPrefix(sessionID, planID), 20000, func(_ string, value []byte) error {
		var plan SessionPlanSnapshot
		if err := json.Unmarshal(value, &plan); err != nil {
			return err
		}
		if strings.TrimSpace(plan.ID) == "" || strings.TrimSpace(plan.SessionID) == "" {
			return nil
		}
		out = append(out, plan)
		return nil
	})
	if err != nil {
		return nil, err
	}
	sort.Slice(out, func(i, j int) bool {
		if out[i].Version == out[j].Version {
			return out[i].UpdatedAt > out[j].UpdatedAt
		}
		return out[i].Version > out[j].Version
	})
	if len(out) > limit {
		out = out[:limit]
	}
	return out, nil
}

func listV3SyncSnapshotRecentTombstonesFromReader(reader pebble.Reader, options V3SyncSnapshotOptions, limit int) ([]V3SessionTombstone, V3SyncSnapshotPagination, error) {
	if limit <= 0 {
		return nil, V3SyncSnapshotPagination{}, nil
	}
	prefixes := v3SyncSnapshotTombstoneRecentPrefixes(options)
	if len(prefixes) == 0 {
		return nil, V3SyncSnapshotPagination{}, nil
	}
	candidates := make([]V3SessionTombstone, 0, limit+len(prefixes))
	seen := map[string]struct{}{}
	for _, prefix := range prefixes {
		startKey := sessionRecentIndexStartAfter(prefix, options.RecentBeforeUpdatedAt, options.RecentBeforeSessionID)
		err := scanRangeFromReader(reader, scanRangeOptions{Prefix: prefix, StartKey: startKey, Limit: limit + 1}, func(_ string, value []byte) (bool, error) {
			var tombstone V3SessionTombstone
			if err := json.Unmarshal(value, &tombstone); err != nil {
				return false, err
			}
			tombstone = normalizeV3SessionTombstone(tombstone)
			if tombstone.SessionID == "" {
				return true, nil
			}
			if _, ok := seen[tombstone.SessionID]; ok {
				return true, nil
			}
			seen[tombstone.SessionID] = struct{}{}
			if !v3SyncSnapshotTombstoneVisibleForSelector(tombstone, options) {
				return true, nil
			}
			candidates = append(candidates, tombstone)
			return true, nil
		})
		if err != nil {
			return nil, V3SyncSnapshotPagination{}, err
		}
	}
	sort.Slice(candidates, func(i, j int) bool {
		if candidates[i].UpdatedAt == candidates[j].UpdatedAt {
			return candidates[i].SessionID > candidates[j].SessionID
		}
		return candidates[i].UpdatedAt > candidates[j].UpdatedAt
	})
	pagination := V3SyncSnapshotPagination{}
	if len(candidates) > limit {
		last := candidates[limit-1]
		updatedAt := last.UpdatedAt
		pagination.HasMore = true
		pagination.NextBeforeUpdatedAt = &updatedAt
		pagination.NextBeforeSessionID = last.SessionID
		candidates = candidates[:limit]
	}
	return candidates, pagination, nil
}

func v3SyncSnapshotTombstoneRecentPrefixes(options V3SyncSnapshotOptions) []string {
	accountScopeID := strings.TrimSpace(options.AccountScopeID)
	userID := strings.TrimSpace(options.UserID)
	if accountScopeID == "" || userID == "" {
		return nil
	}
	paths := options.WorkspacePaths
	if len(paths) == 0 {
		paths = normalizeV3SyncSnapshotWorkspacePaths(options.WorkspacePath, nil)
	}
	if len(paths) > 0 {
		prefixes := make([]string, 0, len(paths))
		for _, path := range paths {
			prefixes = append(prefixes, V3SessionTombstoneByAccountUserWorkspacePrefix(accountScopeID, userID, path))
		}
		return prefixes
	}
	return []string{V3SessionTombstoneByAccountUserPrefix(accountScopeID, userID)}
}

func listV3SyncSnapshotBoundedTombstonesFromReader(reader pebble.Reader, options V3SyncSnapshotOptions, limit int) ([]V3SessionTombstone, bool, error) {
	if limit <= 0 {
		limit = 1000
	}
	prefixes := v3SyncSnapshotTombstoneRecentPrefixes(options)
	if len(prefixes) == 0 {
		return nil, false, nil
	}
	out := make([]V3SessionTombstone, 0, limit)
	seen := map[string]struct{}{}
	capped := false
	for _, prefix := range prefixes {
		err := scanRangeFromReader(reader, scanRangeOptions{Prefix: prefix, Limit: limit + 1}, func(_ string, value []byte) (bool, error) {
			var tombstone V3SessionTombstone
			if err := json.Unmarshal(value, &tombstone); err != nil {
				return false, err
			}
			tombstone = normalizeV3SessionTombstone(tombstone)
			if tombstone.SessionID == "" {
				return true, nil
			}
			if _, ok := seen[tombstone.SessionID]; ok {
				return true, nil
			}
			seen[tombstone.SessionID] = struct{}{}
			if !v3SyncSnapshotTombstoneVisibleForSelector(tombstone, options) {
				return true, nil
			}
			if len(out) >= limit {
				capped = true
				return false, nil
			}
			out = append(out, tombstone)
			return true, nil
		})
		if err != nil {
			return nil, false, err
		}
		if capped {
			break
		}
	}
	sort.Slice(out, func(i, j int) bool {
		if out[i].UpdatedAt == out[j].UpdatedAt {
			return out[i].SessionID > out[j].SessionID
		}
		return out[i].UpdatedAt > out[j].UpdatedAt
	})
	return out, capped, nil
}
