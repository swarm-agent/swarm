package app

import (
	"context"
	"fmt"
	"strings"
)

// handleSwarmCommand manages the device identity name.
// Important: this is intentionally separate from SwarmingConfig.
// - /swarm updates the persisted device name shown in the sidebar and TUI.
// - SwarmingConfig continues to drive the activity indicator copy used during live runs.
// Keep these concepts separate in future edits.
func (a *App) handleSwarmCommand(args []string) {
	a.home.ClearCommandOverlay()
	if a.api == nil {
		a.home.SetStatus("swarm API unavailable")
		return
	}
	if len(args) == 0 {
		a.showSwarmDashboardOverlay("swarm dashboard")
		return
	}

	sub := strings.ToLower(strings.TrimSpace(args[0]))
	switch sub {
	case "status":
		a.showSwarmDashboardOverlay(fmt.Sprintf("swarm name: %s, role: %s", a.currentSwarmName(), a.currentSwarmRole()))
	case "pending":
		a.showSwarmDashboardOverlay("pending swarm enrollments")
	case "approve", "reject":
		if len(args) < 2 {
			a.home.SetStatus(fmt.Sprintf("usage: /swarm %s <enrollment_id>", sub))
			return
		}
		approve := sub == "approve"
		enrollment, _, err := a.api.DecideSwarmEnrollment(context.Background(), args[1], approve, strings.TrimSpace(strings.Join(args[2:], " ")))
		if err != nil {
			a.home.SetStatus(fmt.Sprintf("/swarm %s failed: %v", sub, err))
			return
		}
		if approve {
			a.showSwarmDashboardOverlay(fmt.Sprintf("approved child %s", enrollment.ChildName))
			return
		}
		a.showSwarmDashboardOverlay(fmt.Sprintf("rejected child %s", enrollment.ChildName))
	case "set", "name":
		if len(args) < 2 {
			a.showSwarmDashboardOverlay(fmt.Sprintf("usage: /swarm %s <name>", sub))
			return
		}
		a.applySwarmNameSetting(strings.Join(args[1:], " "))
	case "role":
		if len(args) != 2 {
			a.home.SetCommandOverlay(a.swarmStatusLines())
			a.home.SetStatus("usage: /swarm role <master|child>")
			return
		}
		a.applySwarmRoleSetting(args[1])
	default:
		a.applySwarmNameSetting(strings.Join(args, " "))
	}
}

func (a *App) applySwarmNameSetting(name string) {
	name = strings.TrimSpace(name)
	if name == "" {
		a.home.SetCommandOverlay(a.swarmStatusLines())
		a.home.SetStatus("swarm name cannot be empty")
		return
	}

	a.config.Swarm.Name = name
	a.home.SetSwarmName(name)
	if a.chat != nil {
		a.chat.SetSwarmName(name)
	}

	a.showSwarmDashboardOverlay("saving swarm name...")
	if err := saveSwarmNameSetting(a.api, name); err != nil {
		a.home.SetStatus(fmt.Sprintf("swarm name updated to %q (settings save failed: %v)", name, err))
		return
	}
	a.showSwarmDashboardOverlay(fmt.Sprintf("swarm name set to %q", name))
}

func (a *App) applySwarmRoleSetting(role string) {
	role = strings.ToLower(strings.TrimSpace(role))
	if !isValidSwarmRoleSetting(role) {
		a.home.SetCommandOverlay(a.swarmStatusLines())
		a.home.SetStatus("swarm role must be master or child")
		return
	}

	a.config.Swarm.Role = role
	a.home.SetCommandOverlay(a.swarmStatusLines())
	if err := saveStartupSwarmRole(role); err != nil {
		a.home.SetStatus(fmt.Sprintf("swarm role updated to %q (settings save failed: %v)", role, err))
		return
	}
	a.home.SetStatus(fmt.Sprintf("swarm role set to %q", role))
}

func (a *App) currentSwarmName() string {
	return emptyFallback(strings.TrimSpace(a.config.Swarm.Name), defaultSwarmName)
}

func (a *App) currentSwarmRole() string {
	role := strings.ToLower(strings.TrimSpace(a.config.Swarm.Role))
	if role != bootstrapRoleMaster && role != bootstrapRoleChild {
		return bootstrapRoleMaster
	}
	return role
}

func (a *App) swarmStatusLines() []string {
	lines := []string{
		"swarm name: " + a.currentSwarmName(),
		"swarm role: " + a.currentSwarmRole(),
		fmt.Sprintf("pending swarm notifications: %d", a.swarmNotificationCount),
		"usage: /swarm [status|pending|approve <id>|reject <id>|set <name>|role <master|child>|<name>]",
		"master role note: future controller for child swarms; pairing and sync come later.",
	}
	if strings.TrimSpace(a.settingsLabel) != "" {
		lines = append(lines, "settings: "+a.settingsLabel)
	}
	return lines
}

func (a *App) showSwarmDashboardOverlay(status string) {
	if a == nil || a.api == nil || a.home == nil {
		return
	}
	ctx := context.Background()
	state, stateErr := a.api.GetSwarmState(ctx)
	pending, pendingErr := a.api.ListPendingSwarmEnrollments(ctx)
	if stateErr != nil {
		a.home.SetStatus(fmt.Sprintf("swarm state failed: %v", stateErr))
		return
	}
	if pendingErr != nil {
		a.home.SetStatus(fmt.Sprintf("swarm pending enrollments failed: %v", pendingErr))
		return
	}
	dashboardLink := a.swarmDashboardLink()
	lines := []string{
		"swarm name: " + a.currentSwarmName(),
		"swarm role: " + a.currentSwarmRole(),
		"pairing state: " + strings.TrimSpace(state.Pairing.PairingState),
		"parent swarm: " + emptyFallback(strings.TrimSpace(state.Pairing.ParentSwarmID), "none"),
		fmt.Sprintf("pending enrollments: %d", len(pending)),
		fmt.Sprintf("trusted peers: %d", len(state.TrustedPeers)),
		fmt.Sprintf("pending notifications: %d", a.swarmNotificationCount),
		"dashboard: " + dashboardLink,
		"advanced settings: open the desktop dashboard for pairing and replication controls.",
		"usage: /swarm name <name> | /swarm pending | /swarm approve <id> | /swarm reject <id>",
	}
	for _, item := range pending {
		lines = append(lines, fmt.Sprintf("pending %s  %s  %s", item.ID, emptyFallback(strings.TrimSpace(item.ChildName), "child"), emptyFallback(strings.TrimSpace(item.ChildFingerprint), "fingerprint unavailable")))
	}
	for _, peer := range state.TrustedPeers {
		lines = append(lines, fmt.Sprintf("trusted %s  %s  %s", emptyFallback(strings.TrimSpace(peer.Relationship), "peer"), emptyFallback(strings.TrimSpace(peer.Name), peer.SwarmID), emptyFallback(strings.TrimSpace(peer.Fingerprint), "fingerprint unavailable")))
	}
	a.home.ShowSwarmModal("Swarm Dashboard", lines, dashboardLink)
	a.home.SetStatus(status)
}

func (a *App) swarmDashboardLink() string {
	base := defaultDaemonURL
	if a != nil && a.api != nil {
		base = strings.TrimRight(strings.TrimSpace(a.api.BaseURL()), "/")
	}
	if base == "" {
		base = defaultDaemonURL
	}
	return base + "/desktop"
}

func isValidSwarmRoleSetting(role string) bool {
	switch strings.ToLower(strings.TrimSpace(role)) {
	case bootstrapRoleMaster, bootstrapRoleChild:
		return true
	default:
		return false
	}
}
