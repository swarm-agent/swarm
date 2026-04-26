package app

import (
	"context"
	"fmt"
	"strings"
	"time"

	"swarm-refactor/swarmtui/internal/client"
	"swarm-refactor/swarmtui/internal/ui"
	"swarm-refactor/swarmtui/pkg/startupconfig"
)

const (
	defaultThemeID         = "nord"
	defaultSwarmingTitle   = "Swarming"
	defaultSwarmingStatus  = "swarming"
	defaultSwarmName       = "Local"
	bootstrapRoleMaster    = "master"
	bootstrapRoleChild     = "child"
	settingsBackendLabel   = "daemon"
	uiSettingsRequestLimit = 6 * time.Second
)

type AppConfig struct {
	Chat     ChatConfig
	Input    InputConfig
	UI       UIConfig
	Swarming SwarmingConfig
	Swarm    SwarmConfig
	Updates  UpdateConfig
	Startup  StartupConfig
}

// StartupConfig holds the subset of swarm.conf that the TUI needs for
// feature visibility. Keep these values read-only here; mutations should go
// through startupconfig helpers.
type StartupConfig struct {
	DevMode bool
}

type ChatConfig struct {
	ShowHeader            bool
	ThinkingTags          bool
	DefaultNewSessionMode string
	ToolStream            ChatToolStreamConfig
}

type ChatToolStreamConfig struct {
	ShowAnchor    bool
	PulseFrames   []string
	RunningSymbol string
	SuccessSymbol string
	ErrorSymbol   string
}

type InputConfig struct {
	MouseEnabled bool
	Keybinds     map[string]string
}

type UIConfig struct {
	Theme        string
	CustomThemes []CustomThemeConfig
}

type SwarmingConfig struct {
	Title  string
	Status string
}

// SwarmConfig is the machine/device identity branch.
// Keep this separate from SwarmingConfig:
// - SwarmingConfig drives the activity indicator (for example, the live "Swarming" run label).
// - SwarmConfig stores the user-editable machine name used by /swarm and desktop identity UI.
// This separation is intentional so future AI edits do not conflate run-state copy with machine identity.
type SwarmConfig struct {
	Name             string
	Role             string
	RemoteSSHTargets []string
}

type UpdateConfig struct {
	LocalContainerWarningDismissed bool
}

type CustomThemeConfig struct {
	ID      string
	Name    string
	Palette ui.ThemePalette
}

func defaultAppConfig() AppConfig {
	return AppConfig{
		Chat: ChatConfig{
			ShowHeader:            true,
			ThinkingTags:          true,
			DefaultNewSessionMode: "auto",
			ToolStream: ChatToolStreamConfig{
				ShowAnchor:    true,
				PulseFrames:   []string{"·", "•", "◦", "•"},
				RunningSymbol: "•",
				SuccessSymbol: "✓",
				ErrorSymbol:   "✕",
			},
		},
		Input: InputConfig{
			MouseEnabled: false,
			Keybinds:     nil,
		},
		UI: UIConfig{
			Theme:        defaultThemeID,
			CustomThemes: nil,
		},
		Swarming: SwarmingConfig{
			Title:  defaultSwarmingTitle,
			Status: defaultSwarmingStatus,
		},
		Swarm: SwarmConfig{
			Name: defaultSwarmName,
			Role: bootstrapRoleMaster,
		},
	}
}

func loadAppConfig(api *client.API) (AppConfig, error) {
	cfg := defaultAppConfig()
	if api == nil {
		return cfg, fmt.Errorf("ui settings client not configured")
	}
	ctx, cancel := context.WithTimeout(context.Background(), uiSettingsRequestLimit)
	defer cancel()
	if strings.TrimSpace(api.Token()) == "" {
		if err := api.EnsureLocalAuth(ctx); err != nil {
			return cfg, fmt.Errorf("bootstrap ui settings auth: %w", err)
		}
	}
	settings, err := api.GetUISettings(ctx)
	if err != nil {
		return cfg, fmt.Errorf("load ui settings: %w", err)
	}
	cfg = appConfigFromUISettings(settings)

	startupCfg, err := loadStartupConfigForApp()
	if err == nil {
		cfg.Swarm.Role = startupConfigRole(startupCfg)
		cfg.Startup.DevMode = startupCfg.DevMode
	}
	return cfg, nil
}

func saveAppConfig(api *client.API, cfg AppConfig) error {
	if api == nil {
		return fmt.Errorf("ui settings client not configured")
	}
	ctx, cancel := context.WithTimeout(context.Background(), uiSettingsRequestLimit)
	defer cancel()
	if strings.TrimSpace(api.Token()) == "" {
		if err := api.EnsureLocalAuth(ctx); err != nil {
			return fmt.Errorf("bootstrap ui settings auth: %w", err)
		}
	}
	_, err := api.UpdateUISettings(ctx, uiSettingsFromAppConfig(cfg))
	if err != nil {
		return fmt.Errorf("persist ui settings: %w", err)
	}
	return nil
}

func appConfigFromUISettings(settings client.UISettings) AppConfig {
	cfg := defaultAppConfig()

	themeID := strings.TrimSpace(settings.Theme.ActiveID)
	if themeID != "" {
		cfg.UI.Theme = themeID
	}
	if len(settings.Theme.CustomThemes) > 0 {
		cfg.UI.CustomThemes = make([]CustomThemeConfig, 0, len(settings.Theme.CustomThemes))
		for _, item := range settings.Theme.CustomThemes {
			id := strings.TrimSpace(item.ID)
			if id == "" {
				continue
			}
			cfg.UI.CustomThemes = append(cfg.UI.CustomThemes, CustomThemeConfig{
				ID:      id,
				Name:    strings.TrimSpace(item.Name),
				Palette: appThemePaletteFromClient(item.Palette),
			})
		}
	}

	cfg.Input.MouseEnabled = settings.Input.MouseEnabled
	cfg.Input.Keybinds = sanitizeConfigKeybindMap(settings.Input.Keybinds)

	cfg.Chat.ShowHeader = settings.Chat.ShowHeader
	cfg.Chat.ThinkingTags = settings.Chat.ThinkingTags
	cfg.Chat.DefaultNewSessionMode = emptyFallback(strings.TrimSpace(settings.Chat.DefaultNewSessionMode), "auto")
	cfg.Chat.ToolStream.ShowAnchor = settings.Chat.ToolStream.ShowAnchor
	if frames := sanitizeConfigPulseFrames(settings.Chat.ToolStream.PulseFrames); len(frames) > 0 {
		cfg.Chat.ToolStream.PulseFrames = frames
	}
	cfg.Chat.ToolStream.RunningSymbol = emptyFallback(strings.TrimSpace(settings.Chat.ToolStream.RunningSymbol), cfg.Chat.ToolStream.RunningSymbol)
	cfg.Chat.ToolStream.SuccessSymbol = emptyFallback(strings.TrimSpace(settings.Chat.ToolStream.SuccessSymbol), cfg.Chat.ToolStream.SuccessSymbol)
	cfg.Chat.ToolStream.ErrorSymbol = emptyFallback(strings.TrimSpace(settings.Chat.ToolStream.ErrorSymbol), cfg.Chat.ToolStream.ErrorSymbol)

	cfg.Swarming.Title = emptyFallback(strings.TrimSpace(settings.Swarming.Title), defaultSwarmingTitle)
	cfg.Swarming.Status = emptyFallback(strings.TrimSpace(settings.Swarming.Status), defaultSwarmingStatus)
	cfg.Swarm.Name = emptyFallback(strings.TrimSpace(settings.Swarm.Name), defaultSwarmName)
	cfg.Swarm.RemoteSSHTargets = append([]string(nil), settings.Swarm.RemoteSSHTargets...)
	cfg.Swarm.Role = bootstrapRoleMaster
	cfg.Updates.LocalContainerWarningDismissed = settings.Updates.LocalContainerWarningDismissed
	return cfg
}

func uiSettingsFromAppConfig(cfg AppConfig) client.UISettings {
	theme := strings.TrimSpace(cfg.UI.Theme)
	if theme == "" {
		theme = defaultThemeID
	}
	out := client.UISettings{
		Theme: client.UIThemeSettings{
			ActiveID:     theme,
			CustomThemes: make([]client.UIThemeCustomTheme, 0, len(cfg.UI.CustomThemes)),
		},
		Input: client.UIInputSettings{
			MouseEnabled: cfg.Input.MouseEnabled,
			Keybinds:     sanitizeConfigKeybindMap(cfg.Input.Keybinds),
		},
		Chat: client.UIChatSettings{
			ShowHeader:            cfg.Chat.ShowHeader,
			ThinkingTags:          cfg.Chat.ThinkingTags,
			DefaultNewSessionMode: emptyFallback(strings.TrimSpace(cfg.Chat.DefaultNewSessionMode), "auto"),
			ToolStream: client.UIChatToolStreamSettings{
				ShowAnchor:    cfg.Chat.ToolStream.ShowAnchor,
				PulseFrames:   sanitizeConfigPulseFrames(cfg.Chat.ToolStream.PulseFrames),
				RunningSymbol: strings.TrimSpace(cfg.Chat.ToolStream.RunningSymbol),
				SuccessSymbol: strings.TrimSpace(cfg.Chat.ToolStream.SuccessSymbol),
				ErrorSymbol:   strings.TrimSpace(cfg.Chat.ToolStream.ErrorSymbol),
			},
		},
		Swarming: client.UISwarmingSettings{
			Title:  emptyFallback(strings.TrimSpace(cfg.Swarming.Title), defaultSwarmingTitle),
			Status: emptyFallback(strings.TrimSpace(cfg.Swarming.Status), defaultSwarmingStatus),
		},
		Swarm: client.UISwarmSettings{
			Name:             emptyFallback(strings.TrimSpace(cfg.Swarm.Name), defaultSwarmName),
			RemoteSSHTargets: append([]string(nil), cfg.Swarm.RemoteSSHTargets...),
		},
		Updates: client.UIUpdateSettings{
			LocalContainerWarningDismissed: cfg.Updates.LocalContainerWarningDismissed,
		},
	}
	for _, item := range cfg.UI.CustomThemes {
		id := strings.TrimSpace(item.ID)
		if id == "" {
			continue
		}
		out.Theme.CustomThemes = append(out.Theme.CustomThemes, client.UIThemeCustomTheme{
			ID:      id,
			Name:    strings.TrimSpace(item.Name),
			Palette: clientThemePaletteFromApp(item.Palette),
		})
	}
	return out
}

func loadStartupConfigForApp() (startupconfig.FileConfig, error) {
	path, err := startupconfig.ResolvePath()
	if err != nil {
		return startupconfig.FileConfig{}, fmt.Errorf("resolve startup config path: %w", err)
	}
	cfg, err := startupconfig.Load(path)
	if err != nil {
		return startupconfig.FileConfig{}, fmt.Errorf("load startup config: %w", err)
	}
	if !cfg.Exists {
		cfg = startupconfig.Default(path)
	}
	return cfg, nil
}

func saveStartupSwarmRole(role string) error {
	cfg, err := loadStartupConfigForApp()
	if err != nil {
		return fmt.Errorf("persist startup swarm role: %w", err)
	}
	role = strings.ToLower(strings.TrimSpace(role))
	switch role {
	case bootstrapRoleChild:
		cfg.Child = true
	default:
		cfg.Child = false
	}
	if err := startupconfig.Write(cfg); err != nil {
		return fmt.Errorf("persist startup swarm role: %w", err)
	}
	return nil
}

func appThemePaletteFromClient(p client.UIThemePalette) ui.ThemePalette {
	return ui.ThemePalette{
		Background:     p.Background,
		Panel:          p.Panel,
		Element:        p.Element,
		Border:         p.Border,
		BorderActive:   p.BorderActive,
		Text:           p.Text,
		TextMuted:      p.TextMuted,
		Primary:        p.Primary,
		Secondary:      p.Secondary,
		Accent:         p.Accent,
		Success:        p.Success,
		Warning:        p.Warning,
		Error:          p.Error,
		Prompt:         p.Prompt,
		PromptCursorBG: p.PromptCursorBG,
		PromptCursorFG: p.PromptCursorFG,
		CodeBackground: p.CodeBackground,
		CodeText:       p.CodeText,
		CodeKeyword:    p.CodeKeyword,
		CodeType:       p.CodeType,
		CodeString:     p.CodeString,
		CodeNumber:     p.CodeNumber,
		CodeComment:    p.CodeComment,
		CodeFunction:   p.CodeFunction,
		CodeOperator:   p.CodeOperator,
	}
}

func clientThemePaletteFromApp(p ui.ThemePalette) client.UIThemePalette {
	return client.UIThemePalette{
		Background:     p.Background,
		Panel:          p.Panel,
		Element:        p.Element,
		Border:         p.Border,
		BorderActive:   p.BorderActive,
		Text:           p.Text,
		TextMuted:      p.TextMuted,
		Primary:        p.Primary,
		Secondary:      p.Secondary,
		Accent:         p.Accent,
		Success:        p.Success,
		Warning:        p.Warning,
		Error:          p.Error,
		Prompt:         p.Prompt,
		PromptCursorBG: p.PromptCursorBG,
		PromptCursorFG: p.PromptCursorFG,
		CodeBackground: p.CodeBackground,
		CodeText:       p.CodeText,
		CodeKeyword:    p.CodeKeyword,
		CodeType:       p.CodeType,
		CodeString:     p.CodeString,
		CodeNumber:     p.CodeNumber,
		CodeComment:    p.CodeComment,
		CodeFunction:   p.CodeFunction,
		CodeOperator:   p.CodeOperator,
	}
}

func startupConfigRole(cfg startupconfig.FileConfig) string {
	if cfg.Child {
		return bootstrapRoleChild
	}
	return bootstrapRoleMaster
}

func boolPtr(value bool) *bool {
	v := value
	return &v
}

func stringPtr(value string) *string {
	v := value
	return &v
}

func sanitizeConfigPulseFrames(frames []string) []string {
	if len(frames) == 0 {
		return nil
	}
	out := make([]string, 0, len(frames))
	for _, frame := range frames {
		frame = strings.TrimSpace(frame)
		if frame == "" {
			continue
		}
		out = append(out, frame)
		if len(out) >= 12 {
			break
		}
	}
	if len(out) == 0 {
		return nil
	}
	return out
}

func sanitizeConfigKeybindMap(raw map[string]string) map[string]string {
	if len(raw) == 0 {
		return nil
	}
	out := make(map[string]string, len(raw))
	for key, value := range raw {
		key = strings.TrimSpace(key)
		value = strings.TrimSpace(value)
		if key == "" {
			continue
		}
		out[key] = value
	}
	if len(out) == 0 {
		return nil
	}
	return out
}
