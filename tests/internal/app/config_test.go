package app

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"testing"

	"swarm-refactor/swarmtui/internal/client"
	"swarm-refactor/swarmtui/internal/ui"
)

func testUISettings() client.UISettings {
	return client.UISettings{
		Theme: client.UIThemeSettings{ActiveID: "nord"},
		Input: client.UIInputSettings{MouseEnabled: false},
		Chat: client.UIChatSettings{
			ShowHeader:   true,
			ThinkingTags: true,
			ToolStream: client.UIChatToolStreamSettings{
				ShowAnchor:    true,
				PulseFrames:   []string{"·", "•", "◦", "•"},
				RunningSymbol: "•",
				SuccessSymbol: "✓",
				ErrorSymbol:   "✕",
			},
		},
		Swarming: client.UISwarmingSettings{Title: "Swarming", Status: "swarming"},
	}
}

func TestLoadAppConfig_FromDaemon(t *testing.T) {
	settings := testUISettings()
	settings.Chat.ShowHeader = false
	settings.Input.MouseEnabled = true
	settings.Theme.ActiveID = "crimson"

	h := http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch r.URL.Path {
		case "/v1/ui/settings":
			_ = json.NewEncoder(w).Encode(settings)
		default:
			w.WriteHeader(http.StatusNotFound)
		}
	})
	server := httptest.NewServer(h)
	defer server.Close()

	cfg, err := loadAppConfig(client.New(server.URL))
	if err != nil {
		t.Fatalf("loadAppConfig() error = %v", err)
	}
	if cfg.Chat.ShowHeader {
		t.Fatalf("cfg.Chat.ShowHeader = %v, want false", cfg.Chat.ShowHeader)
	}
	if !cfg.Input.MouseEnabled {
		t.Fatalf("cfg.Input.MouseEnabled = %v, want true", cfg.Input.MouseEnabled)
	}
	if cfg.UI.Theme != "crimson" {
		t.Fatalf("cfg.UI.Theme = %q, want crimson", cfg.UI.Theme)
	}
}

func TestSaveAppConfig_ToDaemon(t *testing.T) {
	var saved client.UISettings
	h := http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch r.URL.Path {
		case "/v1/ui/settings":
			if r.Method != http.MethodPost {
				w.WriteHeader(http.StatusMethodNotAllowed)
				return
			}
			if err := json.NewDecoder(r.Body).Decode(&saved); err != nil {
				w.WriteHeader(http.StatusBadRequest)
				_ = json.NewEncoder(w).Encode(map[string]any{"error": err.Error()})
				return
			}
			_ = json.NewEncoder(w).Encode(saved)
		default:
			w.WriteHeader(http.StatusNotFound)
		}
	})
	server := httptest.NewServer(h)
	defer server.Close()

	cfg := defaultAppConfig()
	cfg.Chat.ShowHeader = false
	cfg.Chat.ThinkingTags = false
	cfg.Input.MouseEnabled = true
	cfg.Input.Keybinds = map[string]string{"global.quit": "ctrl+q"}
	cfg.UI.Theme = "crimson"
	cfg.UI.CustomThemes = []CustomThemeConfig{{
		ID:   "my-theme",
		Name: "My Theme",
		Palette: ui.ThemePalette{
			Background: "#101010",
			Text:       "#F0F0F0",
			Accent:     "#44CCAA",
		},
	}}
	cfg.Swarming.Title = "Agents"
	cfg.Swarming.Status = "agents"

	if err := saveAppConfig(client.New(server.URL), cfg); err != nil {
		t.Fatalf("saveAppConfig() error = %v", err)
	}
	if saved.Theme.ActiveID != "crimson" {
		t.Fatalf("saved.Theme.ActiveID = %q, want crimson", saved.Theme.ActiveID)
	}
	if !saved.Input.MouseEnabled {
		t.Fatalf("saved.Input.MouseEnabled = %v, want true", saved.Input.MouseEnabled)
	}
	if saved.Chat.ShowHeader {
		t.Fatalf("saved.Chat.ShowHeader = %v, want false", saved.Chat.ShowHeader)
	}
	if got := saved.Input.Keybinds["global.quit"]; got != "ctrl+q" {
		t.Fatalf("saved.Input.Keybinds[global.quit] = %q, want ctrl+q", got)
	}
	if len(saved.Theme.CustomThemes) != 1 {
		t.Fatalf("len(saved.Theme.CustomThemes) = %d, want 1", len(saved.Theme.CustomThemes))
	}
	if saved.Swarming.Title != "Agents" || saved.Swarming.Status != "agents" {
		t.Fatalf("saved.Swarming = %+v, want Agents/agents", saved.Swarming)
	}
}

func TestAppConfigFromUISettings_Defaults(t *testing.T) {
	cfg := appConfigFromUISettings(client.UISettings{})
	if cfg.UI.Theme != defaultThemeID {
		t.Fatalf("cfg.UI.Theme = %q, want %q", cfg.UI.Theme, defaultThemeID)
	}
	if cfg.Swarming.Title != defaultSwarmingTitle || cfg.Swarming.Status != defaultSwarmingStatus {
		t.Fatalf("cfg.Swarming = %+v", cfg.Swarming)
	}
}

func TestUISettingsFromAppConfig_CustomThemePalette(t *testing.T) {
	cfg := defaultAppConfig()
	cfg.UI.CustomThemes = []CustomThemeConfig{{
		ID:   "my-theme",
		Name: "My Theme",
		Palette: ui.ThemePalette{
			Background: "#101010",
			Text:       "#F0F0F0",
			Accent:     "#44CCAA",
		},
	}}
	settings := uiSettingsFromAppConfig(cfg)
	if len(settings.Theme.CustomThemes) != 1 {
		t.Fatalf("len(settings.Theme.CustomThemes) = %d, want 1", len(settings.Theme.CustomThemes))
	}
	if settings.Theme.CustomThemes[0].Palette.Accent != "#44CCAA" {
		t.Fatalf("accent = %q, want #44CCAA", settings.Theme.CustomThemes[0].Palette.Accent)
	}
}

func TestLoadAppConfig_ErrorWithoutClient(t *testing.T) {
	_, err := loadAppConfig(nil)
	if err == nil {
		t.Fatal("expected error")
	}
}

func TestSaveAppConfig_ErrorWithoutClient(t *testing.T) {
	err := saveAppConfig(nil, defaultAppConfig())
	if err == nil {
		t.Fatal("expected error")
	}
}

func TestLoadAppConfig_ContextUsesDefaultTimeoutPath(t *testing.T) {
	api := client.New("http://127.0.0.1:1")
	_, err := loadAppConfig(api)
	if err == nil {
		t.Fatal("expected error")
	}
}

func TestAPIUISettingsRoundTripHelpersCompile(t *testing.T) {
	api := client.New("http://127.0.0.1:1")
	_, _ = api.GetUISettings(context.Background())
	_, _ = api.UpdateUISettings(context.Background(), testUISettings())
	_ = fmt.Sprintf("%T", api)
}
