package app

import (
	"strings"

	"swarm-refactor/swarmtui/internal/ui"
)

func (a *App) showToast(level ui.ToastLevel, message string) {
	message = strings.TrimSpace(message)
	if message == "" {
		return
	}
	if a.route == "chat" && a.chat != nil {
		a.chat.ShowToast(level, message)
		return
	}
	if a.home != nil {
		a.home.ShowToast(level, message)
	}
}
