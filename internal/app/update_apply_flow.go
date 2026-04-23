package app

import (
	"os"
	"strings"

	"swarm-refactor/swarmtui/internal/ui"
)

const appliedUpdateToastEnv = "SWARM_APPLIED_UPDATE_TOAST"

func (a *App) announceAppliedUpdate() {
	if a == nil {
		return
	}
	message := strings.TrimSpace(os.Getenv(appliedUpdateToastEnv))
	if message == "" {
		return
	}
	_ = os.Unsetenv(appliedUpdateToastEnv)
	a.home.SetStatus(message)
	a.showToast(ui.ToastSuccess, message)
}
