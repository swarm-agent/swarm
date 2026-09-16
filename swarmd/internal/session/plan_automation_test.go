package session

import (
	"strings"
	"testing"

	store "swarm/packages/swarmd/internal/store/pebble"
)

// Purpose: structured automation intent must reject executable/ambiguous policy
// and preserve immutable pins when a plan is cloned for editing. This unit layer
// proves validation and alias isolation without requiring an executor.
func TestPlanAutomationIntentValidationAndClone(t *testing.T) {
	a := &store.SessionPlanAutomationIntent{Scope: store.AutomationScope{AccountID: "account", WorkspaceID: "workspace"}, AutomationID: "automation", DefinitionRevision: 1, Definition: store.AutomationDefinition{SessionID: "session", Name: "Hourly", Plans: []store.AutomationPlanBinding{{ID: "work", Plan: store.AutomationPlanReference{SessionID: "session", PlanID: "plan", Revision: 1, DocumentSHA256: strings.Repeat("a", 64)}}}, Schedule: store.AutomationSchedulePolicy{Kind: "interval", IntervalSeconds: 3600}, Authorization: store.AutomationAuthorizationPolicy{Mode: "approval_required", ExpiresAt: 100000, AllowedTools: []string{"read"}}}}
	if err := validatePlanAutomation(a); err != nil {
		t.Fatal(err)
	}
	copy := clonePlanDocument(&store.SessionPlanDocument{Automation: a})
	copy.Automation.Definition.Authorization.AllowedTools[0] = "write"
	if a.Definition.Authorization.AllowedTools[0] != "read" {
		t.Fatal("clone rewrote original policy")
	}
	for _, change := range []func(*store.SessionPlanAutomationIntent){
		func(a *store.SessionPlanAutomationIntent) { a.Definition.Enabled = true },
		func(a *store.SessionPlanAutomationIntent) { a.Definition.Schedule.Timezone = "UTC" },
		func(a *store.SessionPlanAutomationIntent) { a.Definition.Schedule.IntervalSeconds = 59 },
		func(a *store.SessionPlanAutomationIntent) { a.Definition.Schedule.IntervalSeconds = 31622401 },
		func(a *store.SessionPlanAutomationIntent) { a.Definition.Plans[0].Plan.DocumentSHA256 = "" },
		func(a *store.SessionPlanAutomationIntent) { a.DefinitionRevision = 0 },
	} {
		bad := clonePlanAutomation(a)
		change(bad)
		if err := validatePlanAutomation(bad); err == nil {
			t.Fatal("invalid automation accepted")
		}
	}
}
