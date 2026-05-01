package api

import (
	"testing"

	"swarm/packages/swarmd/internal/notification"
	pebblestore "swarm/packages/swarmd/internal/store/pebble"
)

type updateNotificationRecorder struct {
	records []pebblestore.NotificationRecord
}

func (r *updateNotificationRecorder) LocalSwarmID() string { return "local" }

func (r *updateNotificationRecorder) ListNotifications(string, int) ([]pebblestore.NotificationRecord, error) {
	return nil, nil
}

func (r *updateNotificationRecorder) Summary(string) (pebblestore.NotificationSummary, error) {
	return pebblestore.NotificationSummary{}, nil
}

func (r *updateNotificationRecorder) ClearNotifications(string) (notification.ClearResult, error) {
	return notification.ClearResult{}, nil
}

func (r *updateNotificationRecorder) UpdateNotification(notification.UpdateInput) (pebblestore.NotificationRecord, bool, error) {
	return pebblestore.NotificationRecord{}, false, nil
}

func (r *updateNotificationRecorder) UpsertSystemNotification(record pebblestore.NotificationRecord) (pebblestore.NotificationRecord, bool, error) {
	r.records = append(r.records, record)
	return record, true, nil
}

func TestEmitUpdateNotificationSkipsCompletedInfoNotifications(t *testing.T) {
	notifications := &updateNotificationRecorder{}
	server := &Server{notifications: notifications}
	server.emitUpdateNotification(desktopUpdateJob{
		ID:            "job-1",
		Kind:          updateKindDev,
		Status:        updateJobStatusCompleted,
		Message:       "Dev rebuild completed.",
		StartedAtUnix: 10,
	}, pebblestore.NotificationSeverityInfo, "Swarm update completed", "Dev rebuild completed.", "update.completed")
	if len(notifications.records) != 0 {
		t.Fatalf("completed info update notification was persisted: %#v", notifications.records)
	}
}

func TestEmitUpdateNotificationPersistsFailures(t *testing.T) {
	notifications := &updateNotificationRecorder{}
	server := &Server{notifications: notifications}
	server.emitUpdateNotification(desktopUpdateJob{
		ID:            "job-2",
		Kind:          updateKindDev,
		Status:        updateJobStatusFailed,
		Error:         "boom",
		StartedAtUnix: 10,
	}, pebblestore.NotificationSeverityError, "Swarm update failed", "boom", "update.failed")
	if len(notifications.records) != 1 {
		t.Fatalf("failure update notification count = %d, want 1", len(notifications.records))
	}
	if got := notifications.records[0].Status; got != pebblestore.NotificationStatusActive {
		t.Fatalf("failure notification status = %q, want %q", got, pebblestore.NotificationStatusActive)
	}
	if got := notifications.records[0].SourceEventType; got != "update.failed" {
		t.Fatalf("failure notification event type = %q", got)
	}
}
