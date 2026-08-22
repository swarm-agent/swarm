package api

import (
	"bytes"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"path/filepath"
	"testing"

	sharedtheme "swarm-refactor/swarmtui/theme"

	"swarm/packages/swarmd/internal/identity"
	pebblestore "swarm/packages/swarmd/internal/store/pebble"
	"swarm/packages/swarmd/internal/stream"
	swarmruntime "swarm/packages/swarmd/internal/swarm"
	"swarm/packages/swarmd/internal/uisettings"
)

func TestUISettingsGetIncludesCanonicalThemeCatalog(t *testing.T) {
	store, err := pebblestore.Open(filepath.Join(t.TempDir(), "ui-settings-theme-catalog.pebble"))
	if err != nil {
		t.Fatalf("open store: %v", err)
	}
	defer func() { _ = store.Close() }()

	settingsSvc := uisettings.NewService(pebblestore.NewUISettingsStore(store))
	server := NewServer(nil, nil, nil, nil, nil, nil, nil, nil, nil, nil, nil, nil, nil)
	server.SetUISettingsService(settingsSvc)

	req := httptest.NewRequest(http.MethodGet, "/v1/ui/settings", nil)
	rec := httptest.NewRecorder()
	server.Handler().ServeHTTP(rec, req)
	if rec.Code != http.StatusOK {
		t.Fatalf("GET /v1/ui/settings status = %d, want %d, body=%s", rec.Code, http.StatusOK, rec.Body.String())
	}

	var response uisettings.UISettings
	if err := json.Unmarshal(rec.Body.Bytes(), &response); err != nil {
		t.Fatalf("decode response: %v", err)
	}
	if response.Theme.DefaultThemeID != sharedtheme.DefaultThemeID() {
		t.Fatalf("default theme id = %q, want %q", response.Theme.DefaultThemeID, sharedtheme.DefaultThemeID())
	}
	catalog := sharedtheme.BuiltinThemeCatalog()
	if len(response.Theme.BuiltinThemes) != len(catalog) {
		t.Fatalf("builtin themes = %d, want %d", len(response.Theme.BuiltinThemes), len(catalog))
	}
	for index, item := range catalog {
		got := response.Theme.BuiltinThemes[index]
		if got.ID != item.ID || got.Name != item.Name || got.Palette != uisettings.ThemePalette(item.Palette) {
			t.Fatalf("builtin theme[%d] = %+v, want %+v", index, got, item)
		}
	}
	if _, ok := sharedtheme.ResolveBuiltinTheme("castor"); !ok {
		t.Fatal("canonical compatibility theme castor is unavailable")
	}
	foundCastor := false
	for _, item := range response.Theme.BuiltinThemes {
		if item.ID == "castor" {
			foundCastor = true
			break
		}
	}
	if !foundCastor {
		t.Fatal("ui settings response omitted canonical compatibility theme castor")
	}
}

func TestUISettingsPostRejectsAgentModelSettings(t *testing.T) {
	store, err := pebblestore.Open(filepath.Join(t.TempDir(), "ui-settings-designer-api.pebble"))
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

	req := httptest.NewRequest(http.MethodPost, "/v1/ui/settings", bytes.NewReader([]byte(`{"agents":{"designer":{"provider":"OPENAI","model":"utility-model","thinking":"medium","service_tier":"PRIORITY"}}}`)))
	req.Header.Set("Content-Type", "application/json")
	rec := httptest.NewRecorder()
	server.Handler().ServeHTTP(rec, req)
	if rec.Code != http.StatusBadRequest {
		t.Fatalf("POST /v1/ui/settings status = %d, want %d, body=%s", rec.Code, http.StatusBadRequest, rec.Body.String())
	}
}

func TestUISettingsPostRejectsRouterModelSettings(t *testing.T) {
	store, err := pebblestore.Open(filepath.Join(t.TempDir(), "ui-settings-router-api.pebble"))
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

	req := httptest.NewRequest(http.MethodPost, "/v1/ui/settings", bytes.NewReader([]byte(`{"agents":{"router":{"provider":"OPENAI","model":"router-model","thinking":"medium","service_tier":"PRIORITY"}}}`)))
	req.Header.Set("Content-Type", "application/json")
	rec := httptest.NewRecorder()
	server.Handler().ServeHTTP(rec, req)
	if rec.Code != http.StatusBadRequest {
		t.Fatalf("POST /v1/ui/settings status = %d, want %d, body=%s", rec.Code, http.StatusBadRequest, rec.Body.String())
	}
}

func TestUISettingsPostRejectsCoderModelSettings(t *testing.T) {
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
	if rec.Code != http.StatusBadRequest {
		t.Fatalf("POST /v1/ui/settings status = %d, want %d, body=%s", rec.Code, http.StatusBadRequest, rec.Body.String())
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

func TestUISettingsPostPatchesShowTipsWithoutOverwritingOtherSettings(t *testing.T) {
	store, err := pebblestore.Open(filepath.Join(t.TempDir(), "ui-settings-api-show-tips.pebble"))
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

	seed := uisettings.UISettings{
		Chat: uisettings.ChatSettings{
			ShowHeader:             true,
			ShowTips:               true,
			ThinkingTags:           false,
			DefaultNewSessionMode:  "plan",
			DefaultWorkspaceRoutes: map[string]string{"/repo": "swarm:self:/repo"},
			ToolStream:             uisettings.ChatToolStreamSettings{ShowAnchor: true, RunningSymbol: "•"},
		},
		Theme: uisettings.ThemeSettings{ActiveID: "crimson"},
	}
	if _, err := settingsSvc.SetForAccount("tips-account", seed); err != nil {
		t.Fatalf("seed settings: %v", err)
	}

	req := httptest.NewRequest(http.MethodPost, "/v1/ui/settings", bytes.NewReader([]byte(`{"chat":{"show_tips":false}}`)))
	req = req.WithContext(identity.ContextWithPrincipal(req.Context(), identity.Principal{
		Type: identity.PrincipalTypeUser, AccountScopeID: "tips-account",
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
	if response.Chat.ShowTips {
		t.Fatal("response show tips = true, want false")
	}
	if response.Chat.ThinkingTags || response.Chat.DefaultNewSessionMode != "plan" || response.Theme.ActiveID != "crimson" {
		t.Fatalf("partial patch overwrote settings: %+v", response)
	}
	if response.Chat.DefaultWorkspaceRoutes["/repo"] != "swarm:self:/repo" || !response.Chat.ToolStream.ShowAnchor {
		t.Fatalf("partial patch overwrote chat settings: %+v", response.Chat)
	}

	loaded, err := settingsSvc.GetForAccount("tips-account")
	if err != nil {
		t.Fatalf("get settings: %v", err)
	}
	if loaded.Chat.ShowTips {
		t.Fatal("persisted show tips = true, want false")
	}
}

func TestUISettingsPostPersistsMediaTranscriptionModel(t *testing.T) {
	store, err := pebblestore.Open(filepath.Join(t.TempDir(), "ui-settings-api-media-transcription.pebble"))
	if err != nil {
		t.Fatalf("open store: %v", err)
	}
	defer func() { _ = store.Close() }()

	settingsSvc := uisettings.NewService(pebblestore.NewUISettingsStore(store))
	server := NewServer(nil, nil, nil, nil, nil, nil, nil, nil, nil, nil, nil, nil, nil)
	server.SetUISettingsService(settingsSvc)

	req := httptest.NewRequest(http.MethodPost, "/v1/ui/settings", bytes.NewReader([]byte(`{"media":{"transcription_model":"gemini-2.5-flash"}}`)))
	req = req.WithContext(identity.ContextWithPrincipal(req.Context(), identity.Principal{Type: identity.PrincipalTypeUser, AccountScopeID: "media-account"}))
	req.Header.Set("Content-Type", "application/json")
	rec := httptest.NewRecorder()
	server.Handler().ServeHTTP(rec, req)
	if rec.Code != http.StatusOK {
		t.Fatalf("POST /v1/ui/settings status = %d, want %d, body=%s", rec.Code, http.StatusOK, rec.Body.String())
	}

	loaded, err := settingsSvc.GetForAccount("media-account")
	if err != nil {
		t.Fatalf("get settings: %v", err)
	}
	if loaded.Media.TranscriptionModel != "gemini-2.5-flash" {
		t.Fatalf("persisted transcription model = %q, want gemini-2.5-flash", loaded.Media.TranscriptionModel)
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

func TestUISettingsPlanContextGuardRoundTripNormalizesAndPreservesAccountScope(t *testing.T) {
	store, err := pebblestore.Open(filepath.Join(t.TempDir(), "ui-settings-api-plan-guard.pebble"))
	if err != nil {
		t.Fatalf("open store: %v", err)
	}
	t.Cleanup(func() { _ = store.Close() })
	events, err := pebblestore.NewEventLog(store)
	if err != nil {
		t.Fatalf("new event log: %v", err)
	}
	hub := stream.NewHub(nil)
	settingsSvc := uisettings.NewService(pebblestore.NewUISettingsStore(store))
	settingsSvc.SetEventPublisher(events, hub.Publish)
	server := NewServer(nil, nil, nil, nil, nil, nil, nil, nil, nil, nil, nil, events, hub)
	server.SetUISettingsService(settingsSvc)
	principal := identity.Principal{Type: identity.PrincipalTypeUser, UserID: "guard-user", AccountScopeID: "guard-account"}

	post := httptest.NewRequest(http.MethodPost, "/v1/ui/settings", bytes.NewReader([]byte(`{"chat":{"plan_context_guard_enabled":false,"plan_context_guard_used_percent":99,"plan_context_guard_max_compactions":9}}`)))
	post = post.WithContext(identity.ContextWithPrincipal(post.Context(), principal))
	post.Header.Set("Content-Type", "application/json")
	postRec := httptest.NewRecorder()
	server.Handler().ServeHTTP(postRec, post)
	if postRec.Code != http.StatusOK {
		t.Fatalf("POST /v1/ui/settings status = %d body=%s", postRec.Code, postRec.Body.String())
	}
	var posted uisettings.UISettings
	if err := json.Unmarshal(postRec.Body.Bytes(), &posted); err != nil {
		t.Fatalf("decode post response: %v", err)
	}
	if posted.Chat.PlanContextGuardEnabled || posted.Chat.PlanContextGuardUsedPercent != 95 || posted.Chat.PlanContextGuardMaxCompactions != 3 {
		t.Fatalf("normalized POST guard = %+v", posted.Chat)
	}

	get := httptest.NewRequest(http.MethodGet, "/v1/ui/settings", nil)
	get = get.WithContext(identity.ContextWithPrincipal(get.Context(), principal))
	getRec := httptest.NewRecorder()
	server.Handler().ServeHTTP(getRec, get)
	if getRec.Code != http.StatusOK {
		t.Fatalf("GET /v1/ui/settings status = %d body=%s", getRec.Code, getRec.Body.String())
	}
	var loaded uisettings.UISettings
	if err := json.Unmarshal(getRec.Body.Bytes(), &loaded); err != nil {
		t.Fatalf("decode get response: %v", err)
	}
	if loaded.Chat.PlanContextGuardEnabled || loaded.Chat.PlanContextGuardUsedPercent != 95 || loaded.Chat.PlanContextGuardMaxCompactions != 3 {
		t.Fatalf("round-tripped guard = %+v", loaded.Chat)
	}

	other, err := settingsSvc.GetForAccount("other-account")
	if err != nil {
		t.Fatalf("get other account: %v", err)
	}
	if !other.Chat.PlanContextGuardEnabled || other.Chat.PlanContextGuardUsedPercent != 80 || other.Chat.PlanContextGuardMaxCompactions != 1 {
		t.Fatalf("other account inherited guard settings: %+v", other.Chat)
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
