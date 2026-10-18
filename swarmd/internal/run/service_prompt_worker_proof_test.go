package run

import (
	"strings"
	agent "swarm/packages/swarmd/internal/agent"
	store "swarm/packages/swarmd/internal/store/pebble"
	"swarm/packages/swarmd/internal/tool"
	"testing"
)

// Requirement H4: retained worker validation follows its declared phase and
// acceptance contract, without contradictory universal reproduction commands.
// Owners: WorkerCoderAgentPrompt and harnessForProfile. This composition layer
// checks delivered instructions, not model compliance or a landing execution.
func TestWorkerProofContract(t *testing.T) {
	profile := agent.WorkerCoderAgentProfileForParent(store.AgentProfile{})
	prompt := harnessForProfile(tool.WorkspaceScope{}, profile) + profile.Prompt
	for _, text := range []string{"required by the brief, repository rules and declared job acceptance contract in their designated phase", ".swarm-proof.json", "never claim landing results before they exist", "skipped or unavailable checks"} {
		if !strings.Contains(prompt, text) {
			t.Errorf("missing contract: %s", text)
		}
	}
	for _, text := range []string{"first write the reproduction and run it before editing product code", "it must fail on the current code", "Do not run builds, test suites or linters on your own initiative"} {
		if strings.Contains(prompt, text) {
			t.Errorf("contradictory universal instruction: %s", text)
		}
	}
	reviewer := harnessForProfile(tool.WorkspaceScope{}, store.AgentProfile{Name: agent.ReviewerAgentID})
	if !strings.Contains(reviewer, "tests the actual requirement") || !strings.Contains(reviewer, "proof.base and proof.head") {
		t.Fatal("reviewer lacks proof scrutiny")
	}
	regular := harnessForProfile(tool.WorkspaceScope{}, agent.CoderAgentProfileForParent(store.AgentProfile{}))
	if strings.Contains(regular, "landing phase") || strings.Contains(regular, ".swarm-proof.json") {
		t.Fatal("worker landing policy leaked into regular Coder")
	}
}
