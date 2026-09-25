package api

import (
	"bytes"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"path/filepath"
	"testing"

	"swarm/packages/swarmd/internal/identity"
	"swarm/packages/swarmd/internal/notification"
	pebblestore "swarm/packages/swarmd/internal/store/pebble"
)

func TestHandleNotificationInbox(t *testing.T) {
	tempDir := t.TempDir()
	store, err := pebblestore.Open(filepath.Join(tempDir, "pebble"))
	if err != nil {
		t.Fatalf("failed to open pebble store: %v", err)
	}
	defer store.Close()

	notifStore := pebblestore.NewNotificationStore(store)
	notifSvc := notification.NewService(notifStore, nil, nil)
	notifSvc.SetLocalSwarmIDResolver(func() string { return "test-swarm" })

	server := &Server{
		notifications: notifSvc,
	}

	mux := http.NewServeMux()
	mux.HandleFunc("/v1/notifications/inbox", server.handleNotifications)

	// 1. Without auth -> 401
	body, _ := json.Marshal(map[string]any{"title": "Test notification"})
	req := httptest.NewRequest(http.MethodPost, "/v1/notifications/inbox", bytes.NewReader(body))
	req.Header.Set("Content-Type", "application/json")
	w := httptest.NewRecorder()
	mux.ServeHTTP(w, req)
	if w.Code != http.StatusUnauthorized {
		t.Fatalf("expected status 401 without auth, got %d", w.Code)
	}

	// 2. With principal auth -> 201 Created
	principal := identity.Principal{
		Type:           "user",
		UserID:         "user-1",
		AccountScopeID: "acct-1",
	}
	ctx := identity.ContextWithPrincipal(req.Context(), principal)
	req = req.WithContext(ctx)

	payload := map[string]any{
		"title": "Social Campaign Drafted",
		"body":  "5 posts and 1 video ready",
		"kind":  "ai_deliverable",
		"payload": map[string]any{
			"media_url": "s3://vault/video.mp4",
		},
		"actions": []map[string]any{
			{
				"id":          "approve",
				"label":       "Approve & Schedule",
				"action_type": "publish",
				"variant":     "primary",
			},
		},
	}
	body, _ = json.Marshal(payload)
	req = httptest.NewRequest(http.MethodPost, "/v1/notifications/inbox", bytes.NewReader(body))
	req.Header.Set("Content-Type", "application/json")
	req = req.WithContext(ctx)
	w = httptest.NewRecorder()
	mux.ServeHTTP(w, req)

	if w.Code != http.StatusCreated {
		t.Fatalf("expected status 201, got %d: %s", w.Code, w.Body.String())
	}

	var resp struct {
		OK           bool                         `json:"ok"`
		Notification pebblestore.NotificationRecord `json:"notification"`
	}
	if err := json.Unmarshal(w.Body.Bytes(), &resp); err != nil {
		t.Fatalf("failed to decode response: %v", err)
	}
	if !resp.OK {
		t.Fatal("expected ok: true")
	}
	if resp.Notification.Kind != "ai_deliverable" {
		t.Fatalf("expected kind ai_deliverable, got %q", resp.Notification.Kind)
	}
	if len(resp.Notification.Actions) != 1 {
		t.Fatalf("expected 1 action, got %d", len(resp.Notification.Actions))
	}
	if resp.Notification.Actions[0].ID != "approve" {
		t.Fatalf("expected action ID approve, got %q", resp.Notification.Actions[0].ID)
	}
}
