package run

import (
	"testing"

	"swarm/packages/swarmd/internal/tool"
)

// Purpose: wait_tasks never ends or restarts the Orchestrator's turn.
// Requirement: a manage_projects wait_tasks result (status watching, from the
// watch model) is an ordinary tool result: providerManagedToolRequiresTurnRestart
// is false, so the turn goes on and the AI writes its reply. A result shaped
// like the retired pending wait (next_action yield_until_task_outcome) is not
// honoured either. Control: the existing restart signals (restart_turn) still
// restart. Threat: the live failure where the turn ended on the wait call with
// no text and the person saw a silent conversation. Authority:
// providerManagedToolRequiresTurnRestart; a pure predicate test is the
// narrowest layer for this decision.
func TestWaitTasksDoesNotRestartTheTurn(t *testing.T) {
	call := tool.Call{Name: "manage_projects", Arguments: `{"action":"wait_tasks","project_id":"p","task_ids":["t1"]}`}
	for name, output := range map[string]string{
		"watching":      `{"tool":"manage_projects","action":"wait_tasks","status":"watching","watch_id":"watch-1","notice":"Watching 1 task(s): T. Reply to the person now and end your turn; a new turn starts with the outcomes when they settle."}`,
		"retired_yield": `{"tool":"manage_projects","action":"wait_tasks","status":"waiting_tasks","next_action":"yield_until_task_outcome"}`,
		"cancelled":     `{"tool":"manage_projects","action":"wait_tasks","status":"cancelled","task_ids":["t1"]}`,
	} {
		if providerManagedToolRequiresTurnRestart(call, tool.Result{Output: output}) {
			t.Fatalf("%s: wait_tasks result restarts the turn", name)
		}
	}
	if !providerManagedToolRequiresTurnRestart(call, tool.Result{Output: `{"restart_turn":true}`}) {
		t.Fatal("control: restart_turn no longer restarts")
	}
}
