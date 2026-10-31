package api

import (
	"reflect"
	"testing"

	pebblestore "swarm/packages/swarmd/internal/store/pebble"
	"swarm/packages/swarmd/internal/tool"
)

// Purpose:
//   - Requirement: a conversation's manage_workers call never carries the
//     action's Swarm Control inputs (the swarm_* worker tools' immediate
//     changes) to a linked machine. The linked machine runs a linked call as
//     a Swarm Control one, so a forwarded preset, workspace_path, role,
//     worker, agent, run_id or schedule_id would make or change a worker
//     there at once; the Orchestrator only proposes workers (AGENTS.md).
//   - Threat/regression: the home Orchestrator calling manage_workers create
//     {machine, preset} or update {machine, run_id} and activating or
//     changing a worker on a linked machine without the person's acceptance.
//   - Boundary/authority: Server.ManageWorkers (workerConversationArgs
//     applied before the linked body is built) over
//     tool.CatalogConversationArgs; the linked machine's worker store is
//     the observable state.
//   - Narrowest layer: the Orchestrator's real tool call to the harness's
//     real linked machine ("box"); no provider runs.
func TestManageWorkersConversationNeverForwardsSwarmControlInputs(t *testing.T) {
	h := newCatalogParityHarness(t)
	remote := h.remote.s.sessions.Store()
	workers := remote.WorkerStore()
	activeOnBox := func() int {
		t.Helper()
		listed, err := workers.ListWorkers(h.remote.account, pebblestore.ListWorkersQuery{Limit: 200})
		if err != nil {
			t.Fatal(err)
		}
		count := 0
		for _, worker := range listed.Workers {
			if worker.LifecycleState == pebblestore.WorkerLifecycleStateActive {
				count++
			}
		}
		return count
	}
	before := activeOnBox()

	// create with swarm_deploy_worker's preset: forwarded, the linked
	// machine would deploy an active finder at once.
	_, _ = h.orchestratorTool(t, "manage_workers", map[string]any{"action": "create", "machine": "box", "preset": "finder"})
	_, _ = h.orchestratorTool(t, "manage_workers", map[string]any{"action": "create", "machine": "box", "role": "finder", "name": "Linked probe", "instructions": "Probe."})
	if after := activeOnBox(); after != before {
		t.Fatalf("a conversation's linked create made an active worker: %d -> %d active", before, after)
	}

	// update with run_id (swarm_manage_worker cancel_run): forwarded, the
	// linked machine would cancel the run at once.
	worker := h.workers["box"]
	session := pebblestore.SessionSnapshot{ID: "worker-execution-linked-probe", AccountScopeID: h.remote.account, UserID: h.remote.user, Metadata: map[string]any{"worker_id": worker, "worker_execution_run_id": "linked-probe"}}
	if err := remote.CreateSessionForAccount(session, h.remote.user, h.remote.account); err != nil {
		t.Fatal(err)
	}
	run := pebblestore.WorkerRunRecord{ID: "linked-probe", WorkerID: worker, UserID: h.remote.user, Role: pebblestore.WorkerRoleCoder, SessionID: session.ID, Status: "running", RequestSource: "direct", Input: map[string]any{"prompt": "Probe."}, CreatedAt: 1, StartedAt: 1}
	if _, err := workers.RecordWorkerRun(h.remote.account, run); err != nil {
		t.Fatal(err)
	}
	_, _ = h.orchestratorTool(t, "manage_workers", map[string]any{"action": "update", "machine": "box", "worker_id": worker, "run_id": "linked-probe"})
	if got, ok, err := workers.GetWorkerRun(h.remote.account, worker, "linked-probe"); err != nil || !ok || got.Status != "running" {
		t.Fatalf("a conversation's linked update changed the run: %v %v %+v", ok, err, got)
	}

	// The filter itself: every Swarm Control input of create and update is
	// dropped for a conversation, own inputs stay, and a Swarm Control
	// caller keeps everything.
	for _, name := range []string{"create", "update"} {
		declared, _ := manageWorkersAction(name)
		call := map[string]any{"action": name, "machine": "box", "worker_id": "w", "name": "N"}
		for _, key := range declared.ControlInputs {
			call[key] = "x"
		}
		conversation := tool.CatalogConversationArgs(declared, false, call)
		for _, key := range declared.ControlInputs {
			if _, kept := conversation[key]; kept && !containsString(declared.Inputs, key) {
				t.Fatalf("%s: a conversation kept Swarm Control input %s", name, key)
			}
		}
		if conversation["machine"] != "box" || conversation["name"] != "N" {
			t.Fatalf("%s: a conversation lost its own inputs: %v", name, conversation)
		}
		if control := tool.CatalogConversationArgs(declared, true, call); !reflect.DeepEqual(control, call) {
			t.Fatalf("%s: a Swarm Control call lost inputs", name)
		}
	}
}
