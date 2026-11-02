package tool

import (
	"reflect"
	"slices"
	"strings"
	"testing"

	pebblestore "swarm/packages/swarmd/internal/store/pebble"
)

// Purpose: the Orchestrator's manage_projects definition declares approve_task
// and refine_task with every field of the exact plan binding the approval
// service checks (ProjectTaskApprovalGuards), so a plan waiting on a linked
// machine can be accepted or revised from the conversation that sent it.
// Requirements: the action enum lists approve_task and refine_task (accept_task
// and deploy_task stay unlisted aliases/steps), each of approve_task,
// accept_task and refine_task takes machine and every guard field by its JSON
// name with the right type, both reach a linked machine whose linked work rule
// gates them as task starts, and Swarm Control still does not serve them.
// Threat (forge, 2026-11-02): the handler and the approve route took
// session_id, plan_id and definition_revision, but the resolved schema omitted
// approve_task, refine_task and those fields, so a model could only guess them
// through additionalProperties, and a guard renamed in the service would drift
// silently from the declaration. Owners: manageProjectsCatalog,
// CatalogTool.OrchestratorDefinition and InputSchema, ProjectTaskApprovalGuards,
// FleetProjectActionAllowed and FleetTaskStart. Narrowest layer: the
// declarations against the service's own guard type.
func TestApproveAndRefineDeclareTheExactPlanBindingForTheOrchestrator(t *testing.T) {
	definition := manageProjectsDefinition()
	properties := definition.Parameters["properties"].(map[string]any)
	actions := properties["action"].(map[string]any)["enum"].([]string)
	for _, listed := range []string{"approve_task", "refine_task"} {
		if !slices.Contains(actions, listed) {
			t.Fatalf("the Orchestrator's manage_projects does not list %s: %v", listed, actions)
		}
	}
	for _, unlisted := range []string{"accept_task", "deploy_task", "edit_requirements"} {
		if slices.Contains(actions, unlisted) {
			t.Fatalf("the Orchestrator's manage_projects lists %s", unlisted)
		}
	}
	guards := reflect.TypeOf(ProjectTaskApprovalGuards{})
	catalog := manageProjectsCatalog()
	for _, name := range []string{"approve_task", "accept_task", "refine_task"} {
		action, ok := catalog.Action(name)
		if !ok {
			t.Fatalf("%s is not declared", name)
		}
		schema := catalog.InputSchema(action)["properties"].(map[string]any)
		if schema["machine"] == nil || schema["task_id"] == nil {
			t.Fatalf("%s does not take machine and task_id: %v", name, schema)
		}
		for i := 0; i < guards.NumField(); i++ {
			field := guards.Field(i)
			key, _, _ := strings.Cut(field.Tag.Get("json"), ",")
			declared, ok := schema[key].(map[string]any)
			if !ok {
				t.Fatalf("%s does not declare the plan guard %q the approval service reads", name, key)
			}
			want := "string"
			if field.Type.Kind() == reflect.Int {
				want = "integer"
			}
			if declared["type"] != want {
				t.Fatalf("%s %s is declared %v, the service reads %s", name, key, declared["type"], want)
			}
			if name != "accept_task" && properties[key] == nil {
				t.Fatalf("the Orchestrator's definition lacks %s for %s", key, name)
			}
		}
		if action.Reach != CatalogReachAny || !FleetProjectActionAllowed(name) || !FleetTaskStart("manage_projects", name) {
			t.Fatalf("%s: reach %s, linked %v, gated as a task start %v", name, action.Reach, FleetProjectActionAllowed(name), FleetTaskStart("manage_projects", name))
		}
		if action.ControlCallable() {
			t.Fatalf("%s is served to Swarm Control; its parity case is not served", name)
		}
	}
	// The Orchestrator is shown the tool's description, not each action's:
	// the rule that only the person accepts a plan is stated there.
	if !strings.Contains(definition.Description, "approve_task with get_task's plan binding only on their explicit acceptance, never on your own") || !strings.Contains(definition.Description, "get_task detail=plan") {
		t.Fatalf("the Orchestrator's description does not state plan acceptance: %q", definition.Description)
	}
	approve, _ := catalog.Action("approve_task")
	if !strings.Contains(approve.Description, "explicit word") || !strings.Contains(approve.Description, "never on your own") || !strings.Contains(approve.Description, "detail=plan") {
		t.Fatalf("approve_task does not say it runs only on the person's acceptance of the shown plan: %q", approve.Description)
	}
}

// Purpose: get_task names a task bound to a submitted plan as waiting for the
// person to accept that plan, with the binding and how to show and accept it,
// while a task without a plan keeps the account approval wait. Requirements:
// a pending bound task with auto_approve set reads needs_you with
// ProjectTaskPlanAcceptanceReason, next_action approve_task, its exact
// plan_binding and a next that shows the plan first (with the machine for a
// linked task); an unbound pending task reads "waiting for approval" and
// "starts when approved", and a started bound task shows no binding to
// accept. Threat: a plan wait mislabelled as the Start tasks rule (which
// auto_approve already passed), or the small-task approval path changed.
// Owners: compactProjectTask, projectTaskNext, projectTaskPlanGuardFor and
// pebblestore.ProjectTaskState. Narrowest layer: the view function itself.
func TestGetTaskNamesAWaitingPlanAsPlanAcceptance(t *testing.T) {
	binding := &pebblestore.ProjectTaskPlanBinding{SessionID: "plan-session", PlanID: "plan-a", DefinitionRevision: 3, Receipt: "synthetic-receipt"}
	bound := pebblestore.ProjectTaskRecord{ID: "t1", ProjectID: "p1", Title: "Plan task", Agent: "coder", Status: "pending_approval", AutoApprove: true, SessionID: "plan-session", PlanBinding: binding}
	for _, remote := range []bool{false, true} {
		view := compactProjectTask(projectTaskRead{ProjectTaskRecord: bound}, remote)
		if view.State != pebblestore.ProjectTaskStateNeedsYou || view.StateReason != pebblestore.ProjectTaskPlanAcceptanceReason || view.NextAction != "approve_task" {
			t.Fatalf("remote=%v: %+v", remote, view)
		}
		if view.PlanBinding == nil || view.PlanBinding.SessionID != "plan-session" || view.PlanBinding.PlanID != "plan-a" || view.PlanBinding.DefinitionRevision != 3 {
			t.Fatalf("remote=%v: binding %+v", remote, view.PlanBinding)
		}
		next := view.Progress.Next
		if !strings.Contains(next, "detail=plan") || !strings.Contains(next, "explicit acceptance") || strings.Contains(next, "machine") != remote {
			t.Fatalf("remote=%v: next %q", remote, next)
		}
	}
	unbound := pebblestore.ProjectTaskRecord{ID: "t2", ProjectID: "p1", Title: "Small task", Agent: "coder", Status: "pending_approval"}
	view := compactProjectTask(projectTaskRead{ProjectTaskRecord: unbound}, false)
	if view.StateReason != "waiting for approval" || view.NextAction != "approve_task" || view.Progress.Next != "starts when approved" || view.PlanBinding != nil {
		t.Fatalf("unbound pending task: %+v", view)
	}
	started := bound
	started.Status = "in_progress"
	if view := compactProjectTask(projectTaskRead{ProjectTaskRecord: started}, false); view.PlanBinding != nil || view.NextAction == "approve_task" {
		t.Fatalf("a started plan task still asks for acceptance: %+v", view)
	}
}
