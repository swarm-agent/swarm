package run

import (
	"encoding/json"
	"strings"

	provideriface "swarm/packages/swarmd/internal/provider/interfaces"
)

// responseContainsTerminalPlanManageCall identifies the provider response that
// transfers user-visible completion ownership to the durable lifecycle handoff.
// Any co-emitted assistant text is intentionally not persisted as a second
// completion message.
func responseContainsTerminalPlanManageCall(calls []provideriface.FunctionCall) bool {
	for _, call := range calls {
		if canonicalToolName(call.Name) != "plan_manage" {
			continue
		}
		var args map[string]any
		if json.Unmarshal([]byte(strings.TrimSpace(call.Arguments)), &args) != nil {
			continue
		}
		action := strings.ToLower(strings.TrimSpace(mapString(args, "action")))
		action = strings.ReplaceAll(action, "-", "_")
		switch action {
		case "complete_checkpoint", "checkpoint_outcome", "mark_needs_review", "mark_blocked", "mark_failed":
			return true
		case "complete_subtask":
			if mapBool(args, "complete_checkpoint") {
				return true
			}
		}
	}
	return false
}
