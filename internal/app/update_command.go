package app

import (
	"context"
	"fmt"
	"strings"
	"time"

	"github.com/gdamore/tcell/v2"

	"swarm-refactor/swarmtui/internal/client"
	"swarm-refactor/swarmtui/internal/ui"
)

func (a *App) handleUpdateCommand(args []string) {
	a.home.ClearCommandOverlay()
	if len(args) == 0 {
		a.home.SetStatus("usage: /update [status|apply|dev]")
		return
	}
	switch strings.ToLower(strings.TrimSpace(args[0])) {
	case "status":
		a.refreshUpdateStatus(true)
	case "apply":
		a.applyUpdate()
	case "dev":
		a.applyDevUpdate()
	default:
		a.home.SetStatus("usage: /update [status|apply|dev]")
	}
}

func (a *App) refreshUpdateStatus(force bool) {
	ctx, cancel := context.WithTimeout(context.Background(), 8*time.Second)
	defer cancel()
	status, err := a.api.GetUpdateStatus(ctx)
	if err != nil {
		a.home.SetStatus(fmt.Sprintf("/update status failed: %v", err))
		return
	}
	a.updateStatus = status
	if status.Suppressed {
		a.home.SetStatus("updates are suppressed for this build/lane")
		return
	}
	if status.UpdateAvailable {
		a.home.SetStatus(fmt.Sprintf("update available: %s → %s", updateCurrentVersionLabel(status), strings.TrimSpace(status.LatestVersion)))
		if force {
			a.showToast(ui.ToastInfo, fmt.Sprintf("update available: %s → %s", updateCurrentVersionLabel(status), strings.TrimSpace(status.LatestVersion)))
		}
		return
	}
	if latest := strings.TrimSpace(status.LatestVersion); latest != "" {
		a.home.SetStatus(fmt.Sprintf("up to date: %s", latest))
		return
	}
	a.home.SetStatus("update status refreshed")
}

func (a *App) applyUpdate() {
	a.releaseUpdateRequested = true
	a.home.SetStatus("checking and applying release update after TUI shutdown")
	a.quitRequested = true
	if a.screen != nil {
		a.screen.PostEventWait(tcell.NewEventInterrupt(interruptQuit))
	}
}

func (a *App) applyDevUpdate() {
	ctx, cancel := context.WithTimeout(context.Background(), 8*time.Second)
	defer cancel()
	status, err := a.api.GetUpdateStatus(ctx)
	if err != nil {
		a.home.SetStatus(fmt.Sprintf("/update dev failed: %v", err))
		a.showToast(ui.ToastError, fmt.Sprintf("update dev failed: %v", err))
		return
	}
	a.updateStatus = status
	if !status.DevMode {
		a.home.SetStatus("/update dev requires dev_mode=true in swarm.conf")
		a.showToast(ui.ToastError, "/update dev requires dev_mode=true in swarm.conf")
		return
	}
	a.devUpdateRequested = true
	a.home.SetStatus("rebuilding local dev checkout after TUI shutdown")
	a.quitRequested = true
	if a.screen != nil {
		a.screen.PostEventWait(tcell.NewEventInterrupt(interruptQuit))
	}
}

func updateCurrentVersionLabel(status client.UpdateStatus) string {
	current := strings.TrimSpace(status.CurrentVersion)
	if current == "" {
		return "current"
	}
	return current
}
