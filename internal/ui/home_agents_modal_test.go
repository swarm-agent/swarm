package ui

import (
	"testing"

	"swarm-refactor/swarmtui/internal/client"
)

func TestAgentsModalCompiledSubagentsOnlyOfferSingleProfiles(t *testing.T) {
	page := &HomePage{}
	page.agentsModal.ModelProfiles = []client.ModelProfile{
		{ProfileID: "single", Name: "Single", ModelMode: "single", Single: &client.ModelProfileSelection{Provider: "codex", Model: "single-model"}},
		{ProfileID: "split", Name: "Split", ModelMode: "split", Plan: &client.ModelProfileSelection{Provider: "codex", Model: "plan-model"}, Auto: &client.ModelProfileSelection{Provider: "codex", Model: "auto-model"}},
	}

	for _, agentName := range []string{"system-finder", "system-clone", "coder", "system-designer"} {
		got := page.agentsModalModelProfileOptions(AgentModalProfile{Name: agentName, Mode: "subagent"})
		if len(got) != 1 || got[0] != "single" {
			t.Fatalf("%s profile options = %#v, want [single]", agentName, got)
		}
	}

	got := page.agentsModalModelProfileOptions(AgentModalProfile{Name: "swarm", Mode: "primary"})
	if len(got) != 2 {
		t.Fatalf("primary profile options = %#v, want both profiles", got)
	}
}

func TestAgentsModalCompiledSubagentDisplayNames(t *testing.T) {
	for input, want := range map[string]string{
		"system-clone":    "Coder",
		"clone":           "Coder",
		"coder":           "Coder",
		"system-finder":   "Finder",
		"system-designer": "Designer",
		"designer":        "Designer",
		"writer":          "writer",
	} {
		if got := agentsModalDisplayName(input); got != want {
			t.Fatalf("agentsModalDisplayName(%q) = %q, want %q", input, got, want)
		}
	}
}
