package pebblestore

import (
	"testing"
)

func TestAutomationV2WebhooksCRUD(t *testing.T) {
	path := t.TempDir()
	db, err := Open(path)
	if err != nil {
		t.Fatalf("Open failed: %v", err)
	}
	defer db.Close()

	sessionStore := NewSessionStore(db)
	account := "acct_test_123"

	// List empty
	list, err := sessionStore.ListAutomationV2Webhooks(account)
	if err != nil {
		t.Fatalf("ListAutomationV2Webhooks failed: %v", err)
	}
	if len(list) != 0 {
		t.Fatalf("expected 0 webhooks, got %d", len(list))
	}

	// Put invalid URL
	badWhk := AutomationV2GlobalWebhook{
		URL: "not-a-valid-url",
	}
	if err := sessionStore.PutAutomationV2Webhook(account, &badWhk); err == nil {
		t.Fatal("expected error on invalid webhook URL, got nil")
	}

	// Put valid
	whk1 := AutomationV2GlobalWebhook{
		ID:      "whk_custom_1",
		URL:     "https://example.com/webhook",
		Secret:  "super-secret-hmac",
		Format:  "generic",
		Events:  []string{"started", "succeeded"},
		Enabled: true,
	}
	if err := sessionStore.PutAutomationV2Webhook(account, &whk1); err != nil {
		t.Fatalf("PutAutomationV2Webhook failed: %v", err)
	}

	// Get
	got, found, err := sessionStore.GetAutomationV2Webhook(account, "whk_custom_1")
	if err != nil || !found {
		t.Fatalf("GetAutomationV2Webhook failed: found=%v, err=%v", found, err)
	}
	if got.URL != "https://example.com/webhook" || got.Secret != "super-secret-hmac" {
		t.Fatalf("unexpected webhook: %+v", got)
	}

	// List has 1
	list, err = sessionStore.ListAutomationV2Webhooks(account)
	if err != nil {
		t.Fatalf("ListAutomationV2Webhooks failed: %v", err)
	}
	if len(list) != 1 || list[0].ID != "whk_custom_1" {
		t.Fatalf("expected 1 webhook, got %d", len(list))
	}

	// Delete
	if err := sessionStore.DeleteAutomationV2Webhook(account, "whk_custom_1"); err != nil {
		t.Fatalf("DeleteAutomationV2Webhook failed: %v", err)
	}

	// Get after delete
	_, found, err = sessionStore.GetAutomationV2Webhook(account, "whk_custom_1")
	if err != nil || found {
		t.Fatalf("expected not found after delete, got found=%v, err=%v", found, err)
	}
}
