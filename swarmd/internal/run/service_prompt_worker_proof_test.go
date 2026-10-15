package run

import (
	"strings"
	agent "swarm/packages/swarmd/internal/agent"
	store "swarm/packages/swarmd/internal/store/pebble"
	"swarm/packages/swarmd/internal/tool"
	"testing"
)

// Requirement: harnessForProfile must give a problem-solving coder an explicit
// reproduce-before-editing contract, not generic passing-test evidence. This
// prompt-layer test proves instruction delivery, not live model compliance.
func TestWorkerProofContract(t *testing.T) {
	prompt := harnessForProfile(tool.WorkspaceScope{}, store.AgentProfile{Name: agent.WorkerCoderAgentID})
	for _, text := range []string{"first write the reproduction and run it before editing product code", "it must fail on the current code", "stop and report that instead of changing code", "same reproduction again; it must pass", "Commit the reproduction with the fix", ".swarm-proof.json", "failing-then-passing reproduction"} {
		if !strings.Contains(prompt, text) {
			t.Errorf("missing contract: %s", text)
		}
	}
	reviewer := harnessForProfile(tool.WorkspaceScope{}, store.AgentProfile{Name: agent.ReviewerAgentID})
	if !strings.Contains(reviewer, "not something easier") || !strings.Contains(reviewer, "proof.base and proof.head") {
		t.Fatal("reviewer lacks proof scrutiny")
	}
}
