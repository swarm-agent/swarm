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
			"media_url":   "s3://my-vault/video.mp4",
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

func TestInboxNotificationSecurityHardening(t *testing.T) {
	tempDir := t.TempDir()
	store, err := pebblestore.Open(filepath.Join(tempDir, "pebble"))
	if err != nil {
		t.Fatalf("failed to open pebble store: %v", err)
	}
	defer store.Close()

	notifStore := pebblestore.NewNotificationStore(store)
	svc := NewService(notifStore, nil, nil)
	svc.SetLocalSwarmIDResolver(func() string { return "test-swarm" })

	// 1. Disallowed endpoint rejection
	_, err = svc.SubmitInboxNotification(InboxNotificationInput{
		AccountScopeID: "acct-1",
		SwarmID:        "test-swarm",
		Title:          "Malicious Action",
		Actions: []pebblestore.NotificationAction{
			{
				ID:       "destroy",
				Label:    "Destroy",
				Endpoint: "/v1/environments/destroy",
			},
		},
	})
	if err == nil {
		t.Fatal("expected error for disallowed endpoint /v1/environments/destroy, got nil")
	}

	// 2. Reject absolute external URL as action endpoint
	_, err = svc.SubmitInboxNotification(InboxNotificationInput{
		AccountScopeID: "acct-1",
		SwarmID:        "test-swarm",
		Title:          "SSRF Endpoint",
		Actions: []pebblestore.NotificationAction{
			{
				ID:       "ssrf",
				Label:    "Trigger Broker",
				Endpoint: "http://localhost:8765/v1/activate",
			},
		},
	})
	if err == nil {
		t.Fatal("expected error for absolute SSRF endpoint, got nil")
	}

	// 3. Reject javascript: ActionURL (Stored XSS)
	_, err = svc.SubmitInboxNotification(InboxNotificationInput{
		AccountScopeID: "acct-1",
		SwarmID:        "test-swarm",
		Title:          "XSS Link",
		ActionURL:      "javascript:alert(document.cookie)",
	})
	if err == nil {
		t.Fatal("expected error for javascript: action_url, got nil")
	}

	// 4. Reject unsafe media URL
	_, err = svc.SubmitInboxNotification(InboxNotificationInput{
		AccountScopeID: "acct-1",
		SwarmID:        "test-swarm",
		Title:          "XSS Media",
		Payload: map[string]any{
			"media_url": "javascript:alert(1)",
		},
	})
	if err == nil {
		t.Fatal("expected error for javascript: media_url, got nil")
	}

	// 5. Allowed action endpoints succeed
	validRecord, err := svc.SubmitInboxNotification(InboxNotificationInput{
		AccountScopeID: "acct-1",
		SwarmID:        "test-swarm",
		Title:          "Valid Deliverable",
		WorkerID:       "worker-1",
		Verified:       true,
		Actions: []pebblestore.NotificationAction{
			{
				ID:       "approve",
				Label:    "Approve",
				Endpoint: "/v3/deliverables/del_123/approve",
			},
			{
				ID:       "trigger",
				Label:    "Trigger",
				Endpoint: "/v3/automations/v2/trigger",
			},
		},
	})
	if err != nil {
		t.Fatalf("expected valid submission to succeed, got: %v", err)
	}
	if !validRecord.Verified {
		t.Fatal("expected record to be verified")
	}
	if validRecord.WorkerID != "worker-1" {
		t.Fatalf("expected worker_id worker-1, got %q", validRecord.WorkerID)
	}

	// 6. Worker collision protection: another worker cannot overwrite worker-1's record
	_, err = svc.SubmitInboxNotification(InboxNotificationInput{
		ID:             validRecord.ID,
		AccountScopeID: "acct-1",
		SwarmID:        "test-swarm",
		Title:          "Hijack Attempt",
		WorkerID:       "worker-2",
	})
	if err == nil {
		t.Fatal("expected error when worker-2 attempts to overwrite worker-1 notification, got nil")
	}

	// 7. Non-inbox category overwrite protection
	sysRecord := pebblestore.NotificationRecord{
		ID:             "sys_alert_1",
		AccountScopeID: "acct-1",
		SwarmID:        "test-swarm",
		Category:       pebblestore.NotificationCategorySystem,
		Title:          "System Alert",
		Status:         pebblestore.NotificationStatusActive,
	}
	if err := notifStore.PutNotification(sysRecord, nil); err != nil {
		t.Fatalf("failed to insert system notification: %v", err)
	}
	// Attempting to overwrite sys_alert_1 with inbox category
	_, err = svc.SubmitInboxNotification(InboxNotificationInput{
		ID:             "sys_alert_1",
		AccountScopeID: "acct-1",
		SwarmID:        "test-swarm",
		Title:          "Attempt to Overwrite System Alert",
	})
	if err == nil {
		t.Fatal("expected error attempting to overwrite system notification, got nil")
	}
}
