package app

import (
	"strings"

	"swarm-refactor/swarmtui/internal/model"
)

func createSessionSwarmIDForRoute(route model.ChatRoute, _ *model.SwarmTarget) string {
	if swarmID := strings.TrimSpace(route.SwarmID); swarmID != "" {
		return swarmID
	}
	return ""
}
