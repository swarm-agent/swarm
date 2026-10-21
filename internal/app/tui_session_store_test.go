package app

import (
	"swarm-refactor/swarmtui/internal/model"
)

func sessionIDs(sessions []model.SessionSummary) []string {
	ids := make([]string, 0, len(sessions))
	for _, session := range sessions {
		ids = append(ids, session.ID)
	}
	return ids
}
