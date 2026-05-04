package api

import (
	"bytes"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"path/filepath"
	"testing"

	pebblestore "swarm/packages/swarmd/internal/store/pebble"
	"swarm/packages/swarmd/internal/stream"
	"swarm/packages/swarmd/internal/uisettings"
)

func TestUISettingsPostPreservesExistingThinkingTagsWhenChatOmitted(t *testing.T) {
	store, err := pebblestore.Open(filepath.Join(t.TempDir(), "ui-settings-api.pebble"))
	if err != nil {
		t.Fatalf("open store: %v", err)
	}
	defer func() { _ = store.Close() }()

	events, err := pebblestore.NewEventLog(store)
	if err != nil {
		t.Fatalf("new event log: %v", err)
	}
	hub := stream.NewHub(nil)
	settingsSvc := uisettings.NewService(pebblestore.NewUISettingsStore(store))
	settingsSvc.SetEventPublisher(events, hub.Publish)
	server := NewServer("test", nil, nil, nil, nil, nil, nil, nil, nil, nil, nil, nil, events, hub)
	server.SetUISettingsService(settingsSvc)

	saved, err := settingsSvc.Set(uisettings.UISettings{
		Chat: uisettings.ChatSettings{
			ShowHeader:            true,
			ThinkingTags:          false,
			DefaultNewSessionMode: "auto",
			ToolStream: uisettings.ChatToolStreamSettings{
				ShowAnchor: true,
			},
		},
		Theme: uisettings.ThemeSettings{ActiveID: "crimson"},
		Swarm: uisettings.SwarmSettings{Name: "Local"},
	})
	if err != nil {
		t.Fatalf("seed settings: %v", err)
	}
	if saved.Chat.ThinkingTags {
		t.Fatal("seed thinking tags = true, want false")
	}

	reqBody := []byte(`{"theme":{"active_id":"midnight"},"swarm":{"name":"Desk"}}`)
	req := httptest.NewRequest(http.MethodPost, "/v1/ui/settings", bytes.NewReader(reqBody))
	req.Header.Set("Content-Type", "application/json")
	rec := httptest.NewRecorder()
	server.Handler().ServeHTTP(rec, req)
	if rec.Code != http.StatusOK {
		t.Fatalf("POST /v1/ui/settings status = %d, want %d, body=%s", rec.Code, http.StatusOK, rec.Body.String())
	}

	var response uisettings.UISettings
	if err := json.Unmarshal(rec.Body.Bytes(), &response); err != nil {
		t.Fatalf("decode response: %v", err)
	}
	if response.Chat.ThinkingTags {
		t.Fatal("response thinking tags = true after chat-omitted update, want preserved false")
	}
	if !response.Chat.ShowHeader {
		t.Fatal("response show header = false after chat-omitted update, want preserved true")
	}
	if response.Theme.ActiveID != "midnight" {
		t.Fatalf("theme active id = %q, want midnight", response.Theme.ActiveID)
	}
	if response.Swarm.Name != "Desk" {
		t.Fatalf("swarm name = %q, want Desk", response.Swarm.Name)
	}
}

func TestUISettingsPostPreservesExistingThinkingTagsWhenThemeOnlyPayloadSent(t *testing.T) {
	store, err := pebblestore.Open(filepath.Join(t.TempDir(), "ui-settings-api-theme-only.pebble"))
	if err != nil {
		t.Fatalf("open store: %v", err)
	}
	defer func() { _ = store.Close() }()

	events, err := pebblestore.NewEventLog(store)
	if err != nil {
		t.Fatalf("new event log: %v", err)
	}
	hub := stream.NewHub(nil)
	settingsSvc := uisettings.NewService(pebblestore.NewUISettingsStore(store))
	settingsSvc.SetEventPublisher(events, hub.Publish)
	server := NewServer("test", nil, nil, nil, nil, nil, nil, nil, nil, nil, nil, nil, events, hub)
	server.SetUISettingsService(settingsSvc)

	_, err = settingsSvc.Set(uisettings.UISettings{
		Chat: uisettings.ChatSettings{
			ShowHeader:            true,
			ThinkingTags:          false,
			DefaultNewSessionMode: "auto",
			ToolStream:            uisettings.ChatToolStreamSettings{ShowAnchor: true},
		},
		Theme: uisettings.ThemeSettings{ActiveID: "crimson"},
		Swarm: uisettings.SwarmSettings{Name: "Local"},
	})
	if err != nil {
		t.Fatalf("seed settings: %v", err)
	}

	reqBody := []byte(`{"theme":{"active_id":"midnight"}}`)
	req := httptest.NewRequest(http.MethodPost, "/v1/ui/settings", bytes.NewReader(reqBody))
	req.Header.Set("Content-Type", "application/json")
	rec := httptest.NewRecorder()
	server.Handler().ServeHTTP(rec, req)
	if rec.Code != http.StatusOK {
		t.Fatalf("POST /v1/ui/settings status = %d, want %d, body=%s", rec.Code, http.StatusOK, rec.Body.String())
	}

	var response uisettings.UISettings
	if err := json.Unmarshal(rec.Body.Bytes(), &response); err != nil {
		t.Fatalf("decode response: %v", err)
	}
	if response.Chat.ThinkingTags {
		t.Fatal("response thinking tags = true after theme-only update, want preserved false")
	}
}
