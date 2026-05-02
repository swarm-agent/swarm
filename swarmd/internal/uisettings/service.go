package uisettings

import (
	"encoding/json"
	"fmt"
	"strings"

	pebblestore "swarm/packages/swarmd/internal/store/pebble"
)

type ThemePalette struct {
	Background     string `json:"background,omitempty"`
	Panel          string `json:"panel,omitempty"`
	Element        string `json:"element,omitempty"`
	Border         string `json:"border,omitempty"`
	BorderActive   string `json:"border_active,omitempty"`
	Text           string `json:"text,omitempty"`
	TextMuted      string `json:"text_muted,omitempty"`
	Primary        string `json:"primary,omitempty"`
	Secondary      string `json:"secondary,omitempty"`
	Accent         string `json:"accent,omitempty"`
	Success        string `json:"success,omitempty"`
	Warning        string `json:"warning,omitempty"`
	Error          string `json:"error,omitempty"`
	Prompt         string `json:"prompt,omitempty"`
	PromptCursorBG string `json:"prompt_cursor_bg,omitempty"`
	PromptCursorFG string `json:"prompt_cursor_fg,omitempty"`
	CodeBackground string `json:"code_background,omitempty"`
	CodeText       string `json:"code_text,omitempty"`
	CodeKeyword    string `json:"code_keyword,omitempty"`
	CodeType       string `json:"code_type,omitempty"`
	CodeString     string `json:"code_string,omitempty"`
	CodeNumber     string `json:"code_number,omitempty"`
	CodeComment    string `json:"code_comment,omitempty"`
	CodeFunction   string `json:"code_function,omitempty"`
	CodeOperator   string `json:"code_operator,omitempty"`
}

type ThemeCustomTheme struct {
	ID      string       `json:"id"`
	Name    string       `json:"name,omitempty"`
	Palette ThemePalette `json:"palette,omitempty"`
}

type ThemeSettings struct {
	ActiveID     string             `json:"active_id"`
	CustomThemes []ThemeCustomTheme `json:"custom_themes,omitempty"`
}

type InputSettings struct {
	MouseEnabled bool              `json:"mouse_enabled"`
	Keybinds     map[string]string `json:"keybinds,omitempty"`
}

type ChatToolStreamSettings struct {
	ShowAnchor    bool     `json:"show_anchor"`
	PulseFrames   []string `json:"pulse_frames,omitempty"`
	RunningSymbol string   `json:"running_symbol,omitempty"`
	SuccessSymbol string   `json:"success_symbol,omitempty"`
	ErrorSymbol   string   `json:"error_symbol,omitempty"`
}

type ChatSettings struct {
	ShowHeader            bool                   `json:"show_header"`
	ThinkingTags          bool                   `json:"thinking_tags"`
	DefaultNewSessionMode string                 `json:"default_new_session_mode,omitempty"`
	ToolStream            ChatToolStreamSettings `json:"tool_stream,omitempty"`
}

type SwarmingSettings struct {
	Title  string `json:"title,omitempty"`
	Status string `json:"status,omitempty"`
}

// SwarmSettings is machine/device identity only.
// Keep this separate from SwarmingSettings:
// - SwarmingSettings controls activity-indicator copy.
// - SwarmSettings stores the user-editable machine name surfaced by /swarm and desktop settings.
// Future edits should preserve this split.
type SwarmSettings struct {
	Name             string   `json:"name,omitempty"`
	RemoteSSHTargets []string `json:"remote_ssh_targets,omitempty"`
}

type UpdateSettings struct {
	LocalContainerWarningDismissed bool `json:"local_container_warning_dismissed,omitempty"`
}

type UISettings struct {
	Theme     ThemeSettings    `json:"theme,omitempty"`
	Input     InputSettings    `json:"input,omitempty"`
	Chat      ChatSettings     `json:"chat,omitempty"`
	Swarming  SwarmingSettings `json:"swarming,omitempty"`
	Swarm     SwarmSettings    `json:"swarm,omitempty"`
	Updates   UpdateSettings   `json:"updates,omitempty"`
	UpdatedAt int64            `json:"updated_at"`
}

type Service struct {
	store   *pebblestore.UISettingsStore
	events  *pebblestore.EventLog
	publish func(pebblestore.EventEnvelope)
}

func NewService(store *pebblestore.UISettingsStore) *Service {
	return &Service{store: store}
}

func (s *Service) SetEventPublisher(events *pebblestore.EventLog, publish func(pebblestore.EventEnvelope)) {
	if s == nil {
		return
	}
	s.events = events
	s.publish = publish
}

func (s *Service) Get() (UISettings, error) {
	if s == nil || s.store == nil {
		return UISettings{}, fmt.Errorf("ui settings service not configured")
	}
	record, ok, err := s.store.Get()
	if err != nil {
		return UISettings{}, fmt.Errorf("read ui settings: %w", err)
	}
	if !ok {
		return defaultUISettings(), nil
	}
	return uiSettingsFromRecord(record), nil
}

func (s *Service) Set(settings UISettings) (UISettings, error) {
	if s == nil || s.store == nil {
		return UISettings{}, fmt.Errorf("ui settings service not configured")
	}
	record, err := s.store.Update(pebblestore.UISettingsPatch{
		Theme:    themeRecordFromSettings(settings.Theme),
		Input:    inputRecordFromSettings(settings.Input),
		Chat:     chatRecordFromSettings(settings.Chat),
		Swarming: swarmingRecordFromSettings(settings.Swarming),
		Swarm:    swarmRecordFromSettings(settings.Swarm),
		Updates:  updateRecordFromSettings(settings.Updates),
	})
	if err != nil {
		return UISettings{}, fmt.Errorf("persist ui settings: %w", err)
	}
	saved := uiSettingsFromRecord(record)
	if err := s.publishUpdated(saved); err != nil {
		return UISettings{}, err
	}
	return saved, nil
}

func (s *Service) publishUpdated(settings UISettings) error {
	if s == nil || s.events == nil || s.publish == nil {
		return nil
	}
	payload, err := json.Marshal(settings)
	if err != nil {
		return fmt.Errorf("marshal ui settings event payload: %w", err)
	}
	env, err := s.events.Append("ui:settings", "ui.settings.updated", "ui_settings", payload, "", "")
	if err != nil {
		return fmt.Errorf("append ui settings event: %w", err)
	}
	s.publish(env)
	return nil
}

func defaultUISettings() UISettings {
	return uiSettingsFromRecord(pebblestore.DefaultUISettingsRecord())
}

func uiSettingsFromRecord(record pebblestore.UISettingsRecord) UISettings {
	record = pebblestore.NormalizeUISettingsRecordForExternal(record)
	out := UISettings{
		Theme: ThemeSettings{
			ActiveID:     strings.TrimSpace(record.Theme.ActiveID),
			CustomThemes: make([]ThemeCustomTheme, 0, len(record.Theme.CustomThemes)),
		},
		Input: InputSettings{
			MouseEnabled: record.Input.MouseEnabled,
			Keybinds:     cloneMap(record.Input.Keybinds),
		},
		Chat: ChatSettings{
			ShowHeader:            record.Chat.ShowHeader,
			ThinkingTags:          record.Chat.ThinkingTags,
			DefaultNewSessionMode: strings.TrimSpace(record.Chat.DefaultNewSessionMode),
			ToolStream: ChatToolStreamSettings{
				ShowAnchor:    record.Chat.ToolStream.ShowAnchor,
				PulseFrames:   append([]string(nil), record.Chat.ToolStream.PulseFrames...),
				RunningSymbol: strings.TrimSpace(record.Chat.ToolStream.RunningSymbol),
				SuccessSymbol: strings.TrimSpace(record.Chat.ToolStream.SuccessSymbol),
				ErrorSymbol:   strings.TrimSpace(record.Chat.ToolStream.ErrorSymbol),
			},
		},
		Swarming: SwarmingSettings{
			Title:  strings.TrimSpace(record.Swarming.Title),
			Status: strings.TrimSpace(record.Swarming.Status),
		},
		Swarm: SwarmSettings{
			Name:             strings.TrimSpace(record.Swarm.Name),
			RemoteSSHTargets: append([]string(nil), record.Swarm.RemoteSSHTargets...),
		},
		Updates: UpdateSettings{
			LocalContainerWarningDismissed: record.Updates.LocalContainerWarningDismissed,
		},
		UpdatedAt: record.UpdatedAt,
	}
	for _, item := range record.Theme.CustomThemes {
		id := strings.TrimSpace(item.ID)
		if id == "" {
			continue
		}
		out.Theme.CustomThemes = append(out.Theme.CustomThemes, ThemeCustomTheme{
			ID:      id,
			Name:    strings.TrimSpace(item.Name),
			Palette: paletteFromRecord(item.Palette),
		})
	}
	return out
}

func themeRecordFromSettings(settings ThemeSettings) *pebblestore.UIThemeSettingsRecord {
	out := &pebblestore.UIThemeSettingsRecord{
		ActiveID:     strings.TrimSpace(settings.ActiveID),
		CustomThemes: make([]pebblestore.UIThemeCustomThemeRecord, 0, len(settings.CustomThemes)),
	}
	for _, item := range settings.CustomThemes {
		id := strings.TrimSpace(item.ID)
		if id == "" {
			continue
		}
		out.CustomThemes = append(out.CustomThemes, pebblestore.UIThemeCustomThemeRecord{
			ID:      id,
			Name:    strings.TrimSpace(item.Name),
			Palette: paletteRecordFromSettings(item.Palette),
		})
	}
	return out
}

func inputRecordFromSettings(settings InputSettings) *pebblestore.UIInputSettingsRecord {
	return &pebblestore.UIInputSettingsRecord{
		MouseEnabled: settings.MouseEnabled,
		Keybinds:     cloneMap(settings.Keybinds),
	}
}

func chatRecordFromSettings(settings ChatSettings) *pebblestore.UIChatSettingsRecord {
	return &pebblestore.UIChatSettingsRecord{
		ShowHeader:            settings.ShowHeader,
		ShowHeaderSet:         true,
		ThinkingTags:          settings.ThinkingTags,
		ThinkingTagsSet:       true,
		DefaultNewSessionMode: strings.TrimSpace(settings.DefaultNewSessionMode),
		ToolStream: pebblestore.UIChatToolStreamSettingsRecord{
			ShowAnchor:    settings.ToolStream.ShowAnchor,
			PulseFrames:   append([]string(nil), settings.ToolStream.PulseFrames...),
			RunningSymbol: strings.TrimSpace(settings.ToolStream.RunningSymbol),
			SuccessSymbol: strings.TrimSpace(settings.ToolStream.SuccessSymbol),
			ErrorSymbol:   strings.TrimSpace(settings.ToolStream.ErrorSymbol),
		},
	}
}

func swarmingRecordFromSettings(settings SwarmingSettings) *pebblestore.UISwarmingSettingsRecord {
	return &pebblestore.UISwarmingSettingsRecord{
		Title:  strings.TrimSpace(settings.Title),
		Status: strings.TrimSpace(settings.Status),
	}
}

func swarmRecordFromSettings(settings SwarmSettings) *pebblestore.UISwarmSettingsRecord {
	return &pebblestore.UISwarmSettingsRecord{
		Name:             strings.TrimSpace(settings.Name),
		RemoteSSHTargets: append([]string(nil), settings.RemoteSSHTargets...),
	}
}

func updateRecordFromSettings(settings UpdateSettings) *pebblestore.UIUpdateSettingsRecord {
	return &pebblestore.UIUpdateSettingsRecord{
		LocalContainerWarningDismissed: settings.LocalContainerWarningDismissed,
	}
}

func paletteFromRecord(record pebblestore.UIThemePaletteRecord) ThemePalette {
	return ThemePalette(record)
}

func paletteRecordFromSettings(settings ThemePalette) pebblestore.UIThemePaletteRecord {
	return pebblestore.UIThemePaletteRecord(settings)
}

func cloneMap(in map[string]string) map[string]string {
	if len(in) == 0 {
		return nil
	}
	out := make(map[string]string, len(in))
	for k, v := range in {
		k = strings.TrimSpace(k)
		if k == "" {
			continue
		}
		out[k] = strings.TrimSpace(v)
	}
	if len(out) == 0 {
		return nil
	}
	return out
}
