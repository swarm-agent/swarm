package tool

import (
	"context"
	"errors"
	store "swarm/packages/swarmd/internal/store/pebble"
)

// Legacy signatures remain for historical callers/tests only. No V1 execution
// or saved-definition conversion is reachable through the production tool path.
func sessionPlanAutomationToolSchema() map[string]any { return sessionPlanAutomationV2ToolSchema() }
func (r *Runtime) EditParentAutomation(context.Context, WorkspaceScope, store.SessionPlanAutomationIntent, string, uint64) (string, error) {
	return "", errors.New("V1 automation editing retired; submit a complete Automation V2 plan review")
}
func (r *Runtime) ReviewPlanAutomation(context.Context, WorkspaceScope, store.SessionPlanAutomationIntent) (string, error) {
	return "", errors.New("V1 automation approval retired; submit automation_v2 through the canonical plan review")
}
func (r *Runtime) ProposeParentAutomationInstructions(context.Context, WorkspaceScope, store.SessionPlanAutomationIntent, string, uint64, *store.SessionPlanDocument) (string, error) {
	return "", errors.New("V1 separate instruction approval retired; instructions and automation_v2 settings share one review")
}
