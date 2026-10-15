package agent

import (
	"strings"
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

// Purpose: ReviewerAgentPrompt must instruct checks-first review, fail-closed
// reporting and explicit absence handling. This compiled-contract test guards
// instruction regressions; it does not claim provider compliance.
func TestReviewerChecksPrompt(t *testing.T) {
	prompt := ReviewerAgentPrompt()
	for _, instruction := range []string{
		"Before narrow relevant builds and tests",
		`timeout "${SWARM_REVIEW_CHECKS_TIMEOUT_SECONDS}s" bash -lc "$SWARM_REVIEW_CHECKS"`,
		"Never replace these environment values",
		"If checks fail or time out, return VERDICT: BLOCK and quote the failing output",
		"no folder checks are defined and run the narrow tests",
	} {
		if !strings.Contains(prompt, instruction) {
			t.Fatalf("missing reviewer instruction: %s", instruction)
		}
	}
}
