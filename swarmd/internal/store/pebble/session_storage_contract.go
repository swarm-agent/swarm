package pebblestore

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"strings"
	"time"

	"github.com/cockroachdb/pebble"
)

const (
	// V3 session retention is deliberately conservative: replay and completed
	// idempotency rows remain available for 30 days, realtime cleanup retains at
	// least the newest 100,000 endpoint records, and maintenance commits are
	// capped at 500 logical records. Callers may supply stricter values, but may
	// not disable any bound by passing zero.
	DefaultV3RealtimeReplayRetention              = 30 * 24 * time.Hour
	DefaultV3CompletedIdempotencyRetention        = 30 * 24 * time.Hour
	DefaultV3RealtimeMinimumRecords        uint64 = 100_000
	DefaultV3MaintenanceBatchRecords              = 500

	keyV3SessionMaintenanceState = "v3/session_maintenance/state"
	v3SessionMaintenanceVersion  = 1
)

// V3SessionRetentionPolicy is the explicit input contract for bounded session
// maintenance. Realtime cleanup must satisfy both the age window and minimum
// record floor. Idempotency cleanup applies only to completed records older
// than CompletedIdempotencyRetention.
type V3SessionRetentionPolicy struct {
	RealtimeReplayRetention       time.Duration
	CompletedIdempotencyRetention time.Duration
	RealtimeMinimumRecords        uint64
	BatchRecords                  int
}

func DefaultV3SessionRetentionPolicy() V3SessionRetentionPolicy {
	return V3SessionRetentionPolicy{
		RealtimeReplayRetention:       DefaultV3RealtimeReplayRetention,
		CompletedIdempotencyRetention: DefaultV3CompletedIdempotencyRetention,
		RealtimeMinimumRecords:        DefaultV3RealtimeMinimumRecords,
		BatchRecords:                  DefaultV3MaintenanceBatchRecords,
	}
}

func (p V3SessionRetentionPolicy) Validate() error {
	if p.RealtimeReplayRetention <= 0 {
		return errors.New("v3 realtime replay retention must be positive")
	}
	if p.CompletedIdempotencyRetention <= 0 {
		return errors.New("v3 completed idempotency retention must be positive")
	}
	if p.RealtimeMinimumRecords == 0 {
		return errors.New("v3 realtime minimum retained records must be positive")
	}
	if p.BatchRecords <= 0 {
		return errors.New("v3 maintenance batch records must be positive")
	}
	return nil
}

// V3SessionMaintenanceState is the durable resume and replay-boundary record.
// OldestRetainedRealtimeEndpointSeq == 0 means no realtime rows have been
// pruned. Otherwise every endpoint below that sequence is unavailable and a
// client cursor earlier than boundary-1 requires bootstrap/rehydration.
type V3SessionMaintenanceState struct {
	Version                           int    `json:"version"`
	OldestRetainedRealtimeEndpointSeq uint64 `json:"oldest_retained_realtime_endpoint_seq,omitempty"`
	RealtimePrunedThroughEndpointSeq  uint64 `json:"realtime_pruned_through_endpoint_seq,omitempty"`
	CompletedIdempotencyCutoffUnixMs  int64  `json:"completed_idempotency_cutoff_unix_ms,omitempty"`
	CompletedIdempotencyResumeKey     string `json:"completed_idempotency_resume_key,omitempty"`
	UpdatedAtUnixMs                   int64  `json:"updated_at_unix_ms"`
}

func (state V3SessionMaintenanceState) Validate() error {
	if state.Version != 0 && state.Version != v3SessionMaintenanceVersion {
		return fmt.Errorf("unsupported v3 session maintenance state version %d", state.Version)
	}
	if state.UpdatedAtUnixMs <= 0 {
		return errors.New("v3 session maintenance updated_at_unix_ms must be positive")
	}
	if state.OldestRetainedRealtimeEndpointSeq == 0 {
		if state.RealtimePrunedThroughEndpointSeq != 0 {
			return errors.New("v3 realtime prune progress requires an oldest retained endpoint")
		}
	} else if state.OldestRetainedRealtimeEndpointSeq != state.RealtimePrunedThroughEndpointSeq+1 {
		return errors.New("v3 oldest retained endpoint must immediately follow realtime prune progress")
	}
	if state.CompletedIdempotencyCutoffUnixMs < 0 {
		return errors.New("v3 completed idempotency cutoff must not be negative")
	}
	if state.CompletedIdempotencyResumeKey != "" && state.CompletedIdempotencyCutoffUnixMs == 0 {
		return errors.New("v3 completed idempotency resume key requires a cutoff")
	}
	return nil
}

func (s *SessionStore) GetV3SessionMaintenanceState() (V3SessionMaintenanceState, bool, error) {
	if s == nil || s.store == nil {
		return V3SessionMaintenanceState{}, false, errors.New("session store is not configured")
	}
	var state V3SessionMaintenanceState
	ok, err := s.store.GetJSON(keyV3SessionMaintenanceState, &state)
	if err != nil || !ok {
		return V3SessionMaintenanceState{}, ok, err
	}
	if err := state.Validate(); err != nil {
		return V3SessionMaintenanceState{}, false, err
	}
	return state, true, nil
}

func (s *SessionStore) PutV3SessionMaintenanceState(state V3SessionMaintenanceState) error {
	if s == nil || s.store == nil {
		return errors.New("session store is not configured")
	}
	if state.Version == 0 {
		state.Version = v3SessionMaintenanceVersion
	}
	if err := state.Validate(); err != nil {
		return err
	}
	return s.store.PutJSON(keyV3SessionMaintenanceState, state)
}

func (s *SessionStore) OldestRetainedV3RealtimeEndpointSeq() (uint64, error) {
	state, ok, err := s.GetV3SessionMaintenanceState()
	if err != nil || !ok {
		return 0, err
	}
	return state.OldestRetainedRealtimeEndpointSeq, nil
}

type SessionStorageCleanupCandidate struct {
	Namespace    string `json:"namespace"`
	Records      uint64 `json:"records"`
	LogicalBytes uint64 `json:"logical_bytes"`
}

// V3SessionMaintenanceResult reports only bounded aggregate work. A non-nil
// error means the atomic pass was not committed and these counts are not
// durable.
type V3SessionMaintenanceResult struct {
	RealtimeRecordsDeleted         int                              `json:"realtime_records_deleted"`
	RealtimeRowsDeleted            int                              `json:"realtime_rows_deleted"`
	RealtimeLogicalBytesDeleted    uint64                           `json:"realtime_logical_bytes_deleted"`
	IdempotencyRecordsScanned      int                              `json:"idempotency_records_scanned"`
	IdempotencyRecordsDeleted      int                              `json:"idempotency_records_deleted"`
	IdempotencyLogicalBytesDeleted uint64                           `json:"idempotency_logical_bytes_deleted"`
	MoreRealtimeWork               bool                             `json:"more_realtime_work"`
	MoreIdempotencyWork            bool                             `json:"more_idempotency_work"`
	OldestRetainedEndpointSeq      uint64                           `json:"oldest_retained_endpoint_seq,omitempty"`
	Namespaces                     []SessionStorageCleanupCandidate `json:"namespaces,omitempty"`
}

// RunV3SessionRetentionPass performs at most one bounded maintenance batch.
// Realtime canonical rows and their three reference indexes are deleted in the
// same Pebble batch as the durable oldest-retained boundary. Completed
// idempotency rows remain replayable/conflict-detectable through the configured
// retry window; after expiry, a repeated client request is treated as a new
// request because its completed record no longer exists. Non-completed records
// are never deleted.
func (s *SessionStore) RunV3SessionRetentionPass(ctx context.Context, now time.Time, policy V3SessionRetentionPolicy) (V3SessionMaintenanceResult, error) {
	return s.runV3SessionRetentionPass(ctx, now, policy, true)
}

// PreviewV3SessionRetentionPass computes the exact next bounded retention batch
// without committing its staged deletes or maintenance progress.
func (s *SessionStore) PreviewV3SessionRetentionPass(ctx context.Context, now time.Time, policy V3SessionRetentionPolicy) (V3SessionMaintenanceResult, error) {
	return s.runV3SessionRetentionPass(ctx, now, policy, false)
}

func (s *SessionStore) runV3SessionRetentionPass(ctx context.Context, now time.Time, policy V3SessionRetentionPolicy, commit bool) (V3SessionMaintenanceResult, error) {
	if s == nil || s.store == nil || s.store.db == nil {
		return V3SessionMaintenanceResult{}, errors.New("session store is not configured")
	}
	if err := policy.Validate(); err != nil {
		return V3SessionMaintenanceResult{}, err
	}
	if err := contextError(ctx); err != nil {
		return V3SessionMaintenanceResult{}, err
	}
	if now.IsZero() {
		now = time.Now()
	}

	s.store.sessionMutations.maintenanceMu.Lock()
	defer s.store.sessionMutations.maintenanceMu.Unlock()

	state, ok, err := s.GetV3SessionMaintenanceState()
	if err != nil {
		return V3SessionMaintenanceResult{}, err
	}
	if !ok {
		state = V3SessionMaintenanceState{Version: v3SessionMaintenanceVersion}
	}
	result := V3SessionMaintenanceResult{}
	batch := s.store.NewBatch()
	defer batch.Close()

	if err := s.stageExpiredV3RealtimeOutbox(ctx, batch, now, policy, &state, &result); err != nil {
		return V3SessionMaintenanceResult{}, err
	}
	idempotencyBudget := policy.BatchRecords - result.RealtimeRecordsDeleted
	if err := s.stageExpiredV3Idempotency(ctx, batch, now, policy, idempotencyBudget, &state, &result); err != nil {
		return V3SessionMaintenanceResult{}, err
	}
	result.OldestRetainedEndpointSeq = state.OldestRetainedRealtimeEndpointSeq
	if !commit {
		return result, nil
	}
	state.Version = v3SessionMaintenanceVersion
	state.UpdatedAtUnixMs = now.UnixMilli()
	statePayload, err := json.Marshal(state)
	if err != nil {
		return V3SessionMaintenanceResult{}, fmt.Errorf("marshal v3 session maintenance state: %w", err)
	}
	if err := batch.Set([]byte(keyV3SessionMaintenanceState), statePayload, nil); err != nil {
		return V3SessionMaintenanceResult{}, fmt.Errorf("stage v3 session maintenance state: %w", err)
	}
	if hook := s.store.sessionMutations.beforeSessionMaintenanceCommit; hook != nil {
		if err := hook(); err != nil {
			return V3SessionMaintenanceResult{}, fmt.Errorf("before v3 session maintenance commit: %w", err)
		}
	}
	if err := batch.Commit(pebble.Sync); err != nil {
		return V3SessionMaintenanceResult{}, fmt.Errorf("commit v3 session maintenance pass: %w", err)
	}
	return result, nil
}

func (s *SessionStore) stageExpiredV3RealtimeOutbox(ctx context.Context, batch *pebble.Batch, now time.Time, policy V3SessionRetentionPolicy, state *V3SessionMaintenanceState, result *V3SessionMaintenanceResult) error {
	head, err := s.readV3RealtimeOutboxSequence()
	if err != nil {
		return fmt.Errorf("read v3 realtime outbox head for retention: %w", err)
	}
	if head <= policy.RealtimeMinimumRecords {
		return nil
	}
	maximumPrunable := head - policy.RealtimeMinimumRecords
	next := state.RealtimePrunedThroughEndpointSeq + 1
	cutoffUnixMs := now.Add(-policy.RealtimeReplayRetention).UnixMilli()
	for result.RealtimeRecordsDeleted < policy.BatchRecords && next <= maximumPrunable {
		if err := contextError(ctx); err != nil {
			return err
		}
		var record V3RealtimeOutboxRecord
		found, err := getJSONFromReader(s.store.db, KeyV3RealtimeOutbox(next), &record)
		if err != nil {
			return fmt.Errorf("read v3 realtime outbox %d for retention: %w", next, err)
		}
		if !found {
			return fmt.Errorf("v3 realtime retention found unexpected gap at endpoint %d", next)
		}
		if record.EndpointSeq != next {
			return fmt.Errorf("v3 realtime retention canonical key %d contains endpoint %d", next, record.EndpointSeq)
		}
		if record.CreatedAt <= 0 {
			return fmt.Errorf("v3 realtime retention endpoint %d has no creation timestamp", next)
		}
		if record.CreatedAt >= cutoffUnixMs {
			break
		}
		keys := []string{
			KeyV3RealtimeOutbox(next),
			KeyV3RealtimeOutboxBySessionEndpoint(record.SessionID, next),
			KeyV3RealtimeOutboxBySessionSeq(record.SessionID, record.Event.Seq),
			KeyV3RealtimeOutboxByAuthScope(record.AccountScopeID, record.UserID, next),
		}
		namespaces := []string{"v3_realtime_outbox", "v3_realtime_by_session_endpoint", "v3_realtime_by_session_seq", "v3_realtime_by_auth"}
		for index, key := range keys {
			value, found, err := getBytesFromReader(s.store.db, key)
			if err != nil {
				return fmt.Errorf("measure v3 realtime retention delete for endpoint %d: %w", next, err)
			}
			if found {
				logicalBytes := uint64(len(key) + len(value))
				result.RealtimeRowsDeleted++
				result.RealtimeLogicalBytesDeleted += logicalBytes
				addSessionStorageCleanupCandidate(result, namespaces[index], logicalBytes)
			}
			if err := batch.Delete([]byte(key), nil); err != nil {
				return fmt.Errorf("stage v3 realtime retention delete for endpoint %d: %w", next, err)
			}
		}
		state.RealtimePrunedThroughEndpointSeq = next
		state.OldestRetainedRealtimeEndpointSeq = next + 1
		result.RealtimeRecordsDeleted++
		next++
	}
	result.MoreRealtimeWork = result.RealtimeRecordsDeleted == policy.BatchRecords && next <= maximumPrunable
	return nil
}

func addSessionStorageCleanupCandidate(result *V3SessionMaintenanceResult, namespace string, logicalBytes uint64) {
	for index := range result.Namespaces {
		if result.Namespaces[index].Namespace == namespace {
			result.Namespaces[index].Records++
			result.Namespaces[index].LogicalBytes += logicalBytes
			return
		}
	}
	result.Namespaces = append(result.Namespaces, SessionStorageCleanupCandidate{Namespace: namespace, Records: 1, LogicalBytes: logicalBytes})
}

func (s *SessionStore) stageExpiredV3Idempotency(ctx context.Context, batch *pebble.Batch, now time.Time, policy V3SessionRetentionPolicy, budget int, state *V3SessionMaintenanceState, result *V3SessionMaintenanceResult) error {
	const prefix = "v3/session_idempotency/"
	if budget <= 0 {
		result.MoreIdempotencyWork = true
		return nil
	}
	if state.CompletedIdempotencyCutoffUnixMs == 0 {
		state.CompletedIdempotencyCutoffUnixMs = now.Add(-policy.CompletedIdempotencyRetention).UnixMilli()
		state.CompletedIdempotencyResumeKey = ""
	}
	iter, err := s.store.db.NewIter(&pebble.IterOptions{LowerBound: []byte(prefix), UpperBound: []byte(prefix + "\xff")})
	if err != nil {
		return fmt.Errorf("create v3 idempotency retention iterator: %w", err)
	}
	defer iter.Close()

	valid := iter.First()
	if state.CompletedIdempotencyResumeKey != "" {
		valid = iter.SeekGE([]byte(state.CompletedIdempotencyResumeKey + "\x00"))
	}
	lastKey := ""
	for valid && result.IdempotencyRecordsScanned < budget {
		if err := contextError(ctx); err != nil {
			return err
		}
		key := string(append([]byte(nil), iter.Key()...))
		var record V3SessionIdempotencyRecord
		if err := json.Unmarshal(iter.Value(), &record); err != nil {
			return fmt.Errorf("decode v3 idempotency record %q during retention: %w", key, err)
		}
		if record.Status == V3SessionMutationStatusCompleted && record.CompletedAt > 0 && record.CompletedAt < state.CompletedIdempotencyCutoffUnixMs {
			logicalBytes := uint64(len(key) + len(iter.Value()))
			result.IdempotencyLogicalBytesDeleted += logicalBytes
			addSessionStorageCleanupCandidate(result, "v3_idempotency", logicalBytes)
			if err := batch.Delete([]byte(key), nil); err != nil {
				return fmt.Errorf("stage completed v3 idempotency retention delete: %w", err)
			}
			result.IdempotencyRecordsDeleted++
		}
		result.IdempotencyRecordsScanned++
		lastKey = key
		valid = iter.Next()
	}
	if err := iter.Error(); err != nil {
		return fmt.Errorf("scan v3 idempotency records for retention: %w", err)
	}
	if valid {
		state.CompletedIdempotencyResumeKey = lastKey
		result.MoreIdempotencyWork = true
	} else {
		state.CompletedIdempotencyCutoffUnixMs = 0
		state.CompletedIdempotencyResumeKey = ""
	}
	return nil
}

// SessionStorageNamespace identifies a key prefix only. Measurement results do
// not retain sampled keys or values, keeping reports safe for logs and APIs.
type SessionStorageNamespace struct {
	Name   string `json:"name"`
	Prefix string `json:"prefix"`
}

// SessionStorageNamespaceMeasurement contains aggregate sizes only; it never
// contains database keys, message text, event payloads, or stored values.
type SessionStorageNamespaceMeasurement struct {
	Name         string `json:"name"`
	Prefix       string `json:"prefix"`
	Count        uint64 `json:"count"`
	KeyBytes     uint64 `json:"key_bytes"`
	ValueBytes   uint64 `json:"value_bytes"`
	LogicalBytes uint64 `json:"logical_bytes"`
}

type SessionStorageMeasurement struct {
	Namespaces        []SessionStorageNamespaceMeasurement `json:"namespaces"`
	TotalCount        uint64                               `json:"total_count"`
	TotalKeyBytes     uint64                               `json:"total_key_bytes"`
	TotalValueBytes   uint64                               `json:"total_value_bytes"`
	TotalLogicalBytes uint64                               `json:"total_logical_bytes"`
}

// DefaultSessionStorageNamespaces covers the canonical V3 rows and the legacy
// or compatibility indexes implicated in session growth. Prefixes are disjoint
// so totals do not double count logical bytes.
func DefaultSessionStorageNamespaces() []SessionStorageNamespace {
	return []SessionStorageNamespace{
		{Name: "session_snapshots", Prefix: "session/"},
		{Name: "session_by_account", Prefix: "session_by_account/"},
		{Name: "session_recent_global", Prefix: KeySessionRecentGlobalPrefix},
		{Name: "session_recent_account", Prefix: KeySessionRecentAccountPrefix},
		{Name: "session_recent_workspace", Prefix: KeySessionRecentWorkspacePrefix},
		{Name: "session_recent_account_workspace", Prefix: KeySessionRecentAccountWorkspacePrefix},
		{Name: "v3_events", Prefix: "v3/session_event/"},
		{Name: "v3_projections", Prefix: "v3/session_projection/"},
		{Name: "v3_messages", Prefix: "v3/session_message/"},
		{Name: "v3_run_intents", Prefix: "v3/session_run_intent/"},
		{Name: "v3_idempotency", Prefix: "v3/session_idempotency/"},
		{Name: "v3_realtime_outbox", Prefix: V3RealtimeOutboxPrefix()},
		{Name: "v3_realtime_by_session_endpoint", Prefix: "v3/realtime_outbox_by_session_endpoint/"},
		{Name: "v3_realtime_by_session_seq", Prefix: "v3/realtime_outbox_by_session_seq/"},
		{Name: "v3_realtime_by_auth", Prefix: "v3/realtime_outbox_by_auth/"},
		{Name: "v3_search_meta", Prefix: keyV3SessionSearchMetaPrefix},
		{Name: "v3_search_postings", Prefix: keyV3SessionSearchPostingPrefix},
		{Name: "legacy_v3_search_account_postings", Prefix: keyV3SessionSearchAccountPrefix},
		{Name: "v3_tombstones", Prefix: KeyV3SessionTombstonePrefix},
	}
}

func (s *Store) MeasureSessionStorageNamespaces(ctx context.Context, namespaces []SessionStorageNamespace) (SessionStorageMeasurement, error) {
	if s == nil || s.db == nil {
		return SessionStorageMeasurement{}, errors.New("store is not configured")
	}
	if len(namespaces) == 0 {
		namespaces = DefaultSessionStorageNamespaces()
	}
	seenNames := make(map[string]struct{}, len(namespaces))
	seenPrefixes := make(map[string]struct{}, len(namespaces))
	for _, namespace := range namespaces {
		name := strings.TrimSpace(namespace.Name)
		prefix := strings.TrimSpace(namespace.Prefix)
		if name == "" || prefix == "" {
			return SessionStorageMeasurement{}, errors.New("session storage namespace name and prefix are required")
		}
		if _, exists := seenNames[name]; exists {
			return SessionStorageMeasurement{}, fmt.Errorf("duplicate session storage namespace name %q", name)
		}
		if _, exists := seenPrefixes[prefix]; exists {
			return SessionStorageMeasurement{}, fmt.Errorf("duplicate session storage namespace prefix %q", prefix)
		}
		seenNames[name] = struct{}{}
		seenPrefixes[prefix] = struct{}{}
	}

	snapshot := s.db.NewSnapshot()
	defer snapshot.Close()
	result := SessionStorageMeasurement{Namespaces: make([]SessionStorageNamespaceMeasurement, 0, len(namespaces))}
	for _, namespace := range namespaces {
		if err := contextError(ctx); err != nil {
			return SessionStorageMeasurement{}, err
		}
		measurement, err := measureSessionStorageNamespace(ctx, snapshot, namespace)
		if err != nil {
			return SessionStorageMeasurement{}, err
		}
		result.Namespaces = append(result.Namespaces, measurement)
		result.TotalCount += measurement.Count
		result.TotalKeyBytes += measurement.KeyBytes
		result.TotalValueBytes += measurement.ValueBytes
		result.TotalLogicalBytes += measurement.LogicalBytes
	}
	return result, nil
}

func measureSessionStorageNamespace(ctx context.Context, reader pebble.Reader, namespace SessionStorageNamespace) (SessionStorageNamespaceMeasurement, error) {
	name := strings.TrimSpace(namespace.Name)
	prefix := strings.TrimSpace(namespace.Prefix)
	measurement := SessionStorageNamespaceMeasurement{Name: name, Prefix: prefix}
	iter, err := reader.NewIter(&pebble.IterOptions{LowerBound: []byte(prefix), UpperBound: []byte(prefix + "\xff")})
	if err != nil {
		return SessionStorageNamespaceMeasurement{}, fmt.Errorf("create session storage namespace iterator %q: %w", name, err)
	}
	defer iter.Close()
	for valid := iter.First(); valid; valid = iter.Next() {
		if err := contextError(ctx); err != nil {
			return SessionStorageNamespaceMeasurement{}, err
		}
		measurement.Count++
		measurement.KeyBytes += uint64(len(iter.Key()))
		measurement.ValueBytes += uint64(len(iter.Value()))
	}
	if err := iter.Error(); err != nil {
		return SessionStorageNamespaceMeasurement{}, fmt.Errorf("measure session storage namespace %q: %w", name, err)
	}
	measurement.LogicalBytes = measurement.KeyBytes + measurement.ValueBytes
	return measurement, nil
}
