package notification

import (
	"path/filepath"
	"testing"

	pebblestore "swarm/packages/swarmd/internal/store/pebble"
)

func TestSubmitInboxNotification(t *testing.T) {
	tempDir := t.TempDir()
	store, err := pebblestore.Open(filepath.Join(tempDir, "pebble"))
	if err != nil {
		t.Fatalf("failed to open pebble store: %v", err)
	}
	defer store.Close()

	notifStore := pebblestore.NewNotificationStore(store)
	svc := NewService(notifStore, nil, nil)
	svc.SetLocalSwarmIDResolver(func() string { return "test-swarm" })

	input := InboxNotificationInput{
		AccountScopeID: "acct-1",
		SwarmID:        "test-swarm",
		Title:          "Weekly Content Campaign",
		Body:           "5 tweets drafted and 1 video rendered",
		Kind:           pebblestore.NotificationKindAIDeliverable,
		Payload: map[string]any{
			"media_url": "s3://my-vault/video.mp4",
			"posts_count": 5,
		},
		Actions: []pebblestore.NotificationAction{
			{
				ID:         "approve",
				Label:      "Approve & Schedule",
				ActionType: "approve",
				Variant:    "primary",
			},
			{
				ID:         "reject",
				Label:      "Request Changes",
				ActionType: "reject",
				Variant:    "secondary",
			},
		},
	}

	record, err := svc.SubmitInboxNotification(input)
	if err != nil {
		t.Fatalf("SubmitInboxNotification failed: %v", err)
	}

	if record.ID == "" {
		t.Fatal("expected generated notification ID")
	}
	if record.Kind != pebblestore.NotificationKindAIDeliverable {
		t.Fatalf("expected kind %q, got %q", pebblestore.NotificationKindAIDeliverable, record.Kind)
	}
	if record.Category != pebblestore.NotificationCategoryInbox {
		t.Fatalf("expected category %q, got %q", pebblestore.NotificationCategoryInbox, record.Category)
	}
	if record.Status != pebblestore.NotificationStatusActive {
		t.Fatalf("expected status %q, got %q", pebblestore.NotificationStatusActive, record.Status)
	}
	if len(record.Actions) != 2 {
		t.Fatalf("expected 2 actions, got %d", len(record.Actions))
	}
	if record.Payload["media_url"] != "s3://my-vault/video.mp4" {
		t.Fatalf("expected payload media_url preserved, got %v", record.Payload["media_url"])
	}

	// Verify listed notifications for account
	records, err := svc.ListNotificationsForAccount("acct-1", "test-swarm", 10)
	if err != nil {
		t.Fatalf("ListNotificationsForAccount failed: %v", err)
	}
	if len(records) != 1 {
		t.Fatalf("expected 1 record in list, got %d", len(records))
	}
	if records[0].Kind != pebblestore.NotificationKindAIDeliverable {
		t.Fatalf("listed record kind mismatch: %q", records[0].Kind)
	}
}
