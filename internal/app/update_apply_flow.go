package app

import (
	"fmt"
	"os"
	"strings"

	"swarm-refactor/swarmtui/internal/launcher"
	"swarm-refactor/swarmtui/internal/ui"
)

const appliedUpdateToastEnv = "SWARM_APPLIED_UPDATE_TOAST"

func (a *App) runPendingUpdate() error {
	if a == nil || a.pendingUpdate == nil {
		return nil
	}
	request := a.pendingUpdate
	a.pendingUpdate = nil

	profile, err := launcher.LoadRuntimeProfile(strings.TrimSpace(request.lane), nil)
	if err != nil {
		return err
	}
	version := strings.TrimSpace(request.plan.TargetVersion)
	if version == "" {
		version = "new release"
	}
	fmt.Fprintf(os.Stdout, "\nUpdating to %s...\n", version)
	fmt.Fprintln(os.Stdout, "Swarm will relaunch automatically when the update finishes.")
	return launcher.RunUpdateHelperForeground(profile, request.plan, request.relaunchArgs)
}

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
