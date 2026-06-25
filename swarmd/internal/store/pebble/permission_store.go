package pebblestore

import (
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"sort"
	"strings"

	"swarm/packages/swarmd/internal/privacy"

	"github.com/cockroachdb/pebble"
)

const (
	PermissionStatusPending     = "pending"
	PermissionStatusApproved    = "approved"
	PermissionStatusDenied      = "denied"
	PermissionStatusCancelled   = "cancelled"
	PermissionStatusNotRequired = "not_required"

	PermissionExecQueued          = "queued"
	PermissionExecWaitingApproval = "waiting_approval"
	PermissionExecRunning         = "running"
	PermissionExecCompleted       = "completed"
	PermissionExecFailed          = "failed"
	PermissionExecSkipped         = "skipped"
	PermissionExecCancelled       = "cancelled"
)

type PermissionRecord struct {
	ID                  string `json:"id"`
	SessionID           string `json:"session_id"`
	RunID               string `json:"run_id"`
	Step                int    `json:"step,omitempty"`
	CallID              string `json:"call_id"`
	ToolName            string `json:"tool_name"`
	ToolArguments       string `json:"tool_arguments"`
	ToolCallArguments   string `json:"tool_call_arguments,omitempty"`
	ApprovedArguments   string `json:"approved_arguments,omitempty"`
	Requirement         string `json:"requirement"`
	Mode                string `json:"mode"`
	Status              string `json:"status"`
	Decision            string `json:"decision"`
	Reason              string `json:"reason"`
	PermissionRequested int64  `json:"permission_requested_at,omitempty"`
	ResolvedAt          int64  `json:"resolved_at"`
	ExecutionStatus     string `json:"execution_status,omitempty"`
	Output              string `json:"output,omitempty"`
	Error               string `json:"error,omitempty"`
	DurationMS          int64  `json:"duration_ms,omitempty"`
	StartedAt           int64  `json:"started_at,omitempty"`
	CompletedAt         int64  `json:"completed_at,omitempty"`
	CreatedAt           int64  `json:"created_at"`
	UpdatedAt           int64  `json:"updated_at"`
}

type PermissionSummary struct {
	AccountScopeID  string `json:"account_scope_id,omitempty"`
	PrincipalID     string `json:"principal_id"`
	SessionID       string `json:"session_id"`
	PendingCount    int    `json:"pending_count"`
	OldestPendingAt int64  `json:"oldest_pending_at"`
	NewestPendingAt int64  `json:"newest_pending_at"`
	UpdatedAt       int64  `json:"updated_at"`
}

type RunWaitState struct {
	SessionID            string   `json:"session_id"`
	RunID                string   `json:"run_id"`
	PendingPermissionIDs []string `json:"pending_permission_ids"`
	CreatedAt            int64    `json:"created_at"`
	UpdatedAt            int64    `json:"updated_at"`
}

type PermissionStore struct {
	store *Store
}

func NewPermissionStore(store *Store) *PermissionStore {
	return &PermissionStore{store: store}
}

func (s *PermissionStore) GetPermission(sessionID, permissionID string) (PermissionRecord, bool, error) {
	var record PermissionRecord
	ok, err := s.store.GetJSON(KeyPermission(sessionID, permissionID), &record)
	if err != nil {
		return PermissionRecord{}, false, err
	}
	if !ok {
		return PermissionRecord{}, false, nil
	}
	return record, true, nil
}

func (s *PermissionStore) PutPermission(record PermissionRecord, previous *PermissionRecord) error {
	record = sanitizePermissionRecord(record)
	recordKey := KeyPermission(record.SessionID, record.ID)
	serialized, err := json.Marshal(record)
	if err != nil {
		return fmt.Errorf("marshal permission record: %w", err)
	}

	batch := s.store.NewBatch()
	defer batch.Close()

	if err := batch.Set([]byte(recordKey), serialized, nil); err != nil {
		return fmt.Errorf("set permission record: %w", err)
	}

	prevPendingKey := ""
	if previous != nil && strings.EqualFold(strings.TrimSpace(previous.Status), PermissionStatusPending) {
		prevPendingKey = KeyPermissionPending(previous.SessionID, previous.CreatedAt, previous.ID)
	}
	nextPending := strings.EqualFold(strings.TrimSpace(record.Status), PermissionStatusPending)
	nextPendingKey := ""
	if nextPending {
		nextPendingKey = KeyPermissionPending(record.SessionID, record.CreatedAt, record.ID)
	}

	switch {
	case prevPendingKey != "" && nextPendingKey == "":
		if err := batch.Delete([]byte(prevPendingKey), nil); err != nil {
			return fmt.Errorf("delete stale pending index: %w", err)
		}
	case prevPendingKey == "" && nextPendingKey != "":
		if err := batch.Set([]byte(nextPendingKey), serialized, nil); err != nil {
			return fmt.Errorf("set pending index: %w", err)
		}
	case prevPendingKey != "" && nextPendingKey != "" && prevPendingKey != nextPendingKey:
		if err := batch.Delete([]byte(prevPendingKey), nil); err != nil {
			return fmt.Errorf("delete stale pending index: %w", err)
		}
		if err := batch.Set([]byte(nextPendingKey), serialized, nil); err != nil {
			return fmt.Errorf("set pending index: %w", err)
		}
	case prevPendingKey != "" && nextPendingKey != "":
		if err := batch.Set([]byte(nextPendingKey), serialized, nil); err != nil {
			return fmt.Errorf("update pending index: %w", err)
		}
	}

	runID := strings.TrimSpace(record.RunID)
	if runID != "" {
		runPermKey := KeyRunPermission(record.SessionID, runID, record.ID)
		if err := batch.Set([]byte(runPermKey), []byte(recordKey), nil); err != nil {
			return fmt.Errorf("set run permission index: %w", err)
		}
	}

	if err := batch.Commit(pebble.Sync); err != nil {
		return fmt.Errorf("commit permission batch: %w", err)
	}
	return nil
}

func (s *PermissionStore) ListPermissions(sessionID string, limit int) ([]PermissionRecord, error) {
	if limit <= 0 {
		limit = 1000
	}
	out := make([]PermissionRecord, 0, limit)
	err := s.store.IteratePrefix(PermissionPrefix(sessionID), limit, func(_ string, value []byte) error {
		var record PermissionRecord
		if err := json.Unmarshal(value, &record); err != nil {
			return err
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

func (s *PermissionStore) ListPendingPermissions(sessionID string, limit int) ([]PermissionRecord, error) {
	if limit <= 0 {
		limit = 200
	}
	out := make([]PermissionRecord, 0, limit)
	err := s.store.IteratePrefix(PermissionPendingPrefix(sessionID), limit, func(_ string, value []byte) error {
		record, ok, err := decodePendingPermissionIndexValue(s.store.db, value)
		if err != nil {
			return err
		}
		if !ok {
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

func decodePendingPermissionIndexValue(reader pebble.Reader, value []byte) (PermissionRecord, bool, error) {
	raw := strings.TrimSpace(string(value))
	if raw == "" {
		return PermissionRecord{}, false, nil
	}
	var record PermissionRecord
	if strings.HasPrefix(raw, "{") {
		if err := json.Unmarshal([]byte(raw), &record); err != nil {
			return PermissionRecord{}, false, err
		}
	} else {
		ok, err := getJSONFromReader(reader, raw, &record)
		if err != nil {
			return PermissionRecord{}, false, err
		}
		if !ok {
			return PermissionRecord{}, false, nil
		}
	}
	if !strings.EqualFold(strings.TrimSpace(record.Status), PermissionStatusPending) {
		return PermissionRecord{}, false, nil
	}
	if strings.TrimSpace(record.ID) == "" || strings.TrimSpace(record.SessionID) == "" {
		return PermissionRecord{}, false, nil
	}
	return record, true, nil
}

func (s *PermissionStore) CountPendingPermissions(sessionID string) (int, int64, int64, error) {
	count := 0
	oldest := int64(0)
	newest := int64(0)
	err := s.store.IteratePrefix(PermissionPendingPrefix(sessionID), 1000000, func(_ string, value []byte) error {
		record, ok, err := decodePendingPermissionIndexValue(s.store.db, value)
		if err != nil {
			return err
		}
		if !ok {
			return nil
		}
		count++
		if oldest == 0 || record.CreatedAt < oldest {
			oldest = record.CreatedAt
		}
		if record.CreatedAt > newest {
			newest = record.CreatedAt
		}
		return nil
	})
	if err != nil {
		return 0, 0, 0, err
	}
	return count, oldest, newest, nil
}

func (s *PermissionStore) PutSummary(summary PermissionSummary) error {
	summary = sanitizePermissionSummary(summary)
	payload, err := json.Marshal(summary)
	if err != nil {
		return fmt.Errorf("marshal permission summary: %w", err)
	}
	var previous PermissionSummary
	previousOK, err := s.store.GetJSON(KeyPermissionSummary(summary.PrincipalID, summary.SessionID), &previous)
	if err != nil {
		return err
	}
	previous = sanitizePermissionSummary(previous)
	batch := s.store.NewBatch()
	defer batch.Close()
	if err := batch.Set([]byte(KeyPermissionSummary(summary.PrincipalID, summary.SessionID)), payload, nil); err != nil {
		return fmt.Errorf("set permission summary: %w", err)
	}
	if previousOK {
		previousPendingKey := KeyPermissionSummaryPending(previous.AccountScopeID, previous.PrincipalID, previous.SessionID)
		currentPendingKey := KeyPermissionSummaryPending(summary.AccountScopeID, summary.PrincipalID, summary.SessionID)
		if previousPendingKey != currentPendingKey || summary.PendingCount <= 0 {
			if err := batch.Delete([]byte(previousPendingKey), nil); err != nil && !errors.Is(err, pebble.ErrNotFound) {
				return fmt.Errorf("delete stale permission summary pending index: %w", err)
			}
		}
	}
	pendingKey := KeyPermissionSummaryPending(summary.AccountScopeID, summary.PrincipalID, summary.SessionID)
	if summary.PendingCount > 0 {
		if err := batch.Set([]byte(pendingKey), payload, nil); err != nil {
			return fmt.Errorf("set permission summary pending index: %w", err)
		}
	} else {
		if err := batch.Delete([]byte(pendingKey), nil); err != nil && !errors.Is(err, pebble.ErrNotFound) {
			return fmt.Errorf("delete permission summary pending index: %w", err)
		}
	}
	if err := batch.Commit(pebble.Sync); err != nil {
		return fmt.Errorf("commit permission summary batch: %w", err)
	}
	return nil
}

func (s *PermissionStore) GetSummary(principalID, sessionID string) (PermissionSummary, bool, error) {
	var summary PermissionSummary
	ok, err := s.store.GetJSON(KeyPermissionSummary(principalID, sessionID), &summary)
	if err != nil {
		return PermissionSummary{}, false, err
	}
	if !ok {
		return PermissionSummary{}, false, nil
	}
	return sanitizePermissionSummary(summary), true, nil
}

func (s *PermissionStore) ListPendingSummaries(accountScopeID, principalID string, limit int) ([]PermissionSummary, error) {
	if limit <= 0 {
		limit = 100000
	}
	out := make([]PermissionSummary, 0)
	err := s.store.IteratePrefix(PermissionSummaryPendingPrefix(accountScopeID, principalID), limit, func(_ string, value []byte) error {
		var summary PermissionSummary
		if err := json.Unmarshal(value, &summary); err != nil {
			return err
		}
		summary = sanitizePermissionSummary(summary)
		if summary.PendingCount <= 0 || summary.SessionID == "" {
			return nil
		}
		out = append(out, summary)
		return nil
	})
	if err != nil {
		return nil, err
	}
	sort.Slice(out, func(i, j int) bool {
		if out[i].OldestPendingAt == out[j].OldestPendingAt {
			return out[i].SessionID < out[j].SessionID
		}
		return out[i].OldestPendingAt < out[j].OldestPendingAt
	})
	return out, nil
}

func (s *PermissionStore) RepairSummaryPendingIndex(accountScopeID, principalID string) error {
	if s == nil || s.store == nil {
		return fmt.Errorf("permission store is not configured")
	}
	stale := make([]string, 0)
	if err := s.store.IteratePrefix(PermissionSummaryPendingPrefix(accountScopeID, principalID), 1000000, func(key string, _ []byte) error {
		stale = append(stale, key)
		return nil
	}); err != nil {
		return err
	}
	batch := s.store.NewBatch()
	defer batch.Close()
	for _, key := range stale {
		if err := batch.Delete([]byte(key), nil); err != nil && !errors.Is(err, pebble.ErrNotFound) {
			return fmt.Errorf("delete stale permission summary pending index: %w", err)
		}
	}
	prefix := "perm_summary/"
	if strings.TrimSpace(principalID) != "" {
		prefix = fmt.Sprintf("perm_summary/%s/", keyPart(principalID))
	}
	if err := s.store.IteratePrefix(prefix, 1000000, func(_ string, value []byte) error {
		var summary PermissionSummary
		if err := json.Unmarshal(value, &summary); err != nil {
			return err
		}
		summary = sanitizePermissionSummary(summary)
		if accountScopeID != "" && summary.AccountScopeID != strings.TrimSpace(accountScopeID) {
			return nil
		}
		if principalID != "" && summary.PrincipalID != strings.TrimSpace(principalID) {
			return nil
		}
		if summary.PendingCount <= 0 || summary.SessionID == "" {
			return nil
		}
		payload, err := json.Marshal(summary)
		if err != nil {
			return err
		}
		return batch.Set([]byte(KeyPermissionSummaryPending(summary.AccountScopeID, summary.PrincipalID, summary.SessionID)), payload, nil)
	}); err != nil {
		return err
	}
	if err := batch.Commit(pebble.Sync); err != nil {
		return fmt.Errorf("commit permission summary pending repair: %w", err)
	}
	return nil
}

func sanitizePermissionSummary(summary PermissionSummary) PermissionSummary {
	summary.AccountScopeID = strings.TrimSpace(summary.AccountScopeID)
	summary.PrincipalID = strings.TrimSpace(summary.PrincipalID)
	summary.SessionID = strings.TrimSpace(summary.SessionID)
	if summary.PendingCount < 0 {
		summary.PendingCount = 0
	}
	if summary.PendingCount == 0 {
		summary.OldestPendingAt = 0
		summary.NewestPendingAt = 0
	}
	return summary
}

func (s *PermissionStore) PutPolicy(payload []byte) error {
	return s.PutPolicyForAccount("", payload)
}

func (s *PermissionStore) PutPolicyForAccount(accountScopeID string, payload []byte) error {
	if s == nil || s.store == nil {
		return fmt.Errorf("permission store is not configured")
	}
	return s.store.PutBytes(permissionPolicyKeyForAccount(accountScopeID), payload)
}

func (s *PermissionStore) GetPolicy() ([]byte, bool, error) {
	return s.GetPolicyForAccount("")
}

func (s *PermissionStore) GetPolicyForAccount(accountScopeID string) ([]byte, bool, error) {
	if s == nil || s.store == nil {
		return nil, false, fmt.Errorf("permission store is not configured")
	}
	return s.store.GetBytes(permissionPolicyKeyForAccount(accountScopeID))
}

func permissionPolicyKeyForAccount(accountScopeID string) string {
	accountScopeID = strings.TrimSpace(accountScopeID)
	if accountScopeID == "" {
		return KeyPermissionPolicy()
	}
	sum := sha256.Sum256([]byte(accountScopeID))
	return KeyPermissionPolicy() + "/account/" + hex.EncodeToString(sum[:])
}

func (s *PermissionStore) UpsertRunWait(state RunWaitState) error {
	return s.store.PutJSON(KeyRunWait(state.SessionID, state.RunID), state)
}

func (s *PermissionStore) GetRunWait(sessionID, runID string) (RunWaitState, bool, error) {
	var state RunWaitState
	ok, err := s.store.GetJSON(KeyRunWait(sessionID, runID), &state)
	if err != nil {
		return RunWaitState{}, false, err
	}
	if !ok {
		return RunWaitState{}, false, nil
	}
	return state, true, nil
}

func (s *PermissionStore) DeleteRunWait(sessionID, runID string) error {
	return s.store.Delete(KeyRunWait(sessionID, runID))
}

func (s *PermissionStore) ListRunWaits(sessionID string, limit int) ([]RunWaitState, error) {
	if limit <= 0 {
		limit = 1000
	}
	out := make([]RunWaitState, 0, limit)
	err := s.store.IteratePrefix(RunWaitPrefix(sessionID), limit, func(_ string, value []byte) error {
		var state RunWaitState
		if err := json.Unmarshal(value, &state); err != nil {
			return err
		}
		out = append(out, state)
		return nil
	})
	if err != nil {
		return nil, err
	}
	sort.Slice(out, func(i, j int) bool {
		if out[i].CreatedAt == out[j].CreatedAt {
			if out[i].SessionID == out[j].SessionID {
				return out[i].RunID < out[j].RunID
			}
			return out[i].SessionID < out[j].SessionID
		}
		return out[i].CreatedAt < out[j].CreatedAt
	})
	if len(out) > limit {
		out = out[:limit]
	}
	return out, nil
}

func (s *PermissionStore) ListRunPermissions(sessionID, runID string, limit int) ([]PermissionRecord, error) {
	if limit <= 0 {
		limit = 1000
	}
	out := make([]PermissionRecord, 0, limit)
	err := s.store.IteratePrefix(RunPermissionPrefix(sessionID, runID), limit, func(_ string, value []byte) error {
		recordKey := strings.TrimSpace(string(value))
		if recordKey == "" {
			return nil
		}
		var record PermissionRecord
		ok, err := s.store.GetJSON(recordKey, &record)
		if err != nil {
			return err
		}
		if !ok {
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

func sanitizePermissionRecord(record PermissionRecord) PermissionRecord {
	record.ToolArguments = sanitizePermissionArguments(record.ToolArguments)
	record.ToolCallArguments = sanitizePermissionArguments(record.ToolCallArguments)
	record.ApprovedArguments = sanitizePermissionArguments(record.ApprovedArguments)
	record.Output = sanitizePermissionOutput(record.Output)
	record.Error = privacy.SanitizeText(record.Error)
	record.Reason = privacy.SanitizeText(record.Reason)
	return record
}

func sanitizePermissionArguments(raw string) string {
	trimmed := strings.TrimSpace(raw)
	if trimmed == "" {
		return "{}"
	}
	sanitized := privacy.SanitizeJSONText(trimmed)
	if strings.TrimSpace(sanitized) == "" {
		return "{}"
	}
	return sanitized
}

func sanitizePermissionOutput(raw string) string {
	trimmed := strings.TrimSpace(raw)
	if trimmed == "" {
		return ""
	}
	return privacy.SanitizeJSONText(trimmed)
}
