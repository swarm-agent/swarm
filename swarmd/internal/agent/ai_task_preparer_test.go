package agent

import (
	"strings"
	"testing"

	pebblestore "swarm/packages/swarmd/internal/store/pebble"
)

func TestAITaskPreparerUsesCanonicalParentModelAndReadOnlyTools(t *testing.T) {
	profile := AITaskPreparerAgentProfileForParent(pebblestore.AgentProfile{
		Provider: "fallback-provider", Model: "fallback-model", Thinking: "low",
	})
	if profile.Provider != "fallback-provider" || profile.Model != "fallback-model" || profile.Thinking != "low" {
		t.Fatalf("preparer did not inherit canonical parent preference: %#v", profile)
	}
	for _, name := range []string{"read", "search", "list"} {
		tool, ok := profile.ToolContract.Tools[name]
		if !ok || tool.Enabled == nil || !*tool.Enabled {
			t.Fatalf("expected %s to be enabled", name)
		}
	}
	if prompt := AITaskPreparerAgentPrompt(); !strings.Contains(prompt, "3-5 word title") || !strings.Contains(prompt, "guidance rather than a hard word-count restriction") {
		t.Fatalf("preparer prompt title guidance = %q", prompt)
	}
	for _, name := range []string{"write", "edit", "bash", "manage_todos", "manage_sessions", "manage_agent", "plan_manage"} {
		if tool, ok := profile.ToolContract.Tools[name]; ok && tool.Enabled != nil && *tool.Enabled {
			t.Fatalf("unexpected mutating tool %s", name)
		}
	}
}
