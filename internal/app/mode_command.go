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
		a.home.SetStatus("use /mode auto or /mode plan to set the default for new chats")
		return
	}

	if strings.EqualFold(strings.TrimSpace(args[0]), "status") {
		a.home.SetCommandOverlay(a.modeStatusLines())
		a.home.SetStatus("default new chat mode: " + a.config.Chat.DefaultNewSessionMode)
		return
	}

	sub := normalizeAppSessionMode(args[0])
	switch sub {
	case "auto", "plan":
		a.applyDefaultNewSessionModeSetting(sub)
	default:
		a.home.SetCommandOverlay(a.modeStatusLines())
		a.home.SetStatus("usage: /mode [auto|plan|status]")
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
	if err := saveAppConfig(a.api, a.config); err != nil {
		a.home.SetStatus(fmt.Sprintf("default new chat mode %s (settings save failed: %v)", mode, err))
		return
	}
	a.showToast(ui.ToastSuccess, "default new chat mode set to "+mode)
}

func (a *App) modeStatusLines() []string {
	lines := []string{
		"default new chat mode: " + emptyFallback(strings.TrimSpace(a.config.Chat.DefaultNewSessionMode), "auto"),
		"/mode auto   start new chats in auto by default",
		"/mode plan   start new chats in plan by default",
		"/mode status   show the current default",
		"note: this only affects new chats; existing chats can still enter/exit plan mode per session",
	}
	if strings.TrimSpace(a.settingsLabel) != "" {
		lines = append(lines, "settings: "+a.settingsLabel)
	}
	return lines
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
