package app

import (
	"fmt"
	"strings"

	"swarm-refactor/swarmtui/internal/ui"
)

func (a *App) handleSwarmModalAction(action ui.SwarmModalAction) {
	switch action.Kind {
	case ui.SwarmModalActionCopyDashboardLink:
		text := strings.TrimSpace(action.Text)
		if text == "" {
			a.home.SetSwarmModalStatus("dashboard link unavailable")
			return
		}
		if err := copyTextToClipboardOSC52(text); err != nil {
			a.home.SetSwarmModalStatus(fmt.Sprintf("copy failed: %v", err))
			return
		}
		a.home.SetSwarmModalStatus("dashboard link copied")
	}
}
