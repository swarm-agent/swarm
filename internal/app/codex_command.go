package app

import (
	"context"
	"fmt"
	"strings"
	"time"

	"swarm-refactor/swarmtui/internal/model"
)

const codexServiceTierFast = "fast"

func (a *App) handleCodexCommand(args []string) {
	a.home.ClearCommandOverlay()

	providerID := normalizeModelProviderID(a.homeModel.ModelProvider)
	modelName := strings.TrimSpace(a.homeModel.ModelName)
	thinking := strings.TrimSpace(a.homeModel.ThinkingLevel)
	serviceTier := strings.TrimSpace(a.homeModel.ServiceTier)
	contextMode := strings.TrimSpace(a.homeModel.ContextMode)
	sessionID := ""
	if a.route == "chat" && a.chat != nil {
		sessionID = strings.TrimSpace(a.chat.SessionID())
		providerID, modelName, thinking, serviceTier, contextMode = a.chat.ModelState()
		providerID = normalizeModelProviderID(providerID)
		modelName = strings.TrimSpace(modelName)
		thinking = strings.TrimSpace(thinking)
		serviceTier = strings.TrimSpace(serviceTier)
		contextMode = strings.TrimSpace(contextMode)
	}

	if len(args) == 0 || strings.EqualFold(strings.TrimSpace(args[0]), "status") || strings.EqualFold(strings.TrimSpace(args[0]), "help") {
		a.showCodexCommandStatus(sessionID)
		return
	}
	if a.api == nil {
		a.home.SetStatus("codex update failed: model API is unavailable")
		return
	}
	if providerID != "codex" {
		a.home.SetStatus(fmt.Sprintf("/codex requires the current model provider to be codex (current: %s)", emptyFallback(providerID, "unset")))
		return
	}
	if !strings.EqualFold(modelName, "gpt-5.4") {
		a.home.SetStatus(fmt.Sprintf("/codex runtime settings are only supported on Codex gpt-5.4 (current model: %s)", emptyFallback(modelName, "unset")))
		return
	}

	toggleFast := false
	for _, rawArg := range args {
		switch strings.ToLower(strings.TrimSpace(rawArg)) {
		case "":
			continue
		case "fast":
			toggleFast = true
		default:
			a.home.SetStatus("usage: /codex [status|fast]")
			return
		}
	}
	if !toggleFast {
		a.home.SetStatus("usage: /codex [status|fast]")
		return
	}
	if toggleFast {
		if strings.EqualFold(serviceTier, codexServiceTierFast) {
			serviceTier = ""
		} else {
			serviceTier = codexServiceTierFast
		}
	}

	ctx, cancel := context.WithTimeout(context.Background(), 6*time.Second)
	defer cancel()
	if sessionID != "" {
		resolved, err := a.api.SetSessionPreference(ctx, sessionID, map[string]any{
			"provider":     providerID,
			"model":        modelName,
			"thinking":     thinking,
			"service_tier": serviceTier,
			"context_mode": contextMode,
		})
		if err != nil {
			a.home.SetStatus(fmt.Sprintf("/codex update failed: %v", err))
			return
		}
		if a.chat != nil && strings.TrimSpace(a.chat.SessionID()) == sessionID {
			a.chat.SetModelState(
				strings.TrimSpace(resolved.Preference.Provider),
				strings.TrimSpace(resolved.Preference.Model),
				strings.TrimSpace(resolved.Preference.Thinking),
				strings.TrimSpace(resolved.Preference.ServiceTier),
				strings.TrimSpace(resolved.Preference.ContextMode),
			)
			a.chat.SetContextWindow(resolved.ContextWindow)
		}
		a.updateHomeSessionPreference(sessionID, resolved.Preference)
		a.home.ClearCommandOverlay()
		a.home.SetStatus(codexCommandStatusMessage(
			strings.TrimSpace(resolved.Preference.Provider),
			strings.TrimSpace(resolved.Preference.Model),
			strings.TrimSpace(resolved.Preference.ServiceTier),
			strings.TrimSpace(resolved.Preference.ContextMode),
			resolved.ContextWindow,
		))
		if len(args) == 0 || strings.EqualFold(strings.TrimSpace(args[0]), "status") || strings.EqualFold(strings.TrimSpace(args[0]), "help") {
			a.showCodexCommandStatus(sessionID)
		}
		a.queueReload(false)
		return
	}

	resolved, err := a.api.SetModel(ctx, providerID, modelName, thinking, serviceTier, contextMode)
	if err != nil {
		a.home.SetStatus(fmt.Sprintf("/codex update failed: %v", err))
		return
	}
	next := applyHomeModelResolved(a.homeModel, resolved)
	a.applyHomeModel(next)
	if a.home.ModelsModalVisible() {
		a.refreshModelsModalData(providerID, "Refreshing model manager...")
	}
	a.home.ClearCommandOverlay()
	a.home.SetStatus(codexCommandStatusMessageFromHome(next))
	if len(args) == 0 || strings.EqualFold(strings.TrimSpace(args[0]), "status") || strings.EqualFold(strings.TrimSpace(args[0]), "help") {
		a.showCodexCommandStatus("")
	}
	a.queueReload(false)
}

func (a *App) showCodexCommandStatus(sessionID string) {
	if a == nil {
		return
	}
	provider := strings.TrimSpace(a.homeModel.ModelProvider)
	modelName := strings.TrimSpace(a.homeModel.ModelName)
	serviceTier := strings.TrimSpace(a.homeModel.ServiceTier)
	contextMode := strings.TrimSpace(a.homeModel.ContextMode)
	contextWindow := a.homeModel.ContextWindow
	if a.chat != nil && strings.TrimSpace(a.chat.SessionID()) == strings.TrimSpace(sessionID) {
		provider, modelName, _, serviceTier, contextMode = a.chat.ModelState()
		provider = strings.TrimSpace(provider)
		modelName = strings.TrimSpace(modelName)
		serviceTier = strings.TrimSpace(serviceTier)
		contextMode = strings.TrimSpace(contextMode)
		if window := a.chat.ContextWindow(); window > 0 {
			contextWindow = window
		}
	}
	lines := []string{
		fmt.Sprintf("provider: %s", emptyFallback(provider, "unset")),
		fmt.Sprintf("model: %s", emptyFallback(model.DisplayModelLabel(provider, modelName, serviceTier, contextMode), "unset")),
		fmt.Sprintf("fast: %s", map[bool]string{true: "on", false: "off"}[model.CodexFastEnabled(provider, modelName, serviceTier)]),
		fmt.Sprintf("1m: %s", map[bool]string{true: "on", false: "off"}[model.Codex1MEnabled(provider, modelName, contextMode)]),
		fmt.Sprintf("effective context window: %d", contextWindow),
		"usage: /codex [status|fast]",
		"/codex fast toggles Fast on/off for Codex gpt-5.4 only",
		"select gpt-5.4 (1m) in /models to use 1M context",
	}
	a.home.SetCommandOverlay(lines)
	a.home.SetStatus("codex runtime")
}

func codexCommandStatusMessage(provider, modelName, serviceTier, contextMode string, contextWindow int) string {
	serviceTier = emptyFallback(strings.TrimSpace(serviceTier), "default")
	modelLabel := emptyFallback(model.DisplayModelLabel(provider, modelName, serviceTier, contextMode), "unset")
	return fmt.Sprintf("codex runtime updated: model=%s service=%s (%d)", modelLabel, serviceTier, contextWindow)
}

func codexCommandStatusMessageFromHome(homeModel model.HomeModel) string {
	return codexCommandStatusMessage(homeModel.ModelProvider, homeModel.ModelName, homeModel.ServiceTier, homeModel.ContextMode, homeModel.ContextWindow)
}
