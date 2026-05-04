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

type localContainerUpdateConfirmation struct {
	DevMode        bool
	TargetVersion  string
	Plan           client.LocalContainerUpdatePlan
	RemoteSessions []client.RemoteDeploySession
}

func (a *App) handleUpdateCommand(args []string) {
	a.home.ClearCommandOverlay()
	if len(args) == 0 {
		a.applyUpdate()
		return
	}
	switch strings.ToLower(strings.TrimSpace(args[0])) {
	case "dev":
		a.applyDevUpdate()
	case "confirm":
		a.confirmPendingLocalContainerUpdate(false)
	case "dismiss":
		a.confirmPendingLocalContainerUpdate(true)
	case "cancel":
		a.pendingLocalContainerUpdate = nil
		a.home.SetStatus("update cancelled")
	default:
		a.home.SetStatus(updateUsage(a.startupDevMode()))
	}
}

func (a *App) startupDevMode() bool {
	return a != nil && a.config.Startup.DevMode
}

func updateUsage(devMode bool) string {
	if devMode {
		return "usage: /update [dev]"
	}
	return "usage: /update"
}

func updateHelpLine(devMode bool) string {
	if devMode {
		return "/update [dev|confirm|dismiss|cancel]   (update Swarm)"
	}
	return "/update [confirm|dismiss|cancel]   (update Swarm)"
}

func (a *App) refreshUpdateStatus(force bool) {
	ctx, cancel := context.WithTimeout(context.Background(), 8*time.Second)
	defer cancel()
	status, err := a.api.GetUpdateStatus(ctx)
	if err != nil {
		a.home.SetStatus(fmt.Sprintf("update status failed: %v", err))
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
	if a == nil || a.api == nil {
		return
	}
	ctx, cancel := context.WithTimeout(context.Background(), 8*time.Second)
	defer cancel()
	status, err := a.api.GetUpdateStatus(ctx)
	if err != nil {
		a.home.SetStatus(fmt.Sprintf("/update failed: %v", err))
		a.showToast(ui.ToastError, fmt.Sprintf("update failed: %v", err))
		return
	}
	a.updateStatus = status
	if status.Suppressed {
		a.home.SetStatus("updates are suppressed for this build/lane")
		a.showToast(ui.ToastError, "updates are suppressed for this build/lane")
		return
	}
	if !status.UpdateAvailable {
		a.home.SetStatus("no Swarm update is available yet")
		return
	}
	if !a.confirmLocalContainerUpdate(false, status.LatestVersion) {
		return
	}
	a.requestReleaseUpdate()
}

func (a *App) requestReleaseUpdate() {
	a.releaseUpdateRequested = true
	a.home.SetStatus("updating Swarm and container images after TUI shutdown")
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
	if !a.confirmLocalContainerUpdate(true, status.LatestVersion) {
		return
	}
	a.requestDevUpdate()
}

func (a *App) requestDevUpdate() {
	a.devUpdateRequested = true
	a.home.SetStatus("updating Swarm and container images after TUI shutdown")
	a.quitRequested = true
	if a.screen != nil {
		a.screen.PostEventWait(tcell.NewEventInterrupt(interruptQuit))
	}
}

func (a *App) confirmLocalContainerUpdate(devMode bool, targetVersion string) bool {
	if a == nil || a.api == nil {
		return true
	}
	ctx, cancel := context.WithTimeout(context.Background(), 8*time.Second)
	defer cancel()
	remoteSessions, err := a.api.GetRemoteDeploySessions(ctx, false)
	if err != nil {
		a.home.SetStatus(fmt.Sprintf("remote container update check failed: %v", err))
		a.showToast(ui.ToastError, fmt.Sprintf("remote container update check failed: %v", err))
		return false
	}
	if a.config.Updates.LocalContainerWarningDismissed && remoteDeployUpdateSessionCount(remoteSessions) == 0 {
		return true
	}
	plan, err := a.api.GetLocalContainerUpdatePlanWithPostRebuild(ctx, &devMode, targetVersion, devMode)
	if err != nil {
		a.home.SetStatus(fmt.Sprintf("local container update check failed: %v", err))
		a.showToast(ui.ToastError, fmt.Sprintf("local container update check failed: %v", err))
		return false
	}
	if !localContainerUpdatePlanAffected(plan) && remoteDeployUpdateSessionCount(remoteSessions) == 0 {
		return true
	}
	a.pendingLocalContainerUpdate = &localContainerUpdateConfirmation{DevMode: devMode, TargetVersion: strings.TrimSpace(targetVersion), Plan: plan, RemoteSessions: remoteSessions}
	lines := localContainerUpdateWarningLines(plan, remoteSessions)
	lines = append(lines, "")
	if remoteDeployUpdateSessionCount(remoteSessions) > 0 {
		lines = append(lines, "Run /update confirm to continue once, or /update cancel to stop.")
	} else {
		lines = append(lines, "Run /update confirm to continue once, /update dismiss to continue and not show this local-only container-image warning again, or /update cancel to stop.")
	}
	a.home.SetCommandOverlay(lines)
	a.home.SetStatus("container image update confirmation required")
	return false
}

func (a *App) confirmPendingLocalContainerUpdate(dismiss bool) {
	pending := a.pendingLocalContainerUpdate
	if pending == nil {
		a.home.SetStatus("no local container update confirmation is pending")
		return
	}
	if dismiss && remoteDeployUpdateSessionCount(pending.RemoteSessions) == 0 {
		a.config.Updates.LocalContainerWarningDismissed = true
		if err := saveUpdateWarningDismissedSetting(a.api, true); err != nil {
			a.home.SetStatus(fmt.Sprintf("update warning dismissed for this run (settings save failed: %v)", err))
		} else {
			a.home.SetStatus("container image update warning dismissed")
		}
	} else if dismiss {
		a.home.SetStatus("remote container image warnings are shown for each update")
	}
	a.pendingLocalContainerUpdate = nil
	if pending.DevMode {
		a.requestDevUpdate()
		return
	}
	a.requestReleaseUpdate()
}

func localContainerUpdatePlanAffected(plan client.LocalContainerUpdatePlan) bool {
	return plan.Summary.Affected > 0 || plan.Summary.NeedsUpdate > 0 || plan.Summary.Unknown > 0 || plan.Summary.Errors > 0
}

func remoteDeployUpdateSessionCount(sessions []client.RemoteDeploySession) int {
	count := 0
	for _, session := range sessions {
		if strings.EqualFold(strings.TrimSpace(session.Status), "attached") && strings.TrimSpace(session.SSHSessionTarget) != "" {
			count++
		}
	}
	return count
}

func localContainerUpdateWarningLines(plan client.LocalContainerUpdatePlan, remoteSessions []client.RemoteDeploySession) []string {
	copy := strings.TrimSpace(plan.Contract.WarningCopy)
	if copy == "" {
		copy = "This will also update local and remote container images."
	}
	lines := []string{copy}
	lines = append(lines, fmt.Sprintf("local containers: total=%d affected=%d needs_update=%d unknown=%d errors=%d", plan.Summary.Total, plan.Summary.Affected, plan.Summary.NeedsUpdate, plan.Summary.Unknown, plan.Summary.Errors))
	if remoteCount := remoteDeployUpdateSessionCount(remoteSessions); remoteCount > 0 {
		lines = append(lines, fmt.Sprintf("remote SSH sessions: attached=%d", remoteCount))
	}
	if plan.DevMode {
		fingerprint := strings.TrimSpace(firstNonEmptyString(plan.Target.PostRebuildFingerprint, plan.Target.Fingerprint))
		if fingerprint != "" {
			lines = append(lines, "target dev fingerprint: "+fingerprint)
		}
	} else {
		if target := strings.TrimSpace(firstNonEmptyString(plan.Target.Version, plan.Target.DigestRef, plan.Target.ImageRef)); target != "" {
			lines = append(lines, "target: "+target)
		}
	}
	if failure := strings.TrimSpace(plan.Contract.FailureSemantics); failure != "" {
		lines = append(lines, failure)
	}
	return lines
}

func firstNonEmptyString(values ...string) string {
	for _, value := range values {
		if strings.TrimSpace(value) != "" {
			return strings.TrimSpace(value)
		}
	}
	return ""
}

func updateCurrentVersionLabel(status client.UpdateStatus) string {
	current := strings.TrimSpace(status.CurrentVersion)
	if current == "" {
		return "current"
	}
	return current
}
