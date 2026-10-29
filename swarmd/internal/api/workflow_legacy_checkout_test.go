package api

import (
	"strings"
	"testing"

	"swarm/packages/swarmd/internal/jobautomation"
)

// Requirement: swarm_list_workflows and GET /v3/job-automations/{id} (both
// render jobAutomationView) show a stored step that runs migrated (swarm or
// coder with checkout none or same): its stored and effective checkout and
// a note to resave; a delivery's step shows the note its run recorded.
// Steps that run as stored get no hint.
// Threat: a migration the owner cannot see, so the legacy definition is
// never resaved; or a hint on steps that are not migrated.
// Owners: jobAutomationView (jobAutomationStepMigrations) and
// jobDeliveryView. The views are pure renderings of the stored records, the
// narrowest layer that proves what the listing shows.
func TestWorkflowViewsShowLegacyCheckoutMigration(t *testing.T) {
	legacy := jobautomation.Automation{ID: "jauto_1", Name: "after merge", Steps: []jobautomation.Step{
		{ID: "script", Type: jobautomation.StepAgent, Worker: "swarm", Checkout: jobautomation.CheckoutNone, Brief: "b"},
		{ID: "find", Type: jobautomation.StepAgent, Worker: "finder", Checkout: jobautomation.CheckoutNone, Brief: "b"},
		{ID: "review", Type: jobautomation.StepAgent, Worker: "reviewer", Checkout: jobautomation.CheckoutSame, Brief: "b"},
	}}
	view := jobAutomationView(legacy)
	migrations, ok := view["step_migrations"].([]map[string]any)
	if !ok || len(migrations) != 1 {
		t.Fatalf("step_migrations = %#v, want one entry", view["step_migrations"])
	}
	m := migrations[0]
	note, _ := m["note"].(string)
	if m["step"] != "script" || m["checkout"] != jobautomation.CheckoutNone || m["effective_checkout"] != jobautomation.CheckoutNew ||
		!strings.HasPrefix(note, jobautomation.LegacyCheckoutNote(jobautomation.CheckoutNone)) || !strings.Contains(note, "resave") {
		t.Fatalf("migration = %#v", m)
	}

	current := legacy
	current.Steps = []jobautomation.Step{{ID: "script", Type: jobautomation.StepAgent, Worker: "swarm", Checkout: jobautomation.CheckoutNew, Brief: "b"}, legacy.Steps[1], legacy.Steps[2]}
	if _, ok := jobAutomationView(current)["step_migrations"]; ok {
		t.Fatal("a workflow with no legacy step shows step_migrations")
	}

	d := jobautomation.Delivery{ID: "jdel_1", Steps: []jobautomation.StepRecord{
		{ID: "script", Type: jobautomation.StepAgent, State: jobautomation.StateSucceeded, Note: jobautomation.LegacyCheckoutNote(jobautomation.CheckoutNone)},
		{ID: "find", Type: jobautomation.StepAgent, State: jobautomation.StateSucceeded},
	}}
	steps := jobDeliveryView(d)["steps"].([]map[string]any)
	if steps[0]["note"] != jobautomation.LegacyCheckoutNote(jobautomation.CheckoutNone) {
		t.Fatalf("delivery step = %#v, want the note", steps[0])
	}
	if _, ok := steps[1]["note"]; ok {
		t.Fatalf("unmigrated step shows a note: %#v", steps[1])
	}
}
