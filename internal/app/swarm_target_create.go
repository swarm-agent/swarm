package app

import (
	"strings"

	"swarm-refactor/swarmtui/internal/model"
)

func createSessionSwarmIDForRoute(route model.ChatRoute, target *model.SwarmTarget) string {
	if swarmID := strings.TrimSpace(route.SwarmID); swarmID != "" {
		return swarmID
	}
	if target == nil {
		return ""
	}
	if strings.TrimSpace(route.ID) == "host" || isPrimaryHostChatRoute(route) {
		return strings.TrimSpace(target.SwarmID)
	}
	return ""
}
