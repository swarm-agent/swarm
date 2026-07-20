package runtime

import (
	"fmt"

	sessionruntime "swarm/packages/swarmd/internal/session"
)

func cutOverActivePlansToV3Runtime(sessions *sessionruntime.Service) error {
	if sessions == nil || sessions.Store() == nil {
		return fmt.Errorf("session service is not configured")
	}
	complete, err := sessions.Store().PlanRuntimeCutoverComplete()
	if err != nil || complete {
		return err
	}
	const allSessions = int(^uint(0) >> 1)
	all, err := sessions.ListSessions(allSessions)
	if err != nil {
		return err
	}
	for _, item := range all {
		plan, ok, getErr := sessions.GetActivePlan(item.ID)
		if getErr != nil {
			return getErr
		}
		if !ok {
			continue
		}
		if _, importErr := sessions.Store().ImportLegacyActivePlan(plan, 0); importErr != nil {
			return fmt.Errorf("import active plan %q for session %q: %w", plan.ID, item.ID, importErr)
		}
	}
	return sessions.Store().MarkPlanRuntimeCutoverComplete()
}
