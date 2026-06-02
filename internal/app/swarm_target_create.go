package app

import (
	"strings"

	"swarm-refactor/swarmtui/internal/model"
)

func createSessionSwarmIDForRoute(route model.ChatRoute, target *model.SwarmTarget) string {
	if swarmID := strings.TrimSpace(route.SwarmID); swarmID != "" {
		return swarmID
	}
	if strings.TrimSpace(route.ID) != "host" || target == nil {
		return ""
	}
	return strings.TrimSpace(target.SwarmID)
}
