package agent

import (
	store "swarm/packages/swarmd/internal/store/pebble"
	"testing"
)

// Purpose: ReviewerAgentProfileForParent owns the compiled reviewer authority.
// Exact tool allowlist assertions prevent accidental mutation/delegation tools;
// unit testing this materialized contract is narrower than provider execution.
func TestReviewerToolContract(t *testing.T) {
	p := ReviewerAgentProfileForParent(store.AgentProfile{})
	if p.RuntimeMode != store.AgentRuntimeModeRead {
		t.Fatal(p.RuntimeMode)
	}
	want := []string{"read", "search", "find", "list", "websearch", "webfetch", "bash"}
	if len(p.ToolContract.Tools) != len(want) {
		t.Fatal(p.ToolContract.Tools)
	}
	for _, name := range want {
		config, ok := p.ToolContract.Tools[name]
		if !ok || config.Enabled == nil || !*config.Enabled {
			t.Fatal(name)
		}
	}
}
