package app

import (
	"fmt"
	"strings"
)

func (a *App) handleHeaderCommand(args []string) {
	if len(args) == 0 {
		a.applyHeaderSetting(!a.config.Chat.ShowHeader)
		return
	}

	sub := strings.ToLower(strings.TrimSpace(args[0]))
	switch sub {
	case "toggle":
		a.applyHeaderSetting(!a.config.Chat.ShowHeader)
	case "on", "show", "true", "1":
		a.applyHeaderSetting(true)
	case "off", "hide", "false", "0":
		a.applyHeaderSetting(false)
	case "status":
		a.home.SetCommandOverlay(a.headerStatusLines())
		a.home.SetStatus("chat header " + enabledLabel(a.config.Chat.ShowHeader))
	default:
		a.home.ClearCommandOverlay()
		a.home.SetStatus("usage: /header [on|off|toggle|status]")
	}
}

func (a *App) handleThinkingCommand(args []string) {
	if len(args) == 0 {
		a.home.SetCommandOverlay(a.thinkingTagsStatusLines())
		a.home.SetStatus("use /thinking on to show reasoning tags, /thinking off to hide them")
		return
	}

	sub := strings.ToLower(strings.TrimSpace(args[0]))
	switch sub {
	case "toggle":
		a.applyThinkingTagsSetting(!a.config.Chat.ThinkingTags)
	case "on", "show", "true", "1":
		a.applyThinkingTagsSetting(true)
	case "off", "hide", "false", "0":
		a.applyThinkingTagsSetting(false)
	case "status":
		a.home.SetCommandOverlay(a.thinkingTagsStatusLines())
		a.home.SetStatus("thinking tags " + enabledLabel(a.config.Chat.ThinkingTags))
	default:
		a.home.SetCommandOverlay(a.thinkingTagsStatusLines())
		a.home.SetStatus("usage: /thinking [on|off|toggle|status]")
	}
}

func (a *App) applyHeaderSetting(enabled bool) {
	a.config.Chat.ShowHeader = enabled
	if a.chat != nil {
		a.chat.SetHeaderVisible(enabled)
	}

	a.home.SetCommandOverlay(a.headerStatusLines())
	if err := saveHeaderSetting(a.api, enabled); err != nil {
		a.home.SetStatus(fmt.Sprintf("chat header %s (settings save failed: %v)", enabledLabel(enabled), err))
		return
	}
	a.home.SetStatus("chat header " + enabledLabel(enabled))
}

func (a *App) applyThinkingTagsSetting(enabled bool) {
	a.config.Chat.ThinkingTags = enabled
	if a.chat != nil {
		a.chat.SetThinkingTagsVisible(enabled)
	}

	a.home.SetCommandOverlay(a.thinkingTagsStatusLines())
	if err := saveThinkingTagsSetting(a.api, enabled); err != nil {
		a.home.SetStatus(fmt.Sprintf("thinking tags %s (settings save failed: %v)", enabledLabel(enabled), err))
		return
	}
	a.home.SetStatus("thinking tags " + enabledLabel(enabled))
}

func (a *App) headerStatusLines() []string {
	lines := []string{
		"chat header: " + enabledLabel(a.config.Chat.ShowHeader),
		"usage: /header [on|off|toggle|status]",
	}
	if strings.TrimSpace(a.settingsLabel) != "" {
		lines = append(lines, "settings: "+a.settingsLabel)
	}
	return lines
}

func (a *App) thinkingTagsStatusLines() []string {
	lines := []string{
		"thinking tags: " + enabledLabel(a.config.Chat.ThinkingTags),
		"/thinking on   show raw reasoning/thinking tag output in chat",
		"/thinking off   hide raw tags and keep the normal response view",
		"/thinking toggle   switch between on and off",
		"/thinking status   show the current setting",
	}
	if strings.TrimSpace(a.settingsLabel) != "" {
		lines = append(lines, "settings: "+a.settingsLabel)
	}
	return lines
}

func enabledLabel(enabled bool) string {
	if enabled {
		return "on"
	}
	return "off"
}
