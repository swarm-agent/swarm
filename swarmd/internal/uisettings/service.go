package uisettings

import (
	"encoding/json"
	"fmt"
	"strings"

	sharedtheme "swarm-refactor/swarmtui/theme"

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

type ThemeBuiltinTheme struct {
	ID      string       `json:"id"`
	Name    string       `json:"name"`
	Palette ThemePalette `json:"palette"`
}

type ThemeSettings struct {
	ActiveID       string              `json:"active_id"`
	DefaultThemeID string              `json:"default_theme_id,omitempty"`
	BuiltinThemes  []ThemeBuiltinTheme `json:"builtin_themes,omitempty"`
	CustomThemes   []ThemeCustomTheme  `json:"custom_themes,omitempty"`
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
	ShowHeader                      bool                   `json:"show_header"`
	ShowTips                        bool                   `json:"show_tips"`
	ThinkingTags                    bool                   `json:"thinking_tags"`
	ShowCompactButton               bool                   `json:"show_compact_button"`
	DefaultNewSessionMode           string                 `json:"default_new_session_mode,omitempty"`
	FollowupCheckpointPolicyDefault string                 `json:"followup_checkpoint_policy_default,omitempty"`
	PlanContextGuardEnabled         bool                   `json:"plan_context_guard_enabled"`
	PlanContextGuardUsedPercent     int                    `json:"plan_context_guard_used_percent"`
	PlanContextGuardMaxCompactions  int                    `json:"plan_context_guard_max_compactions"`
	TaskContextMaxCompactions       int                    `json:"task_context_max_compactions"`
	ReviewAutoArchiveMinutes        int                    `json:"review_auto_archive_minutes"`
	SidebarHideInactiveHours        int                    `json:"sidebar_hide_inactive_hours"`
	DefaultWorkspaceRoutes          map[string]string      `json:"default_workspace_routes,omitempty"`
	ToolStream                      ChatToolStreamSettings `json:"tool_stream,omitempty"`
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

type ToolImageSettings struct {
	DefaultModel string `json:"default_model,omitempty"`
}

type ToolSettings struct {
	Image ToolImageSettings `json:"image,omitempty"`
}

type UISettings struct {
	Theme     ThemeSettings    `json:"theme,omitempty"`
	Input     InputSettings    `json:"input,omitempty"`
	Chat      ChatSettings     `json:"chat,omitempty"`
	Swarming  SwarmingSettings `json:"swarming,omitempty"`
	Swarm     SwarmSettings    `json:"swarm,omitempty"`
	Tools     ToolSettings     `json:"tools,omitempty"`
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
	return s.GetForAccount("")
}

func (s *Service) GetForAccount(accountScopeID string) (UISettings, error) {
	if s == nil || s.store == nil {
		return UISettings{}, fmt.Errorf("ui settings service not configured")
	}
	record, ok, err := s.store.GetForAccount(strings.TrimSpace(accountScopeID))
	if err != nil {
		return UISettings{}, fmt.Errorf("read ui settings: %w", err)
	}
	if !ok {
		return defaultUISettings(), nil
	}
	return uiSettingsFromRecord(record), nil
}

func (s *Service) Set(settings UISettings) (UISettings, error) {
	return s.SetForAccount("", settings)
}

func (s *Service) SetForAccount(accountScopeID string, settings UISettings) (UISettings, error) {
	if s == nil || s.store == nil {
		return UISettings{}, fmt.Errorf("ui settings service not configured")
	}
	record, err := s.store.UpdateForAccount(strings.TrimSpace(accountScopeID), pebblestore.UISettingsPatch{
		Theme:    themeRecordFromSettings(settings.Theme),
		Input:    inputRecordFromSettings(settings.Input),
		Chat:     chatRecordFromSettings(settings.Chat),
		Swarming: swarmingRecordFromSettings(settings.Swarming),
		Swarm:    swarmRecordFromSettings(settings.Swarm),
		Tools:    toolRecordFromSettings(settings.Tools),
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
			ActiveID:       strings.TrimSpace(record.Theme.ActiveID),
			DefaultThemeID: sharedtheme.DefaultThemeID(),
			BuiltinThemes:  builtinThemeSettings(),
			CustomThemes:   make([]ThemeCustomTheme, 0, len(record.Theme.CustomThemes)),
		},
		Input: InputSettings{
			MouseEnabled: record.Input.MouseEnabled,
			Keybinds:     cloneMap(record.Input.Keybinds),
		},
		Chat: ChatSettings{
			ShowHeader:                      record.Chat.ShowHeader,
			ShowTips:                        *record.Chat.ShowTips,
			ThinkingTags:                    record.Chat.ThinkingTags,
			ShowCompactButton:               record.Chat.ShowCompactButton,
			DefaultNewSessionMode:           strings.TrimSpace(record.Chat.DefaultNewSessionMode),
			FollowupCheckpointPolicyDefault: strings.TrimSpace(record.Chat.FollowupCheckpointPolicyDefault),
			PlanContextGuardEnabled:         *record.Chat.PlanContextGuardEnabled,
			PlanContextGuardUsedPercent:     record.Chat.PlanContextGuardUsedPercent,
			PlanContextGuardMaxCompactions:  *record.Chat.PlanContextGuardMaxCompactions,
			TaskContextMaxCompactions:       *record.Chat.TaskContextMaxCompactions,
			ReviewAutoArchiveMinutes:        record.Chat.ReviewAutoArchiveMinutes,
			SidebarHideInactiveHours:        *record.Chat.SidebarHideInactiveHours,
			DefaultWorkspaceRoutes:          cloneMap(record.Chat.DefaultWorkspaceRoutes),
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
		Tools: ToolSettings{
			Image: ToolImageSettings{
				DefaultModel: strings.TrimSpace(record.Tools.Image.DefaultModel),
			},
		},
		UpdatedAt: record.UpdatedAt,
	}
	for _, item := range record.Theme.CustomThemes {
		option, err := sharedtheme.NewCustomThemeOption(
			strings.TrimSpace(item.ID),
			strings.TrimSpace(item.Name),
			sharedtheme.ThemePalette(paletteFromRecord(item.Palette)),
		)
		if err != nil {
			continue
		}
		out.Theme.CustomThemes = append(out.Theme.CustomThemes, ThemeCustomTheme{
			ID:      option.ID,
			Name:    option.Name,
			Palette: ThemePalette(option.Palette),
		})
	}
	return out
}

func builtinThemeSettings() []ThemeBuiltinTheme {
	catalog := sharedtheme.BuiltinThemeCatalog()
	out := make([]ThemeBuiltinTheme, 0, len(catalog))
	for _, item := range catalog {
		out = append(out, ThemeBuiltinTheme{
			ID:      item.ID,
			Name:    item.Name,
			Palette: ThemePalette(item.Palette),
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
		ShowHeader:                      settings.ShowHeader,
		ShowHeaderSet:                   true,
		ShowTips:                        boolPointer(settings.ShowTips),
		ThinkingTags:                    settings.ThinkingTags,
		ThinkingTagsSet:                 true,
		ShowCompactButton:               settings.ShowCompactButton,
		DefaultNewSessionMode:           strings.TrimSpace(settings.DefaultNewSessionMode),
		FollowupCheckpointPolicyDefault: strings.TrimSpace(settings.FollowupCheckpointPolicyDefault),
		PlanContextGuardEnabled:         boolPointer(settings.PlanContextGuardEnabled),
		PlanContextGuardUsedPercent:     settings.PlanContextGuardUsedPercent,
		PlanContextGuardMaxCompactions:  intPointer(settings.PlanContextGuardMaxCompactions),
		TaskContextMaxCompactions:       intPointer(settings.TaskContextMaxCompactions),
		ReviewAutoArchiveMinutes:        settings.ReviewAutoArchiveMinutes,
		SidebarHideInactiveHours:        intPointer(settings.SidebarHideInactiveHours),
		DefaultWorkspaceRoutes:          cloneMap(settings.DefaultWorkspaceRoutes),
		ToolStream: pebblestore.UIChatToolStreamSettingsRecord{
			ShowAnchor:    settings.ToolStream.ShowAnchor,
			PulseFrames:   append([]string(nil), settings.ToolStream.PulseFrames...),
			RunningSymbol: strings.TrimSpace(settings.ToolStream.RunningSymbol),
			SuccessSymbol: strings.TrimSpace(settings.ToolStream.SuccessSymbol),
			ErrorSymbol:   strings.TrimSpace(settings.ToolStream.ErrorSymbol),
		},
	}
}

func intPointer(value int) *int {
	return &value
}

func boolPointer(value bool) *bool {
	return &value
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

func toolRecordFromSettings(settings ToolSettings) *pebblestore.UIToolSettingsRecord {
	return &pebblestore.UIToolSettingsRecord{
		Image: pebblestore.UIToolImageSettingsRecord{
			DefaultModel: strings.TrimSpace(settings.Image.DefaultModel),
		},
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
