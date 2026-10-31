package api

import (
	"reflect"
	"testing"
)

// Purpose:
//   - Requirement: manage_jobs takes the swarm_* job tools' then, model and
//     wait_seconds as Swarm Control inputs only: a conversation's call (the
//     Orchestrator or an agent granted manage_jobs) never carries them to
//     this machine's job handlers or, through the link, to a linked
//     machine's (which runs the call as a Swarm Control one), while a Swarm
//     Control caller's call keeps them, as its swarm_* tool took them.
//   - Threat/regression: an agent attaching a workflow (then), overriding a
//     worker's model or holding a call through manage_jobs, directly or by
//     sending the call to a linked machine.
//   - Boundary/authority: nativeJobCallerArgs over the declared
//     ControlInputs (tool.Catalog), as ManageJobs applies it before the local
//     handlers and manageJobsLinked.
//   - Narrowest layer: the argument filter itself; the parity and envelope
//     tests run the actions through both surfaces.
func TestManageJobsControlInputsReachOnlySwarmControl(t *testing.T) {
	call := map[string]any{
		"action": "give_job", "machine": "box", "worker": "w1", "brief": "Do it", "files": []any{"src/"},
		"then": map[string]any{"steps": []any{}}, "model": map[string]any{"provider": "p", "model": "m", "thinking": "low"}, "wait_seconds": float64(30),
	}
	session := nativeJobCallerArgs("give_job", false, call)
	for _, key := range []string{"then", "model", "wait_seconds"} {
		if _, ok := session[key]; ok {
			t.Fatalf("a conversation's give_job kept %s: %v", key, session)
		}
	}
	for _, key := range []string{"machine", "worker", "brief", "files"} {
		if !reflect.DeepEqual(session[key], call[key]) {
			t.Fatalf("a conversation's give_job lost %s: %v", key, session)
		}
	}
	if _, ok := call["then"]; !ok {
		t.Fatal("the filter changed the caller's own arguments")
	}
	if control := nativeJobCallerArgs("give_job", true, call); !reflect.DeepEqual(control, call) {
		t.Fatalf("a Swarm Control give_job lost inputs: %v", control)
	}
	for _, action := range []string{"get_job", "message_job"} {
		if got := nativeJobCallerArgs(action, false, map[string]any{"job_id": "j", "wait_seconds": float64(5)}); got["wait_seconds"] != nil {
			t.Fatalf("a conversation's %s kept wait_seconds", action)
		}
	}
	if got := nativeJobCallerArgs("schedule_job", false, map[string]any{"worker": "w1", "then": map[string]any{}}); got["then"] != nil {
		t.Fatal("a conversation's schedule_job kept then")
	}
	// An action with no Swarm Control inputs passes its arguments as they are.
	stop := map[string]any{"job_id": "j", "reason": "done"}
	if got := nativeJobCallerArgs("stop_job", false, stop); !reflect.DeepEqual(got, stop) {
		t.Fatalf("stop_job arguments changed: %v", got)
	}
}
