package run

import (
	"fmt"

	"strings"
	"testing"

	agentruntime "swarm/packages/swarmd/internal/agent"

	pebblestore "swarm/packages/swarmd/internal/store/pebble"
)

func TestParseTaskSwarmIdeaRepeatsExactQuestionWithoutRouterFields(t *testing.T) {
	parsed, err := parseTaskCallArguments(`{"mode":"swarm","description":"Ask the swarm","prompt":"Which name is clearest?","agent_type":"idea","count":3}`)
	if err != nil {
		t.Fatalf("parse Idea swarm: %v", err)
	}
	if parsed.Mode != taskModeSwarm || parsed.Swarm == nil || parsed.Swarm.AgentType != "idea" || len(parsed.Launches) != 3 {
		t.Fatalf("unexpected Idea swarm: %#v", parsed)
	}
	for i, launch := range parsed.Launches {
		if launch.RequestedSubagentType != "idea" || launch.MetaPrompt != parsed.Prompt || launch.AssignmentLabel != fmt.Sprintf("Agent #%d", i+1) || launch.StreamKey != fmt.Sprintf("swarm:%d", i+1) || !launch.SwarmMode {
			t.Fatalf("launch %d = %#v", i, launch)
		}
	}
}

func TestParseTaskSwarmRejectsFinderExplicitLaunchesAndTrustFields(t *testing.T) {
	for _, raw := range []string{
		`{"mode":"swarm","prompt":"x","agent_type":"finder","count":2}`,
		`{"mode":"swarm","prompt":"x","agent_type":"coder","count":2,"launches":[{"subagent_type":"coder"}]}`,
		`{"mode":"swarm","prompt":"x","agent_type":"coder","count":2,"always_ask":false}`,
		`{"mode":"swarm","prompt":"x","agent_type":"coder","count":2,"groups":[{"name":"a","count":2,"trusted":true}]}`,
	} {
		if _, err := parseTaskCallArguments(raw); err == nil {
			t.Fatalf("expected rejection for %s", raw)
		}
	}
}

func TestParseTaskSwarmImageBuildsDirectRouterHydratedItems(t *testing.T) {
	parsed, err := parseTaskCallArguments(`{"mode":"swarm","description":"images","prompt":"create campaign art","agent_type":"image","count":2,"themes":["minimal","maximal"],"output_contract":"one ready image"}`)
	if err != nil {
		t.Fatalf("parse image swarm: %v", err)
	}
	if parsed.Swarm == nil || parsed.Swarm.AgentType != "image" || parsed.Swarm.OutputMode != taskOutputModeManaged || len(parsed.Launches) != 2 {
		t.Fatalf("image swarm = %#v", parsed)
	}
	for i, item := range parsed.Launches {
		if item.RequestedSubagentType != "image" || item.OutputMode != taskOutputModeManaged || len(item.OwnedScope) != 0 {
			t.Fatalf("image item %d = %#v", i, item)
		}
	}
	prompt := composeDirectImageSwarmPrompt("create campaign art", "minimal", nil, taskSwarmHydratedDelta{Index: 1, Title: "Minimal Image", Theme: "quiet geometry", Role: "Compose a minimal visual.", Deliverable: "Ready image"})
	for _, want := range []string{"create campaign art", "Parent-selected base theme", "minimal", "Router-hydrated image direction", "Compose a minimal visual."} {
		if !strings.Contains(prompt, want) {
			t.Fatalf("direct image prompt missing %q: %s", want, prompt)
		}
	}
	context := managedDesignerArtifactContext(pebblestore.SessionSnapshot{ID: "parent", AccountScopeID: "account", UserID: "user"}, "call", parsed.Launches[0], 1)
	if context == nil || context.CollectionID == "" || context.VariantID == "" {
		t.Fatalf("image managed destination = %#v", context)
	}
	for _, raw := range []string{
		`{"mode":"swarm","prompt":"x","agent_type":"image","count":1,"output_mode":"workspace"}`,
		`{"mode":"swarm","prompt":"x","agent_type":"image","count":1,"owned_scope_template":"images/{index}.png"}`,
		`{"mode":"swarm","swarm_strategy":"assembly","prompt":"x","agent_type":"image","count":1,"assembly_parts":[{"name":"image","owned_scope":["images/1.png"]}],"integration_contract":"image"}`,
	} {
		if _, err := parseTaskCallArguments(raw); err == nil {
			t.Fatalf("invalid image swarm was accepted: %s", raw)
		}
	}
}

func TestParseTaskSwarmDesignerRejectsWorkspaceOutput(t *testing.T) {
	for _, raw := range []string{
		`{"mode":"swarm","description":"objects","prompt":"make objects","agent_type":"designer","count":3,"output_mode":"workspace"}`,
		`{"mode":"swarm","description":"objects","prompt":"make objects","agent_type":"designer","count":3,"owned_scope_template":"web/src/objects/item-{index}.tsx"}`,
	} {
		_, err := parseTaskCallArguments(raw)
		if err == nil {
			t.Fatalf("workspace Designer Iteration Swarm was accepted: %s", raw)
		}
		if !strings.Contains(err.Error(), "regular task launches") && !strings.Contains(err.Error(), "unsupported field") {
			t.Fatalf("workspace Designer Iteration Swarm error = %v", err)
		}
	}
}

func TestValidateTaskSwarmHydrationFailsClosed(t *testing.T) {
	duplicate := taskSwarmHydratedDelta{Index: 1, Title: "One", Theme: "A", Role: "role", Deliverable: "output"}
	other := duplicate
	other.Index = 2
	other.Title = "Two"
	if err := validateTaskSwarmHydrationResult(taskSwarmHydrationResult{GroupTitle: "Dashboard Layout Studies", Deltas: []taskSwarmHydratedDelta{duplicate, other}}, 2); err == nil || !strings.Contains(err.Error(), "duplicates") {
		t.Fatalf("expected duplicate delta failure, got %v", err)
	}
}

func TestIdeaProfileIsCompiledToolFreeAndProtected(t *testing.T) {
	profile := agentruntime.IdeaAgentProfileForParent(pebblestore.AgentProfile{Provider: "codex", Model: "model", Thinking: "high"})
	if !agentruntime.IsIdeaAgentName(profile.Name) || !profile.Protected || profile.ToolContract == nil {
		t.Fatalf("unexpected Idea profile: %#v", profile)
	}
	for name, config := range profile.ToolContract.Tools {
		if config.Enabled != nil && *config.Enabled {
			t.Fatalf("Idea tool %s unexpectedly enabled", name)
		}
	}
}
