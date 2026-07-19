package app

import (
	"context"
	"fmt"
	"sort"
	"strings"
	"time"

	"swarm-refactor/swarmtui/internal/client"
	"swarm-refactor/swarmtui/internal/model"
	"swarm-refactor/swarmtui/internal/ui"
)

func (a *App) handleModelsCommand(args []string) {
	if len(args) == 0 || strings.EqualFold(args[0], "open") || strings.EqualFold(args[0], "manage") || strings.EqualFold(args[0], "list") {
		a.openModelsModal("")
		return
	}
	a.openModelsModal(normalizeModelProviderID(args[0]))
}

func (a *App) cycleThinkingLevel() {
	setStatus := func(message string) {
		if a.route == "chat" && a.chat != nil {
			a.chat.SetStatus(message)
			return
		}
		a.home.SetStatus(message)
	}

	if a.api == nil {
		setStatus("model API is unavailable")
		return
	}

	homeProvider, homeModelName, homeThinking, homeServiceTier, homeContextMode := a.home.ModelState()
	providerID := normalizeModelProviderID(homeProvider)
	modelID := strings.TrimSpace(homeModelName)
	serviceTier := strings.TrimSpace(homeServiceTier)
	contextMode := strings.TrimSpace(homeContextMode)
	if a.route == "chat" && a.chat != nil {
		providerID, modelID, _, serviceTier, contextMode = a.chat.ModelState()
		providerID = normalizeModelProviderID(providerID)
		modelID = strings.TrimSpace(modelID)
		serviceTier = strings.TrimSpace(serviceTier)
		contextMode = strings.TrimSpace(contextMode)
	}
	if providerID == "" || modelID == "" {
		setStatus("thinking cycle unavailable: set a model first in /models")
		return
	}

	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	record, err := a.modelCatalogRecord(ctx, providerID, modelID)
	if err != nil {
		setStatus(fmt.Sprintf("thinking options unavailable: %v", err))
		return
	}
	current := normalizeModelThinkingLevel(homeThinking)
	if a.route == "chat" && a.chat != nil {
		_, _, sessionThinking, _, _ := a.chat.ModelState()
		current = normalizeModelThinkingLevel(sessionThinking)
	}
	if current == "" {
		current = normalizeModelThinkingLevel(record.DefaultThinking)
	}
	nextThinking, ok := nextThinkingLevel(record.ThinkingOptions, current)
	if !ok {
		setStatus("thinking cycle unavailable: model snapshot has no thinking options")
		return
	}
	if a.route == "chat" && a.chat != nil {
		sessionID := strings.TrimSpace(a.chat.SessionID())
		if sessionID == "" {
			setStatus("session id is unavailable")
			return
		}
		resolved, err := a.api.SetSessionPreference(ctx, sessionID, map[string]any{
			"provider":     providerID,
			"model":        modelID,
			"thinking":     nextThinking,
			"service_tier": serviceTier,
			"context_mode": contextMode,
		})
		if err != nil {
			setStatus(fmt.Sprintf("thinking update failed: %v", err))
			return
		}
		providerID = strings.TrimSpace(resolved.Preference.Provider)
		modelID = strings.TrimSpace(resolved.Preference.Model)
		nextThinking = strings.TrimSpace(resolved.Preference.Thinking)
		serviceTier = strings.TrimSpace(resolved.Preference.ServiceTier)
		contextMode = strings.TrimSpace(resolved.Preference.ContextMode)
		contextWindow := resolved.ContextWindow
		a.chat.SetModelState(providerID, modelID, nextThinking, serviceTier, contextMode)
		a.chat.SetContextWindow(contextWindow)
		setStatus(fmt.Sprintf("thinking set: %s", nextThinking))
		a.showToast(ui.ToastInfo, fmt.Sprintf("thinking set to %s", nextThinking))
		return
	}
	if _, err := a.api.SetModel(ctx, providerID, modelID, nextThinking, serviceTier, contextMode); err != nil {
		setStatus(fmt.Sprintf("thinking update failed: %v", err))
		return
	}

	next := a.currentHomeModel()
	next.ThinkingLevel = nextThinking
	next.QuickActions = homeQuickActions(next)
	a.applyHomeModel(next)

	if a.chat != nil {
		a.chat.SetModelState(next.ModelProvider, modelID, nextThinking, next.ServiceTier, next.ContextMode)
	}

	setStatus(fmt.Sprintf("thinking set: %s", nextThinking))
	a.showToast(ui.ToastInfo, fmt.Sprintf("thinking set to %s", nextThinking))
	if a.home.ModelsModalVisible() {
		a.refreshModelsModalData(providerID, "")
	}
	a.queueReload(false)
}

func (a *App) openModelsModal(providerHint string) {
	a.home.ClearCommandOverlay()
	a.home.HideSessionsModal()
	a.home.HideAuthModal()
	a.home.HideWorkspaceModal()
	a.home.HideWorktreesModal()
	a.home.HideAgentsModal()
	a.home.HideVoiceModal()
	a.home.HideThemeModal()
	a.home.HideKeybindsModal()
	a.home.ShowModelsModal()
	a.refreshModelsModalData(providerHint, "Loading model manager...")
}

func (a *App) currentModelPreferenceState() (string, string, string, string, string, string) {
	homeProvider, homeModelName, homeThinking, homeServiceTier, homeContextMode := a.home.ModelState()
	providerID := normalizeModelProviderID(homeProvider)
	modelID := strings.TrimSpace(homeModelName)
	thinking := normalizeModelThinkingLevel(homeThinking)
	serviceTier := strings.TrimSpace(homeServiceTier)
	contextMode := strings.TrimSpace(homeContextMode)
	sessionID := ""
	if a.route == "chat" && a.chat != nil {
		sessionID = strings.TrimSpace(a.chat.SessionID())
		sessionProvider, sessionModel, sessionThinking, sessionServiceTier, sessionContextMode := a.chat.ModelState()
		if normalized := normalizeModelProviderID(sessionProvider); normalized != "" {
			providerID = normalized
		}
		if trimmed := strings.TrimSpace(sessionModel); trimmed != "" {
			modelID = trimmed
		}
		if normalized := normalizeModelThinkingLevel(sessionThinking); normalized != "" {
			thinking = normalized
		}
		serviceTier = strings.TrimSpace(sessionServiceTier)
		contextMode = strings.TrimSpace(sessionContextMode)
	}
	return providerID, modelID, thinking, serviceTier, contextMode, sessionID
}

func (a *App) refreshModelsModalData(providerHint, statusHint string) {
	if !a.home.ModelsModalVisible() {
		return
	}
	if strings.TrimSpace(statusHint) != "" {
		a.home.SetModelsModalStatus(statusHint)
	}
	a.home.SetModelsModalLoading(true)

	if a.api == nil {
		a.home.SetModelsModalLoading(false)
		a.home.SetModelsModalError("model API is unavailable")
		return
	}

	ctx, cancel := context.WithTimeout(context.Background(), 8*time.Second)
	defer cancel()

	activeProvider, activeModel, activeThinking, _, activeContextMode, _ := a.currentModelPreferenceState()
	resolved := a.resolveProviderModelData(ctx, []string{providerHint, activeProvider}, 2000, 1200)

	providers := make([]ui.ModelsModalProvider, 0, len(resolved.ProviderIDs))
	for _, providerID := range resolved.ProviderIDs {
		provider := ui.ModelsModalProvider{
			ID:       providerID,
			Ready:    false,
			Runnable: false,
		}
		if status, ok := resolved.ProviderStatuses[providerID]; ok {
			provider.Ready = status.Ready
			provider.Runnable = status.Runnable
			switch {
			case status.Runnable:
				provider.Reason = ""
			case status.Ready:
				provider.Reason = strings.TrimSpace(status.RunReason)
			default:
				provider.Reason = strings.TrimSpace(status.Reason)
			}
		}
		providers = append(providers, provider)
	}

	entries := make([]ui.ModelsModalEntry, 0, 1024)
	for _, providerID := range resolved.ProviderIDs {
		status, hasStatus := resolved.ProviderStatuses[providerID]
		if !hasStatus || !status.Ready {
			continue
		}
		models := resolved.ModelsByProvider[providerID]
		for _, modelID := range models {
			modelID = strings.TrimSpace(modelID)
			key := modelEntryKey(providerID, modelID)
			if key == "" {
				continue
			}
			entry := ui.ModelsModalEntry{
				Provider: providerID,
				Model:    modelID,
			}
			if reasoning, ok := resolved.ReasoningByKey[key]; ok {
				entry.Reasoning = reasoning
			} else {
				entry.Reasoning = true
			}
			if record, ok := resolved.CatalogByKey[key]; ok {
				entry.ContextWindow = record.ContextWindow
				entry.MaxOutputTokens = record.MaxOutputTokens
				entry.Source = strings.TrimSpace(record.Source)
				entry.Reasoning = record.Reasoning
				entry.ThinkingOptions = append([]string(nil), record.ThinkingOptions...)
				entry.DefaultThinking = strings.TrimSpace(record.DefaultThinking)
				entry.ServiceTiers = append([]string(nil), record.ServiceTiers...)
				entry.DefaultServiceTier = strings.TrimSpace(record.DefaultServiceTier)
				entry.UpdatedAt = record.FetchedAt
			}
			if favorite, ok := resolved.FavoritesByKey[key]; ok {
				entry.Favorite = true
				entry.FavoriteLabel = strings.TrimSpace(favorite.Label)
				entry.FavoriteThinking = normalizeModelThinkingLevel(favorite.Thinking)
				entry.AddedAt = favorite.CreatedAt
				if favorite.UpdatedAt > entry.UpdatedAt {
					entry.UpdatedAt = favorite.UpdatedAt
				}
				if strings.TrimSpace(entry.Source) == "" {
					entry.Source = "favorite"
				}
			}
			if strings.TrimSpace(entry.Source) == "" {
				entry.Source = "catalog"
			}
			entries = append(entries, entry)
			if model.SupportsCodex1MMode(providerID, modelID) {
				entry1M := entry
				entry1M.ContextMode = model.CodexContextMode1M
				entry1M.ContextWindow = model.CodexContextWindow(providerID, modelID, entry1M.ContextMode, entry.ContextWindow)
				entries = append(entries, entry1M)
			}
		}
	}

	sort.SliceStable(entries, func(i, j int) bool {
		leftProvider := normalizeModelProviderID(entries[i].Provider)
		rightProvider := normalizeModelProviderID(entries[j].Provider)
		if leftProvider != rightProvider {
			return leftProvider < rightProvider
		}
		if entries[i].Favorite != entries[j].Favorite {
			return entries[i].Favorite
		}
		leftAdded := modelEntrySortTimestamp(entries[i])
		rightAdded := modelEntrySortTimestamp(entries[j])
		if leftAdded != rightAdded {
			return leftAdded > rightAdded
		}
		return modelIDLessForProvider(leftProvider, entries[i].Model, entries[j].Model)
	})

	a.home.SetModelsModalData(
		providers,
		entries,
		activeProvider,
		activeModel,
		contextModeForModelEntry(activeProvider, activeModel, activeContextMode),
		activeThinking,
	)
	a.home.SetModelsModalLoading(false)

	warnings := uniqueNonEmpty(resolved.Warnings)
	authPendingProviders := 0
	for _, provider := range providers {
		if !provider.Ready {
			authPendingProviders++
		}
	}
	if len(entries) == 0 && len(warnings) > 0 {
		a.home.SetModelsModalError(strings.Join(warnings, "; "))
		return
	}
	if len(entries) == 0 {
		if authPendingProviders > 0 {
			a.home.SetModelsModalStatus("auth required before models are listed. select provider and press Enter to add API key")
			return
		}
		if len(resolved.AuthOnlyProviderIDs) > 0 {
			a.home.SetModelsModalStatus("no runnable model providers. auth-only providers stay in /auth")
			return
		}
		a.home.SetModelsModalStatus("no models available yet; run /auth and then refresh")
		return
	}
	if len(warnings) > 0 {
		a.home.SetModelsModalStatus("models loaded with warnings: " + strings.Join(warnings, "; "))
		return
	}
	a.home.SetModelsModalStatus(fmt.Sprintf("models loaded: %d", len(entries)))
}

func (a *App) handleModelsModalAction(action ui.ModelsModalAction) {
	if !a.home.ModelsModalVisible() {
		return
	}

	switch action.Kind {
	case ui.ModelsModalActionRefresh:
		a.refreshModelsModalData(action.Provider, action.StatusHint)
	case ui.ModelsModalActionUpsertAPIKey:
		providerID := normalizeModelProviderID(action.Provider)
		apiKey := strings.TrimSpace(action.APIKey)
		if providerID == "" {
			a.home.SetModelsModalLoading(false)
			a.home.SetModelsModalError("provider is required for auth setup")
			return
		}
		if apiKey == "" {
			a.home.SetModelsModalLoading(false)
			a.home.SetModelsModalError("API key is required")
			return
		}
		ctx, cancel := context.WithTimeout(context.Background(), 6*time.Second)
		defer cancel()
		record, err := a.api.UpsertAuthCredential(ctx, client.AuthCredentialUpsertRequest{
			Provider: providerID,
			Type:     "api",
			Label:    strings.TrimSpace(action.KeyLabel),
			APIKey:   apiKey,
			Active:   action.SetActive,
		})
		if err != nil {
			a.home.SetModelsModalLoading(false)
			a.home.SetModelsModalError(fmt.Sprintf("save credential failed: %v", err))
			a.showToast(ui.ToastError, fmt.Sprintf("save credential failed: %v", err))
			return
		}
		a.home.SetModelsModalStatus(fmt.Sprintf("API key saved for %s", record.Provider))
		a.showToast(ui.ToastSuccess, fmt.Sprintf("API key saved for %s", record.Provider))
		a.refreshModelsModalData(providerID, "")
		a.queueReload(false)
	case ui.ModelsModalActionSetActiveModel:
		providerID := normalizeModelProviderID(action.Provider)
		modelID := strings.TrimSpace(action.Model)
		if providerID == "" || modelID == "" {
			a.home.SetModelsModalLoading(false)
			a.home.SetModelsModalError("provider/model is required")
			return
		}
		_, _, activeThinking, serviceTier, _, sessionID := a.currentModelPreferenceState()
		contextMode := strings.TrimSpace(action.ContextMode)
		ctx, cancel := context.WithTimeout(context.Background(), 6*time.Second)
		defer cancel()
		catalog, err := a.modelCatalogRecord(ctx, providerID, modelID)
		if err != nil {
			a.home.SetModelsModalLoading(false)
			a.home.SetModelsModalError(err.Error())
			return
		}
		thinking := normalizeModelThinkingLevel(action.Thinking)
		if !stringInSliceFold(catalog.ThinkingOptions, thinking) {
			thinking = normalizeModelThinkingLevel(activeThinking)
		}
		if !stringInSliceFold(catalog.ThinkingOptions, thinking) {
			thinking = normalizeModelThinkingLevel(catalog.DefaultThinking)
		}
		if !stringInSliceFold(catalog.ThinkingOptions, thinking) {
			thinking = ""
		}
		if err := a.ensureProviderReadyForModelSet(ctx, providerID); err != nil {
			a.home.SetModelsModalLoading(false)
			a.home.SetModelsModalError(err.Error())
			return
		}
		messagePrefix := "active model"
		if sessionID != "" {
			messagePrefix = "chat model"
			resolved, err := a.api.SetSessionPreference(ctx, sessionID, map[string]any{
				"provider":     providerID,
				"model":        modelID,
				"thinking":     thinking,
				"service_tier": serviceTier,
				"context_mode": contextMode,
			})
			if err != nil {
				a.home.SetModelsModalLoading(false)
				a.home.SetModelsModalError(fmt.Sprintf("set model failed: %v", err))
				return
			}
			providerID = strings.TrimSpace(resolved.Preference.Provider)
			modelID = strings.TrimSpace(resolved.Preference.Model)
			thinking = strings.TrimSpace(resolved.Preference.Thinking)
			serviceTier = strings.TrimSpace(resolved.Preference.ServiceTier)
			contextMode = strings.TrimSpace(resolved.Preference.ContextMode)
			if a.chat != nil {
				a.chat.SetModelState(providerID, modelID, thinking, serviceTier, contextMode)
				a.chat.SetContextWindow(resolved.ContextWindow)
			}
		} else {
			if _, err := a.api.SetModel(ctx, providerID, modelID, thinking, serviceTier, contextMode); err != nil {
				a.home.SetModelsModalLoading(false)
				a.home.SetModelsModalError(fmt.Sprintf("set model failed: %v", err))
				return
			}
		}
		modelLabel := model.DisplayModelLabel(providerID, modelID, serviceTier, contextMode)
		message := fmt.Sprintf("%s: %s (%s)", messagePrefix, modelLabel, thinking)
		if action.FastOnly {
			message += fmt.Sprintf(" fast=%s", map[bool]string{true: "on", false: "off"}[model.CodexFastEnabled(providerID, modelID, serviceTier)])
		}
		if sessionID != "" && a.chat != nil && a.chat.RunInProgress() {
			a.showToast(ui.ToastInfo, "Current stream keeps running. Chat model changes apply after this stream finishes.")
		} else {
			a.showToast(ui.ToastSuccess, message)
		}
		if action.CloseAfter {
			a.home.HideModelsModal()
			a.home.HideCodexUsageModal()
			if sessionID != "" && a.chat != nil {
				a.chat.SetStatus(message)
			} else {
				a.home.SetStatus(message)
			}
		} else {
			a.home.SetModelsModalStatus(message)
			a.refreshModelsModalData(providerID, "")
		}
		a.queueReload(false)
	case ui.ModelsModalActionToggleFavorite:
		providerID := normalizeModelProviderID(action.Provider)
		modelID := strings.TrimSpace(action.Model)
		if providerID == "" || modelID == "" {
			a.home.SetModelsModalLoading(false)
			a.home.SetModelsModalError("favorite requires provider/model")
			return
		}

		ctx, cancel := context.WithTimeout(context.Background(), 6*time.Second)
		defer cancel()
		if action.Favorite {
			_, err := a.api.UpsertModelFavorite(ctx, client.ModelFavoriteUpsertRequest{
				Provider: providerID,
				Model:    modelID,
				Label:    strings.TrimSpace(action.Label),
				Thinking: normalizeModelThinkingLevel(action.Thinking),
			})
			if err != nil {
				a.home.SetModelsModalLoading(false)
				a.home.SetModelsModalError(fmt.Sprintf("add favorite failed: %v", err))
				return
			}
			a.home.SetModelsModalStatus(fmt.Sprintf("favorite saved: %s/%s", providerID, modelID))
		} else {
			if err := a.api.DeleteModelFavorite(ctx, providerID, modelID); err != nil {
				a.home.SetModelsModalLoading(false)
				a.home.SetModelsModalError(fmt.Sprintf("remove favorite failed: %v", err))
				return
			}
			a.home.SetModelsModalStatus(fmt.Sprintf("favorite removed: %s/%s", providerID, modelID))
		}
		a.refreshModelsModalData(providerID, "")
	default:
		a.home.SetModelsModalLoading(false)
	}
}

func normalizeModelProviderID(value string) string {
	value = strings.ToLower(strings.TrimSpace(value))
	switch value {
	case "openai":
		return "codex"
	case "github-copilot":
		return "copilot"
	default:
		return value
	}
}

func normalizeModelThinkingLevel(value string) string {
	value = strings.ToLower(strings.TrimSpace(value))
	if value == "x-high" {
		return "xhigh"
	}
	return value
}

func nextThinkingLevel(options []string, current string) (string, bool) {
	current = normalizeModelThinkingLevel(current)
	levels := make([]string, 0, len(options))
	for _, option := range options {
		option = normalizeModelThinkingLevel(option)
		if option != "" && !stringInSliceFold(levels, option) {
			levels = append(levels, option)
		}
	}
	if len(levels) == 0 {
		return "", false
	}
	for i, level := range levels {
		if level == current {
			return levels[(i+1)%len(levels)], true
		}
	}
	return levels[0], true
}

func stringInSliceFold(values []string, target string) bool {
	for _, value := range values {
		if strings.EqualFold(strings.TrimSpace(value), strings.TrimSpace(target)) {
			return true
		}
	}
	return false
}

func (a *App) modelCatalogRecord(ctx context.Context, providerID, modelID string) (client.ModelCatalogRecord, error) {
	records, err := a.api.ListModelCatalog(ctx, normalizeModelProviderID(providerID), 1200)
	if err != nil {
		return client.ModelCatalogRecord{}, err
	}
	for _, record := range records {
		if strings.EqualFold(strings.TrimSpace(record.Model), strings.TrimSpace(modelID)) {
			return record, nil
		}
	}
	return client.ModelCatalogRecord{}, fmt.Errorf("model catalog record not found for %s/%s", providerID, modelID)
}

func modelEntryKey(providerID, modelID string) string {
	providerID = normalizeModelProviderID(providerID)
	modelID = strings.TrimSpace(modelID)
	if providerID == "" || modelID == "" {
		return ""
	}
	return providerID + "/" + modelID
}

func stringInSlice(values []string, target string) bool {
	target = normalizeModelProviderID(target)
	for _, value := range values {
		if normalizeModelProviderID(value) == target {
			return true
		}
	}
	return false
}

func uniqueNonEmpty(values []string) []string {
	out := make([]string, 0, len(values))
	seen := make(map[string]struct{}, len(values))
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
	}
	return out
}

func modelEntrySortTimestamp(entry ui.ModelsModalEntry) int64 {
	if entry.AddedAt > 0 {
		return entry.AddedAt
	}
	return entry.UpdatedAt
}

func modelIDLessForProvider(_ string, left, right string) bool {
	leftModel := strings.ToLower(strings.TrimSpace(left))
	rightModel := strings.ToLower(strings.TrimSpace(right))
	return leftModel < rightModel
}

func contextModeForModelEntry(providerID, modelID, contextMode string) string {
	if model.Codex1MEnabled(providerID, modelID, contextMode) {
		return model.CodexContextMode1M
	}
	return ""
}

func (a *App) ensureProviderReadyForModelSet(ctx context.Context, providerID string) error {
	providerID = normalizeModelProviderID(providerID)
	if providerID == "" {
		return fmt.Errorf("provider is required")
	}
	providers, err := a.api.ListProviders(ctx)
	if err != nil {
		return fmt.Errorf("provider status unavailable: %w", err)
	}
	for _, provider := range providers {
		id := normalizeModelProviderID(provider.ID)
		if id != providerID {
			continue
		}
		if provider.Runnable {
			return nil
		}
		reason := strings.TrimSpace(provider.RunReason)
		if reason == "" {
			reason = strings.TrimSpace(provider.Reason)
		}
		if reason == "" {
			reason = "auth required or runner unavailable"
		}
		return fmt.Errorf("%s is not runnable (%s). run /auth", providerID, reason)
	}
	return fmt.Errorf("provider not found: %s", providerID)
}
