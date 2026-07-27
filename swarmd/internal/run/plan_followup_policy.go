package run

import (
	"strings"

	sessionruntime "swarm/packages/swarmd/internal/session"
	pebblestore "swarm/packages/swarmd/internal/store/pebble"
)

func (s *Service) resolvePlanFollowupCheckpointPolicyForPermission(plan pebblestore.SessionPlanSnapshot, explicitDefault string) string {
	globalDefault := ""
	if s != nil && s.uiSettings != nil {
		settings, err := s.uiSettings.GetForAccount(plan.AccountScopeID)
		if err == nil {
			globalDefault = strings.TrimSpace(settings.Chat.FollowupCheckpointPolicyDefault)
		}
	}
	return sessionruntime.ResolvePlanFollowupCheckpointPolicy(plan.Document, globalDefault)
}
