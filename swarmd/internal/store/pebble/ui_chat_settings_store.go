package pebblestore

import (
	"strings"
	"time"
)

type UIThemePaletteRecord struct {
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

type UIThemeCustomThemeRecord struct {
	ID      string               `json:"id"`
	Name    string               `json:"name,omitempty"`
	Palette UIThemePaletteRecord `json:"palette,omitempty"`
}

type UIThemeSettingsRecord struct {
	ActiveID     string                     `json:"active_id"`
	CustomThemes []UIThemeCustomThemeRecord `json:"custom_themes,omitempty"`
}

type UIInputSettingsRecord struct {
	MouseEnabled bool              `json:"mouse_enabled"`
	Keybinds     map[string]string `json:"keybinds,omitempty"`
}

type UIChatToolStreamSettingsRecord struct {
	ShowAnchor    bool     `json:"show_anchor"`
	PulseFrames   []string `json:"pulse_frames,omitempty"`
	RunningSymbol string   `json:"running_symbol,omitempty"`
	SuccessSymbol string   `json:"success_symbol,omitempty"`
	ErrorSymbol   string   `json:"error_symbol,omitempty"`
}

type UIChatSettingsRecord struct {
	ShowHeader                      bool                           `json:"show_header"`
	ShowHeaderSet                   bool                           `json:"-"`
	ThinkingTags                    bool                           `json:"thinking_tags"`
	ThinkingTagsSet                 bool                           `json:"-"`
	DefaultNewSessionMode           string                         `json:"default_new_session_mode,omitempty"`
	FollowupCheckpointPolicyDefault string                         `json:"followup_checkpoint_policy_default,omitempty"`
	SidebarHideInactiveHours        *int                           `json:"sidebar_hide_inactive_hours"`
	DefaultWorkspaceRoutes          map[string]string              `json:"default_workspace_routes,omitempty"`
	ToolStream                      UIChatToolStreamSettingsRecord `json:"tool_stream,omitempty"`
	UpdatedAt                       int64                          `json:"updated_at"`
}

type UISwarmingSettingsRecord struct {
	Title  string `json:"title,omitempty"`
	Status string `json:"status,omitempty"`
}

// UISwarmSettingsRecord stores machine/device identity only.
// Keep this separate from UISwarmingSettingsRecord:
// - UISwarmingSettingsRecord = activity indicator copy.
// - UISwarmSettingsRecord = persisted machine name for /swarm and desktop identity surfaces.
// This intentional separation prevents future refactors from conflating status text with machine identity.
type UISwarmSettingsRecord struct {
	Name             string   `json:"name,omitempty"`
	RemoteSSHTargets []string `json:"remote_ssh_targets,omitempty"`
}

type UIUpdateSettingsRecord struct {
	LocalContainerWarningDismissed bool `json:"local_container_warning_dismissed,omitempty"`
}

type UIToolImageSettingsRecord struct {
	DefaultModel string `json:"default_model,omitempty"`
}

type UIToolSettingsRecord struct {
	Image UIToolImageSettingsRecord `json:"image,omitempty"`
}

type UICompactAgentSettingsRecord struct {
	Provider    string `json:"provider,omitempty"`
	Model       string `json:"model,omitempty"`
	Thinking    string `json:"thinking,omitempty"`
	ServiceTier string `json:"service_tier,omitempty"`
}

type UIAgentSettingsRecord struct {
	Compact  UICompactAgentSettingsRecord `json:"compact,omitempty"`
	Explorer UICompactAgentSettingsRecord `json:"explorer,omitempty"`
}

type UISettingsRecord struct {
	Theme     UIThemeSettingsRecord    `json:"theme,omitempty"`
	Input     UIInputSettingsRecord    `json:"input,omitempty"`
	Chat      UIChatSettingsRecord     `json:"chat,omitempty"`
	Swarming  UISwarmingSettingsRecord `json:"swarming,omitempty"`
	Swarm     UISwarmSettingsRecord    `json:"swarm,omitempty"`
	Updates   UIUpdateSettingsRecord   `json:"updates,omitempty"`
	Tools     UIToolSettingsRecord     `json:"tools,omitempty"`
	Agents    UIAgentSettingsRecord    `json:"agents,omitempty"`
	UpdatedAt int64                    `json:"updated_at"`
}

type UISettingsPatch struct {
	Theme    *UIThemeSettingsRecord
	Input    *UIInputSettingsRecord
	Chat     *UIChatSettingsRecord
	Swarming *UISwarmingSettingsRecord
	Swarm    *UISwarmSettingsRecord
	Updates  *UIUpdateSettingsRecord
	Tools    *UIToolSettingsRecord
	Agents   *UIAgentSettingsRecord
}

type UISettingsStore struct {
	store *Store
}

func NewUISettingsStore(store *Store) *UISettingsStore {
	return &UISettingsStore{store: store}
}

func (s *UISettingsStore) Get() (UISettingsRecord, bool, error) {
	return s.GetForAccount("")
}

func (s *UISettingsStore) GetForAccount(accountScopeID string) (UISettingsRecord, bool, error) {
	if s == nil || s.store == nil {
		return UISettingsRecord{}, false, nil
	}
	var record UISettingsRecord
	key := KeyUISettingsDefault
	if accountScopeID = strings.TrimSpace(accountScopeID); accountScopeID != "" {
		key = KeyUISettingsForAccount(accountScopeID)
	}
	ok, err := s.store.GetJSON(key, &record)
	if err != nil {
		return UISettingsRecord{}, false, err
	}
	if !ok {
		return UISettingsRecord{}, false, nil
	}
	return normalizeUISettingsRecord(record), true, nil
}

func (s *UISettingsStore) Update(patch UISettingsPatch) (UISettingsRecord, error) {
	return s.UpdateForAccount("", patch)
}

func (s *UISettingsStore) UpdateForAccount(accountScopeID string, patch UISettingsPatch) (UISettingsRecord, error) {
	record, ok, err := s.GetForAccount(accountScopeID)
	if err != nil {
		return UISettingsRecord{}, err
	}
	if !ok {
		record = DefaultUISettingsRecord()
	}
	if patch.Theme != nil {
		record.Theme = *patch.Theme
	}
	if patch.Input != nil {
		record.Input = *patch.Input
	}
	if patch.Chat != nil {
		record.Chat = *patch.Chat
	}
	if patch.Swarming != nil {
		record.Swarming = *patch.Swarming
	}
	if patch.Swarm != nil {
		record.Swarm = *patch.Swarm
	}
	if patch.Updates != nil {
		record.Updates = *patch.Updates
	}
	if patch.Tools != nil {
		record.Tools = *patch.Tools
	}
	if patch.Agents != nil {
		record.Agents = *patch.Agents
	}
	record.UpdatedAt = time.Now().UnixMilli()
	record.Chat.UpdatedAt = record.UpdatedAt
	record = normalizeUISettingsRecord(record)
	key := KeyUISettingsDefault
	if accountScopeID = strings.TrimSpace(accountScopeID); accountScopeID != "" {
		key = KeyUISettingsForAccount(accountScopeID)
	}
	if err := s.store.PutJSON(key, record); err != nil {
		return UISettingsRecord{}, err
	}
	_ = s.store.Delete(KeyUIChatSettingsDefault)
	return record, nil
}

func DefaultUISettingsRecord() UISettingsRecord {
	return normalizeUISettingsRecord(UISettingsRecord{
		Theme: UIThemeSettingsRecord{
			ActiveID: "crimson",
		},
		Chat: UIChatSettingsRecord{
			ShowHeader:                      true,
			ShowHeaderSet:                   true,
			ThinkingTags:                    true,
			ThinkingTagsSet:                 true,
			DefaultNewSessionMode:           "auto",
			FollowupCheckpointPolicyDefault: "auto_start",
			SidebarHideInactiveHours:        intPointer(12),
			ToolStream: UIChatToolStreamSettingsRecord{
				ShowAnchor:    true,
				PulseFrames:   []string{"·", "•", "◦", "•"},
				RunningSymbol: "•",
				SuccessSymbol: "✓",
				ErrorSymbol:   "✕",
			},
		},
		Swarming: UISwarmingSettingsRecord{
			Title:  "Swarming",
			Status: "swarming",
		},
		Swarm: UISwarmSettingsRecord{
			Name: "Local",
		},
	})
}

func normalizeUISettingsRecord(record UISettingsRecord) UISettingsRecord {
	if !record.Chat.ShowHeaderSet && record.Chat.UpdatedAt == 0 {
		record.Chat.ShowHeader = true
	}
	if !record.Chat.ThinkingTagsSet && record.Chat.UpdatedAt == 0 {
		record.Chat.ThinkingTags = true
	}
	if record.UpdatedAt < 0 {
		record.UpdatedAt = 0
	}
	if record.Chat.UpdatedAt < 0 {
		record.Chat.UpdatedAt = 0
	}
	if record.Theme.ActiveID == "" {
		record.Theme.ActiveID = "crimson"
	}
	if record.Chat.DefaultNewSessionMode == "" {
		record.Chat.DefaultNewSessionMode = "auto"
	} else {
		record.Chat.DefaultNewSessionMode = normalizeDefaultNewSessionMode(record.Chat.DefaultNewSessionMode)
	}
	record.Chat.FollowupCheckpointPolicyDefault = normalizeFollowupCheckpointPolicyDefault(record.Chat.FollowupCheckpointPolicyDefault)
	if record.Chat.SidebarHideInactiveHours == nil || *record.Chat.SidebarHideInactiveHours < 0 {
		record.Chat.SidebarHideInactiveHours = intPointer(12)
	}
	record.Chat.DefaultWorkspaceRoutes = normalizeDefaultWorkspaceRoutes(record.Chat.DefaultWorkspaceRoutes)
	if len(record.Chat.ToolStream.PulseFrames) == 0 {
		record.Chat.ToolStream.PulseFrames = []string{"·", "•", "◦", "•"}
	}
	if record.Chat.ToolStream.RunningSymbol == "" {
		record.Chat.ToolStream.RunningSymbol = "•"
	}
	if record.Chat.ToolStream.SuccessSymbol == "" {
		record.Chat.ToolStream.SuccessSymbol = "✓"
	}
	if record.Chat.ToolStream.ErrorSymbol == "" {
		record.Chat.ToolStream.ErrorSymbol = "✕"
	}
	if record.Swarming.Title == "" {
		record.Swarming.Title = "Swarming"
	}
	if record.Swarming.Status == "" {
		record.Swarming.Status = "swarming"
	}
	if record.Swarm.Name == "" {
		record.Swarm.Name = "Local"
	}
	record.Swarm.RemoteSSHTargets = normalizeRemoteSSHTargets(record.Swarm.RemoteSSHTargets)
	record.Tools.Image.DefaultModel = strings.TrimSpace(record.Tools.Image.DefaultModel)
	record.Agents.Compact.Provider = strings.ToLower(strings.TrimSpace(record.Agents.Compact.Provider))
	record.Agents.Compact.Model = strings.TrimSpace(record.Agents.Compact.Model)
	record.Agents.Compact.Thinking = strings.TrimSpace(record.Agents.Compact.Thinking)
	record.Agents.Compact.ServiceTier = strings.ToLower(strings.TrimSpace(record.Agents.Compact.ServiceTier))
	record.Agents.Explorer.Provider = strings.ToLower(strings.TrimSpace(record.Agents.Explorer.Provider))
	record.Agents.Explorer.Model = strings.TrimSpace(record.Agents.Explorer.Model)
	record.Agents.Explorer.Thinking = strings.TrimSpace(record.Agents.Explorer.Thinking)
	record.Agents.Explorer.ServiceTier = strings.ToLower(strings.TrimSpace(record.Agents.Explorer.ServiceTier))
	return record
}

func intPointer(value int) *int {
	return &value
}

func normalizeDefaultWorkspaceRoutes(values map[string]string) map[string]string {
	if len(values) == 0 {
		return nil
	}
	out := make(map[string]string, len(values))
	for workspacePath, routeID := range values {
		workspacePath = strings.TrimSpace(workspacePath)
		routeID = strings.TrimSpace(routeID)
		if workspacePath == "" || routeID == "" {
			continue
		}
		out[workspacePath] = routeID
	}
	if len(out) == 0 {
		return nil
	}
	return out
}

func normalizeRemoteSSHTargets(values []string) []string {
	if len(values) == 0 {
		return nil
	}
	seen := make(map[string]struct{}, len(values))
	out := make([]string, 0, min(len(values), 8))
	for _, value := range values {
		value = strings.TrimSpace(value)
		if value == "" {
			continue
		}
		if _, ok := seen[value]; ok {
			continue
		}
		seen[value] = struct{}{}
		out = append(out, value)
		if len(out) >= 8 {
			break
		}
	}
	if len(out) == 0 {
		return nil
	}
	return out
}

func normalizeDefaultNewSessionMode(value string) string {
	switch strings.ToLower(strings.TrimSpace(value)) {
	case "plan":
		return "plan"
	default:
		return "auto"
	}
}

func normalizeFollowupCheckpointPolicyDefault(value string) string {
	switch strings.ToLower(strings.TrimSpace(value)) {
	case "approval", "approve", "require_approval", "manual", "ask":
		return "require_approval"
	case "auto", "automatic", "auto_start", "append_and_start", "start":
		return "auto_start"
	default:
		return "auto_start"
	}
}

func NormalizeUISettingsRecordForExternal(record UISettingsRecord) UISettingsRecord {
	return normalizeUISettingsRecord(record)
}
