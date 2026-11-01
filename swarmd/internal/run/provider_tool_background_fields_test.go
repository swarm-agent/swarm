package run

import (
	"encoding/json"
	"testing"

	"swarm/packages/swarmd/internal/tool"
)

// Purpose: a bash call's terminal session event carries the background
// command fields of its own structured result: background_id (the command
// it started, moved to the background, or waited on) and still_running (a
// wait that returned before the command ended). The event's output is only
// the command's text, and the agent stall check (api
// agentStallCallsFromEvents / agentStallWaitStart) reads these to name the
// command a wait is on and to tell a wait that ended with its command from
// one that hit its bound. Threat (review findings): without them a stall
// named an unrelated bash call as the hung command, and kept reporting an
// ended command as running. Negative: another tool's result with the same
// keys, or a bash result without them, adds nothing. Authority:
// providerManagedV3ToolEventPayload (providerManagedV3ToolBackgroundFields).
// Layer: the payload builder, the narrowest layer that writes the event.
func TestProviderToolTerminalEventCarriesBashBackgroundFields(t *testing.T) {
	payloadOf := func(name, output string) map[string]any {
		t.Helper()
		call := tool.Call{CallID: "c1", Name: name, Arguments: `{"background_id":"bg_1","command":"wait"}`}
		result := tool.Result{CallID: "c1", Name: name, Output: output}
		raw, err := providerManagedV3ToolEventPayload("session.tool.completed", providerToolInvokerConfig{runID: "run-1", step: 1}, call, nil, result, 1)
		if err != nil {
			t.Fatal(err)
		}
		var payload map[string]any
		if err := json.Unmarshal(raw, &payload); err != nil {
			t.Fatal(err)
		}
		return payload
	}

	running := payloadOf("bash", `{"background_id":"bg_1","status":"running","still_running":true,"output":"step 3/9"}`)
	if running["background_id"] != "bg_1" || running["still_running"] != true || running["output"] != "step 3/9" {
		t.Fatalf("still-running wait fields missing: %+v", running)
	}
	ended := payloadOf("mcp__swarm__bash", `{"background_id":"bg_1","status":"exited","exit_code":0,"still_running":false,"output":"ok"}`)
	if ended["background_id"] != "bg_1" || ended["still_running"] != false || ended["exit_code"] != float64(0) {
		t.Fatalf("ended wait fields missing: %+v", ended)
	}
	foreground := payloadOf("bash", `{"exit_code":0,"output":"done"}`)
	if _, ok := foreground["background_id"]; ok {
		t.Fatalf("a foreground result gained a background_id: %+v", foreground)
	}
	if _, ok := foreground["still_running"]; ok {
		t.Fatalf("a foreground result gained still_running: %+v", foreground)
	}
	other := payloadOf("read", `{"background_id":"bg_9","still_running":true}`)
	if _, ok := other["background_id"]; ok {
		t.Fatalf("a non-bash result gained background fields: %+v", other)
	}
}
