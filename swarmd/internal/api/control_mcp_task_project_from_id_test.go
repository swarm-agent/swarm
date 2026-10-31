package api

import (
	"strings"
	"testing"

	pebblestore "swarm/packages/swarmd/internal/store/pebble"
)

// Purpose: a Swarm Control task call that names its task but not its project
// is accepted where the home machine can find the project from the task id,
// and every step of the call uses that project: single-task wait_tasks adds
// swarm_wait_task's answer (task_id, session_id, settled) and get_task
// include_diff reads the task's changes, both as with project_id given.
// Threat: the review finding where the project was found only for the
// action's own answer, so swarm_wait_task got project_id "" and the whole
// call failed after the wait ran, and the diff read answered changes_error.
// Authority: Runtime.executeManageProjects (locateTaskProject once, then
// manageProjectsSwarmExtras and manageProjectsControlAfter) over Swarm
// Control's catalog call on the parity harness's real home daemon.
func TestControlTaskCallsFindTheProjectFromTheTaskID(t *testing.T) {
	h := newCatalogParityHarness(t)
	store := h.home.s.sessions.Store()
	fleetActivityTestSession(t, h.home, "hs-p1")
	if err := store.PutProjectTask(h.home.account, &pebblestore.ProjectTaskRecord{ID: "p1", ProjectID: "home-project", AccountID: h.home.account, Title: "Located task", Agent: "coder", Status: "needs_review", Revision: 1, SessionID: "hs-p1", ActiveAttemptID: "attempt-p1", Attempts: []pebblestore.ProjectTaskAttempt{{ID: "attempt-p1", SessionID: "hs-p1", Role: "coder", Status: "needs_review"}}}); err != nil {
		t.Fatal(err)
	}
	call := func(args map[string]any) map[string]any {
		t.Helper()
		out, refused := h.control(t, catalogParityFullKey, args)
		if refused != "" {
			t.Fatalf("%v refused: %s", args, refused)
		}
		return envelopeDataMap(parityDecode(t, out))
	}

	// Single-task wait_tasks: swarm_wait_task's fields are added.
	waited := call(map[string]any{"action": "wait_tasks", "task_ids": []any{"p1"}, "wait_seconds": float64(1)})
	if waited["task_id"] != "p1" || waited["settled"] == nil {
		t.Fatalf("wait_tasks without project_id: %v", waited)
	}

	// get_task include_diff: the diff read answers as with project_id.
	without := call(map[string]any{"action": "get_task", "task_id": "p1", "include_diff": true})
	with := call(map[string]any{"action": "get_task", "project_id": "home-project", "task_id": "p1", "include_diff": true})
	if mustJSON(t, without["changes"]) != mustJSON(t, with["changes"]) || without["changes_error"] != with["changes_error"] {
		t.Fatalf("include_diff without project_id read %v / %v, with it %v / %v", without["changes"], without["changes_error"], with["changes"], with["changes_error"])
	}
	if text, _ := without["changes_error"].(string); strings.Contains(text, "project_id") {
		t.Fatalf("diff read without a project: %s", text)
	}
}
