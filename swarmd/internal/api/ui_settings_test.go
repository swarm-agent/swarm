package api

import (
	"bytes"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"path/filepath"
	"testing"

	"swarm/packages/swarmd/internal/identity"
	pebblestore "swarm/packages/swarmd/internal/store/pebble"
	"swarm/packages/swarmd/internal/stream"
	swarmruntime "swarm/packages/swarmd/internal/swarm"
	"swarm/packages/swarmd/internal/uisettings"
)

func TestUISettingsPostPersistsCoderModelSettings(t *testing.T) {
	store, err := pebblestore.Open(filepath.Join(t.TempDir(), "ui-settings-coder-api.pebble"))
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
	server := NewServer(nil, nil, nil, nil, nil, nil, nil, nil, nil, nil, nil, events, hub)
	server.SetUISettingsService(settingsSvc)

	req := httptest.NewRequest(http.MethodPost, "/v1/ui/settings", bytes.NewReader([]byte(`{"agents":{"coder":{"provider":"CODEX","model":"gpt-5.6","thinking":"high","service_tier":"PRIORITY"}}}`)))
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
	if response.Agents.Coder.Provider != "codex" || response.Agents.Coder.Model != "gpt-5.6" || response.Agents.Coder.Thinking != "high" || response.Agents.Coder.ServiceTier != "priority" {
		t.Fatalf("response Coder settings = %#v", response.Agents.Coder)
	}
	stored, err := settingsSvc.Get()
	if err != nil {
		t.Fatalf("reload settings: %v", err)
	}
	if stored.Agents.Coder != response.Agents.Coder {
		t.Fatalf("stored Coder settings = %#v, want %#v", stored.Agents.Coder, response.Agents.Coder)
	}
}

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
	server := NewServer(nil, nil, nil, nil, nil, nil, nil, nil, nil, nil, nil, events, hub)
	server.SetUISettingsService(settingsSvc)
	swarmSvc := swarmruntime.NewService(pebblestore.NewSwarmStore(store), events, hub.Publish)
	localState, err := swarmSvc.EnsureLocalState(swarmruntime.EnsureLocalStateInput{Name: "Local", Role: "master"})
	if err != nil {
		t.Fatalf("ensure local swarm: %v", err)
	}
	server.SetSwarmService(swarmSvc)

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

	reqBody := []byte(`{"theme":{"active_id":"midnight"},"swarm":{"name":"Renamed Local"}}`)
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
	if response.Swarm.Name != "Renamed Local" {
		t.Fatalf("swarm name = %q, want Renamed Local", response.Swarm.Name)
	}
	renamedState, err := swarmSvc.EnsureLocalState(swarmruntime.EnsureLocalStateInput{Name: "Startup Config Name", Role: "master"})
	if err != nil {
		t.Fatalf("reload local swarm: %v", err)
	}
	if renamedState.Node.Name != "Renamed Local" {
		t.Fatalf("db swarm name = %q, want Renamed Local", renamedState.Node.Name)
	}
	if renamedState.Node.SwarmID != localState.Node.SwarmID {
		t.Fatalf("swarm id changed on rename: got %q want %q", renamedState.Node.SwarmID, localState.Node.SwarmID)
	}
}

func TestUISettingsPostPreservesThemeWhenUpdatesOnlyPayloadSent(t *testing.T) {
	store, err := pebblestore.Open(filepath.Join(t.TempDir(), "ui-settings-api-updates-only.pebble"))
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
	server := NewServer(nil, nil, nil, nil, nil, nil, nil, nil, nil, nil, nil, events, hub)
	server.SetUISettingsService(settingsSvc)

	_, err = settingsSvc.Set(uisettings.UISettings{
		Theme: uisettings.ThemeSettings{ActiveID: "midnight"},
		Chat: uisettings.ChatSettings{
			ShowHeader:            true,
			ThinkingTags:          false,
			DefaultNewSessionMode: "plan",
			ToolStream:            uisettings.ChatToolStreamSettings{ShowAnchor: true, RunningSymbol: "•"},
		},
		Swarm: uisettings.SwarmSettings{Name: "Local"},
	})
	if err != nil {
		t.Fatalf("seed settings: %v", err)
	}

	reqBody := []byte(`{"updates":{"local_container_warning_dismissed":true}}`)
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
	if response.Theme.ActiveID != "midnight" {
		t.Fatalf("theme active id = %q, want midnight", response.Theme.ActiveID)
	}
	if !response.Updates.LocalContainerWarningDismissed {
		t.Fatal("local container warning dismissed = false, want true")
	}
	if response.Swarm.Name != "Local" {
		t.Fatalf("swarm name = %q, want Local", response.Swarm.Name)
	}
	if response.Chat.DefaultNewSessionMode != "plan" || response.Chat.ThinkingTags {
		t.Fatalf("chat settings were not preserved: %+v", response.Chat)
	}
}

func TestUISettingsPostPersistsImageDefaultModel(t *testing.T) {
	store, err := pebblestore.Open(filepath.Join(t.TempDir(), "ui-settings-api-image-default.pebble"))
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
	server := NewServer(nil, nil, nil, nil, nil, nil, nil, nil, nil, nil, nil, events, hub)
	server.SetUISettingsService(settingsSvc)

	reqBody := []byte(`{"tools":{"image":{"default_model":"gemini-nano-banana-pro"}}}`)
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
	if response.Tools.Image.DefaultModel != "gemini-nano-banana-pro" {
		t.Fatalf("image default model = %q, want gemini-nano-banana-pro", response.Tools.Image.DefaultModel)
	}

	loaded, err := settingsSvc.Get()
	if err != nil {
		t.Fatalf("get settings: %v", err)
	}
	if loaded.Tools.Image.DefaultModel != "gemini-nano-banana-pro" {
		t.Fatalf("persisted image default model = %q, want gemini-nano-banana-pro", loaded.Tools.Image.DefaultModel)
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
	server := NewServer(nil, nil, nil, nil, nil, nil, nil, nil, nil, nil, nil, events, hub)
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

func TestUISettingsPostPersistsSidebarHideInactiveHours(t *testing.T) {
	store, err := pebblestore.Open(filepath.Join(t.TempDir(), "ui-settings-api-sidebar-hours.pebble"))
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
	server := NewServer(nil, nil, nil, nil, nil, nil, nil, nil, nil, nil, nil, events, hub)
	server.SetUISettingsService(settingsSvc)

	reqBody := []byte(`{"chat":{"sidebar_hide_inactive_hours":24}}`)
	req := httptest.NewRequest(http.MethodPost, "/v1/ui/settings", bytes.NewReader(reqBody))
	req = req.WithContext(identity.ContextWithPrincipal(req.Context(), identity.Principal{
		Type:           identity.PrincipalTypeUser,
		UserID:         "test-user",
		AccountScopeID: "test-account",
	}))
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
	if response.Chat.SidebarHideInactiveHours != 24 {
		t.Fatalf("sidebar hide inactive hours = %d, want 24", response.Chat.SidebarHideInactiveHours)
	}
	loaded, err := settingsSvc.GetForAccount("test-account")
	if err != nil {
		t.Fatalf("get settings: %v", err)
	}
	if loaded.Chat.SidebarHideInactiveHours != 24 {
		t.Fatalf("persisted sidebar hide inactive hours = %d, want 24", loaded.Chat.SidebarHideInactiveHours)
	}
}

func TestUISettingsPostPreservesExistingWorkspaceRoutesWhenChatPatchOmitsThem(t *testing.T) {
	store, err := pebblestore.Open(filepath.Join(t.TempDir(), "ui-settings-api-routes.pebble"))
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
	server := NewServer(nil, nil, nil, nil, nil, nil, nil, nil, nil, nil, nil, events, hub)
	server.SetUISettingsService(settingsSvc)

	_, err = settingsSvc.Set(uisettings.UISettings{
		Chat: uisettings.ChatSettings{
			ShowHeader:             true,
			ThinkingTags:           true,
			DefaultNewSessionMode:  "plan",
			DefaultWorkspaceRoutes: map[string]string{"/repo": "swarm:child:/repo"},
			ToolStream:             uisettings.ChatToolStreamSettings{ShowAnchor: true, RunningSymbol: "•"},
		},
		Theme: uisettings.ThemeSettings{ActiveID: "crimson"},
		Swarm: uisettings.SwarmSettings{Name: "Local"},
	})
	if err != nil {
		t.Fatalf("seed settings: %v", err)
	}

	reqBody := []byte(`{"chat":{"default_new_session_mode":"auto"}}`)
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
	if response.Chat.DefaultNewSessionMode != "auto" {
		t.Fatalf("default mode = %q, want auto", response.Chat.DefaultNewSessionMode)
	}
	if got := response.Chat.DefaultWorkspaceRoutes["/repo"]; got != "swarm:child:/repo" {
		t.Fatalf("workspace route = %q, want swarm:child:/repo", got)
	}
	if !response.Chat.ToolStream.ShowAnchor || response.Chat.ToolStream.RunningSymbol == "" {
		t.Fatalf("tool stream was not preserved: %+v", response.Chat.ToolStream)
	}
}
