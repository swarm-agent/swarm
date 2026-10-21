package run

import (
	"strings"
	store "swarm/packages/swarmd/internal/store/pebble"
)

// The executor persists a sanitized terminal reason on the canonical intent.
// Preserve it in the worker receipt and downstream task reconciliation.
func workerExecutionFailureReason(intent store.V3SessionRunIntent) string {
	if reason := strings.TrimSpace(intent.BlockedReason); reason != "" {
		return reason
	}
	return "execution " + intent.Status
}
