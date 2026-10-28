package api

import (
	"context"
	"encoding/json"
	"net/http"
	"strings"
	"testing"
	"time"

	pebblestore "swarm/packages/swarmd/internal/store/pebble"
	"swarm/packages/swarmd/internal/tool"
)

// Purpose: linked-machine errors say what to do. Requirement: a wrong machine
// name lists the linked machines and their linked work access (tool and the
// read_task_session service), never only "check the Permissions page"; a
// machine whose linked work is off says the person turns it on there and
// lists the machines; an action a linked machine does not serve lists the
// ones it does; waiting on a linked task that is pending_approval says the
// person starts it in the app and never asks the agent to obtain approval.
// Each refusal leaves the run running.
// Threat: the live failures: the Orchestrator guessing machine names, asking
// the person to check settings, or chasing approval.
// Owners: tool executeFleetProjects (FleetMachineUnavailableError,
// FleetActionUnavailableError), executeFleetWaitTasks
// (ProjectTaskNotStartedReason), Server.CallFleetProject and
// Server.ReadFleetLinkedTaskSession over two in-process daemons: the tool and
// service entries are the narrowest layers that return the agent-visible text.
func TestLinkedMachineErrorsNameTheFix(t *testing.T) {
	h := newRemoteWaitHarness(t, "t1")
	if err := h.remote.s.sessions.Store().PutProjectTask(h.remote.account, &pebblestore.ProjectTaskRecord{ID: "unstarted", ProjectID: "remote-project", Title: "Not started", Agent: "coder", Status: "pending_approval"}); err != nil {
		t.Fatal(err)
	}
	manage := func(args map[string]any) error {
		t.Helper()
		raw, _ := json.Marshal(args)
		ctx, cancel := context.WithTimeout(tool.WithVideoRunContext(context.Background(), tool.VideoRunContext{SessionID: remoteWaitGoal, RunID: "run-1"}), 20*time.Second)
		defer cancel()
		_, err := h.runtime.ExecuteForWorkspaceScopeWithRuntime(ctx, tool.WorkspaceScope{SessionID: remoteWaitGoal, Principal: h.p}, tool.Call{Name: "manage_projects", Arguments: string(raw)})
		return err
	}
	says := func(name string, err error, want ...string) {
		t.Helper()
		if err == nil {
			t.Fatalf("%s: accepted", name)
		}
		for _, text := range want {
			if !strings.Contains(err.Error(), text) {
				t.Fatalf("%s: error %q does not say %q", name, err, text)
			}
		}
		for _, text := range []string{"check the Permissions page", "obtain task approval"} {
			if strings.Contains(err.Error(), text) {
				t.Fatalf("%s: error %q says %q", name, err, text)
			}
		}
		if owner, _ := h.intent(t, "run-1"); owner.Status != pebblestore.V3RunIntentRunning || owner.TaskWait != nil {
			t.Fatalf("%s changed the run: %+v", name, owner)
		}
	}

	says("wrong machine", manage(map[string]any{"action": "list_tasks", "machine": "boxx", "project_id": "remote-project"}),
		`no linked machine is named "boxx"`, "box (linked work auto")
	says("unserved action", manage(map[string]any{"action": "delete_task", "machine": "box", "project_id": "remote-project", "task_id": "t1"}),
		"delete_task is not available on a linked machine", "list_tasks", "get_task", "wait_tasks", "read_task_session")
	_, err := h.home.s.CallFleetProject(context.Background(), h.p, "box", "delete_task", map[string]any{"project_id": "remote-project", "task_id": "t1"})
	says("unserved action (service)", err, "list_tasks", "read_task_session")
	says("wait on pending_approval", func() error { _, err := h.wait(t, remoteWaitGoal, "unstarted"); return err }(),
		"task unstarted on box is pending_approval", "the person starts it in the app", "Do not ask for or chase the approval")

	ctx, cancel := context.WithTimeout(context.Background(), 20*time.Second)
	defer cancel()
	_, err = h.home.s.ReadFleetLinkedTaskSession(ctx, h.p, "boxx", "remote-project", "t1", 0)
	says("wrong machine (read_task_session)", err, `no linked machine is named "boxx"`, "box (linked work auto")
	if status := linkedTaskStatus(err); status != http.StatusNotFound {
		t.Fatalf("wrong machine status %d", status)
	}

	fleetActivityTestSetWork(t, h.home, "box", pebblestore.RuleOff)
	says("work off", manage(map[string]any{"action": "list_tasks", "machine": "box", "project_id": "remote-project"}),
		`linked work for machine "box" is off`, "turns it on", "box (linked work off")
	_, err = h.home.s.ReadFleetLinkedTaskSession(ctx, h.p, "box", "remote-project", "t1", 0)
	says("work off (read_task_session)", err, `linked work for machine "box" is off`, "box (linked work off")
	if status := linkedTaskStatus(err); status != http.StatusForbidden {
		t.Fatalf("work off status %d", status)
	}
}
