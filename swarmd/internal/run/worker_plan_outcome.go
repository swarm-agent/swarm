package run

import (
	"fmt"
	"strings"

	sessions "swarm/packages/swarmd/internal/session"
	store "swarm/packages/swarmd/internal/store/pebble"
)

// workerPlanStopped says why a worker run's plan stopped short (blocked or
// failed, in its execution state or a checkpoint), or "" while it completed
// or may still go on. A run whose plan stopped is over: it must not wait for
// checkpoints that will never complete.
func workerPlanStopped(doc *store.SessionPlanDocument) string {
	if doc == nil {
		return ""
	}
	stopped := func(status string) bool {
		return status == sessions.PlanCheckpointStatusBlocked || status == sessions.PlanCheckpointStatusFailed
	}
	for _, cp := range doc.Checkpoints {
		if !stopped(cp.Status) {
			continue
		}
		name := strings.TrimSpace(cp.Title)
		if name == "" {
			name = cp.ID
		}
		why := fmt.Sprintf("the plan stopped at step %q (%s)", name, cp.Status)
		if notes := strings.TrimSpace(cp.Notes); notes != "" {
			why += ": " + notes
		}
		return why
	}
	if doc.ExecutionState != nil && stopped(doc.ExecutionState.Status) {
		step := strings.TrimSpace(doc.ExecutionState.LastCheckpointID)
		if step == "" {
			return fmt.Sprintf("the plan stopped (%s)", doc.ExecutionState.Status)
		}
		return fmt.Sprintf("the plan stopped at step %q (%s)", step, doc.ExecutionState.Status)
	}
	return ""
}
