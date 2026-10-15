package api

import (
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

func (r *updateNotificationRecorder) SubmitInboxNotification(input notification.InboxNotificationInput) (pebblestore.NotificationRecord, error) {
	record := pebblestore.NotificationRecord{
		ID:       input.ID,
		Title:    input.Title,
		Body:     input.Body,
		Category: input.Category,
		Kind:     input.Kind,
		Severity: input.Severity,
		Status:   input.Status,
		Payload:  input.Payload,
		Actions:  input.Actions,
	}
	r.records = append(r.records, record)
	return record, nil
}

func (r *updateNotificationRecorder) SubmitInboxNotificationForAccount(accountScopeID string, input notification.InboxNotificationInput) (pebblestore.NotificationRecord, error) {
	input.AccountScopeID = accountScopeID
	return r.SubmitInboxNotification(input)
}
