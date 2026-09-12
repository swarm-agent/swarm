package session

import (
	"encoding/json"
	"errors"

	store "swarm/packages/swarmd/internal/store/pebble"
)

func validatePlanAutomation(a *store.SessionPlanAutomationIntent) error {
	if a == nil {
		return nil
	}
	d := a.Definition
	if a.Scope.AccountID == "" || a.Scope.WorkspaceID == "" || a.AutomationID == "" || a.DefinitionRevision == 0 || d.SessionID == "" || d.Name == "" || d.Enabled || d.Authorization.Mode != "approval_required" || d.Authorization.ApprovalReference != "" || d.Authorization.ExpiresAt <= 0 {
		return errors.New("automation requires an exact paused definition, scope, revision and expiry")
	}
	if d.Schedule.Kind != "interval" && d.Schedule.Kind != "cron" {
		return errors.New("automation proposal requires explicit interval or cron recurrence")
	}
	if d.Schedule.Kind == "interval" && (d.Schedule.IntervalSeconds <= 0 || d.Schedule.Timezone != "UTC") {
		return errors.New("interval automation requires positive seconds and UTC time basis")
	}
	if d.Schedule.Kind == "cron" && (d.Schedule.Expression == "" || d.Schedule.Timezone == "") {
		return errors.New("cron automation requires expression and timezone")
	}
	if err := store.ValidateAutomationBindings(d.Plans); err != nil {
		return err
	}
	for _, b := range d.Plans {
		if len(b.Plan.DocumentSHA256) != 64 {
			return errors.New("automation executable instructions require exact document pins")
		}
	}
	return nil
}

func clonePlanAutomation(a *store.SessionPlanAutomationIntent) *store.SessionPlanAutomationIntent {
	if a == nil {
		return nil
	}
	raw, _ := json.Marshal(a)
	var clone store.SessionPlanAutomationIntent
	_ = json.Unmarshal(raw, &clone)
	return &clone
}
