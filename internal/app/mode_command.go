package app

import (
	"context"
	"fmt"
	"strings"

	"swarm-refactor/swarmtui/internal/ui"
)

func (a *App) applyDefaultNewSessionModeSetting(mode string) {
	mode = normalizeAppSessionMode(mode)
	if mode != "auto" && mode != "plan" {
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
