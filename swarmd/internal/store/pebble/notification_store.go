package pebblestore

import (
	"encoding/json"
	"errors"
	"fmt"
	"sort"
	"strings"

	"github.com/cockroachdb/pebble"
)

const (
	NotificationCategoryPermission = "permission"
	NotificationCategorySystem     = "system"

	NotificationSeverityInfo    = "info"
	NotificationSeverityWarning = "warning"
	NotificationSeverityError   = "error"

	NotificationStatusActive   = "active"
	NotificationStatusResolved = "resolved"
)

type NotificationRecord struct {
	ID              string `json:"id"`
	SwarmID         string `json:"swarm_id"`
	OriginSwarmID   string `json:"origin_swarm_id,omitempty"`
	SessionID       string `json:"session_id,omitempty"`
	RunID           string `json:"run_id,omitempty"`
	Category        string `json:"category"`
	Severity        string `json:"severity"`
	Title           string `json:"title"`
	Body            string `json:"body"`
	Status          string `json:"status"`
	SourceEventType string `json:"source_event_type,omitempty"`
	PermissionID    string `json:"permission_id,omitempty"`
	ToolName        string `json:"tool_name,omitempty"`
	Requirement     string `json:"requirement,omitempty"`
	ReadAt          int64  `json:"read_at,omitempty"`
	AckedAt         int64  `json:"acked_at,omitempty"`
	MutedAt         int64  `json:"muted_at,omitempty"`
	CreatedAt       int64  `json:"created_at"`
	UpdatedAt       int64  `json:"updated_at"`
}

type NotificationSummary struct {
	SwarmID     string `json:"swarm_id"`
	TotalCount  int    `json:"total_count"`
	UnreadCount int    `json:"unread_count"`
	ActiveCount int    `json:"active_count"`
	UpdatedAt   int64  `json:"updated_at"`
}

type NotificationStore struct {
	store *Store
}

func NewNotificationStore(store *Store) *NotificationStore {
	return &NotificationStore{store: store}
}

func (s *NotificationStore) GetNotification(swarmID, notificationID string) (NotificationRecord, bool, error) {
	if s == nil || s.store == nil {
		return NotificationRecord{}, false, errors.New("notification store is not configured")
	}
	var record NotificationRecord
	ok, err := s.store.GetJSON(KeyNotification(swarmID, notificationID), &record)
	if err != nil {
		return NotificationRecord{}, false, err
	}
	if !ok {
		return NotificationRecord{}, false, nil
	}
	return sanitizeNotificationRecord(record), true, nil
}

func (s *NotificationStore) ListNotifications(swarmID string, limit int) ([]NotificationRecord, error) {
	if s == nil || s.store == nil {
		return nil, errors.New("notification store is not configured")
	}
	if limit <= 0 {
		limit = 200
	}
	out := make([]NotificationRecord, 0, limit)
	err := s.store.IteratePrefix(NotificationBySwarmPrefix(swarmID), 100000, func(_ string, value []byte) error {
		recordKey := strings.TrimSpace(string(value))
		if recordKey == "" {
			return nil
		}
		var record NotificationRecord
		ok, err := s.store.GetJSON(recordKey, &record)
		if err != nil {
			return err
		}
		if !ok {
			return nil
		}
		out = append(out, sanitizeNotificationRecord(record))
		return nil
	})
	if err != nil {
		return nil, err
	}
	sort.Slice(out, func(i, j int) bool {
		if out[i].CreatedAt == out[j].CreatedAt {
			return out[i].ID > out[j].ID
		}
		return out[i].CreatedAt > out[j].CreatedAt
	})
	if len(out) > limit {
		out = out[:limit]
	}
	return out, nil
}

func (s *NotificationStore) PutNotification(record NotificationRecord, previous *NotificationRecord) error {
	if s == nil || s.store == nil {
		return errors.New("notification store is not configured")
	}
	record = sanitizeNotificationRecord(record)
	if record.SwarmID == "" || record.ID == "" {
		return errors.New("notification swarm_id and id are required")
	}
	serialized, err := json.Marshal(record)
	if err != nil {
		return fmt.Errorf("marshal notification record: %w", err)
	}
	batch := s.store.NewBatch()
	defer batch.Close()

	recordKey := KeyNotification(record.SwarmID, record.ID)
	if err := batch.Set([]byte(recordKey), serialized, nil); err != nil {
		return fmt.Errorf("set notification record: %w", err)
	}

	prevIndexKey := ""
	if previous != nil {
		prev := sanitizeNotificationRecord(*previous)
		if prev.SwarmID != "" && prev.ID != "" {
			prevIndexKey = KeyNotificationBySwarm(prev.SwarmID, prev.CreatedAt, prev.ID)
		}
	}
	nextIndexKey := KeyNotificationBySwarm(record.SwarmID, record.CreatedAt, record.ID)
	switch {
	case prevIndexKey != "" && prevIndexKey != nextIndexKey:
		if err := batch.Delete([]byte(prevIndexKey), nil); err != nil {
			return fmt.Errorf("delete stale notification index: %w", err)
		}
		fallthrough
	case prevIndexKey == "":
		if err := batch.Set([]byte(nextIndexKey), []byte(recordKey), nil); err != nil {
			return fmt.Errorf("set notification index: %w", err)
		}
	}

	if record.PermissionID != "" && record.SessionID != "" {
		if err := batch.Set([]byte(KeyNotificationPermissionRef(record.SessionID, record.PermissionID)), []byte(record.ID), nil); err != nil {
			return fmt.Errorf("set notification permission ref: %w", err)
		}
	}

	if err := batch.Commit(pebble.Sync); err != nil {
		return fmt.Errorf("commit notification batch: %w", err)
	}
	return nil
}

func (s *NotificationStore) LookupPermissionNotificationID(sessionID, permissionID string) (string, bool, error) {
	if s == nil || s.store == nil {
		return "", false, errors.New("notification store is not configured")
	}
	raw, ok, err := s.store.GetBytes(KeyNotificationPermissionRef(sessionID, permissionID))
	if err != nil {
		return "", false, err
	}
	if !ok {
		return "", false, nil
	}
	id := strings.TrimSpace(string(raw))
	if id == "" {
		return "", false, nil
	}
	return id, true, nil
}

func (s *NotificationStore) PutSummary(summary NotificationSummary) error {
	if s == nil || s.store == nil {
		return errors.New("notification store is not configured")
	}
	summary.SwarmID = strings.TrimSpace(summary.SwarmID)
	if summary.SwarmID == "" {
		return errors.New("notification summary swarm id is required")
	}
	if summary.TotalCount < 0 {
		summary.TotalCount = 0
	}
	if summary.UnreadCount < 0 {
		summary.UnreadCount = 0
	}
	if summary.ActiveCount < 0 {
		summary.ActiveCount = 0
	}
	return s.store.PutJSON(KeyNotificationSummary(summary.SwarmID), summary)
}

func (s *NotificationStore) GetSummary(swarmID string) (NotificationSummary, bool, error) {
	if s == nil || s.store == nil {
		return NotificationSummary{}, false, errors.New("notification store is not configured")
	}
	var summary NotificationSummary
	ok, err := s.store.GetJSON(KeyNotificationSummary(swarmID), &summary)
	if err != nil {
		return NotificationSummary{}, false, err
	}
	if !ok {
		return NotificationSummary{}, false, nil
	}
	summary.SwarmID = strings.TrimSpace(summary.SwarmID)
	return summary, true, nil
}

func sanitizeNotificationRecord(record NotificationRecord) NotificationRecord {
	record.ID = strings.TrimSpace(record.ID)
	record.SwarmID = strings.TrimSpace(record.SwarmID)
	record.OriginSwarmID = strings.TrimSpace(record.OriginSwarmID)
	record.SessionID = strings.TrimSpace(record.SessionID)
	record.RunID = strings.TrimSpace(record.RunID)
	record.Category = strings.TrimSpace(strings.ToLower(record.Category))
	if record.Category == "" {
		record.Category = NotificationCategoryPermission
	}
	record.Severity = strings.TrimSpace(strings.ToLower(record.Severity))
	if record.Severity == "" {
		record.Severity = NotificationSeverityInfo
	}
	record.Title = strings.TrimSpace(record.Title)
	record.Body = strings.TrimSpace(record.Body)
	record.Status = strings.TrimSpace(strings.ToLower(record.Status))
	if record.Status == "" {
		record.Status = NotificationStatusActive
	}
	record.SourceEventType = strings.TrimSpace(record.SourceEventType)
	record.PermissionID = strings.TrimSpace(record.PermissionID)
	record.ToolName = strings.TrimSpace(record.ToolName)
	record.Requirement = strings.TrimSpace(strings.ToLower(record.Requirement))
	if record.CreatedAt < 0 {
		record.CreatedAt = 0
	}
	if record.UpdatedAt < 0 {
		record.UpdatedAt = 0
	}
	if record.OriginSwarmID == "" {
		record.OriginSwarmID = record.SwarmID
	}
	return record
}
