package agent

import (
	"testing"

	pebblestore "swarm/packages/swarmd/internal/store/pebble"
)

// Purpose: the compiled registry permits task only for the worker Coder's
// bounded helper entrypoint; helpers and all other subagents remain depth-one.
func TestSystemSubagentsDoNotEnableRecursiveTask(t *testing.T) {
	registry, err := BuiltinSystemAgentRegistry()
	if err != nil {
		t.Fatal(err)
	}
	for _, id := range registry.IDs() {
		profile, err := registry.Materialize(id, SwarmAgentProfileForContext(pebblestore.AgentProfile{}))
		if err != nil {
			t.Fatalf("materialize %q: %v", id, err)
		}
		if id == WorkerCoderAgentID && !agentToolEnabled(profile.ToolContract, "task") {
			t.Fatal("worker helper entrypoint missing")
		}
		if profile.Mode == ModeSubagent && id != WorkerCoderAgentID && agentToolEnabled(profile.ToolContract, "task") {
			t.Fatalf("system subagent %q enables recursive task delegation", id)
		}
	}
	if !agentToolEnabled(SwarmAgentToolContract(), "task") {
		t.Fatal("primary Swarm task delegation must remain enabled")
	}
}
