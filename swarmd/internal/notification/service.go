package notification

import (
	"encoding/json"
	"errors"
	"fmt"
	"strings"
	"sync"
	"sync/atomic"
	"time"

	pebblestore "swarm/packages/swarmd/internal/store/pebble"
)

const (
	EventNotificationCreated = "notification.created"
	EventNotificationUpdated = "notification.updated"
)

type Service struct {
	store                *pebblestore.NotificationStore
	events               *pebblestore.EventLog
	publish              func(pebblestore.EventEnvelope)
	localSwarmIDResolver func() string
	mu                   sync.Mutex
	counter              atomic.Uint64
}

type PermissionUpsertInput struct {
	SwarmID         string
	OriginSwarmID   string
	SessionID       string
	RunID           string
	PermissionID    string
	ToolName        string
	Requirement     string
	Title           string
	Body            string
	Severity        string
	Status          string
	SourceEventType string
	CreatedAt       int64
	UpdatedAt       int64
	ReadAt          int64
	AckedAt         int64
	MutedAt         int64
}

type UpdateInput struct {
	SwarmID        string
	NotificationID string
	MarkRead       *bool
	MarkAcked      *bool
	MarkMuted      *bool
	ResolvedStatus string
}

func NewService(store *pebblestore.NotificationStore, events *pebblestore.EventLog, publish func(pebblestore.EventEnvelope)) *Service {
	return &Service{store: store, events: events, publish: publish}
}

func (s *Service) SetLocalSwarmIDResolver(resolver func() string) {
	if s == nil {
		return
	}
	s.localSwarmIDResolver = resolver
}

func (s *Service) LocalSwarmID() string {
	if s == nil || s.localSwarmIDResolver == nil {
		return ""
	}
	return strings.TrimSpace(s.localSwarmIDResolver())
}

func (s *Service) ListNotifications(swarmID string, limit int) ([]pebblestore.NotificationRecord, error) {
	swarmID = strings.TrimSpace(swarmID)
	if swarmID == "" {
		swarmID = s.LocalSwarmID()
	}
	if swarmID == "" {
		return nil, errors.New("swarm id is required")
	}
	return s.store.ListNotifications(swarmID, limit)
}

func (s *Service) Summary(swarmID string) (pebblestore.NotificationSummary, error) {
	swarmID = strings.TrimSpace(swarmID)
	if swarmID == "" {
		swarmID = s.LocalSwarmID()
	}
	if swarmID == "" {
		return pebblestore.NotificationSummary{}, errors.New("swarm id is required")
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.refreshSummaryLocked(swarmID, time.Now().UnixMilli())
}

func (s *Service) UpsertPermissionNotification(input PermissionUpsertInput) (pebblestore.NotificationRecord, bool, error) {
	if s == nil || s.store == nil {
		return pebblestore.NotificationRecord{}, false, errors.New("notification service is not configured")
	}
	now := time.Now().UnixMilli()
	swarmID := strings.TrimSpace(input.SwarmID)
	if swarmID == "" {
		swarmID = s.LocalSwarmID()
	}
	if swarmID == "" {
		return pebblestore.NotificationRecord{}, false, errors.New("swarm id is required")
	}
	permissionID := strings.TrimSpace(input.PermissionID)
	if permissionID == "" {
		return pebblestore.NotificationRecord{}, false, errors.New("permission id is required")
	}
	s.mu.Lock()
	defer s.mu.Unlock()

	var previous *pebblestore.NotificationRecord
	notificationID, ok, err := s.store.LookupPermissionNotificationID(input.SessionID, permissionID)
	if err != nil {
		return pebblestore.NotificationRecord{}, false, err
	}
	if ok {
		existing, found, err := s.store.GetNotification(swarmID, notificationID)
		if err != nil {
			return pebblestore.NotificationRecord{}, false, err
		}
		if found {
			previous = &existing
		}
	}

	record := pebblestore.NotificationRecord{
		ID:              notificationID,
		SwarmID:         swarmID,
		OriginSwarmID:   firstNonEmpty(strings.TrimSpace(input.OriginSwarmID), swarmID),
		SessionID:       strings.TrimSpace(input.SessionID),
		RunID:           strings.TrimSpace(input.RunID),
		Category:        pebblestore.NotificationCategoryPermission,
		Severity:        strings.TrimSpace(input.Severity),
		Title:           strings.TrimSpace(input.Title),
		Body:            strings.TrimSpace(input.Body),
		Status:          strings.TrimSpace(input.Status),
		SourceEventType: strings.TrimSpace(input.SourceEventType),
		PermissionID:    permissionID,
		ToolName:        strings.TrimSpace(input.ToolName),
		Requirement:     strings.TrimSpace(input.Requirement),
		ReadAt:          input.ReadAt,
		AckedAt:         input.AckedAt,
		MutedAt:         input.MutedAt,
		CreatedAt:       input.CreatedAt,
		UpdatedAt:       input.UpdatedAt,
	}
	if previous != nil {
		record.ID = previous.ID
		if record.CreatedAt <= 0 {
			record.CreatedAt = previous.CreatedAt
		}
		if record.ReadAt <= 0 {
			record.ReadAt = previous.ReadAt
		}
		if record.AckedAt <= 0 {
			record.AckedAt = previous.AckedAt
		}
		if record.MutedAt <= 0 {
			record.MutedAt = previous.MutedAt
		}
	}
	if strings.TrimSpace(record.ID) == "" {
		record.ID = s.newNotificationID(now, swarmID, permissionID)
	}
	if record.CreatedAt <= 0 {
		record.CreatedAt = firstNonZero(input.CreatedAt, input.UpdatedAt, now)
	}
	if record.UpdatedAt <= 0 {
		record.UpdatedAt = firstNonZero(input.UpdatedAt, input.CreatedAt, now)
	}
	if record.Title == "" {
		record.Title = permissionNotificationTitle(record.ToolName)
	}
	if record.Body == "" {
		record.Body = permissionNotificationBody(record.ToolName, record.Requirement)
	}
	if record.Severity == "" {
		record.Severity = pebblestore.NotificationSeverityWarning
	}
	if record.Status == "" {
		record.Status = pebblestore.NotificationStatusActive
	}
	changed := previous == nil || !notificationRecordsEqual(*previous, record)
	if !changed {
		return record, false, nil
	}
	if err := s.store.PutNotification(record, previous); err != nil {
		return pebblestore.NotificationRecord{}, false, err
	}
	if _, err := s.refreshSummaryLocked(swarmID, record.UpdatedAt); err != nil {
		return pebblestore.NotificationRecord{}, false, err
	}
	eventType := EventNotificationCreated
	if previous != nil {
		eventType = EventNotificationUpdated
	}
	_, _ = s.emitLocked("swarm:notifications", eventType, record.ID, map[string]any{"notification": record})
	return record, true, nil
}

func (s *Service) UpsertSystemNotification(record pebblestore.NotificationRecord) (pebblestore.NotificationRecord, bool, error) {
	if s == nil || s.store == nil {
		return pebblestore.NotificationRecord{}, false, errors.New("notification service is not configured")
	}
	swarmID := strings.TrimSpace(record.SwarmID)
	if swarmID == "" {
		swarmID = s.LocalSwarmID()
	}
	if swarmID == "" {
		return pebblestore.NotificationRecord{}, false, errors.New("swarm id is required")
	}
	notificationID := strings.TrimSpace(record.ID)
	if notificationID == "" {
		return pebblestore.NotificationRecord{}, false, errors.New("notification id is required")
	}
	now := time.Now().UnixMilli()
	s.mu.Lock()
	defer s.mu.Unlock()

	var previous *pebblestore.NotificationRecord
	existing, found, err := s.store.GetNotification(swarmID, notificationID)
	if err != nil {
		return pebblestore.NotificationRecord{}, false, err
	}
	if found {
		previous = &existing
	}
	record.ID = notificationID
	record.SwarmID = swarmID
	if strings.TrimSpace(record.OriginSwarmID) == "" {
		record.OriginSwarmID = swarmID
	}
	record.Category = pebblestore.NotificationCategorySystem
	if strings.TrimSpace(record.Severity) == "" {
		record.Severity = pebblestore.NotificationSeverityInfo
	}
	if strings.TrimSpace(record.Status) == "" {
		record.Status = pebblestore.NotificationStatusActive
	}
	if record.CreatedAt <= 0 {
		if previous != nil {
			record.CreatedAt = previous.CreatedAt
		} else {
			record.CreatedAt = now
		}
	}
	if record.UpdatedAt <= 0 {
		record.UpdatedAt = now
	}
	if previous != nil {
		if record.ReadAt <= 0 {
			record.ReadAt = previous.ReadAt
		}
		if record.AckedAt <= 0 {
			record.AckedAt = previous.AckedAt
		}
		if record.MutedAt <= 0 {
			record.MutedAt = previous.MutedAt
		}
	}
	changed := previous == nil || !notificationRecordsEqual(*previous, record)
	if !changed {
		return record, false, nil
	}
	if err := s.store.PutNotification(record, previous); err != nil {
		return pebblestore.NotificationRecord{}, false, err
	}
	if _, err := s.refreshSummaryLocked(swarmID, record.UpdatedAt); err != nil {
		return pebblestore.NotificationRecord{}, false, err
	}
	eventType := EventNotificationCreated
	if previous != nil {
		eventType = EventNotificationUpdated
	}
	_, _ = s.emitLocked("swarm:notifications", eventType, record.ID, map[string]any{"notification": record})
	return record, true, nil
}

func (s *Service) UpdateNotification(input UpdateInput) (pebblestore.NotificationRecord, bool, error) {
	if s == nil || s.store == nil {
		return pebblestore.NotificationRecord{}, false, errors.New("notification service is not configured")
	}
	swarmID := strings.TrimSpace(input.SwarmID)
	if swarmID == "" {
		swarmID = s.LocalSwarmID()
	}
	notificationID := strings.TrimSpace(input.NotificationID)
	if swarmID == "" || notificationID == "" {
		return pebblestore.NotificationRecord{}, false, errors.New("swarm id and notification id are required")
	}
	now := time.Now().UnixMilli()
	s.mu.Lock()
	defer s.mu.Unlock()
	record, ok, err := s.store.GetNotification(swarmID, notificationID)
	if err != nil {
		return pebblestore.NotificationRecord{}, false, err
	}
	if !ok {
		return pebblestore.NotificationRecord{}, false, fmt.Errorf("notification %q not found", notificationID)
	}
	updated := record
	if input.MarkRead != nil {
		if *input.MarkRead {
			updated.ReadAt = firstNonZero(updated.ReadAt, now)
		} else {
			updated.ReadAt = 0
		}
	}
	if input.MarkAcked != nil {
		if *input.MarkAcked {
			updated.AckedAt = firstNonZero(updated.AckedAt, now)
			if updated.ReadAt <= 0 {
				updated.ReadAt = updated.AckedAt
			}
		} else {
			updated.AckedAt = 0
		}
	}
	if input.MarkMuted != nil {
		if *input.MarkMuted {
			updated.MutedAt = firstNonZero(updated.MutedAt, now)
		} else {
			updated.MutedAt = 0
		}
	}
	if status := strings.TrimSpace(strings.ToLower(input.ResolvedStatus)); status != "" {
		updated.Status = status
	}
	updated.UpdatedAt = now
	if notificationRecordsEqual(record, updated) {
		return updated, false, nil
	}
	if err := s.store.PutNotification(updated, &record); err != nil {
		return pebblestore.NotificationRecord{}, false, err
	}
	if _, err := s.refreshSummaryLocked(swarmID, now); err != nil {
		return pebblestore.NotificationRecord{}, false, err
	}
	_, _ = s.emitLocked("swarm:notifications", EventNotificationUpdated, updated.ID, map[string]any{"notification": updated})
	return updated, true, nil
}

func (s *Service) refreshSummaryLocked(swarmID string, now int64) (pebblestore.NotificationSummary, error) {
	records, err := s.store.ListNotifications(swarmID, 100000)
	if err != nil {
		return pebblestore.NotificationSummary{}, err
	}
	summary := pebblestore.NotificationSummary{SwarmID: swarmID, UpdatedAt: now}
	for _, record := range records {
		summary.TotalCount++
		if record.ReadAt <= 0 {
			summary.UnreadCount++
		}
		if strings.EqualFold(strings.TrimSpace(record.Status), pebblestore.NotificationStatusActive) {
			summary.ActiveCount++
		}
	}
	if err := s.store.PutSummary(summary); err != nil {
		return pebblestore.NotificationSummary{}, err
	}
	return summary, nil
}

func (s *Service) emitLocked(streamID, eventType, entityID string, payload any) (*pebblestore.EventEnvelope, error) {
	if s == nil || s.events == nil {
		return nil, nil
	}
	raw, err := json.Marshal(payload)
	if err != nil {
		return nil, err
	}
	env, err := s.events.Append(streamID, eventType, entityID, raw, "", "")
	if err != nil {
		return nil, err
	}
	if s.publish != nil {
		s.publish(env)
	}
	return &env, nil
}

func (s *Service) newNotificationID(now int64, swarmID, permissionID string) string {
	seq := s.counter.Add(1)
	return fmt.Sprintf("notif_%d_%s_%s_%06d", now, sanitizeIDPart(swarmID), sanitizeIDPart(permissionID), seq)
}

func sanitizeIDPart(value string) string {
	value = strings.TrimSpace(strings.ToLower(value))
	if value == "" {
		return "none"
	}
	var b strings.Builder
	for _, r := range value {
		switch {
		case r >= 'a' && r <= 'z':
			b.WriteRune(r)
		case r >= '0' && r <= '9':
			b.WriteRune(r)
		default:
			b.WriteRune('_')
		}
	}
	out := strings.Trim(b.String(), "_")
	if out == "" {
		return "none"
	}
	return out
}

func permissionNotificationTitle(toolName string) string {
	toolName = strings.TrimSpace(toolName)
	if toolName == "" {
		return "Permission requested"
	}
	return fmt.Sprintf("Permission requested: %s", toolName)
}

func permissionNotificationBody(toolName, requirement string) string {
	toolName = strings.TrimSpace(toolName)
	requirement = strings.TrimSpace(requirement)
	if toolName == "" && requirement == "" {
		return "An agent action is waiting for approval."
	}
	if toolName == "" {
		return fmt.Sprintf("A %s action is waiting for approval.", requirement)
	}
	if requirement == "" {
		return fmt.Sprintf("The %s action is waiting for approval.", toolName)
	}
	return fmt.Sprintf("The %s %s action is waiting for approval.", requirement, toolName)
}

func notificationRecordsEqual(a, b pebblestore.NotificationRecord) bool {
	return a.ID == b.ID &&
		a.SwarmID == b.SwarmID &&
		a.OriginSwarmID == b.OriginSwarmID &&
		a.SessionID == b.SessionID &&
		a.RunID == b.RunID &&
		a.Category == b.Category &&
		a.Severity == b.Severity &&
		a.Title == b.Title &&
		a.Body == b.Body &&
		a.Status == b.Status &&
		a.SourceEventType == b.SourceEventType &&
		a.PermissionID == b.PermissionID &&
		a.ToolName == b.ToolName &&
		a.Requirement == b.Requirement &&
		a.ReadAt == b.ReadAt &&
		a.AckedAt == b.AckedAt &&
		a.MutedAt == b.MutedAt &&
		a.CreatedAt == b.CreatedAt &&
		a.UpdatedAt == b.UpdatedAt
}

func firstNonZero(values ...int64) int64 {
	for _, value := range values {
		if value > 0 {
			return value
		}
	}
	return 0
}

func firstNonEmpty(values ...string) string {
	for _, value := range values {
		if strings.TrimSpace(value) != "" {
			return strings.TrimSpace(value)
		}
	}
	return ""
}
