package app

import (
	"context"
	"fmt"
	"strings"

	"swarm-refactor/swarmtui/internal/ui"
)

func (a *App) handleModeCommand(args []string) {
	a.home.ClearCommandOverlay()
	if a.api == nil {
		a.home.SetStatus("mode settings API unavailable")
		return
	}
	if len(args) == 0 {
		a.home.SetCommandOverlay(a.modeStatusLines())
		a.home.SetStatus("use /mode plan or /mode action to toggle Plan for new chats")
		return
	}

	if strings.EqualFold(strings.TrimSpace(args[0]), "status") {
		a.home.SetCommandOverlay(a.modeStatusLines())
		a.home.SetStatus("Plan: " + modePlanLabel(a.config.Chat.DefaultNewSessionMode))
		return
	}

	rawMode := strings.ToLower(strings.TrimSpace(args[0]))
	if rawMode == "action" {
		rawMode = "auto"
	}
	sub := normalizeAppSessionMode(rawMode)
	switch sub {
	case "auto", "plan":
		a.applyDefaultNewSessionModeSetting(sub)
	default:
		a.home.SetCommandOverlay(a.modeStatusLines())
		a.home.SetStatus("usage: /mode [action|plan|status]")
	}
}

func (a *App) applyDefaultNewSessionModeSetting(mode string) {
	mode = normalizeAppSessionMode(mode)
	if mode != "auto" && mode != "plan" {
		a.home.SetCommandOverlay(a.modeStatusLines())
		a.home.SetStatus("default new chat mode must be auto or plan")
		return
	}

	a.config.Chat.DefaultNewSessionMode = mode
	if a.home != nil {
		a.home.SetSessionMode(mode)
	}
	if err := saveDefaultNewSessionModeSetting(a.api, mode); err != nil {
		a.home.SetStatus(fmt.Sprintf("default new chat mode %s (settings save failed: %v)", mode, err))
		return
	}
	a.syncPrimedV3ChatFromHomeDraft()
	a.showToast(ui.ToastSuccess, "default new chat mode set to "+mode)
}

func (a *App) modeStatusLines() []string {
	lines := []string{
		"Plan: " + modePlanLabel(a.config.Chat.DefaultNewSessionMode),
		"/mode action   turn Plan off for new chats",
		"/mode plan   turn Plan on for new chats",
		"/mode status   show the current default",
		"note: this only affects new chats; existing chats can still enter/exit plan mode per session",
	}
	if strings.TrimSpace(a.settingsLabel) != "" {
		lines = append(lines, "settings: "+a.settingsLabel)
	}
	return lines
}

func modePlanLabel(mode string) string {
	if strings.EqualFold(strings.TrimSpace(mode), "plan") {
		return "on"
	}
	return "off"
}

func (a *App) syncDefaultNewSessionModeFromServer() {
	if a == nil || a.api == nil {
		return
	}
	ctx, cancel := context.WithTimeout(context.Background(), uiSettingsRequestLimit)
	defer cancel()
	settings, err := a.api.GetUISettings(ctx)
	if err != nil {
		return
	}
	mode := emptyFallback(strings.TrimSpace(settings.Chat.DefaultNewSessionMode), "auto")
	a.config.Chat.DefaultNewSessionMode = mode
	if a.home != nil {
		a.home.SetSessionMode(mode)
	}
}
