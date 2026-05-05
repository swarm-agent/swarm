package app

import (
	"context"
	"fmt"
	"strings"

	"swarm-refactor/swarmtui/internal/model"
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
		a.showSwarmSelectorOverlay("swarm selectors")
		return
	}

	sub := strings.ToLower(strings.TrimSpace(args[0]))
	switch sub {
	case "status":
		a.showSwarmStatusOverlay(fmt.Sprintf("swarm name: %s, role: %s", a.currentSwarmName(), a.currentSwarmRole()))
	case "pending":
		a.showSwarmStatusOverlay("pending swarm enrollments")
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
			a.showSwarmStatusOverlay(fmt.Sprintf("approved child %s", enrollment.ChildName))
			return
		}
		a.showSwarmStatusOverlay(fmt.Sprintf("rejected child %s", enrollment.ChildName))
	case "set", "name":
		if len(args) < 2 {
			a.showSwarmStatusOverlay(fmt.Sprintf("usage: /swarm %s <name>", sub))
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

	a.showSwarmStatusOverlay("saving swarm name...")
	if err := saveSwarmNameSetting(a.api, name); err != nil {
		a.home.SetStatus(fmt.Sprintf("swarm name updated to %q (settings save failed: %v)", name, err))
		return
	}
	a.showSwarmStatusOverlay(fmt.Sprintf("swarm name set to %q", name))
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

func (a *App) showSwarmSelectorOverlay(status string) {
	if a == nil || a.home == nil {
		return
	}
	workspacePath := strings.TrimSpace(a.activeWorkspacePath())
	if workspacePath == "" {
		workspacePath = strings.TrimSpace(a.workspacePath)
	}
	if workspacePath == "" {
		workspacePath = strings.TrimSpace(a.startupCWD)
	}
	routes := buildChatRoutesForWorkspaces(a.homeModel.Workspaces, workspacePath)
	selected := normalizeSelectedRouteID(a.selectedChatRouteID, routes)
	lines := []string{
		"current: " + a.selectedChatRouteLabelForWorkspace(workspacePath),
		"default: " + chatRouteLabelForID(routes, emptyFallback(strings.TrimSpace(a.config.Chat.DefaultWorkspaceRoutes[workspacePath]), "host")),
		"selectors:",
	}
	for _, route := range routes {
		marker := "  "
		if strings.TrimSpace(route.ID) == selected {
			marker = "* "
		}
		lines = append(lines, marker+emptyFallback(strings.TrimSpace(route.Label), "host"))
	}
	lines = append(lines,
		"commands:",
		"  Alt+R: change default route",
		"  /swarm status: pairing status",
		"  /swarm pending: pending enrollments",
	)
	a.home.ShowSwarmModal("Swarm", lines, "")
	a.home.SetStatus(status)
}

func chatRouteLabelForID(routes []model.ChatRoute, routeID string) string {
	routeID = strings.TrimSpace(routeID)
	for _, route := range routes {
		if strings.TrimSpace(route.ID) == routeID {
			return emptyFallback(strings.TrimSpace(route.Label), "host")
		}
	}
	return "host"
}

func (a *App) showSwarmStatusOverlay(status string) {
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
	lines := []string{
		"swarm name: " + a.currentSwarmName(),
		"swarm role: " + a.currentSwarmRole(),
		"pairing state: " + strings.TrimSpace(state.Pairing.PairingState),
		fmt.Sprintf("pending enrollments: %d", len(pending)),
		fmt.Sprintf("peer count: %d", len(state.TrustedPeers)),
		fmt.Sprintf("pending notifications: %d", a.swarmNotificationCount),
		"usage: /swarm name <name> | /swarm pending | /swarm approve <id> | /swarm reject <id>",
	}
	if status == "pending swarm enrollments" {
		for _, item := range pending {
			lines = append(lines, fmt.Sprintf("pending %s  %s", item.ID, emptyFallback(strings.TrimSpace(item.ChildName), "child")))
		}
	}
	a.home.ShowSwarmModal("Swarm Status", lines, "")
	a.home.SetStatus(status)
}

func isValidSwarmRoleSetting(role string) bool {
	switch strings.ToLower(strings.TrimSpace(role)) {
	case bootstrapRoleMaster, bootstrapRoleChild:
		return true
	default:
		return false
	}
}
