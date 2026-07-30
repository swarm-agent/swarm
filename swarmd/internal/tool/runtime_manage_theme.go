package tool

import (
	"encoding/json"
	"errors"
	"fmt"
	"strings"

	sharedtheme "swarm-refactor/swarmtui/theme"
	"swarm/packages/swarmd/internal/identity"
	uisettings "swarm/packages/swarmd/internal/uisettings"
)

func (r *Runtime) manageThemeInspect(scope WorkspaceScope, args map[string]any) (string, error) {
	if r == nil || r.uiSettings == nil {
		return "", errors.New("manage-theme ui settings service is not configured")
	}
	settings, err := r.manageThemeSettings(scope)
	if err != nil {
		return "", fmt.Errorf("manage-theme inspect failed: %w", err)
	}
	workspaceSummary, _ := r.manageThemeWorkspaceSummary(scope, args)
	response := map[string]any{
		"status":               "ok",
		"action":               "inspect",
		"global_theme_id":      manageThemeNormalizeID(settings.Theme.ActiveID),
		"account_scope_id":     r.manageThemeAccountScopeID(scope),
		"default_theme_id":     sharedtheme.DefaultThemeID(),
		"builtin_themes":       manageThemeBuiltinThemeMaps(),
		"custom_themes":        manageThemeCustomThemeMaps(settings.Theme.CustomThemes),
		"workspace":            workspaceSummary,
		"supported_actions":    []string{"inspect", "list", "get", "create", "create_batch", "update", "delete", "set"},
		"action_contracts":     manageThemeActionContracts(scope),
		"path_id":              toolPathID("manage-theme"),
		"summary":              fmt.Sprintf("loaded %d custom themes", len(settings.Theme.CustomThemes)),
		"details_truncated":    false,
		"prompt_injection_tag": "tool_output_untrusted",
		"safety":               buildUntrustedSafety(""),
	}
	return manageThemeEncodeResponse(response)
}

func (r *Runtime) manageThemeGet(scope WorkspaceScope, args map[string]any) (string, error) {
	settings, err := r.manageThemeSettings(scope)
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

const manageThemeBatchLimit = 8

func (r *Runtime) manageThemeCreateBatch(scope WorkspaceScope, args map[string]any, confirm bool) (string, error) {
	settings, err := r.manageThemeSettings(scope)
	if err != nil {
		return "", err
	}
	rawThemes, ok := args["themes"].([]any)
	if !ok || len(rawThemes) == 0 {
		return "", errors.New("manage-theme create_batch requires themes with 1 to 8 theme payloads")
	}
	if len(rawThemes) > manageThemeBatchLimit {
		return "", fmt.Errorf("manage-theme create_batch accepts at most %d themes", manageThemeBatchLimit)
	}

	nextThemes := append([]uisettings.ThemeCustomTheme(nil), settings.Theme.CustomThemes...)
	created := make([]uisettings.ThemeCustomTheme, 0, len(rawThemes))
	changes := make([]map[string]any, 0, len(rawThemes)+1)
	seen := make(map[string]struct{}, len(rawThemes))
	for index, rawTheme := range rawThemes {
		content, ok := rawTheme.(map[string]any)
		if !ok {
			return "", fmt.Errorf("manage-theme create_batch themes[%d] must be an object", index)
		}
		content = cloneStringAnyMap(content)
		themeID := manageThemeNormalizeID(firstNonEmptyString(asString(content["id"]), asString(content["theme_id"])))
		name := strings.TrimSpace(asString(content["name"]))
		_, hasPalette := content["palette"]
		baseThemeExplicit := strings.TrimSpace(asString(content["base_theme_id"])) != ""
		if themeID == "" || name == "" || (!hasPalette && !baseThemeExplicit) {
			return "", fmt.Errorf("manage-theme create_batch themes[%d] requires id or theme_id, name, and palette (or base_theme_id)", index)
		}
		if _, duplicate := seen[themeID]; duplicate {
			return "", fmt.Errorf("manage-theme create_batch contains duplicate theme id %q", themeID)
		}
		seen[themeID] = struct{}{}
		if manageThemeCustomThemeIndex(nextThemes, themeID) >= 0 {
			return "", fmt.Errorf("theme %q already exists; use update", themeID)
		}
		if _, builtin := manageThemeBuiltinByID(themeID); builtin {
			return "", fmt.Errorf("theme %q conflicts with builtin theme id", themeID)
		}
		baseThemeID := manageThemeNormalizeID(firstNonEmptyString(
			asString(content["base_theme_id"]),
			asString(settings.Theme.ActiveID),
			sharedtheme.DefaultThemeID(),
		))
		baseTheme, _, err := manageThemeLookup(nextThemes, baseThemeID)
		if err != nil {
			return "", fmt.Errorf("manage-theme base theme %q not found for themes[%d]: %w", baseThemeID, index, err)
		}
		afterTheme, err := manageThemeUpsertRecord(themeID, content, baseTheme)
		if err != nil {
			return "", fmt.Errorf("manage-theme create_batch themes[%d]: %w", index, err)
		}
		nextThemes = append(nextThemes, afterTheme)
		created = append(created, afterTheme)
		changes = append(changes, map[string]any{
			"kind":      "theme_change",
			"target":    "custom_theme",
			"operation": "create",
			"theme_id":  afterTheme.ID,
			"before":    nil,
			"after":     manageThemeRecordMap(afterTheme, "custom"),
		})
	}

	applyThemeID := manageThemeNormalizeID(asString(args["apply_theme_id"]))
	applyTo, err := manageThemeNormalizeApplyTo(asString(args["apply_to"]))
	if err != nil {
		return "", err
	}
	if applyThemeID == "" && applyTo != "" && applyTo != "none" {
		return "", errors.New("manage-theme create_batch apply_to requires apply_theme_id from the batch")
	}
	if applyThemeID != "" {
		if _, included := seen[applyThemeID]; !included {
			return "", fmt.Errorf("manage-theme create_batch apply_theme_id %q must identify a theme in themes", applyThemeID)
		}
		if applyTo == "" || applyTo == "none" {
			return "", errors.New("manage-theme create_batch apply_theme_id requires apply_to=workspace, account, or global")
		}
	}

	settings.Theme.CustomThemes = nextThemes
	applyArgs := cloneStringAnyMap(args)
	applyArgs["apply_to"] = applyTo
	applyPlan := manageThemeApplyPlan{target: "none"}
	if applyThemeID != "" {
		applyPlan, err = r.manageThemeBuildApplyPlan(scope, applyArgs, settings, applyThemeID, false)
		if err != nil {
			return "", err
		}
		if applyPlan.change != nil {
			changes = append(changes, applyPlan.change)
		}
	}

	themeNames := manageThemeNames(created)
	change := map[string]any{
		"kind":      "theme_change",
		"operation": "create_batch",
		"changes":   changes,
	}
	if confirm {
		if applyPlan.target == "account" || applyPlan.target == "global" {
			settings.Theme.ActiveID = applyThemeID
		}
		var workspaceResolution any
		saved, err := r.manageThemeSaveSettings(scope, settings)
		if err != nil {
			return "", err
		}
		if applyPlan.target == "workspace" {
			resolution, err := r.themeWorkspace.SetThemeIDForPrincipal(scope.Principal, applyPlan.workspacePath, applyThemeID)
			if err != nil {
				rollbackSettings := saved
				rollbackThemes := rollbackSettings.Theme.CustomThemes[:len(rollbackSettings.Theme.CustomThemes)-len(created)]
				rollbackSettings.Theme.CustomThemes = append([]uisettings.ThemeCustomTheme(nil), rollbackThemes...)
				if _, rollbackErr := r.manageThemeSaveSettings(scope, rollbackSettings); rollbackErr != nil {
					return "", fmt.Errorf("%w; theme batch rollback failed: %v", err, rollbackErr)
				}
				return "", err
			}
			workspaceResolution = resolution
		}
		response := manageThemeBatchResponse("ok", created, themeNames, change)
		response["applied"] = true
		response["apply_to"] = applyPlan.responseTarget()
		response["global_theme_id"] = manageThemeNormalizeID(saved.Theme.ActiveID)
		response["account_scope_id"] = r.manageThemeAccountScopeID(scope)
		response["custom_themes"] = manageThemeCustomThemeMaps(saved.Theme.CustomThemes)
		response["summary"] = manageThemeBatchSummary("generated", themeNames, applyPlan)
		if workspaceResolution != nil {
			response["workspace"] = workspaceResolution
		}
		return manageThemeEncodeResponse(response)
	}

	response := manageThemeBatchResponse("proposed_create_batch", created, themeNames, change)
	response["applied"] = false
	response["apply_to"] = applyPlan.responseTarget()
	response["approved_arguments"] = cloneStringAnyMap(args)
	response["summary"] = manageThemeBatchSummary("will generate", themeNames, applyPlan)
	if applyPlan.workspacePath != "" {
		response["workspace_path"] = applyPlan.workspacePath
	}
	return manageThemeEncodeResponse(response)
}

func (r *Runtime) manageThemeUpsert(scope WorkspaceScope, args map[string]any, mustExist, confirm bool) (string, error) {
	settings, err := r.manageThemeSettings(scope)
	if err != nil {
		return "", err
	}
	content, err := manageThemeContentObject(args)
	if err != nil {
		return "", err
	}
	if content == nil {
		content = map[string]any{}
	}
	if name := strings.TrimSpace(asString(args["name"])); name != "" && strings.TrimSpace(asString(content["name"])) == "" {
		content["name"] = name
	}
	themeID := manageThemeNormalizeID(firstNonEmptyString(
		asString(args["theme_id"]),
		asString(args["theme"]),
		asString(args["id"]),
		asString(content["id"]),
		asString(content["theme_id"]),
	))
	if themeID == "" {
		if !mustExist {
			return "", errors.New(manageThemeCreateUsage())
		}
		return "", errors.New("manage-theme update requires theme_id or content.id")
	}
	if !mustExist && strings.TrimSpace(asString(content["name"])) == "" {
		return "", errors.New(manageThemeCreateUsage())
	}
	_, hasPalette := content["palette"]
	baseThemeExplicit := strings.TrimSpace(firstNonEmptyString(asString(args["base_theme_id"]), asString(content["base_theme_id"]))) != ""
	if !mustExist && !hasPalette && !baseThemeExplicit {
		return "", errors.New(manageThemeCreateUsage())
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
	if exists {
		action = "update"
		status = "proposed_update"
	}
	applyPlan, err := r.manageThemeBuildApplyPlan(scope, args, settings, afterTheme.ID, !mustExist)
	if err != nil {
		return "", err
	}
	themeChange := map[string]any{
		"kind":      "theme_change",
		"target":    "custom_theme",
		"operation": action,
		"theme_id":  afterTheme.ID,
		"before":    manageThemeOptionalRecordMap(before, "custom"),
		"after":     manageThemeRecordMap(afterTheme, "custom"),
	}
	change := manageThemeCombinedChange(action, themeChange, applyPlan.change)
	if confirm {
		if applyPlan.target == "account" || applyPlan.target == "global" {
			settings.Theme.ActiveID = manageThemeStringFallback(applyPlan.themeID, sharedtheme.DefaultThemeID())
		}
		var workspaceResolution any
		if applyPlan.target == "workspace" {
			resolution, err := r.themeWorkspace.SetThemeIDForPrincipal(scope.Principal, applyPlan.workspacePath, applyPlan.themeID)
			if err != nil {
				return "", err
			}
			workspaceResolution = resolution
		}
		saved, err := r.manageThemeSaveSettings(scope, settings)
		if err != nil {
			return "", err
		}
		response := map[string]any{
			"status":               "ok",
			"action":               action,
			"applied":              true,
			"apply_to":             applyPlan.responseTarget(),
			"theme":                manageThemeRecordMap(afterTheme, "custom"),
			"change":               change,
			"global_theme_id":      manageThemeNormalizeID(saved.Theme.ActiveID),
			"account_scope_id":     r.manageThemeAccountScopeID(scope),
			"custom_themes":        manageThemeCustomThemeMaps(saved.Theme.CustomThemes),
			"path_id":              toolPathID("manage-theme"),
			"summary":              manageThemeUpsertSummary("applied", action, afterTheme.ID, applyPlan),
			"details_truncated":    false,
			"prompt_injection_tag": "tool_output_untrusted",
			"safety":               buildUntrustedSafety(manageThemeSafetyText(change)),
		}
		if workspaceResolution != nil {
			response["workspace"] = workspaceResolution
		}
		return manageThemeEncodeResponse(response)
	}
	response := map[string]any{
		"status":               status,
		"action":               action,
		"apply_to":             applyPlan.responseTarget(),
		"theme":                manageThemeRecordMap(afterTheme, "custom"),
		"change":               change,
		"approved_arguments":   cloneStringAnyMap(args),
		"path_id":              toolPathID("manage-theme"),
		"summary":              manageThemeUpsertSummary("proposed", action, afterTheme.ID, applyPlan),
		"details_truncated":    false,
		"prompt_injection_tag": "tool_output_untrusted",
		"safety":               buildUntrustedSafety(manageThemeSafetyText(change)),
	}
	if applyPlan.workspacePath != "" {
		response["workspace_path"] = applyPlan.workspacePath
	}
	return manageThemeEncodeResponse(response)
}

func (r *Runtime) manageThemeDelete(scope WorkspaceScope, args map[string]any, confirm bool) (string, error) {
	settings, err := r.manageThemeSettings(scope)
	if err != nil {
		return "", err
	}
	if r.themeWorkspace == nil {
		return "", errors.New("manage-theme workspace service is not configured")
	}
	if !scope.Principal.Valid() {
		return "", identity.ErrPrincipalRequired
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

	entries, err := r.themeWorkspace.ListKnownForPrincipal(scope.Principal, 500)
	if err != nil {
		return "", err
	}
	clearedWorkspaces := make([]string, 0)
	for _, entry := range entries {
		if manageThemeNormalizeID(entry.ThemeID) == themeID {
			clearedWorkspaces = append(clearedWorkspaces, strings.TrimSpace(entry.Path))
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
		saved, err := r.manageThemeSaveSettings(scope, settings)
		if err != nil {
			return "", err
		}
		for _, workspacePath := range clearedWorkspaces {
			if _, err := r.themeWorkspace.SetThemeIDForPrincipal(scope.Principal, workspacePath, ""); err != nil {
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
			"account_scope_id":     r.manageThemeAccountScopeID(scope),
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
	settings, err := r.manageThemeSettings(scope)
	if err != nil {
		return "", err
	}
	themeID := manageThemeNormalizeID(firstNonEmptyString(asString(args["theme_id"]), asString(args["theme"]), asString(args["id"])))
	if themeID != "" {
		if _, _, err := manageThemeLookup(settings.Theme.CustomThemes, themeID); err != nil {
			return "", err
		}
	}
	applyTo, err := manageThemeNormalizeApplyTo(asString(args["apply_to"]))
	if err != nil {
		return "", err
	}
	if applyTo == "none" {
		return "", errors.New("manage-theme set cannot use apply_to=none; use apply_to=workspace, account, or global")
	}
	workspacePath := strings.TrimSpace(asString(args["workspace_path"]))
	if workspacePath == "" && applyTo == "" {
		workspacePath = strings.TrimSpace(scope.PrimaryPath)
	}
	if applyTo == "workspace" && workspacePath == "" {
		workspacePath = strings.TrimSpace(scope.PrimaryPath)
	}
	if workspacePath != "" && applyTo != "account" && applyTo != "global" {
		if r.themeWorkspace == nil {
			return "", errors.New("manage-theme workspace service is not configured")
		}
		if !scope.Principal.Valid() {
			return "", identity.ErrPrincipalRequired
		}
		scopeInfo, err := r.themeWorkspace.ScopeForPathForPrincipal(scope.Principal, workspacePath)
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
			resolution, err := r.themeWorkspace.SetThemeIDForPrincipal(scope.Principal, workspacePath, themeID)
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

	if applyTo == "workspace" {
		return "", errors.New("manage-theme set apply_to=workspace requires workspace_path or an active workspace scope")
	}
	beforeThemeID := manageThemeNormalizeID(settings.Theme.ActiveID)
	target := "global_theme"
	if r.manageThemeAccountScopeID(scope) != "" {
		target = "account_theme"
	}
	change := map[string]any{
		"kind":      "theme_change",
		"target":    target,
		"operation": "set",
		"before":    map[string]any{"theme_id": beforeThemeID},
		"after":     map[string]any{"theme_id": themeID},
	}
	if confirm {
		settings.Theme.ActiveID = manageThemeStringFallback(themeID, sharedtheme.DefaultThemeID())
		saved, err := r.manageThemeSaveSettings(scope, settings)
		if err != nil {
			return "", err
		}
		response := map[string]any{
			"status":               "ok",
			"action":               "set",
			"applied":              true,
			"global_theme_id":      manageThemeNormalizeID(saved.Theme.ActiveID),
			"account_scope_id":     r.manageThemeAccountScopeID(scope),
			"change":               change,
			"path_id":              toolPathID("manage-theme"),
			"summary":              fmt.Sprintf("applied %s %s", strings.ReplaceAll(target, "_", " "), manageThemeNormalizeID(saved.Theme.ActiveID)),
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
		"summary":              fmt.Sprintf("proposed %s %s", strings.ReplaceAll(target, "_", " "), manageThemeStringFallback(themeID, sharedtheme.DefaultThemeID())),
		"details_truncated":    false,
		"prompt_injection_tag": "tool_output_untrusted",
		"safety":               buildUntrustedSafety(manageThemeSafetyText(change)),
	}
	return manageThemeEncodeResponse(response)
}

func (r *Runtime) manageThemeAccountScopeID(scope WorkspaceScope) string {
	return r.agentAccountScopeID(scope)
}

type manageThemeApplyPlan struct {
	target        string
	themeID       string
	workspacePath string
	change        map[string]any
}

func (p manageThemeApplyPlan) responseTarget() string {
	return strings.TrimSpace(p.target)
}

func (r *Runtime) manageThemeBuildApplyPlan(scope WorkspaceScope, args map[string]any, settings uisettings.UISettings, themeID string, defaultWorkspace bool) (manageThemeApplyPlan, error) {
	applyTo, err := manageThemeNormalizeApplyTo(asString(args["apply_to"]))
	if err != nil {
		return manageThemeApplyPlan{}, err
	}
	if applyTo == "" && defaultWorkspace {
		if strings.TrimSpace(scope.PrimaryPath) != "" || strings.TrimSpace(asString(args["workspace_path"])) != "" {
			applyTo = "workspace"
		}
	}
	if applyTo == "" || applyTo == "none" {
		return manageThemeApplyPlan{target: "none", themeID: themeID}, nil
	}
	if applyTo == "global" || applyTo == "account" {
		beforeThemeID := manageThemeNormalizeID(settings.Theme.ActiveID)
		target := "global_theme"
		if r.manageThemeAccountScopeID(scope) != "" {
			target = "account_theme"
		}
		return manageThemeApplyPlan{
			target:  applyTo,
			themeID: themeID,
			change: map[string]any{
				"kind":      "theme_change",
				"target":    target,
				"operation": "set",
				"before":    map[string]any{"theme_id": beforeThemeID},
				"after":     map[string]any{"theme_id": themeID},
			},
		}, nil
	}
	workspacePath := strings.TrimSpace(asString(args["workspace_path"]))
	if workspacePath == "" {
		workspacePath = strings.TrimSpace(scope.PrimaryPath)
	}
	if workspacePath == "" {
		return manageThemeApplyPlan{}, errors.New("manage-theme create apply_to=workspace requires workspace_path or an active workspace scope")
	}
	if r.themeWorkspace == nil {
		return manageThemeApplyPlan{}, errors.New("manage-theme workspace service is not configured")
	}
	if !scope.Principal.Valid() {
		return manageThemeApplyPlan{}, identity.ErrPrincipalRequired
	}
	scopeInfo, err := r.themeWorkspace.ScopeForPathForPrincipal(scope.Principal, workspacePath)
	if err != nil {
		return manageThemeApplyPlan{}, err
	}
	resolvedWorkspacePath := strings.TrimSpace(scopeInfo.WorkspacePath)
	beforeThemeID := manageThemeNormalizeID(scopeInfo.ThemeID)
	return manageThemeApplyPlan{
		target:        "workspace",
		themeID:       themeID,
		workspacePath: workspacePath,
		change: map[string]any{
			"kind":           "theme_change",
			"target":         "workspace_theme",
			"operation":      "set",
			"workspace_path": resolvedWorkspacePath,
			"before":         map[string]any{"workspace_path": resolvedWorkspacePath, "theme_id": beforeThemeID},
			"after":          map[string]any{"workspace_path": resolvedWorkspacePath, "theme_id": themeID},
		},
	}, nil
}

func manageThemeNormalizeApplyTo(raw string) (string, error) {
	switch strings.ToLower(strings.TrimSpace(raw)) {
	case "", "default":
		return "", nil
	case "none", "custom", "save", "saved":
		return "none", nil
	case "workspace", "active_workspace", "current_workspace":
		return "workspace", nil
	case "account", "settings":
		return "account", nil
	case "global":
		return "global", nil
	default:
		return "", fmt.Errorf("manage-theme apply_to must be one of none, workspace, account, or global; got %q", raw)
	}
}

func manageThemeCombinedChange(operation string, themeChange, applyChange map[string]any) map[string]any {
	if applyChange == nil {
		return themeChange
	}
	return map[string]any{
		"kind":      "theme_change",
		"operation": operation + "_and_apply",
		"changes":   []map[string]any{themeChange, applyChange},
	}
}

func manageThemeUpsertSummary(prefix, action, themeID string, applyPlan manageThemeApplyPlan) string {
	base := fmt.Sprintf("%s %s for custom theme %s", prefix, action, themeID)
	switch applyPlan.target {
	case "workspace":
		return fmt.Sprintf("%s and workspace theme for %s", base, strings.TrimSpace(applyPlan.workspacePath))
	case "account", "global":
		return fmt.Sprintf("%s and %s theme", base, applyPlan.target)
	default:
		return base
	}
}

func manageThemeBatchResponse(status string, created []uisettings.ThemeCustomTheme, names []string, change map[string]any) map[string]any {
	return map[string]any{
		"status":               status,
		"action":               "create_batch",
		"generated_count":      len(created),
		"generated_names":      append([]string(nil), names...),
		"themes":               manageThemeCustomThemeMaps(created),
		"change":               change,
		"path_id":              toolPathID("manage-theme"),
		"details_truncated":    false,
		"prompt_injection_tag": "tool_output_untrusted",
		"safety":               buildUntrustedSafety(manageThemeSafetyText(change)),
	}
}

func manageThemeNames(themes []uisettings.ThemeCustomTheme) []string {
	names := make([]string, 0, len(themes))
	for _, theme := range themes {
		names = append(names, strings.TrimSpace(theme.Name))
	}
	return names
}

func manageThemeBatchSummary(prefix string, names []string, applyPlan manageThemeApplyPlan) string {
	summary := fmt.Sprintf("%s %d themes: %s", prefix, len(names), strings.Join(names, ", "))
	switch applyPlan.target {
	case "workspace":
		return fmt.Sprintf("%s; applied %s to workspace", summary, applyPlan.themeID)
	case "account", "global":
		return fmt.Sprintf("%s; applied %s to %s", summary, applyPlan.themeID, applyPlan.target)
	default:
		return summary
	}
}

func manageThemePaletteSchema() map[string]any {
	properties := map[string]any{}
	for _, key := range []string{
		"background", "panel", "element", "border", "border_active", "text", "text_muted",
		"primary", "secondary", "accent", "success", "warning", "error", "prompt",
		"prompt_cursor_bg", "prompt_cursor_fg", "code_background", "code_text", "code_keyword",
		"code_type", "code_string", "code_number", "code_comment", "code_function", "code_operator",
	} {
		properties[key] = map[string]any{"type": "string"}
	}
	return map[string]any{"type": "object", "properties": properties, "additionalProperties": false}
}

func manageThemeCreateUsage() string {
	return "manage-theme create requires theme_id or content.id, name or content.name, and content.palette object (or base_theme_id for an inherited palette); to create and apply in one call use apply_to=workspace|account|global|none with confirm=true"
}

func manageThemeActionContracts(scope WorkspaceScope) map[string]any {
	activeWorkspace := strings.TrimSpace(scope.PrimaryPath)
	return map[string]any{
		"create_batch": map[string]any{
			"required": []string{"action=create_batch", "themes with 1 to 8 items", "each item: id or theme_id, name, and palette (or base_theme_id)"},
			"optional": []string{"apply_theme_id with apply_to=workspace|account|global", "workspace_path", "confirm"},
			"safety":   "confirm=false previews the complete batch; confirm=true persists all themes in one settings mutation",
		},
		"create": map[string]any{
			"required": []string{"action=create", "theme_id or content.id", "name or content.name", "content.palette object (or base_theme_id for inherited palette)"},
			"optional": []string{"base_theme_id", "apply_to=workspace|account|global|none", "workspace_path", "confirm"},
			"default_apply_to": map[string]any{
				"with_active_workspace":    "workspace",
				"without_active_workspace": "none",
			},
			"example": map[string]any{
				"action":   "create",
				"theme_id": "lava",
				"name":     "Lava",
				"content": map[string]any{
					"palette": map[string]any{"background": "#120808", "text": "#ffe8d6", "primary": "#ff6b35"},
				},
				"apply_to": "workspace",
				"confirm":  true,
			},
		},
		"set": map[string]any{
			"required": []string{"action=set", "theme_id"},
			"optional": []string{"apply_to=workspace|account|global", "workspace_path", "confirm"},
			"default_scope": map[string]any{
				"with_active_workspace":    "workspace",
				"without_active_workspace": "account/global settings",
			},
		},
		"update": map[string]any{
			"required": []string{"action=update", "theme_id or content.id"},
			"optional": []string{"name or content.name", "content.palette", "apply_to=workspace|account|global|none", "workspace_path", "confirm"},
		},
		"delete": map[string]any{
			"required": []string{"action=delete", "theme_id", "confirm to apply"},
		},
		"workspace_default": map[string]any{
			"active_workspace_path": activeWorkspace,
			"note":                  "workspace-scoped create/set default to the active workspace when available; pass apply_to=account/global to change account settings instead",
		},
	}
}

func (r *Runtime) manageThemeSettings(scope WorkspaceScope) (uisettings.UISettings, error) {
	if r == nil || r.uiSettings == nil {
		return uisettings.UISettings{}, errors.New("manage-theme ui settings service is not configured")
	}
	accountScopeID := r.manageThemeAccountScopeID(scope)
	if accountScopeID != "" {
		settings, err := r.uiSettings.GetForAccount(accountScopeID)
		if err != nil {
			return uisettings.UISettings{}, fmt.Errorf("manage-theme read account settings: %w", err)
		}
		return settings, nil
	}
	settings, err := r.uiSettings.Get()
	if err != nil {
		return uisettings.UISettings{}, fmt.Errorf("manage-theme read settings failed: %w", err)
	}
	return settings, nil
}

func (r *Runtime) manageThemeSaveSettings(scope WorkspaceScope, settings uisettings.UISettings) (uisettings.UISettings, error) {
	if r == nil || r.uiSettings == nil {
		return uisettings.UISettings{}, errors.New("manage-theme ui settings service is not configured")
	}
	accountScopeID := r.manageThemeAccountScopeID(scope)
	if accountScopeID == "" {
		return uisettings.UISettings{}, errors.New("manage-theme requires an account-scoped principal to save theme settings")
	}
	return r.uiSettings.SetForAccount(accountScopeID, settings)
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
	if !scope.Principal.Valid() {
		return nil, identity.ErrPrincipalRequired
	}
	info, err := r.themeWorkspace.ScopeForPathForPrincipal(scope.Principal, workspacePath)
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
