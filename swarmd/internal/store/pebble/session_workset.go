package pebblestore

import (
	"encoding/json"
	"errors"
	"fmt"
	"sort"
	"strings"
	"time"

	"github.com/cockroachdb/pebble"
)

const (
	V3SessionWorksetHistoryModeNone = "none"
	V3SessionWorksetHistoryModeTail = "tail"
	V3SessionWorksetHistoryModeFull = "full"

	V3SessionWorksetManifestPolicyError    = "error"
	V3SessionWorksetManifestPolicyOmit     = "omit"
	V3SessionWorksetManifestPolicyManifest = "manifest"

	V3SessionWorksetOmissionRequiresManifest = "requires_manifest"
	V3SessionWorksetOmissionPageBoundary     = "page_boundary"
)

type V3SessionWorksetOptions struct {
	AccountScopeID                     string
	SessionIDs                         []string
	WorkspacePath                      string
	WorkspacePaths                     []string
	RestrictSessionIDsToWorkspacePaths bool
	RecentLimit                        int
	RecentBeforeUpdatedAt              *int64
	RecentBeforeSessionID              string
	History                            V3SessionWorksetHistoryOptions
	IncludeRunIntents                  bool
	IncludeActiveSessions              bool
}

type V3SessionWorksetHistoryOptions struct {
	Mode                  string
	MaxMessagesPerSession int
	MaxEventsPerSession   int
	ManifestPolicy        string
	IncludeMessages       bool
	IncludeEvents         bool
}

type V3SessionWorksetResult struct {
	Rev                       uint64                                       `json:"rev"`
	SessionsByID              map[string]SessionSnapshot                   `json:"sessions_by_id"`
	ProjectionsBySession      map[string]V3SessionProjection               `json:"projections_by_session"`
	MessagesBySession         map[string][]MessageSnapshot                 `json:"messages_by_session"`
	EventsBySession           map[string][]V3SessionEvent                  `json:"events_by_session"`
	RunIntentsBySession       map[string][]V3SessionRunIntent              `json:"run_intents_by_session"`
	HistoryManifestsBySession map[string][]V3SessionHistoryChunkDescriptor `json:"history_manifests_by_session"`
	HistoryChunksByID         map[string]V3SessionHistoryChunk             `json:"history_chunks_by_id"`
	Omissions                 []V3SessionWorksetOmission                   `json:"omissions"`
	Pagination                V3SessionWorksetPagination                   `json:"pagination"`
	Watermarks                V3SessionWorksetWatermarks                   `json:"watermarks"`
	SessionOrder              []string                                     `json:"session_order"`
}

type V3SessionWorksetPagination struct {
	NextBeforeUpdatedAt *int64 `json:"next_before_updated_at,omitempty"`
	NextBeforeSessionID string `json:"next_before_session_id,omitempty"`
	HasMore             bool   `json:"has_more"`
}

type V3SessionWorksetWatermarks struct {
	LoadedAt     int64 `json:"loaded_at"`
	MaxUpdatedAt int64 `json:"max_updated_at,omitempty"`
}

type V3SessionHistoryChunkDescriptor struct {
	ChunkID      string `json:"chunk_id"`
	Resource     string `json:"resource"`
	FromSeq      uint64 `json:"from_seq"`
	ToSeq        uint64 `json:"to_seq"`
	MessageCount int    `json:"message_count"`
	EventCount   int    `json:"event_count"`
	Complete     bool   `json:"complete"`
}

type V3SessionHistoryChunk struct {
	ChunkID  string            `json:"chunk_id"`
	Resource string            `json:"resource"`
	Messages []MessageSnapshot `json:"messages,omitempty"`
	Events   []V3SessionEvent  `json:"events,omitempty"`
}

type V3SessionWorksetOmission struct {
	SessionID   string `json:"session_id,omitempty"`
	Resource    string `json:"resource"`
	Reason      string `json:"reason"`
	NextCursor  string `json:"next_cursor,omitempty"`
	ManifestRef string `json:"manifest_ref,omitempty"`
}

func (s *SessionStore) BuildV3SessionWorkset(options V3SessionWorksetOptions) (result V3SessionWorksetResult, err error) {
	if s == nil || s.store == nil {
		return V3SessionWorksetResult{}, errors.New("session store is not configured")
	}
	options = normalizeV3SessionWorksetOptions(options)
	if options.RecentLimit > 0 {
		if err := s.ensureSessionRecentIndex(); err != nil {
			return V3SessionWorksetResult{}, err
		}
	}
	if len(options.SessionIDs) == 0 && options.RecentLimit <= 0 && strings.TrimSpace(options.WorkspacePath) == "" && len(options.WorkspacePaths) == 0 {
		return V3SessionWorksetResult{}, errors.New("at least one workset selector is required")
	}
	snapshot := s.store.db.NewSnapshot()
	defer func() {
		if closeErr := snapshot.Close(); err == nil && closeErr != nil {
			err = closeErr
		}
	}()
	return s.buildV3SessionWorksetFromReader(snapshot, options)
}

func normalizeV3SessionWorksetOptions(options V3SessionWorksetOptions) V3SessionWorksetOptions {
	options.AccountScopeID = strings.TrimSpace(options.AccountScopeID)
	options.WorkspacePath = strings.TrimSpace(options.WorkspacePath)
	options.WorkspacePaths = normalizeV3SessionWorksetWorkspacePaths(options.WorkspacePath, options.WorkspacePaths)
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
		options.History.Mode = V3SessionWorksetHistoryModeNone
	}
	if options.History.Mode == V3SessionWorksetHistoryModeTail || options.History.Mode == V3SessionWorksetHistoryModeFull {
		options.History.IncludeMessages = true
	}
	options.History.ManifestPolicy = strings.TrimSpace(strings.ToLower(options.History.ManifestPolicy))
	if options.History.ManifestPolicy == "" {
		options.History.ManifestPolicy = V3SessionWorksetManifestPolicyError
	}
	return options
}

func (s *SessionStore) buildV3SessionWorksetFromReader(reader pebble.Reader, options V3SessionWorksetOptions) (V3SessionWorksetResult, error) {
	selected, pagination, err := s.selectV3SessionWorksetSessions(reader, options)
	if err != nil {
		return V3SessionWorksetResult{}, err
	}
	rev, err := readV3RealtimeOutboxSequenceFromReader(reader)
	if err != nil {
		return V3SessionWorksetResult{}, err
	}
	result := V3SessionWorksetResult{
		Rev:                       rev,
		SessionsByID:              map[string]SessionSnapshot{},
		ProjectionsBySession:      map[string]V3SessionProjection{},
		MessagesBySession:         map[string][]MessageSnapshot{},
		EventsBySession:           map[string][]V3SessionEvent{},
		RunIntentsBySession:       map[string][]V3SessionRunIntent{},
		HistoryManifestsBySession: map[string][]V3SessionHistoryChunkDescriptor{},
		HistoryChunksByID:         map[string]V3SessionHistoryChunk{},
		Omissions:                 []V3SessionWorksetOmission{},
		Pagination:                pagination,
		Watermarks:                V3SessionWorksetWatermarks{LoadedAt: time.Now().UnixMilli()},
		SessionOrder:              make([]string, 0, len(selected)),
	}
	for _, session := range selected {
		if strings.TrimSpace(session.ID) == "" {
			continue
		}
		projection, ok, err := getV3SessionProjectionFromReader(reader, session.ID)
		if err != nil {
			return V3SessionWorksetResult{}, err
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
		result.SessionOrder = append(result.SessionOrder, session.ID)
		if err := s.addV3SessionWorksetHistory(reader, options, session, projection, &result); err != nil {
			return V3SessionWorksetResult{}, err
		}
		if options.IncludeRunIntents {
			if err := s.addV3SessionWorksetRunIntents(reader, options, session, projection, &result); err != nil {
				return V3SessionWorksetResult{}, err
			}
		}
	}
	return result, nil
}

func readV3RealtimeOutboxSequenceFromReader(reader pebble.Reader) (uint64, error) {
	raw, ok, err := getBytesFromReader(reader, KeyV3RealtimeOutboxSequence())
	if err != nil || !ok {
		return 0, err
	}
	seq, err := bytesToUint64(raw)
	if err != nil {
		return 0, fmt.Errorf("decode v3 realtime outbox sequence: %w", err)
	}
	return seq, nil
}

func (s *SessionStore) selectV3SessionWorksetSessions(reader pebble.Reader, options V3SessionWorksetOptions) ([]SessionSnapshot, V3SessionWorksetPagination, error) {
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
			return nil, V3SessionWorksetPagination{}, err
		}
		if !ok || !v3SessionWorksetSessionVisible(session, options.AccountScopeID, "") {
			continue
		}
		if options.RestrictSessionIDsToWorkspacePaths && !v3SessionWorksetSessionVisibleForWorkspaces(session, options.AccountScopeID, options.WorkspacePath, options.WorkspacePaths) {
			continue
		}
		appendSession(session)
	}
	pagination := V3SessionWorksetPagination{}
	if options.RecentLimit > 0 {
		recent, page, err := s.selectV3RecentWorksetSessions(reader, options)
		if err != nil {
			return nil, V3SessionWorksetPagination{}, err
		}
		pagination = page
		for _, session := range recent {
			appendSession(session)
		}
	}
	if options.IncludeActiveSessions {
		active, err := s.selectV3ActiveWorksetSessions(reader, options)
		if err != nil {
			return nil, V3SessionWorksetPagination{}, err
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

func (s *SessionStore) selectV3RecentWorksetSessions(reader pebble.Reader, options V3SessionWorksetOptions) ([]SessionSnapshot, V3SessionWorksetPagination, error) {
	return s.selectV3RecentWorksetSessionsFromIndex(reader, options)
}

func (s *SessionStore) selectV3ActiveWorksetSessions(reader pebble.Reader, options V3SessionWorksetOptions) ([]SessionSnapshot, error) {
	states, err := listV3ActiveSessionRunStatesFromReader(reader, options.AccountScopeID, sessionRecentIndexScanLimit())
	if err != nil {
		return nil, err
	}
	out := make([]SessionSnapshot, 0, len(states))
	for _, state := range states {
		session, ok, err := s.getSessionFromReader(reader, state.SessionID)
		if err != nil {
			return nil, err
		}
		if !ok || !v3SessionWorksetSessionVisibleForWorkspaces(session, options.AccountScopeID, options.WorkspacePath, options.WorkspacePaths) {
			continue
		}
		out = append(out, session)
	}
	return out, nil
}

func normalizeV3SessionWorksetWorkspacePaths(workspacePath string, workspacePaths []string) []string {
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

func v3SessionWorksetSessionVisibleForWorkspaces(session SessionSnapshot, accountScopeID, workspacePath string, workspacePaths []string) bool {
	if !v3SessionWorksetSessionVisible(session, accountScopeID, "") {
		return false
	}
	paths := workspacePaths
	if len(paths) == 0 {
		paths = normalizeV3SessionWorksetWorkspacePaths(workspacePath, nil)
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

func v3SessionWorksetSessionVisible(session SessionSnapshot, accountScopeID, workspacePath string) bool {
	if strings.TrimSpace(accountScopeID) != "" && strings.TrimSpace(session.AccountScopeID) != strings.TrimSpace(accountScopeID) {
		return false
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

func v3SessionWorksetBeforeCursor(session SessionSnapshot, beforeUpdatedAt *int64, beforeSessionID string) bool {
	if beforeUpdatedAt == nil {
		return true
	}
	if session.UpdatedAt < *beforeUpdatedAt {
		return true
	}
	if session.UpdatedAt > *beforeUpdatedAt {
		return false
	}
	beforeSessionID = strings.TrimSpace(beforeSessionID)
	if beforeSessionID == "" {
		return false
	}
	return strings.TrimSpace(session.ID) < beforeSessionID
}

func (s *SessionStore) addV3SessionWorksetHistory(reader pebble.Reader, options V3SessionWorksetOptions, session SessionSnapshot, projection V3SessionProjection, result *V3SessionWorksetResult) error {
	switch options.History.Mode {
	case V3SessionWorksetHistoryModeNone, V3SessionWorksetHistoryModeTail, V3SessionWorksetHistoryModeFull:
	default:
		return fmt.Errorf("unsupported workset history mode %q", options.History.Mode)
	}
	if options.History.IncludeMessages {
		if err := s.addV3SessionWorksetMessages(reader, options, session, result); err != nil {
			return err
		}
	}
	if options.History.IncludeEvents {
		if err := s.addV3SessionWorksetEvents(reader, options, session, projection, result); err != nil {
			return err
		}
	}
	return nil
}

func (s *SessionStore) addV3SessionWorksetMessages(reader pebble.Reader, options V3SessionWorksetOptions, session SessionSnapshot, result *V3SessionWorksetResult) error {
	limit, capped := v3WorksetResourceLimit(options.History.Mode, options.History.MaxMessagesPerSession, session.MessageCount)
	if limit == 0 && session.MessageCount > 0 {
		return s.handleV3WorksetResourceOmission(options, session.ID, "messages", V3SessionWorksetOmissionRequiresManifest, fmt.Sprintf("%s:messages:1", session.ID), nil, result)
	}
	messages := []MessageSnapshot{}
	var err error
	if limit > 0 {
		if options.History.Mode == V3SessionWorksetHistoryModeTail {
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
		if err := s.handleV3WorksetResourceOmission(options, session.ID, "messages", V3SessionWorksetOmissionRequiresManifest, v3WorksetMessagesNextCursor(session.ID, messages), &messages, result); err != nil {
			return err
		}
		return nil
	}
	result.MessagesBySession[session.ID] = messages
	return nil
}

func (s *SessionStore) addV3SessionWorksetEvents(reader pebble.Reader, options V3SessionWorksetOptions, session SessionSnapshot, projection V3SessionProjection, result *V3SessionWorksetResult) error {
	if projection.LastEventSeq == 0 {
		return nil
	}
	limit := options.History.MaxEventsPerSession
	if limit <= 0 && options.History.Mode == V3SessionWorksetHistoryModeFull {
		limit = int(projection.LastEventSeq)
	}
	if limit <= 0 {
		return s.handleV3WorksetResourceOmission(options, session.ID, "events", V3SessionWorksetOmissionRequiresManifest, fmt.Sprintf("%s:events:1", session.ID), nil, result)
	}
	events, capped, err := listV3SessionWorksetEventsFromReader(reader, session.ID, 0, limit)
	if err != nil {
		return err
	}
	if capped {
		if err := s.handleV3WorksetResourceOmission(options, session.ID, "events", V3SessionWorksetOmissionRequiresManifest, v3WorksetEventsNextCursor(session.ID, events), &events, result); err != nil {
			return err
		}
		return nil
	}
	result.EventsBySession[session.ID] = events
	return nil
}

func listV3SessionWorksetEventsFromReader(reader pebble.Reader, sessionID string, afterSeq uint64, limit int) ([]V3SessionEvent, bool, error) {
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
		if event.Seq <= afterSeq || v3SessionWorksetEventOmitted(event) {
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

func v3SessionWorksetEventOmitted(event V3SessionEvent) bool {
	return strings.HasPrefix(strings.TrimSpace(event.EventType), "session.diagnostic")
}

func (s *SessionStore) addV3SessionWorksetRunIntents(reader pebble.Reader, options V3SessionWorksetOptions, session SessionSnapshot, projection V3SessionProjection, result *V3SessionWorksetResult) error {
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

func v3WorksetResourceLimit(mode string, requested, total int) (limit int, capped bool) {
	if total <= 0 {
		return 0, false
	}
	if requested > 0 {
		return requested, requested < total
	}
	if mode == V3SessionWorksetHistoryModeFull {
		return total, false
	}
	return 0, true
}

func (s *SessionStore) handleV3WorksetResourceOmission(options V3SessionWorksetOptions, sessionID, resource, reason, nextCursor string, inline any, result *V3SessionWorksetResult) error {
	switch options.History.ManifestPolicy {
	case V3SessionWorksetManifestPolicyOmit:
		omission := V3SessionWorksetOmission{SessionID: sessionID, Resource: resource, Reason: reason, NextCursor: nextCursor}
		result.Omissions = append(result.Omissions, omission)
		return nil
	case V3SessionWorksetManifestPolicyManifest:
		descriptor, _ := v3WorksetHistoryDescriptor(sessionID, resource, inline)
		result.HistoryManifestsBySession[sessionID] = append(result.HistoryManifestsBySession[sessionID], descriptor)
		manifestRef := fmt.Sprintf("%s:%s", sessionID, resource)
		omission := V3SessionWorksetOmission{SessionID: sessionID, Resource: resource, Reason: reason, NextCursor: nextCursor, ManifestRef: manifestRef}
		result.Omissions = append(result.Omissions, omission)
		return nil
	case V3SessionWorksetManifestPolicyError:
		fallthrough
	default:
		return fmt.Errorf("workset %s for session %q resource %q", reason, sessionID, resource)
	}
}

func v3WorksetHistoryDescriptor(sessionID, resource string, inline any) (V3SessionHistoryChunkDescriptor, V3SessionHistoryChunk) {
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

func v3WorksetMessagesNextCursor(sessionID string, messages []MessageSnapshot) string {
	if len(messages) == 0 {
		return fmt.Sprintf("%s:messages:1", sessionID)
	}
	return fmt.Sprintf("%s:messages:%d", sessionID, messages[len(messages)-1].GlobalSeq+1)
}

func v3WorksetEventsNextCursor(sessionID string, events []V3SessionEvent) string {
	if len(events) == 0 {
		return fmt.Sprintf("%s:events:1", sessionID)
	}
	return fmt.Sprintf("%s:events:%d", sessionID, events[len(events)-1].Seq+1)
}

func listV3SessionRunIntentsFromReader(reader pebble.Reader, sessionID string, afterSeq uint64, limit int) ([]V3SessionRunIntent, error) {
	sessionID = strings.TrimSpace(sessionID)
	if sessionID == "" {
		return nil, errors.New("session id is required")
	}
	if limit <= 0 {
		return []V3SessionRunIntent{}, nil
	}
	out := make([]V3SessionRunIntent, 0, limit)
	err := scanRangeFromReader(reader, scanRangeOptions{Prefix: V3SessionRunIntentPrefix(sessionID), Limit: int(^uint(0) >> 1)}, func(_ string, value []byte) (bool, error) {
		var intent V3SessionRunIntent
		if err := json.Unmarshal(value, &intent); err != nil {
			return false, err
		}
		if intent.EventSeq <= afterSeq {
			return true, nil
		}
		out = append(out, intent)
		return len(out) < limit, nil
	})
	sort.SliceStable(out, func(i, j int) bool { return out[i].EventSeq < out[j].EventSeq })
	return out, err
}
