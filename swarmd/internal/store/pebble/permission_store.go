package pebblestore

import (
	"encoding/json"
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
		if err := batch.Set([]byte(nextPendingKey), []byte(recordKey), nil); err != nil {
			return fmt.Errorf("set pending index: %w", err)
		}
	case prevPendingKey != "" && nextPendingKey != "" && prevPendingKey != nextPendingKey:
		if err := batch.Delete([]byte(prevPendingKey), nil); err != nil {
			return fmt.Errorf("delete stale pending index: %w", err)
		}
		if err := batch.Set([]byte(nextPendingKey), []byte(recordKey), nil); err != nil {
			return fmt.Errorf("set pending index: %w", err)
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
		if !strings.EqualFold(strings.TrimSpace(record.Status), PermissionStatusPending) {
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

func (s *PermissionStore) CountPendingPermissions(sessionID string) (int, int64, int64, error) {
	count := 0
	oldest := int64(0)
	newest := int64(0)
	err := s.store.IteratePrefix(PermissionPendingPrefix(sessionID), 1000000, func(_ string, value []byte) error {
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
		if !strings.EqualFold(strings.TrimSpace(record.Status), PermissionStatusPending) {
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
	return s.store.PutJSON(KeyPermissionSummary(summary.PrincipalID, summary.SessionID), summary)
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
	return summary, true, nil
}

func (s *PermissionStore) PutPolicy(payload []byte) error {
	if s == nil || s.store == nil {
		return fmt.Errorf("permission store is not configured")
	}
	return s.store.PutBytes(KeyPermissionPolicy(), payload)
}

func (s *PermissionStore) GetPolicy() ([]byte, bool, error) {
	if s == nil || s.store == nil {
		return nil, false, fmt.Errorf("permission store is not configured")
	}
	return s.store.GetBytes(KeyPermissionPolicy())
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
