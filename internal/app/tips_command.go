package app

import (
	"fmt"
	"strings"
)

func (a *App) handleTipsCommand(args []string) {
	if len(args) == 0 {
		a.applyTipsSetting(!a.config.Chat.ShowTips)
		return
	}

	switch strings.ToLower(strings.TrimSpace(args[0])) {
	case "toggle":
		a.applyTipsSetting(!a.config.Chat.ShowTips)
	case "on", "show", "true", "1":
		a.applyTipsSetting(true)
	case "off", "hide", "false", "0":
		a.applyTipsSetting(false)
	case "status":
		a.home.SetCommandOverlay(a.tipsStatusLines())
		a.home.SetStatus("home tips " + enabledLabel(a.config.Chat.ShowTips))
	default:
		a.home.SetCommandOverlay(a.tipsStatusLines())
		a.home.SetStatus("usage: /tips [on|off|toggle|status]")
	}
}

func (a *App) applyTipsSetting(enabled bool) {
	previous := a.config.Chat.ShowTips
	a.config.Chat.ShowTips = enabled
	a.home.SetHomeTipsVisible(enabled)
	a.home.SetCommandOverlay(a.tipsStatusLines())

	if err := saveTipsSetting(a.api, enabled); err != nil {
		a.config.Chat.ShowTips = previous
		a.home.SetHomeTipsVisible(previous)
		a.home.SetCommandOverlay(a.tipsStatusLines())
		a.home.SetStatus(fmt.Sprintf("home tips unchanged (%s): settings save failed: %v", enabledLabel(previous), err))
		return
	}
	a.home.SetStatus("home tips " + enabledLabel(enabled))
}

func (a *App) tipsStatusLines() []string {
	lines := []string{
		"home tips: " + enabledLabel(a.config.Chat.ShowTips),
		"/tips on   show rotating tips beneath Talk to Swarm",
		"/tips off   hide rotating home tips",
		"/tips toggle   switch between on and off",
		"/tips status   show the current setting",
	}
	if strings.TrimSpace(a.settingsLabel) != "" {
		lines = append(lines, "settings: "+a.settingsLabel)
	}
	return lines
}
