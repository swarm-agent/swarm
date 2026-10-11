package notification

import (
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"log"
	"strings"
	"time"
	"unicode/utf8"

	pebblestore "swarm/packages/swarmd/internal/store/pebble"
)

const (
	projectTaskNotificationSourceEvent = "project.task.status"
	maxProjectTaskNotificationTitle    = 200
)

// ObserveProjectTaskStatus is the store's project-task status observer. A task
// (orchestrated or the task a worker run executes) that reaches a status the
// person must act on or should know about produces one task notification.
func (s *Service) ObserveProjectTaskStatus(transition pebblestore.ProjectTaskStatusTransition) {
	if _, _, err := s.NotifyProjectTaskStatus(transition); err != nil {
		log.Printf("warning: project task notification failed project=%q task=%q status=%q: %v", transition.Task.ProjectID, transition.Task.ID, transition.Task.Status, err)
	}
}

// NotifyProjectTaskStatus records at most one notification per task attempt and
// status. The ID is derived from that identity, so a retried or replayed
// transition finds the existing record and changes nothing, keeping read state.
func (s *Service) NotifyProjectTaskStatus(transition pebblestore.ProjectTaskStatusTransition) (pebblestore.NotificationRecord, bool, error) {
	if s == nil || s.store == nil {
		return pebblestore.NotificationRecord{}, false, errors.New("notification service is not configured")
	}
	task := transition.Task
	status := strings.TrimSpace(task.Status)
	title, severity, body, ok := projectTaskNotificationText(status, task.Title)
	if !ok || task.Archived || strings.TrimSpace(transition.PriorStatus) == status {
		return pebblestore.NotificationRecord{}, false, nil
	}
	accountScopeID := strings.TrimSpace(transition.AccountScopeID)
	projectID, taskID := strings.TrimSpace(task.ProjectID), strings.TrimSpace(task.ID)
	if accountScopeID == "" || projectID == "" || taskID == "" {
		return pebblestore.NotificationRecord{}, false, errors.New("project task notification requires account, project and task ids")
	}
	swarmID := s.LocalSwarmID()
	if swarmID == "" {
		return pebblestore.NotificationRecord{}, false, errors.New("swarm id is required")
	}
	attemptID := strings.TrimSpace(task.ActiveAttemptID)
	runID := ""
	if attempt := task.ActiveAttempt(); attempt != nil {
		runID = strings.TrimSpace(attempt.RunID)
	}
	payload := map[string]any{
		"project_id": projectID,
		"task_id":    taskID,
		"status":     status,
	}
	if attemptID != "" {
		payload["attempt_id"] = attemptID
	}
	if sessionID := strings.TrimSpace(task.SessionID); sessionID != "" {
		payload["session_id"] = sessionID
	}
	if workerRunID := strings.TrimSpace(task.WorkerRunID); workerRunID != "" {
		payload["worker_run_id"] = workerRunID
		runID = firstNonEmpty(runID, workerRunID)
	}
	if workerID := strings.TrimSpace(task.WorkerID); workerID != "" {
		payload["worker_id"] = workerID
	}
	now := time.Now().UnixMilli()
	record := pebblestore.NotificationRecord{
		ID:              ProjectTaskNotificationID(accountScopeID, projectID, taskID, attemptID, status),
		AccountScopeID:  accountScopeID,
		SwarmID:         swarmID,
		OriginSwarmID:   swarmID,
		SessionID:       strings.TrimSpace(task.SessionID),
		RunID:           runID,
		WorkerID:        strings.TrimSpace(task.WorkerID),
		Category:        pebblestore.NotificationCategoryTask,
		Kind:            pebblestore.NotificationKindSystem,
		Severity:        severity,
		Title:           title,
		Body:            body,
		Status:          pebblestore.NotificationStatusActive,
		SourceEventType: projectTaskNotificationSourceEvent,
		WorkspacePath:   strings.TrimSpace(task.WorkspacePath),
		ActionURL:       NotificationActionURL("", task.WorkspacePath, task.SessionID),
		Payload:         payload,
		CreatedAt:       now,
		UpdatedAt:       now,
	}

	s.mu.Lock()
	defer s.mu.Unlock()
	existing, found, err := s.store.GetNotificationForAccount(accountScopeID, swarmID, record.ID)
	if err != nil {
		return pebblestore.NotificationRecord{}, false, err
	}
	if found {
		return existing, false, nil
	}
	if err := s.store.PutNotification(record, nil); err != nil {
		return pebblestore.NotificationRecord{}, false, err
	}
	summary, err := s.refreshSummaryForAccountLocked(accountScopeID, swarmID, record.UpdatedAt)
	if err != nil {
		return pebblestore.NotificationRecord{}, false, err
	}
	_, _ = s.emitLocked("swarm:notifications", EventNotificationCreated, record.ID, map[string]any{"notification": record, "summary": summary})
	s.publishRealtimeLocked(RealtimeEvent{EventType: EventNotificationCreated, AccountScopeID: accountScopeID, SwarmID: swarmID, Notification: &record, Summary: &summary, RecordedAt: record.UpdatedAt})
	s.dispatchWebPushLocked(record)
	return record, true, nil
}

// ProjectTaskNotificationID is stable for one task attempt reaching one status.
func ProjectTaskNotificationID(accountScopeID, projectID, taskID, attemptID, status string) string {
	sum := sha256.Sum256([]byte(strings.Join([]string{accountScopeID, projectID, taskID, attemptID, status}, "\x00")))
	return "task_" + hex.EncodeToString(sum[:12])
}

func projectTaskNotificationText(status, taskTitle string) (title, severity, body string, ok bool) {
	name := boundedNotificationText(strings.TrimSpace(taskTitle), maxProjectTaskNotificationTitle)
	if name == "" {
		name = "Untitled task"
	}
	switch status {
	case "needs_review":
		return "Task ready for review: " + name, pebblestore.NotificationSeverityInfo, "The task finished and is waiting for your review.", true
	case "failed":
		return "Task failed: " + name, pebblestore.NotificationSeverityWarning, "The task stopped with an error. Open it to see what happened.", true
	case "completed":
		return "Task completed: " + name, pebblestore.NotificationSeverityInfo, "The task is complete.", true
	default:
		return "", "", "", false
	}
}

func boundedNotificationText(value string, maximum int) string {
	if len(value) <= maximum {
		return value
	}
	value = value[:maximum]
	for len(value) > 0 && !utf8.ValidString(value) {
		value = value[:len(value)-1]
	}
	return strings.TrimSpace(value) + "…"
}
