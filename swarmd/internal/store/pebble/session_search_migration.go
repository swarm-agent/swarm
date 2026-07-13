package pebblestore

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net/url"
	"strings"
	"time"

	"github.com/cockroachdb/pebble"
)

const (
	keyV3SessionSearchMigrationState = "v3/session_search_migration/state"
	v3SessionSearchMigrationVersion  = 1
	v3SessionSearchMigrationActive   = "active"
	v3SessionSearchMigrationArchived = "archived"
	v3SessionSearchMigrationComplete = "complete"
)

// V3SessionSearchMigrationState is the durable, monotonic cursor for the
// historical search rebuild. A committed ResumeKey means that session was
// either migrated and verified or deliberately deferred with all of its old
// data retained. Completed keys are therefore never rescanned after restart.
type V3SessionSearchMigrationState struct {
	Version          int    `json:"version"`
	Phase            string `json:"phase"`
	ResumeKey        string `json:"resume_key,omitempty"`
	MigratedSessions uint64 `json:"migrated_sessions"`
	DeferredSessions uint64 `json:"deferred_sessions"`
	UpdatedAtUnixMs  int64  `json:"updated_at_unix_ms"`
}

func (state V3SessionSearchMigrationState) Validate() error {
	if state.Version != v3SessionSearchMigrationVersion {
		return fmt.Errorf("unsupported v3 session search migration version %d", state.Version)
	}
	switch state.Phase {
	case v3SessionSearchMigrationActive, v3SessionSearchMigrationArchived, v3SessionSearchMigrationComplete:
	default:
		return fmt.Errorf("unsupported v3 session search migration phase %q", state.Phase)
	}
	if state.UpdatedAtUnixMs <= 0 {
		return errors.New("v3 session search migration updated_at_unix_ms must be positive")
	}
	if state.Phase == v3SessionSearchMigrationComplete && state.ResumeKey != "" {
		return errors.New("completed v3 session search migration must not retain a resume key")
	}
	return nil
}

type V3SessionSearchMigrationResult struct {
	SessionsScanned  int  `json:"sessions_scanned"`
	SessionsMigrated int  `json:"sessions_migrated"`
	SessionsDeferred int  `json:"sessions_deferred"`
	MoreWork         bool `json:"more_work"`
	Complete         bool `json:"complete"`
}

func (s *SessionStore) GetV3SessionSearchMigrationState() (V3SessionSearchMigrationState, bool, error) {
	if s == nil || s.store == nil {
		return V3SessionSearchMigrationState{}, false, errors.New("session store is not configured")
	}
	var state V3SessionSearchMigrationState
	ok, err := s.store.GetJSON(keyV3SessionSearchMigrationState, &state)
	if err != nil || !ok {
		return V3SessionSearchMigrationState{}, ok, err
	}
	if err := state.Validate(); err != nil {
		return V3SessionSearchMigrationState{}, false, err
	}
	return state, true, nil
}

type v3SessionSearchParityError struct{ reason string }

func (e *v3SessionSearchParityError) Error() string { return e.reason }

func searchParityError(reason string) error { return &v3SessionSearchParityError{reason: reason} }

type v3SessionSearchMigrationCandidate struct {
	key   string
	value []byte
	phase string
}

// RunV3SessionSearchMigrationPass rebuilds at most maxSessions historical
// sessions. Every session and its durable cursor advance commit independently,
// so interruption can repeat at most the uncommitted session. It is intended
// for background maintenance, never request handling.
func (s *SessionStore) RunV3SessionSearchMigrationPass(ctx context.Context, now time.Time, maxSessions int) (V3SessionSearchMigrationResult, error) {
	if s == nil || s.store == nil || s.store.db == nil {
		return V3SessionSearchMigrationResult{}, errors.New("session store is not configured")
	}
	if maxSessions <= 0 {
		return V3SessionSearchMigrationResult{}, errors.New("v3 session search migration max sessions must be positive")
	}
	if err := contextError(ctx); err != nil {
		return V3SessionSearchMigrationResult{}, err
	}
	if now.IsZero() {
		now = time.Now()
	}

	s.store.sessionMutations.maintenanceMu.Lock()
	defer s.store.sessionMutations.maintenanceMu.Unlock()

	state, ok, err := s.GetV3SessionSearchMigrationState()
	if err != nil {
		return V3SessionSearchMigrationResult{}, err
	}
	if !ok {
		state = V3SessionSearchMigrationState{Version: v3SessionSearchMigrationVersion, Phase: v3SessionSearchMigrationActive, UpdatedAtUnixMs: now.UnixMilli()}
	}
	result := V3SessionSearchMigrationResult{}
	for result.SessionsScanned < maxSessions && state.Phase != v3SessionSearchMigrationComplete {
		if err := contextError(ctx); err != nil {
			return V3SessionSearchMigrationResult{}, err
		}
		candidate, found, err := s.nextV3SessionSearchMigrationCandidate(state)
		if err != nil {
			return V3SessionSearchMigrationResult{}, err
		}
		if !found {
			if state.Phase == v3SessionSearchMigrationActive {
				state.Phase = v3SessionSearchMigrationArchived
				state.ResumeKey = ""
			} else {
				state.Phase = v3SessionSearchMigrationComplete
				state.ResumeKey = ""
			}
			state.UpdatedAtUnixMs = now.UnixMilli()
			if err := s.commitV3SessionSearchMigrationState(state); err != nil {
				return V3SessionSearchMigrationResult{}, err
			}
			continue
		}

		migrated, err := s.migrateV3SessionSearchCandidate(ctx, now, candidate, &state)
		if err != nil {
			return V3SessionSearchMigrationResult{}, err
		}
		result.SessionsScanned++
		if migrated {
			result.SessionsMigrated++
		} else {
			result.SessionsDeferred++
		}
	}
	result.Complete = state.Phase == v3SessionSearchMigrationComplete
	result.MoreWork = !result.Complete
	return result, nil
}

func (s *SessionStore) nextV3SessionSearchMigrationCandidate(state V3SessionSearchMigrationState) (v3SessionSearchMigrationCandidate, bool, error) {
	prefix := SessionPrefix()
	if state.Phase == v3SessionSearchMigrationArchived {
		prefix = V3SessionTombstonePrefix()
	}
	iter, err := s.store.db.NewIter(&pebble.IterOptions{LowerBound: []byte(prefix), UpperBound: []byte(prefix + "\xff")})
	if err != nil {
		return v3SessionSearchMigrationCandidate{}, false, fmt.Errorf("create v3 search migration iterator: %w", err)
	}
	defer iter.Close()
	valid := iter.First()
	if state.ResumeKey != "" {
		valid = iter.SeekGE([]byte(state.ResumeKey + "\x00"))
	}
	if !valid {
		if err := iter.Error(); err != nil {
			return v3SessionSearchMigrationCandidate{}, false, err
		}
		return v3SessionSearchMigrationCandidate{}, false, nil
	}
	return v3SessionSearchMigrationCandidate{
		key:   string(append([]byte(nil), iter.Key()...)),
		value: append([]byte(nil), iter.Value()...),
		phase: state.Phase,
	}, true, nil
}

func (s *SessionStore) migrateV3SessionSearchCandidate(ctx context.Context, now time.Time, candidate v3SessionSearchMigrationCandidate, state *V3SessionSearchMigrationState) (bool, error) {
	sessionID := ""
	if candidate.phase == v3SessionSearchMigrationActive {
		var session SessionSnapshot
		if json.Unmarshal(candidate.value, &session) == nil {
			sessionID = strings.TrimSpace(session.ID)
		}
	} else {
		var tombstone V3SessionTombstone
		if json.Unmarshal(candidate.value, &tombstone) == nil {
			sessionID = strings.TrimSpace(tombstone.SessionID)
		}
	}
	unlock := func() {}
	if sessionID != "" {
		unlock = s.store.sessionMutations.lockSessions(sessionID)
	}
	defer unlock()

	snapshot := s.store.db.NewSnapshot()
	defer snapshot.Close()
	batch := s.store.NewBatch()
	defer batch.Close()

	verified := false
	if sessionID != "" {
		stageErr := s.stageVerifiedV3SessionSearchRebuild(ctx, batch, snapshot, candidate, sessionID)
		if stageErr == nil {
			verified = true
		} else {
			var parityErr *v3SessionSearchParityError
			if !errors.As(stageErr, &parityErr) {
				return false, stageErr
			}
		}
	}
	state.ResumeKey = candidate.key
	state.UpdatedAtUnixMs = now.UnixMilli()
	if verified {
		state.MigratedSessions++
	} else {
		// Fail closed: parity failures advance the bounded audit cursor but stage no
		// search or legacy-data deletes. The retained row can be investigated and
		// retried by starting a later migration version.
		state.DeferredSessions++
	}
	payload, err := json.Marshal(state)
	if err != nil {
		return false, err
	}
	if err := batch.Set([]byte(keyV3SessionSearchMigrationState), payload, nil); err != nil {
		return false, err
	}
	if hook := s.store.sessionMutations.beforeSearchMigrationCommit; hook != nil {
		if err := hook(sessionID); err != nil {
			return false, fmt.Errorf("before v3 session search migration commit: %w", err)
		}
	}
	if err := batch.Commit(pebble.Sync); err != nil {
		return false, fmt.Errorf("commit v3 session search migration for %q: %w", sessionID, err)
	}
	return verified, nil
}

func (s *SessionStore) stageVerifiedV3SessionSearchRebuild(ctx context.Context, batch *pebble.Batch, reader pebble.Reader, candidate v3SessionSearchMigrationCandidate, sessionID string) error {
	if err := contextError(ctx); err != nil {
		return err
	}
	var session SessionSnapshot
	if candidate.phase == v3SessionSearchMigrationActive {
		loaded, ok, err := s.getSessionFromReader(reader, sessionID)
		if err != nil || !ok {
			if err != nil {
				return err
			}
			return searchParityError("canonical session snapshot is missing")
		}
		if candidate.key != KeySession(sessionID) {
			return searchParityError("session migration key does not match canonical session id")
		}
		session = loaded
	} else {
		tombstone, ok, err := getV3SessionTombstoneFromReader(reader, sessionID)
		if err != nil || !ok {
			if err != nil {
				return err
			}
			return searchParityError("canonical session tombstone is missing")
		}
		if candidate.key != KeyV3SessionTombstone(sessionID) || !tombstone.Archived || tombstone.Deleted {
			return searchParityError("search migration tombstone is not a canonical archived session")
		}
		session = tombstone.Session
	}
	session = normalizeSessionOwnership(session)
	if session.ID != sessionID || session.AccountScopeID == "" {
		return searchParityError("canonical session ownership parity is incomplete")
	}

	var projection V3SessionProjection
	projectionOK, err := getJSONFromReader(reader, KeyV3SessionProjection(sessionID), &projection)
	if err != nil || !projectionOK {
		if err != nil {
			return err
		}
		return searchParityError("canonical v3 projection is missing")
	}
	sequenceBytes, sequenceOK, err := getBytesFromReader(reader, KeyV3SessionSequence(sessionID))
	if err != nil || !sequenceOK {
		if err != nil {
			return err
		}
		return searchParityError("canonical v3 sequence is missing")
	}
	sequence, err := bytesToUint64(sequenceBytes)
	if err != nil || projection.SessionID != sessionID || projection.LastEventSeq != sequence || projection.ProjectionHighWatermarkSeq != sequence {
		return searchParityError("canonical v3 projection and sequence parity failed")
	}

	messages, err := loadAllV3SessionMessagesForSearchMigration(ctx, reader, sessionID, sequence)
	if err != nil {
		return err
	}
	if session.MessageCount != len(messages) {
		return searchParityError(fmt.Sprintf("canonical v3 message count parity failed: session=%d rows=%d", session.MessageCount, len(messages)))
	}

	metadataRecords := v3SessionSearchMetadataTokens(session)
	messageRecords := make(map[string]v3SessionSearchIndexRecord)
	for _, message := range messages {
		for _, token := range v3SessionSearchTokens(message.Content) {
			messageRecords[token] = v3SessionSearchIndexRecord{SessionID: sessionID, Snippet: &V3SessionSearchSnippet{Source: "message", Role: message.Role, MessageID: message.ID, GlobalSeq: message.GlobalSeq, Text: matchCenteredV3SessionSearchSnippet(message.Content, token), CreatedAt: message.CreatedAt}}
		}
	}
	for _, records := range []map[string]v3SessionSearchIndexRecord{metadataRecords, messageRecords} {
		for token, record := range records {
			if strings.TrimSpace(token) == "" || record.SessionID != sessionID {
				return searchParityError("generated v3 search posting parity failed")
			}
		}
	}

	var previous v3SessionSearchSessionMeta
	previousOK, err := getJSONFromReader(reader, keyV3SessionSearchMeta(sessionID), &previous)
	if err != nil {
		return err
	}
	if !previousOK {
		return searchParityError("search reverse metadata is missing; legacy posting ownership cannot be proven")
	}
	if previous.SessionID != sessionID {
		return searchParityError("search reverse metadata session parity failed")
	}
	if previous.Version != v3SessionSearchIndexVersion && len(previous.Keys) == 0 {
		return searchParityError("legacy search reverse metadata has no owned key list")
	}
	seenLegacy := make(map[string]struct{}, len(previous.Keys))
	for _, key := range previous.Keys {
		if _, duplicate := seenLegacy[key]; duplicate {
			return searchParityError("legacy search reverse metadata contains duplicate keys")
		}
		seenLegacy[key] = struct{}{}
		token, ok := legacyV3SessionSearchTokenForOwnedKey(key, session.AccountScopeID)
		if !ok {
			return searchParityError("legacy search reverse metadata contains an unowned key")
		}
		if _, metadataOK := metadataRecords[token]; !metadataOK {
			if _, messageOK := messageRecords[token]; !messageOK {
				return searchParityError("legacy search token is absent from canonical v3 session/message rows")
			}
		}
		var record v3SessionSearchIndexRecord
		found, err := getJSONFromReader(reader, key, &record)
		if err != nil || !found || record.SessionID != sessionID {
			if err != nil {
				return err
			}
			return searchParityError("legacy search posting parity failed")
		}
	}

	if err := deletePrefixInBatch(batch, v3SessionSearchPostingPrefix(sessionID)); err != nil {
		return err
	}
	metadataTokens := make([]string, 0, len(metadataRecords))
	for source, records := range map[string]map[string]v3SessionSearchIndexRecord{"metadata": metadataRecords, "message": messageRecords} {
		for token, record := range records {
			if source == "metadata" {
				metadataTokens = append(metadataTokens, token)
			}
			payload, err := json.Marshal(record)
			if err != nil {
				return err
			}
			if err := batch.Set([]byte(keyV3SessionSearchPosting(sessionID, source, token)), payload, nil); err != nil {
				return err
			}
		}
	}
	sortStrings(metadataTokens)
	for key := range seenLegacy {
		if err := batch.Delete([]byte(key), nil); err != nil {
			return err
		}
	}
	metaPayload, err := json.Marshal(v3SessionSearchSessionMeta{SessionID: sessionID, Version: v3SessionSearchIndexVersion, MetadataTokens: metadataTokens})
	if err != nil {
		return err
	}
	return batch.Set([]byte(keyV3SessionSearchMeta(sessionID)), metaPayload, nil)
}

func loadAllV3SessionMessagesForSearchMigration(ctx context.Context, reader pebble.Reader, sessionID string, highWatermark uint64) ([]MessageSnapshot, error) {
	prefix := V3SessionMessagePrefix(sessionID)
	iter, err := reader.NewIter(&pebble.IterOptions{LowerBound: []byte(prefix), UpperBound: []byte(prefix + "\xff")})
	if err != nil {
		return nil, err
	}
	defer iter.Close()
	messages := make([]MessageSnapshot, 0)
	for valid := iter.First(); valid; valid = iter.Next() {
		if err := contextError(ctx); err != nil {
			return nil, err
		}
		var message MessageSnapshot
		if err := json.Unmarshal(iter.Value(), &message); err != nil {
			return nil, err
		}
		if message.SessionID != sessionID || message.GlobalSeq == 0 || message.GlobalSeq > highWatermark || string(iter.Key()) != KeyV3SessionMessage(sessionID, message.GlobalSeq) {
			return nil, searchParityError("canonical v3 message key/value parity failed")
		}
		var event V3SessionEvent
		ok, err := getJSONFromReader(reader, KeyV3SessionEvent(sessionID, message.GlobalSeq), &event)
		if err != nil || !ok || event.SessionID != sessionID || event.Seq != message.GlobalSeq {
			if err != nil {
				return nil, err
			}
			return nil, searchParityError("canonical v3 message has no matching durable event")
		}
		messages = append(messages, message)
	}
	if err := iter.Error(); err != nil {
		return nil, err
	}
	return messages, nil
}

func legacyV3SessionSearchTokenForOwnedKey(key, accountScopeID string) (string, bool) {
	rest, ok := strings.CutPrefix(key, keyV3SessionSearchAccountPrefix)
	if !ok {
		return "", false
	}
	parts := strings.SplitN(rest, "/", 4)
	if len(parts) != 4 || parts[0] != keyPart(accountScopeID) || (parts[1] != "active" && parts[1] != "archived") || parts[2] == "" || parts[3] == "" {
		return "", false
	}
	decoded, err := url.PathUnescape(parts[2])
	if err != nil || strings.TrimSpace(decoded) == "" {
		return "", false
	}
	return decoded, true
}

func (s *SessionStore) commitV3SessionSearchMigrationState(state V3SessionSearchMigrationState) error {
	if err := state.Validate(); err != nil {
		return err
	}
	payload, err := json.Marshal(state)
	if err != nil {
		return err
	}
	return s.store.db.Set([]byte(keyV3SessionSearchMigrationState), payload, pebble.Sync)
}

func sortStrings(values []string) {
	if len(values) < 2 {
		return
	}
	for i := 1; i < len(values); i++ {
		for j := i; j > 0 && values[j] < values[j-1]; j-- {
			values[j], values[j-1] = values[j-1], values[j]
		}
	}
}

// LegacySessionNamespaceCleanupDecision records the reader/authority audit used
// to define cleanup eligibility. An ineligible prefix is never returned by
// ProvenRedundantLegacySessionPrefixes.
type LegacySessionNamespaceCleanupDecision struct {
	Prefix   string `json:"prefix"`
	Eligible bool   `json:"eligible"`
	Reason   string `json:"reason"`
}

func LegacySessionNamespaceCleanupAudit() []LegacySessionNamespaceCleanupDecision {
	return []LegacySessionNamespaceCleanupDecision{
		{Prefix: "evt/", Eligible: false, Reason: "active generic event-log storage; records are not proven duplicates of V3 session events"},
		{Prefix: "msg/", Eligible: false, Reason: "still read and written by compatibility SessionStore message APIs"},
		{Prefix: "msg_by_account/", Eligible: false, Reason: "still maintained with compatibility message rows; ownership parity with V3 rows is not established"},
	}
}

func ProvenRedundantLegacySessionPrefixes() []string {
	var prefixes []string
	for _, decision := range LegacySessionNamespaceCleanupAudit() {
		if decision.Eligible {
			prefixes = append(prefixes, decision.Prefix)
		}
	}
	return prefixes
}
