package app

import (
	"context"
	"fmt"
	"os"
	"strings"
	"time"

	"swarm-refactor/swarmtui/internal/client"
	"swarm-refactor/swarmtui/internal/launcher"
	"swarm-refactor/swarmtui/internal/ui"
)

const appliedUpdateToastEnv = "SWARM_APPLIED_UPDATE_TOAST"

var (
	loadRuntimeProfileForUpdate = func(lane string) (launcher.Profile, error) {
		return launcher.LoadRuntimeProfile(strings.TrimSpace(lane), nil)
	}
	updateShutdownFunc = func(api *client.API, ctx context.Context, reason string) error {
		if api == nil {
			return fmt.Errorf("update shutdown api unavailable")
		}
		return api.Shutdown(ctx, reason)
	}
	runUpdateHelperForegroundFunc = func(profile launcher.Profile, plan client.UpdateApplyPlan, relaunchArgs []string) error {
		return launcher.RunUpdateHelperForeground(profile, plan, relaunchArgs)
	}
)

func (a *App) runPendingUpdate() error {
	if a == nil || a.pendingUpdate == nil {
		return nil
	}
	request := a.pendingUpdate
	a.pendingUpdate = nil

	profile, err := loadRuntimeProfileForUpdate(request.lane)
	if err != nil {
		return err
	}
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	if err := updateShutdownFunc(a.api, ctx, "swarmtui update apply"); err != nil {
		return err
	}
	version := strings.TrimSpace(request.plan.TargetVersion)
	if version == "" {
		version = "new release"
	}
	fmt.Fprintf(os.Stdout, "\nUpdating to %s...\n", version)
	fmt.Fprintln(os.Stdout, "Swarm is shutting down cleanly before applying the update.")
	fmt.Fprintln(os.Stdout, "Swarm will relaunch automatically when the update finishes.")
	return runUpdateHelperForegroundFunc(profile, request.plan, request.relaunchArgs)
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
