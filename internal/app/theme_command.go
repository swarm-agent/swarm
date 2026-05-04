package app

import (
	"context"
	"fmt"
	"strconv"
	"strings"
	"time"

	"swarm-refactor/swarmtui/internal/ui"
)

func (a *App) bootstrapTheme(raw string) {
	target := strings.TrimSpace(raw)
	if target == "" {
		target = defaultThemeID
	}
	if _, ok, _ := a.applyThemeByTarget(target, false, false); ok {
		return
	}
	_, _, _ = a.applyThemeByTarget(defaultThemeID, false, false)
}

func (a *App) syncConfiguredCustomThemes() {
	custom := make([]ui.ThemeOption, 0, len(a.config.UI.CustomThemes))
	normalized := make([]CustomThemeConfig, 0, len(a.config.UI.CustomThemes))
	for _, item := range a.config.UI.CustomThemes {
		option, err := ui.NewCustomThemeOption(item.ID, item.Name, item.Palette)
		if err != nil {
			continue
		}
		custom = append(custom, option)
		normalized = append(normalized, CustomThemeConfig{
			ID:      option.ID,
			Name:    option.Name,
			Palette: option.Palette,
		})
	}
	a.config.UI.CustomThemes = normalized
	ui.SetCustomThemes(custom)
}

func (a *App) handleThemesCommand(args []string) {
	if len(args) == 0 {
		a.openThemeModal()
		return
	}

	sub := strings.ToLower(strings.TrimSpace(args[0]))
	switch sub {
	case "open":
		a.openThemeModal()
		return
	case "list":
		a.home.SetCommandOverlay(a.themeListLines())
		a.home.SetStatus("theme list")
		return
	case "status":
		a.home.SetCommandOverlay(a.themeStatusLines())
		a.home.SetStatus("theme " + a.activeThemeOption().ID)
		return
	case "next":
		a.rotateTheme(1)
		return
	case "prev", "previous", "back":
		a.rotateTheme(-1)
		return
	case "set", "use":
		if len(args) < 2 {
			a.home.ClearCommandOverlay()
			a.home.SetStatus("usage: /themes set <name|#n>")
			return
		}
		target := strings.TrimSpace(strings.Join(args[1:], " "))
		if _, ok, _ := a.applyThemeByTarget(target, true, true); !ok {
			a.home.SetCommandOverlay(a.themeListLines())
			a.home.SetStatus(fmt.Sprintf("unknown theme: %s", target))
		}
		return
	case "create", "new":
		if len(args) < 2 {
			a.home.ClearCommandOverlay()
			a.home.SetStatus("usage: /themes create <custom-id> [from <theme>]")
			return
		}
		customID := strings.TrimSpace(args[1])
		baseTarget := ""
		if len(args) > 2 {
			rest := args[2:]
			if strings.EqualFold(rest[0], "from") {
				rest = rest[1:]
			}
			baseTarget = strings.TrimSpace(strings.Join(rest, " "))
		}
		a.createCustomTheme(customID, baseTarget)
		return
	case "edit":
		if len(args) < 4 {
			a.home.ClearCommandOverlay()
			a.home.SetCommandOverlay(themeColorSlotUsageLines())
			a.home.SetStatus("usage: /themes edit <custom-id> <slot> <#RRGGBB>")
			return
		}
		customID := strings.TrimSpace(args[1])
		slot := strings.TrimSpace(args[2])
		color := strings.TrimSpace(args[3])
		a.editCustomTheme(customID, slot, color)
		return
	case "delete", "remove":
		if len(args) < 2 {
			a.home.ClearCommandOverlay()
			a.home.SetStatus("usage: /themes delete <custom-id>")
			return
		}
		a.deleteCustomTheme(strings.TrimSpace(args[1]))
		return
	case "slots":
		a.home.SetCommandOverlay(themeColorSlotUsageLines())
		a.home.SetStatus("theme color slots")
		return
	default:
		target := strings.TrimSpace(strings.Join(args, " "))
		if _, ok, _ := a.applyThemeByTarget(target, true, true); !ok {
			a.home.SetCommandOverlay(a.themeListLines())
			a.home.SetStatus(fmt.Sprintf("unknown theme: %s", target))
		}
		return
	}
}

func (a *App) rotateTheme(step int) {
	themes := ui.ThemeCatalog()
	if len(themes) == 0 {
		a.home.ClearCommandOverlay()
		a.home.SetStatus("no themes available")
		return
	}
	current := a.effectiveThemeOption().ID
	currentIndex := 0
	for i, item := range themes {
		if item.ID == current {
			currentIndex = i
			break
		}
	}
	next := (currentIndex + step) % len(themes)
	if next < 0 {
		next += len(themes)
	}
	_, _, _ = a.applyThemeByTarget(themes[next].ID, true, true)
}

func (a *App) applyThemeByTarget(target string, persist, showFeedback bool) (ui.ThemeOption, bool, error) {
	option, ok := a.resolveThemeTarget(target)
	if !ok {
		return ui.ThemeOption{}, false, nil
	}
	if persist && a.hasActiveWorkspaceThemeScope() {
		err := a.applyWorkspaceThemeOption(option, showFeedback)
		return option, true, err
	}
	err := a.applyThemeOption(option, persist, showFeedback)
	return option, true, err
}

func (a *App) applyWorkspaceThemeOption(option ui.ThemeOption, showFeedback bool) error {
	workspacePath := strings.TrimSpace(a.activeWorkspacePath())
	if workspacePath == "" {
		return a.applyThemeOption(option, true, showFeedback)
	}
	if a.api == nil {
		return fmt.Errorf("workspace theme save unavailable: API client not configured")
	}
	ctx, cancel := context.WithTimeout(context.Background(), 6*time.Second)
	defer cancel()
	resolution, err := a.api.SetWorkspaceTheme(ctx, workspacePath, option.ID)
	if err != nil {
		return err
	}
	resolution.WorkspacePath = strings.TrimSpace(resolution.WorkspacePath)
	if resolution.WorkspacePath == "" {
		resolution.WorkspacePath = workspacePath
	}
	if strings.TrimSpace(resolution.ResolvedPath) == "" {
		resolution.ResolvedPath = workspacePath
	}
	if strings.TrimSpace(resolution.ThemeID) == "" {
		resolution.ThemeID = option.ID
	}
	a.clearThemePreview()
	a.syncActiveWorkspaceSelection(resolution)
	if showFeedback {
		a.home.SetCommandOverlay(a.themeStatusLines())
		a.home.SetStatus(fmt.Sprintf("workspace theme set: %s", option.ID))
	}
	return nil
}

func (a *App) previewThemeByTarget(target string) (ui.ThemeOption, bool) {
	option, ok := a.resolveThemeTarget(target)
	if !ok {
		return ui.ThemeOption{}, false
	}
	a.themePreviewID = option.ID
	a.applyEffectiveTheme()
	return option, true
}

func (a *App) clearThemePreview() {
	if strings.TrimSpace(a.themePreviewID) == "" {
		return
	}
	a.themePreviewID = ""
	a.applyEffectiveTheme()
}

func (a *App) applyThemeOption(option ui.ThemeOption, persist, showFeedback bool) error {
	a.clearThemePreview()
	a.config.UI.Theme = option.ID
	a.applyEffectiveTheme()

	if showFeedback {
		a.home.SetCommandOverlay(a.themeStatusLines())
	}
	if !persist {
		return nil
	}

	err := a.saveThemeConfig()
	if !showFeedback {
		return err
	}
	if err != nil {
		a.home.SetStatus(fmt.Sprintf("theme set: %s (config save failed: %v)", option.ID, err))
		return err
	}
	a.home.SetStatus(fmt.Sprintf("theme set: %s", option.ID))
	return nil
}

func (a *App) saveThemeConfig() error {
	return saveThemeSettings(a.api, a.config.UI)
}

func (a *App) applyEffectiveTheme() {
	a.renderThemeOption(a.effectiveThemeOption())
}

func (a *App) renderThemeOption(option ui.ThemeOption) {
	a.home.SetTheme(option.Theme)
	if a.chat != nil {
		a.chat.SetTheme(option.Theme)
	}
}

func (a *App) applySelectedWorkspaceTheme(themeID string) {
	if candidate := ui.NormalizeThemeID(themeID); candidate != "" {
		if option, ok := ui.ResolveTheme(candidate); ok {
			a.renderThemeOption(option)
			return
		}
	}
	a.renderThemeOption(a.activeThemeOption())
}

func (a *App) applyThemeToChat() {
	if a.chat == nil {
		return
	}
	option := a.effectiveThemeOption()
	a.chat.SetTheme(option.Theme)
}

func (a *App) resolveThemeTarget(target string) (ui.ThemeOption, bool) {
	target = strings.TrimSpace(target)
	if target == "" {
		return ui.ThemeOption{}, false
	}
	themes := ui.ThemeCatalog()
	if len(themes) == 0 {
		return ui.ThemeOption{}, false
	}

	if strings.HasPrefix(target, "#") {
		index, err := strconv.Atoi(strings.TrimSpace(strings.TrimPrefix(target, "#")))
		if err == nil && index >= 1 && index <= len(themes) {
			return themes[index-1], true
		}
	}

	if option, ok := ui.ResolveTheme(target); ok {
		return option, true
	}

	normalized := ui.NormalizeThemeID(target)
	if normalized == "" {
		return ui.ThemeOption{}, false
	}

	match := -1
	for i, item := range themes {
		id := ui.NormalizeThemeID(item.ID)
		name := ui.NormalizeThemeID(item.Name)
		if strings.HasPrefix(id, normalized) || strings.HasPrefix(name, normalized) {
			if match != -1 {
				match = -1
				break
			}
			match = i
		}
	}
	if match >= 0 {
		return themes[match], true
	}

	return ui.ThemeOption{}, false
}

func (a *App) activeThemeOption() ui.ThemeOption {
	if option, ok := ui.ResolveTheme(a.config.UI.Theme); ok {
		return option
	}
	option, _ := ui.ResolveTheme(defaultThemeID)
	return option
}

func (a *App) effectiveThemeOption() ui.ThemeOption {
	if previewID := strings.TrimSpace(a.themePreviewID); previewID != "" {
		if option, ok := ui.ResolveTheme(previewID); ok {
			return option
		}
	}
	if themeID := a.effectiveWorkspaceThemeID(a.activeWorkspacePath()); themeID != "" {
		if option, ok := ui.ResolveTheme(themeID); ok {
			return option
		}
	}
	return a.activeThemeOption()
}

func (a *App) activeWorkspaceThemeID() string {
	for _, ws := range a.homeModel.Workspaces {
		if ws.Active {
			return strings.TrimSpace(ws.ThemeID)
		}
	}
	return ""
}

func (a *App) themeStatusLines() []string {
	option := a.effectiveThemeOption()
	customCount := len(ui.CustomThemeCatalog())
	lines := []string{
		fmt.Sprintf("active theme: %s (%s)", option.ID, option.Name),
		fmt.Sprintf("available themes: %d (custom: %d)", len(ui.ThemeCatalog()), customCount),
		"usage: /themes [open|list|set|next|prev|status|create|edit|delete|slots]",
		"set by index: /themes set #<n>",
		"create custom: /themes create <id> [from <theme>]",
		"edit color: /themes edit <id> <slot> <#RRGGBB>",
		"hot swap applies to all text and UI colors",
	}
	if strings.TrimSpace(a.settingsLabel) != "" {
		lines = append(lines, "settings: "+a.settingsLabel)
	}
	return lines
}

func (a *App) themeListLines() []string {
	themes := ui.ThemeCatalog()
	lines := make([]string, 0, len(themes)+3)
	current := a.effectiveThemeOption().ID
	lines = append(lines, fmt.Sprintf("themes (%d total) - active: %s", len(themes), current))
	for i, item := range themes {
		scope := "custom"
		if item.Builtin {
			scope = "builtin"
		}
		lines = append(lines, fmt.Sprintf("#%d %s (%s) [%s]", i+1, item.ID, item.Name, scope))
	}
	lines = append(lines, "usage: /themes set <name|#n>")
	lines = append(lines, "custom: /themes create <id> [from <theme>]")
	lines = append(lines, "custom edit: /themes edit <id> <slot> <#RRGGBB>")
	lines = append(lines, "custom delete: /themes delete <id>")
	return lines
}

func (a *App) openThemeModal() {
	a.home.ClearCommandOverlay()
	a.home.HideSessionsModal()
	a.home.HideAuthModal()
	a.home.HideWorkspaceModal()
	a.home.HideWorktreesModal()
	a.home.HideMCPModal()
	a.home.HideModelsModal()
	a.home.HideAgentsModal()
	a.home.HideVoiceModal()
	a.home.HideKeybindsModal()

	themes := ui.ThemeCatalog()
	entries := make([]ui.ThemeModalEntry, 0, len(themes))
	for _, item := range themes {
		entries = append(entries, ui.ThemeModalEntry{
			ID:   item.ID,
			Name: item.Name,
		})
	}
	current := a.effectiveThemeOption().ID
	a.themePreviewID = ""
	a.home.SetThemeModalData(entries, current)
	a.home.ShowThemeModal(current)
	a.home.SetStatus("theme modal")
}

func (a *App) createCustomTheme(customID, baseTarget string) {
	customID = ui.NormalizeThemeID(customID)
	if customID == "" {
		a.home.SetStatus("custom theme id is required")
		return
	}
	if existing, ok := ui.ResolveTheme(customID); ok {
		scope := "custom"
		if existing.Builtin {
			scope = "builtin"
		}
		a.home.SetStatus(fmt.Sprintf("theme exists: %s (%s)", existing.ID, scope))
		return
	}

	base := a.activeThemeOption()
	if strings.TrimSpace(baseTarget) != "" {
		option, ok := a.resolveThemeTarget(baseTarget)
		if !ok {
			a.home.SetStatus(fmt.Sprintf("base theme not found: %s", baseTarget))
			return
		}
		base = option
	}

	option, err := ui.NewCustomThemeOption(customID, customID, base.Palette)
	if err != nil {
		a.home.SetStatus(fmt.Sprintf("create custom theme failed: %v", err))
		return
	}

	a.config.UI.CustomThemes = append(a.config.UI.CustomThemes, CustomThemeConfig{
		ID:      option.ID,
		Name:    option.Name,
		Palette: option.Palette,
	})
	a.syncConfiguredCustomThemes()
	created, ok := ui.ResolveTheme(option.ID)
	if !ok {
		a.home.SetStatus(fmt.Sprintf("created theme not found: %s", option.ID))
		return
	}
	if err := a.applyThemeOption(created, true, true); err != nil {
		a.home.SetStatus(fmt.Sprintf("theme created but save failed: %v", err))
		return
	}
	a.home.SetStatus(fmt.Sprintf("custom theme created: %s (base: %s)", option.ID, base.ID))
}

func (a *App) editCustomTheme(customID, slot, color string) {
	index := a.findCustomThemeConfigIndex(customID)
	if index < 0 {
		a.home.SetStatus(fmt.Sprintf("custom theme not found: %s", strings.TrimSpace(customID)))
		return
	}

	current := a.config.UI.CustomThemes[index]
	updatedPalette, err := ui.SetThemePaletteSlot(current.Palette, slot, color)
	if err != nil {
		a.home.SetStatus(fmt.Sprintf("theme edit failed: %v", err))
		return
	}
	updatedOption, err := ui.NewCustomThemeOption(current.ID, current.Name, updatedPalette)
	if err != nil {
		a.home.SetStatus(fmt.Sprintf("theme edit failed: %v", err))
		return
	}

	a.config.UI.CustomThemes[index] = CustomThemeConfig{
		ID:      updatedOption.ID,
		Name:    updatedOption.Name,
		Palette: updatedOption.Palette,
	}
	a.syncConfiguredCustomThemes()

	active := ui.NormalizeThemeID(a.config.UI.Theme) == updatedOption.ID
	if active {
		if err := a.applyThemeOption(updatedOption, true, false); err != nil {
			a.home.SetStatus(fmt.Sprintf("theme updated but save failed: %v", err))
			return
		}
		a.home.SetStatus(fmt.Sprintf("theme updated: %s %s=%s", updatedOption.ID, slot, color))
		a.home.SetCommandOverlay(a.themeStatusLines())
		return
	}
	if err := a.saveThemeConfig(); err != nil {
		a.home.SetStatus(fmt.Sprintf("theme updated but config save failed: %v", err))
		return
	}
	a.home.SetCommandOverlay(a.themeStatusLines())
	a.home.SetStatus(fmt.Sprintf("theme updated: %s %s=%s", updatedOption.ID, slot, color))
}

func (a *App) deleteCustomTheme(customID string) {
	targetID := ui.NormalizeThemeID(customID)
	if targetID == "" {
		a.home.SetStatus("custom theme id is required")
		return
	}
	index := a.findCustomThemeConfigIndex(targetID)
	if index < 0 {
		a.home.SetStatus(fmt.Sprintf("custom theme not found: %s", customID))
		return
	}

	wasActive := ui.NormalizeThemeID(a.config.UI.Theme) == targetID
	a.config.UI.CustomThemes = append(a.config.UI.CustomThemes[:index], a.config.UI.CustomThemes[index+1:]...)
	a.syncConfiguredCustomThemes()

	if wasActive {
		if _, ok, err := a.applyThemeByTarget(defaultThemeID, true, false); !ok {
			a.home.SetStatus("deleted theme but failed to switch to default")
		} else if err != nil {
			a.home.SetStatus(fmt.Sprintf("theme deleted, fallback save failed: %v", err))
		} else {
			a.home.SetStatus(fmt.Sprintf("custom theme deleted: %s (switched to %s)", targetID, defaultThemeID))
		}
		a.home.SetCommandOverlay(a.themeStatusLines())
		return
	}
	if err := a.saveThemeConfig(); err != nil {
		a.home.SetStatus(fmt.Sprintf("theme deleted but config save failed: %v", err))
		return
	}
	a.home.SetCommandOverlay(a.themeStatusLines())
	a.home.SetStatus(fmt.Sprintf("custom theme deleted: %s", targetID))
}

func (a *App) findCustomThemeConfigIndex(target string) int {
	target = ui.NormalizeThemeID(target)
	if target == "" {
		return -1
	}
	for i, item := range a.config.UI.CustomThemes {
		if ui.NormalizeThemeID(item.ID) == target {
			return i
		}
	}
	return -1
}

func themeColorSlotUsageLines() []string {
	return []string{
		"theme color slots:",
		strings.Join(ui.ThemePaletteSlotNames(), ", "),
		"usage: /themes edit <custom-id> <slot> <#RRGGBB>",
		"example: /themes edit my-theme accent #FFAA66",
		"example: /themes edit my-theme code-keyword #C792EA",
	}
}
