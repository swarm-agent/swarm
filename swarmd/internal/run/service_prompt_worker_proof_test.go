package run

import (
	"strings"
	agent "swarm/packages/swarmd/internal/agent"
	store "swarm/packages/swarmd/internal/store/pebble"
	"swarm/packages/swarmd/internal/tool"
	"testing"
)

// Requirement H4: retained worker validation follows the brief and acceptance
// contract before handoff, without contradictory universal reproduction
// commands and without any later landing or proof phase. Owners:
// WorkerCoderAgentPrompt and harnessForProfile. This composition layer checks
// delivered instructions, not model compliance.
func TestWorkerProofContract(t *testing.T) {
	profile := agent.WorkerCoderAgentProfileForParent(store.AgentProfile{})
	prompt := harnessForProfile(tool.WorkspaceScope{}, profile) + profile.Prompt
	for _, text := range []string{"required by the brief, repository rules and declared job acceptance contract before handoff", "skipped or unavailable checks"} {
		if !strings.Contains(prompt, text) {
			t.Errorf("missing contract: %s", text)
		}
	}
	for _, text := range []string{"first write the reproduction and run it before editing product code", "it must fail on the current code", "Do not run builds, test suites or linters on your own initiative", ".swarm-proof.json", "landing"} {
		if strings.Contains(prompt, text) {
			t.Errorf("contradictory or removed instruction: %s", text)
		}
	}
	reviewer := harnessForProfile(tool.WorkspaceScope{}, store.AgentProfile{Name: agent.ReviewerAgentID})
	if !strings.Contains(reviewer, "Review only the assigned tree without modifying it.") || strings.Contains(reviewer, "proof.base") {
		t.Fatal("reviewer harness wrong")
	}
}
