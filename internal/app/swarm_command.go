package app

import (
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
		a.home.SetCommandOverlay(a.swarmStatusLines())
		a.home.SetStatus("usage: /swarm set <name>")
		return
	}

	sub := strings.ToLower(strings.TrimSpace(args[0]))
	switch sub {
	case "status", "pending", "approve", "reject":
		a.home.SetCommandOverlay(a.swarmStatusLines())
		a.home.SetStatus("that /swarm panel command was removed; use /swarm set <name>")
	case "set", "name":
		if len(args) < 2 {
			a.home.SetCommandOverlay(a.swarmStatusLines())
			a.home.SetStatus(fmt.Sprintf("usage: /swarm %s <name>", sub))
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

	a.home.SetCommandOverlay(a.swarmStatusLines())
	a.home.SetStatus("saving swarm name...")
	if err := saveSwarmNameSetting(a.api, name); err != nil {
		a.home.SetStatus(fmt.Sprintf("swarm name updated to %q (settings save failed: %v)", name, err))
		return
	}
	a.home.SetCommandOverlay(a.swarmStatusLines())
	a.home.SetStatus(fmt.Sprintf("swarm name set to %q", name))
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
	}
	if target := a.homeModel.CurrentSwarmTarget; target != nil && strings.TrimSpace(target.SwarmID) != "" {
		lines = append(lines, "current target swarm_id: "+strings.TrimSpace(target.SwarmID))
	}
	lines = append(lines,
		"usage: /swarm set <name> | /swarm name <name> | /swarm <name> | /swarm role <master|child>",
		"role note: topology role metadata only.",
	)
	if strings.TrimSpace(a.settingsLabel) != "" {
		lines = append(lines, "settings: "+a.settingsLabel)
	}
	return lines
}

func isValidSwarmRoleSetting(role string) bool {
	switch strings.ToLower(strings.TrimSpace(role)) {
	case bootstrapRoleMaster, bootstrapRoleChild:
		return true
	default:
		return false
	}
}
