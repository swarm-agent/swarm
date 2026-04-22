package app

import (
	"context"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"time"

	"github.com/gdamore/tcell/v2"

	"swarm-refactor/swarmtui/internal/client"
	"swarm-refactor/swarmtui/internal/launcher"
	"swarm-refactor/swarmtui/internal/ui"
)

func (a *App) handleUpdateCommand(args []string) {
	a.home.ClearCommandOverlay()
	if len(args) == 0 {
		a.home.SetStatus("usage: /update [status|apply]")
		return
	}
	switch strings.ToLower(strings.TrimSpace(args[0])) {
	case "status":
		a.refreshUpdateStatus(true)
	case "apply":
		a.applyUpdate()
	default:
		a.home.SetStatus("usage: /update [status|apply]")
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
	ctx, cancel := context.WithTimeout(context.Background(), 15*time.Second)
	defer cancel()
	plan, err := a.api.ApplyUpdate(ctx)
	if err != nil {
		a.home.SetStatus(fmt.Sprintf("/update apply failed: %v", err))
		a.showToast(ui.ToastError, fmt.Sprintf("update apply failed: %v", err))
		return
	}
	profile, err := launcher.LoadRuntimeProfile(strings.TrimSpace(a.updateStatus.CurrentLane), nil)
	if err != nil {
		a.home.SetStatus(fmt.Sprintf("/update apply failed: %v", err))
		return
	}
	relaunchArgs := collectRelaunchArgs()
	if err := launcher.StartUpdateHelper(profile, plan, os.Getpid(), relaunchArgs); err != nil {
		a.home.SetStatus(fmt.Sprintf("/update apply failed: %v", err))
		a.showToast(ui.ToastError, fmt.Sprintf("update apply failed: %v", err))
		return
	}
	a.home.SetStatus(fmt.Sprintf("updating to %s; restarting", strings.TrimSpace(plan.TargetVersion)))
	a.showToast(ui.ToastSuccess, fmt.Sprintf("updating to %s; restarting", strings.TrimSpace(plan.TargetVersion)))
	a.quitRequested = true
	if a.screen != nil {
		a.screen.PostEventWait(tcell.NewEventInterrupt(interruptQuit))
	}
}

func collectRelaunchArgs() []string {
	args := append([]string{}, os.Args[1:]...)
	base := filepath.Base(strings.TrimSpace(os.Args[0]))
	if strings.EqualFold(base, "swarmdev") {
		return append([]string{"dev"}, args...)
	}
	if len(args) == 0 {
		return args
	}
	first := strings.ToLower(strings.TrimSpace(args[0]))
	if first == "main" || first == "dev" {
		return args
	}
	return append([]string{"main"}, args...)
}

func updateCurrentVersionLabel(status client.UpdateStatus) string {
	current := strings.TrimSpace(status.CurrentVersion)
	if current == "" {
		return "current"
	}
	return current
}
