package tool

import (
	"encoding/json"
	"errors"
	"fmt"
	"strings"

	sharedtheme "swarm-refactor/swarmtui/theme"
	uisettings "swarm/packages/swarmd/internal/uisettings"
)

func (r *Runtime) manageThemeInspect(scope WorkspaceScope, args map[string]any) (string, error) {
	if r == nil || r.uiSettings == nil {
		return "", errors.New("manage-theme ui settings service is not configured")
	}
	settings, err := r.uiSettings.Get()
	if err != nil {
		return "", fmt.Errorf("manage-theme inspect failed: %w", err)
	}
	workspaceSummary, _ := r.manageThemeWorkspaceSummary(scope, args)
	response := map[string]any{
		"status":               "ok",
		"action":               "inspect",
		"global_theme_id":      manageThemeNormalizeID(settings.Theme.ActiveID),
		"default_theme_id":     sharedtheme.DefaultThemeID(),
		"builtin_themes":       manageThemeBuiltinThemeMaps(),
		"custom_themes":        manageThemeCustomThemeMaps(settings.Theme.CustomThemes),
		"workspace":            workspaceSummary,
		"supported_actions":    []string{"inspect", "list", "get", "create", "update", "delete", "set"},
		"path_id":              toolPathID("manage-theme"),
		"summary":              fmt.Sprintf("loaded %d custom themes", len(settings.Theme.CustomThemes)),
		"details_truncated":    false,
		"prompt_injection_tag": "tool_output_untrusted",
		"safety":               buildUntrustedSafety(""),
	}
	return manageThemeEncodeResponse(response)
}

func (r *Runtime) manageThemeGet(scope WorkspaceScope, args map[string]any) (string, error) {
	settings, err := r.manageThemeSettings()
	if err != nil {
		return "", err
	}
	themeID := strings.TrimSpace(firstNonEmptyString(
		asString(args["theme_id"]),
		asString(args["theme"]),
		asString(args["id"]),
	))
	if themeID == "" {
		return "", errors.New("manage-theme get requires theme_id")
	}
	theme, kind, err := manageThemeLookup(settings.Theme.CustomThemes, themeID)
	if err != nil {
		return "", err
	}
	response := map[string]any{
		"status":               "ok",
		"action":               "get",
		"theme":                manageThemeRecordMap(theme, kind),
		"workspace":            r.manageThemeMaybeWorkspaceSummary(scope, args),
		"path_id":              toolPathID("manage-theme"),
		"summary":              fmt.Sprintf("loaded %s theme %s", kind, theme.ID),
		"details_truncated":    false,
		"prompt_injection_tag": "tool_output_untrusted",
		"safety":               buildUntrustedSafety(""),
	}
	return manageThemeEncodeResponse(response)
}

func (r *Runtime) manageThemeUpsert(scope WorkspaceScope, args map[string]any, mustExist, confirm bool) (string, error) {
	settings, err := r.manageThemeSettings()
	if err != nil {
		return "", err
	}
	content, err := manageThemeContentObject(args)
	if err != nil {
		return "", err
	}
	themeID := manageThemeNormalizeID(firstNonEmptyString(
		asString(args["theme_id"]),
		asString(args["theme"]),
		asString(args["id"]),
		asString(content["id"]),
		asString(content["theme_id"]),
	))
	if themeID == "" {
		return "", errors.New("manage-theme requires theme_id")
	}
	baseThemeID := manageThemeNormalizeID(firstNonEmptyString(
		asString(args["base_theme_id"]),
		asString(content["base_theme_id"]),
		asString(settings.Theme.ActiveID),
		sharedtheme.DefaultThemeID(),
	))
	beforeIndex := manageThemeCustomThemeIndex(settings.Theme.CustomThemes, themeID)
	exists := beforeIndex >= 0
	if mustExist && !exists {
		return "", fmt.Errorf("custom theme %q not found", themeID)
	}
	if !mustExist {
		if exists {
			return "", fmt.Errorf("theme %q already exists; use update", themeID)
		}
		if _, ok := manageThemeBuiltinByID(themeID); ok {
			return "", fmt.Errorf("theme %q conflicts with builtin theme id", themeID)
		}
	}
	baseTheme, _, err := manageThemeLookup(settings.Theme.CustomThemes, baseThemeID)
	if err != nil {
		return "", fmt.Errorf("manage-theme base theme %q not found: %w", baseThemeID, err)
	}
	afterTheme, err := manageThemeUpsertRecord(themeID, content, baseTheme)
	if err != nil {
		return "", err
	}

	var before *uisettings.ThemeCustomTheme
	nextThemes := append([]uisettings.ThemeCustomTheme(nil), settings.Theme.CustomThemes...)
	if exists {
		beforeCopy := nextThemes[beforeIndex]
		before = &beforeCopy
		nextThemes[beforeIndex] = afterTheme
	} else {
		nextThemes = append(nextThemes, afterTheme)
	}
	settings.Theme.CustomThemes = nextThemes

	action := "create"
	status := "proposed_create"
	summary := fmt.Sprintf("proposed new custom theme %s", afterTheme.ID)
	if exists {
		action = "update"
		status = "proposed_update"
		summary = fmt.Sprintf("proposed update for custom theme %s", afterTheme.ID)
	}
	change := map[string]any{
		"kind":      "theme_change",
		"target":    "custom_theme",
		"operation": action,
		"theme_id":  afterTheme.ID,
		"before":    manageThemeOptionalRecordMap(before, "custom"),
		"after":     manageThemeRecordMap(afterTheme, "custom"),
	}
	if confirm {
		saved, err := r.uiSettings.Set(settings)
		if err != nil {
			return "", err
		}
		response := map[string]any{
			"status":               "ok",
			"action":               action,
			"applied":              true,
			"theme":                manageThemeRecordMap(afterTheme, "custom"),
			"change":               change,
			"global_theme_id":      manageThemeNormalizeID(saved.Theme.ActiveID),
			"custom_themes":        manageThemeCustomThemeMaps(saved.Theme.CustomThemes),
			"path_id":              toolPathID("manage-theme"),
			"summary":              strings.Replace(summary, "proposed ", "applied ", 1),
			"details_truncated":    false,
			"prompt_injection_tag": "tool_output_untrusted",
			"safety":               buildUntrustedSafety(manageThemeSafetyText(change)),
		}
		return manageThemeEncodeResponse(response)
	}
	response := map[string]any{
		"status":               status,
		"action":               action,
		"theme":                manageThemeRecordMap(afterTheme, "custom"),
		"change":               change,
		"approved_arguments":   cloneStringAnyMap(args),
		"path_id":              toolPathID("manage-theme"),
		"summary":              summary,
		"details_truncated":    false,
		"prompt_injection_tag": "tool_output_untrusted",
		"safety":               buildUntrustedSafety(manageThemeSafetyText(change)),
	}
	return manageThemeEncodeResponse(response)
}

func (r *Runtime) manageThemeDelete(scope WorkspaceScope, args map[string]any, confirm bool) (string, error) {
	settings, err := r.manageThemeSettings()
	if err != nil {
		return "", err
	}
	if r.themeWorkspace == nil {
		return "", errors.New("manage-theme workspace service is not configured")
	}
	themeID := manageThemeNormalizeID(firstNonEmptyString(asString(args["theme_id"]), asString(args["theme"]), asString(args["id"])))
	if themeID == "" {
		return "", errors.New("manage-theme delete requires theme_id")
	}
	index := manageThemeCustomThemeIndex(settings.Theme.CustomThemes, themeID)
	if index < 0 {
		return "", fmt.Errorf("custom theme %q not found", themeID)
	}
	before := settings.Theme.CustomThemes[index]
	nextThemes := append([]uisettings.ThemeCustomTheme(nil), settings.Theme.CustomThemes[:index]...)
	nextThemes = append(nextThemes, settings.Theme.CustomThemes[index+1:]...)
	settings.Theme.CustomThemes = nextThemes
	resetGlobal := manageThemeNormalizeID(settings.Theme.ActiveID) == themeID
	if resetGlobal {
		settings.Theme.ActiveID = sharedtheme.DefaultThemeID()
	}

	clearedWorkspaces := make([]string, 0)
	if entries, err := r.themeWorkspace.ListKnown(500); err == nil {
		for _, entry := range entries {
			if manageThemeNormalizeID(entry.ThemeID) == themeID {
				clearedWorkspaces = append(clearedWorkspaces, strings.TrimSpace(entry.Path))
			}
		}
	}
	change := map[string]any{
		"kind":               "theme_change",
		"target":             "custom_theme",
		"operation":          "delete",
		"theme_id":           before.ID,
		"before":             manageThemeRecordMap(before, "custom"),
		"after":              nil,
		"global_theme_reset": resetGlobal,
		"cleared_workspaces": append([]string(nil), clearedWorkspaces...),
	}
	if confirm {
		saved, err := r.uiSettings.Set(settings)
		if err != nil {
			return "", err
		}
		for _, workspacePath := range clearedWorkspaces {
			if _, err := r.themeWorkspace.SetThemeID(workspacePath, ""); err != nil {
				return "", err
			}
		}
		response := map[string]any{
			"status":               "ok",
			"action":               "delete",
			"applied":              true,
			"theme":                manageThemeRecordMap(before, "custom"),
			"change":               change,
			"global_theme_id":      manageThemeNormalizeID(saved.Theme.ActiveID),
			"custom_themes":        manageThemeCustomThemeMaps(saved.Theme.CustomThemes),
			"path_id":              toolPathID("manage-theme"),
			"summary":              fmt.Sprintf("applied delete for custom theme %s", before.ID),
			"details_truncated":    false,
			"prompt_injection_tag": "tool_output_untrusted",
			"safety":               buildUntrustedSafety(manageThemeSafetyText(change)),
		}
		return manageThemeEncodeResponse(response)
	}
	response := map[string]any{
		"status":               "proposed_delete",
		"action":               "delete",
		"theme":                manageThemeRecordMap(before, "custom"),
		"change":               change,
		"approved_arguments":   cloneStringAnyMap(args),
		"path_id":              toolPathID("manage-theme"),
		"summary":              fmt.Sprintf("proposed delete for custom theme %s", before.ID),
		"details_truncated":    false,
		"prompt_injection_tag": "tool_output_untrusted",
		"safety":               buildUntrustedSafety(manageThemeSafetyText(change)),
	}
	return manageThemeEncodeResponse(response)
}

func (r *Runtime) manageThemeSet(scope WorkspaceScope, args map[string]any, confirm bool) (string, error) {
	settings, err := r.manageThemeSettings()
	if err != nil {
		return "", err
	}
	themeID := manageThemeNormalizeID(firstNonEmptyString(asString(args["theme_id"]), asString(args["theme"]), asString(args["id"])))
	if themeID != "" {
		if _, _, err := manageThemeLookup(settings.Theme.CustomThemes, themeID); err != nil {
			return "", err
		}
	}
	workspacePath := strings.TrimSpace(asString(args["workspace_path"]))
	if workspacePath != "" {
		if r.themeWorkspace == nil {
			return "", errors.New("manage-theme workspace service is not configured")
		}
		scopeInfo, err := r.themeWorkspace.ScopeForPath(workspacePath)
		if err != nil {
			return "", err
		}
		beforeThemeID := manageThemeNormalizeID(scopeInfo.ThemeID)
		change := map[string]any{
			"kind":           "theme_change",
			"target":         "workspace_theme",
			"operation":      "set",
			"workspace_path": strings.TrimSpace(scopeInfo.WorkspacePath),
			"before":         map[string]any{"workspace_path": strings.TrimSpace(scopeInfo.WorkspacePath), "theme_id": beforeThemeID},
			"after":          map[string]any{"workspace_path": strings.TrimSpace(scopeInfo.WorkspacePath), "theme_id": themeID},
		}
		if confirm {
			resolution, err := r.themeWorkspace.SetThemeID(workspacePath, themeID)
			if err != nil {
				return "", err
			}
			response := map[string]any{
				"status":               "ok",
				"action":               "set",
				"applied":              true,
				"workspace":            resolution,
				"change":               change,
				"path_id":              toolPathID("manage-theme"),
				"summary":              fmt.Sprintf("applied workspace theme %s for %s", manageThemeStringFallback(themeID, "inherit"), strings.TrimSpace(resolution.WorkspacePath)),
				"details_truncated":    false,
				"prompt_injection_tag": "tool_output_untrusted",
				"safety":               buildUntrustedSafety(manageThemeSafetyText(change)),
			}
			return manageThemeEncodeResponse(response)
		}
		response := map[string]any{
			"status":               "proposed_set",
			"action":               "set",
			"workspace_path":       strings.TrimSpace(scopeInfo.WorkspacePath),
			"theme_id":             themeID,
			"change":               change,
			"approved_arguments":   cloneStringAnyMap(args),
			"path_id":              toolPathID("manage-theme"),
			"summary":              fmt.Sprintf("proposed workspace theme %s for %s", manageThemeStringFallback(themeID, "inherit"), strings.TrimSpace(scopeInfo.WorkspacePath)),
			"details_truncated":    false,
			"prompt_injection_tag": "tool_output_untrusted",
			"safety":               buildUntrustedSafety(manageThemeSafetyText(change)),
		}
		return manageThemeEncodeResponse(response)
	}

	beforeThemeID := manageThemeNormalizeID(settings.Theme.ActiveID)
	change := map[string]any{
		"kind":      "theme_change",
		"target":    "global_theme",
		"operation": "set",
		"before":    map[string]any{"theme_id": beforeThemeID},
		"after":     map[string]any{"theme_id": themeID},
	}
	if confirm {
		settings.Theme.ActiveID = manageThemeStringFallback(themeID, sharedtheme.DefaultThemeID())
		saved, err := r.uiSettings.Set(settings)
		if err != nil {
			return "", err
		}
		response := map[string]any{
			"status":               "ok",
			"action":               "set",
			"applied":              true,
			"global_theme_id":      manageThemeNormalizeID(saved.Theme.ActiveID),
			"change":               change,
			"path_id":              toolPathID("manage-theme"),
			"summary":              fmt.Sprintf("applied global theme %s", manageThemeNormalizeID(saved.Theme.ActiveID)),
			"details_truncated":    false,
			"prompt_injection_tag": "tool_output_untrusted",
			"safety":               buildUntrustedSafety(manageThemeSafetyText(change)),
		}
		return manageThemeEncodeResponse(response)
	}
	response := map[string]any{
		"status":               "proposed_set",
		"action":               "set",
		"theme_id":             themeID,
		"change":               change,
		"approved_arguments":   cloneStringAnyMap(args),
		"path_id":              toolPathID("manage-theme"),
		"summary":              fmt.Sprintf("proposed global theme %s", manageThemeStringFallback(themeID, sharedtheme.DefaultThemeID())),
		"details_truncated":    false,
		"prompt_injection_tag": "tool_output_untrusted",
		"safety":               buildUntrustedSafety(manageThemeSafetyText(change)),
	}
	return manageThemeEncodeResponse(response)
}

func (r *Runtime) manageThemeSettings() (uisettings.UISettings, error) {
	if r == nil || r.uiSettings == nil {
		return uisettings.UISettings{}, errors.New("manage-theme ui settings service is not configured")
	}
	settings, err := r.uiSettings.Get()
	if err != nil {
		return uisettings.UISettings{}, fmt.Errorf("manage-theme read settings failed: %w", err)
	}
	return settings, nil
}

func (r *Runtime) manageThemeWorkspaceSummary(scope WorkspaceScope, args map[string]any) (map[string]any, error) {
	if r == nil || r.themeWorkspace == nil {
		return nil, nil
	}
	workspacePath := strings.TrimSpace(asString(args["workspace_path"]))
	if workspacePath == "" {
		workspacePath = strings.TrimSpace(scope.PrimaryPath)
	}
	if workspacePath == "" {
		return nil, nil
	}
	info, err := r.themeWorkspace.ScopeForPath(workspacePath)
	if err != nil {
		return nil, err
	}
	return map[string]any{
		"requested_path": strings.TrimSpace(info.RequestedPath),
		"resolved_path":  strings.TrimSpace(info.ResolvedPath),
		"workspace_path": strings.TrimSpace(info.WorkspacePath),
		"workspace_name": strings.TrimSpace(info.WorkspaceName),
		"theme_id":       manageThemeNormalizeID(info.ThemeID),
		"matched":        info.Matched,
	}, nil
}

func (r *Runtime) manageThemeMaybeWorkspaceSummary(scope WorkspaceScope, args map[string]any) map[string]any {
	summary, err := r.manageThemeWorkspaceSummary(scope, args)
	if err != nil {
		return map[string]any{"error": err.Error()}
	}
	return summary
}

func manageThemeContentObject(args map[string]any) (map[string]any, error) {
	raw, ok := args["content"]
	if !ok || raw == nil {
		return nil, nil
	}
	switch typed := raw.(type) {
	case map[string]any:
		return cloneStringAnyMap(typed), nil
	case string:
		text := strings.TrimSpace(typed)
		if text == "" {
			return nil, nil
		}
		var payload map[string]any
		if err := json.Unmarshal([]byte(text), &payload); err != nil {
			return nil, fmt.Errorf("manage-theme content must be a JSON object string or object payload: %w", err)
		}
		return payload, nil
	case []byte:
		text := strings.TrimSpace(string(typed))
		if text == "" {
			return nil, nil
		}
		var payload map[string]any
		if err := json.Unmarshal(typed, &payload); err != nil {
			return nil, fmt.Errorf("manage-theme content must be a JSON object string or object payload: %w", err)
		}
		return payload, nil
	default:
		return nil, errors.New("manage-theme content must be an object or JSON object string")
	}
}

func manageThemeLookup(customThemes []uisettings.ThemeCustomTheme, themeID string) (uisettings.ThemeCustomTheme, string, error) {
	normalized := manageThemeNormalizeID(themeID)
	if normalized == "" {
		return uisettings.ThemeCustomTheme{}, "", errors.New("theme id is required")
	}
	if builtin, ok := manageThemeBuiltinByID(normalized); ok {
		return builtin, "builtin", nil
	}
	for _, item := range customThemes {
		if manageThemeNormalizeID(item.ID) == normalized {
			return item, "custom", nil
		}
	}
	return uisettings.ThemeCustomTheme{}, "", fmt.Errorf("theme %q not found", normalized)
}

func manageThemeBuiltinByID(themeID string) (uisettings.ThemeCustomTheme, bool) {
	normalized := manageThemeNormalizeID(themeID)
	if normalized == "" {
		return uisettings.ThemeCustomTheme{}, false
	}
	option, ok := sharedtheme.ResolveBuiltinTheme(normalized)
	if !ok {
		return uisettings.ThemeCustomTheme{}, false
	}
	return uisettings.ThemeCustomTheme{
		ID:      option.ID,
		Name:    option.Name,
		Palette: uisettings.ThemePalette(option.Palette),
	}, true
}

func manageThemeCustomThemeIndex(items []uisettings.ThemeCustomTheme, themeID string) int {
	normalized := manageThemeNormalizeID(themeID)
	if normalized == "" {
		return -1
	}
	for i, item := range items {
		if manageThemeNormalizeID(item.ID) == normalized {
			return i
		}
	}
	return -1
}

func manageThemeUpsertRecord(themeID string, content map[string]any, base uisettings.ThemeCustomTheme) (uisettings.ThemeCustomTheme, error) {
	record := uisettings.ThemeCustomTheme{
		ID:      manageThemeNormalizeID(themeID),
		Name:    strings.TrimSpace(firstNonEmptyString(asString(content["name"]), base.Name, themeID)),
		Palette: base.Palette,
	}
	if paletteValue, ok := content["palette"]; ok {
		paletteObject, ok := paletteValue.(map[string]any)
		if !ok {
			return uisettings.ThemeCustomTheme{}, errors.New("manage-theme palette must be an object")
		}
		raw, err := json.Marshal(paletteObject)
		if err != nil {
			return uisettings.ThemeCustomTheme{}, err
		}
		if err := json.Unmarshal(raw, &record.Palette); err != nil {
			return uisettings.ThemeCustomTheme{}, fmt.Errorf("manage-theme palette is invalid: %w", err)
		}
	}
	option, err := sharedtheme.NewCustomThemeOption(record.ID, record.Name, sharedtheme.ThemePalette(record.Palette))
	if err != nil {
		return uisettings.ThemeCustomTheme{}, fmt.Errorf("manage-theme content is invalid: %w", err)
	}
	record.ID = option.ID
	record.Name = option.Name
	record.Palette = uisettings.ThemePalette(option.Palette)
	return record, nil
}

func manageThemeBuiltinThemeMaps() []map[string]any {
	themes := sharedtheme.BuiltinThemeCatalog()
	out := make([]map[string]any, 0, len(themes))
	for _, item := range themes {
		out = append(out, manageThemeRecordMap(uisettings.ThemeCustomTheme{
			ID:      item.ID,
			Name:    item.Name,
			Palette: uisettings.ThemePalette(item.Palette),
		}, "builtin"))
	}
	return out
}

func manageThemeCustomThemeMaps(items []uisettings.ThemeCustomTheme) []map[string]any {
	out := make([]map[string]any, 0, len(items))
	for _, item := range items {
		out = append(out, manageThemeRecordMap(item, "custom"))
	}
	return out
}

func manageThemeRecordMap(item uisettings.ThemeCustomTheme, kind string) map[string]any {
	return map[string]any{
		"id":      manageThemeNormalizeID(item.ID),
		"name":    strings.TrimSpace(item.Name),
		"kind":    strings.TrimSpace(kind),
		"palette": item.Palette,
	}
}

func manageThemeOptionalRecordMap(item *uisettings.ThemeCustomTheme, kind string) any {
	if item == nil {
		return nil
	}
	return manageThemeRecordMap(*item, kind)
}

func manageThemeNormalizeID(themeID string) string {
	return sharedtheme.NormalizeThemeID(themeID)
}

func manageThemeStringFallback(value, fallback string) string {
	value = strings.TrimSpace(value)
	if value == "" {
		return fallback
	}
	return value
}

func manageThemeEncodeResponse(response map[string]any) (string, error) {
	encoded, err := json.Marshal(response)
	if err != nil {
		return "", err
	}
	return string(encoded), nil
}

func manageThemeSafetyText(change map[string]any) string {
	raw, err := json.Marshal(change)
	if err != nil {
		return ""
	}
	return string(raw)
}
