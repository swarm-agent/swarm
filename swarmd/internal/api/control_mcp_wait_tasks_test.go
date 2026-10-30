package api

import (
	"encoding/json"
	"strings"
	"sync"
	"testing"
	"time"

	sessionruntime "swarm/packages/swarmd/internal/session"
	pebblestore "swarm/packages/swarmd/internal/store/pebble"
	"swarm/packages/swarmd/internal/tool"
)

// followedChanges captures the follows FollowProjectTaskChanges makes, so a
// test acts only after the waiter pinned the task's session, which it does
// right after reading the tasks' baseline.
func followedChanges(t *testing.T) func(sessionID string) {
	t.Helper()
	var mu sync.Mutex
	var follows []*projectTaskChanges
	projectTaskChangesFollowed = func(f *projectTaskChanges) {
		mu.Lock()
		defer mu.Unlock()
		follows = append(follows, f)
	}
	t.Cleanup(func() { projectTaskChangesFollowed = nil })
	return func(sessionID string) {
		t.Helper()
		deadline := time.Now().Add(20 * time.Second)
		for time.Now().Before(deadline) {
			mu.Lock()
			for _, f := range follows {
				if f.has(sessionID) {
					mu.Unlock()
					return
				}
			}
			mu.Unlock()
			time.Sleep(10 * time.Millisecond)
		}
		t.Fatalf("the wait never read its baseline of %s", sessionID)
	}
}

// touchHome records a run start and end on a home task session through the
// home's own V3 mutation path, which publishes to its realtime outbox.
func touchHome(t *testing.T, h *catalogParityHarness, session, key string) {
	t.Helper()
	request := session + "-" + key
	for _, in := range []sessionruntime.SessionMutationInput{
		{Kind: sessionruntime.SessionMutationRecordRunIntent, SessionID: session, UserID: h.home.user, AccountScopeID: h.home.account, ClientRequestID: "pending-" + request, IdempotencyKey: "pending-" + request, PayloadHash: "pending-" + request, RunIntent: &pebblestore.V3SessionRunIntent{RunID: "child-" + key, Status: pebblestore.V3RunIntentPendingExecutor}},
		{Kind: sessionruntime.SessionMutationRecordRunIntent, EventType: "session.run.completed", SessionID: session, UserID: h.home.user, AccountScopeID: h.home.account, ClientRequestID: request, IdempotencyKey: request, PayloadHash: request, RunIntent: &pebblestore.V3SessionRunIntent{RunID: "child-" + key, Status: pebblestore.V3RunIntentCompleted}},
	} {
		if _, err := h.home.s.applySessionV3PrimaryMutation(in); err != nil {
			t.Fatal(err)
		}
	}
}

// controlWait runs wait_tasks over Swarm Control in the background.
func controlWait(t *testing.T, h *catalogParityHarness, args map[string]any) <-chan [2]string {
	t.Helper()
	done := make(chan [2]string, 1)
	go func() {
		out, errText := h.control(t, catalogParityFullKey, args)
		done <- [2]string{out, errText}
	}()
	return done
}

func awaitControl(t *testing.T, done <-chan [2]string) map[string]any {
	t.Helper()
	select {
	case got := <-done:
		if got[1] != "" {
			t.Fatalf("wait_tasks: %s", got[1])
		}
		return parityDecode(t, got[0])
	case <-time.After(30 * time.Second):
		t.Fatal("wait_tasks did not return")
	}
	return nil
}

// Purpose: over Swarm Control, wait_tasks waits instead of returning a
// snapshot: the call is held until a listed task reaches its next milestone,
// here and on a linked machine, and returns the tasks as get_task reads
// them with state milestone and the milestone reached; with nothing
// happening it returns timed_out after wait_seconds. Threat: an MCP client
// polling (a snapshot it must call again and again), a wait that never
// returns, or one that misses the change. Boundary: Runtime.controlTaskWait
// over Server.FollowProjectTaskChanges (the realtime outbox hub here, the
// fleet activity feed there), driven by real V3 mutations on two in-process
// daemons; no provider runs.
func TestControlWaitTasksReturnsAtTheNextMilestone(t *testing.T) {
	h := newCatalogParityHarness(t)
	pinned := followedChanges(t)
	store := h.home.s.sessions.Store()
	fleetActivityTestSession(t, h.home, "hs-m1")
	if err := store.PutProjectTask(h.home.account, &pebblestore.ProjectTaskRecord{ID: "m1", ProjectID: "home-project", AccountID: h.home.account, Title: "Milestone task", Agent: "coder", Status: "running", Revision: 1, SessionID: "hs-m1", ActiveAttemptID: "attempt-m1", Attempts: []pebblestore.ProjectTaskAttempt{{ID: "attempt-m1", SessionID: "hs-m1", Role: "coder", Status: "running"}}}); err != nil {
		t.Fatal(err)
	}

	t.Run("here", func(t *testing.T) {
		started := time.Now()
		done := controlWait(t, h, map[string]any{"action": "wait_tasks", "project_id": "home-project", "task_ids": []any{"m1"}, "wait_seconds": float64(25)})
		pinned("hs-m1")
		if _, err := store.UpdateProjectTask(h.home.account, "home-project", "m1", func(task *pebblestore.ProjectTaskRecord) error {
			task.Status, task.LastError = "failed", "tests failed"
			return nil
		}); err != nil {
			t.Fatal(err)
		}
		touchHome(t, h, "hs-m1", "failed")
		envelope := awaitControl(t, done)
		data := envelopeDataMap(envelope)
		rows, _ := data["tasks"].([]any)
		if envelope["state"] != tool.ControlTaskWaitMilestone || envelope["machine"] == "box" || len(rows) != 1 || time.Since(started) > 20*time.Second {
			t.Fatalf("wait here: %v", envelope)
		}
		row := rows[0].(map[string]any)
		if row["reached"] != pebblestore.ProjectTaskMilestoneFailed || row["task"].(map[string]any)["id"] != "m1" {
			t.Fatalf("milestone row: %v", row)
		}
	})

	t.Run("linked", func(t *testing.T) {
		done := controlWait(t, h, map[string]any{"action": "wait_tasks", "machine": "box", "project_id": "remote-project", "task_ids": []any{"t2"}, "wait_seconds": float64(25)})
		pinned("rs-t2")
		h.finish(t, "t2", "needs_review", "Done.", "")
		envelope := awaitControl(t, done)
		rows, _ := envelopeDataMap(envelope)["tasks"].([]any)
		if envelope["state"] != tool.ControlTaskWaitMilestone || envelope["machine"] != "box" || envelope["untrusted_content"] != true || len(rows) != 1 {
			t.Fatalf("wait on box: %v", envelope)
		}
		if row := rows[0].(map[string]any); row["reached"] != pebblestore.ProjectTaskMilestoneDone {
			t.Fatalf("milestone row on box: %v", row)
		}
	})

	t.Run("timed out", func(t *testing.T) {
		started := time.Now()
		envelope := awaitControl(t, controlWait(t, h, map[string]any{"action": "wait_tasks", "project_id": "home-project", "task_ids": []any{"w1"}, "wait_seconds": float64(1)}))
		if envelope["state"] != tool.ControlTaskWaitTimedOut || time.Since(started) < time.Second || !strings.Contains(asTestString(envelope["next_action"]), "wait_tasks again") {
			t.Fatalf("timed out wait: %v", envelope)
		}
	})

	t.Run("bounds", func(t *testing.T) {
		for _, seconds := range []float64{-1, 500} {
			if _, errText := h.control(t, catalogParityFullKey, map[string]any{"action": "wait_tasks", "project_id": "home-project", "task_ids": []any{"w1"}, "wait_seconds": seconds}); errText == "" {
				t.Fatalf("wait_seconds %v was accepted", seconds)
			}
		}
	})
}

// Purpose: a task a Swarm Control caller sends to a linked machine is
// recorded at home like one the Orchestrator sends, with the caller's AI key
// as its sender (no session is invented), so list_linked_tasks lists it and
// get_task_session and read_task_session follow it, with the same answer the
// Orchestrator gets for the same read. Threat: MCP sends nobody at home can
// follow, or records attributed to a fake session. Boundary:
// executeFleetProjects -> Server.RecordFleetLinkedTask -> FleetStore
// PutLinkedTask (sender index), executeListLinkedTasks, over the real link.
func TestControlSendToLinkedMachineIsRecordedAndFollowed(t *testing.T) {
	h := newCatalogParityHarness(t)
	out, errText := h.control(t, catalogParityFullKey, map[string]any{"action": "propose_task", "machine": "box", "project_id": "remote-project", "title": "Sent over MCP", "agent": "finder", "prompt": "Find it."})
	if errText != "" {
		t.Fatal(errText)
	}
	sent := envelopeDataMap(parityDecode(t, out))
	taskID, _ := sent["linked_task_id"].(string)
	if taskID == "" || sent["linked_record_error"] != nil {
		t.Fatalf("send was not recorded: %s", out)
	}
	record, found, err := h.home.s.fleet.GetLinkedTask(h.home.account, "box", "remote-project", taskID)
	if err != nil || !found || record.HomeSender != "key:key-parity" || record.HomeSessionID != "" {
		t.Fatalf("record: %+v %v %v", record, found, err)
	}
	// Listed as the caller's own sends.
	out, errText = h.control(t, catalogParityFullKey, map[string]any{"action": "list_linked_tasks"})
	listed, _ := envelopeDataMap(parityDecode(t, out))["linked_tasks"].([]any)
	if errText != "" || len(listed) != 1 || listed[0].(map[string]any)["task_id"] != taskID {
		t.Fatalf("list_linked_tasks: %s %s", out, errText)
	}
	// get_task follows it.
	out, errText = h.control(t, catalogParityFullKey, map[string]any{"action": "get_task", "machine": "box", "project_id": "remote-project", "task_id": taskID})
	if errText != "" || !strings.Contains(out, taskID) {
		t.Fatalf("get_task: %s %s", out, errText)
	}
	// The task's session on the machine (the executor would make it).
	session := "rs-" + taskID
	fleetActivityTestSession(t, h.remote, session)
	if _, err := h.remote.s.sessions.Store().UpdateProjectTask(h.remote.account, "remote-project", taskID, func(task *pebblestore.ProjectTaskRecord) error {
		task.Status, task.SessionID = "running", session
		task.Attempts = []pebblestore.ProjectTaskAttempt{{ID: "attempt-" + taskID, SessionID: session, Role: "finder", Status: "running"}}
		task.ActiveAttemptID = "attempt-" + taskID
		return nil
	}); err != nil {
		t.Fatal(err)
	}
	// read_task_session and get_task_session over MCP give what the
	// Orchestrator gets for the same read.
	for _, action := range []string{"read_task_session", tool.FleetTaskSessionAction} {
		args := map[string]any{"action": action, "machine": "box", "project_id": "remote-project", "task_id": taskID}
		control, errText := h.control(t, catalogParityFullKey, args)
		orchestrator, err := h.orchestrator(t, args)
		if errText != "" || err != nil {
			t.Fatalf("%s: Swarm Control %q, Orchestrator %v", action, errText, err)
		}
		a, _ := json.Marshal(parityStrip(parityDecode(t, control)))
		b, _ := json.Marshal(parityStrip(parityDecode(t, orchestrator)))
		if string(a) != string(b) || parityDecode(t, control)["machine"] != "box" {
			t.Fatalf("%s differs:\nSwarm Control: %s\nOrchestrator:  %s", action, a, b)
		}
	}
	// The Orchestrator's own conversation does not list the caller's sends.
	orch, err := h.orchestrator(t, map[string]any{"action": "list_linked_tasks"})
	if err != nil {
		t.Fatal(err)
	}
	for _, row := range envelopeDataMap(parityDecode(t, orch))["linked_tasks"].([]any) {
		if row.(map[string]any)["task_id"] == taskID {
			t.Fatalf("the Orchestrator's conversation lists the MCP caller's send: %s", orch)
		}
	}
}
